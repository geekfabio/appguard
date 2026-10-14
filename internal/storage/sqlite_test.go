package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestAckCommandStoresResultOnce(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.QueueCommand(Command{ID: "c1", AgentID: "pc1", Action: "start-app", AppName: "app", Created: time.Now()}); err != nil {
		t.Fatal(err)
	}

	cmd, err := db.AckCommand("c1", "failed", "boom", "saida ps")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.AgentID != "pc1" || cmd.Action != "start-app" || cmd.AppName != "app" {
		t.Fatalf("unexpected command: %+v", cmd)
	}

	var status, msg, out string
	if err := db.sql.QueryRow("SELECT result_status, result_error, result_output FROM commands WHERE id = 'c1'").Scan(&status, &msg, &out); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || msg != "boom" || out != "saida ps" {
		t.Fatalf("stored result = %q, %q, %q", status, msg, out)
	}

	recs, err := db.ListCommandsByAction("start-app", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Status != "failed" || recs[0].Output != "saida ps" || recs[0].Delivered == nil {
		t.Fatalf("ListCommandsByAction = %+v", recs)
	}

	if _, err := db.AckCommand("c1", "ok", "", ""); err != sql.ErrNoRows {
		t.Fatalf("second ack err = %v, want sql.ErrNoRows", err)
	}
	if pending, _ := db.FetchPendingCommands("pc1", 10); len(pending) != 0 {
		t.Fatalf("pending after ack = %d", len(pending))
	}
}

func TestMigrateAddsResultColumnsToOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE commands (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL, action TEXT NOT NULL,
		app_name TEXT, payload_json TEXT, created_at TEXT NOT NULL, delivered_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	for i := 0; i < 2; i++ { // second open must be a no-op
		db, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		db.Close()
	}
}

func TestEventsFilterAndPrune(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "ev.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	old := now.Add(-3 * 24 * time.Hour) // older than retention
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(db.AppendEvent(EventRecord{Time: now, AgentID: "pc1", Group: "G1", Action: "process-crash"}))
	must(db.AppendEvent(EventRecord{Time: now, AgentID: "pc2", Group: "G2", Action: "restart-success"}))
	must(db.AppendEvent(EventRecord{Time: old, AgentID: "pc1", Group: "G1", Action: "process-crash"}))

	byAgent, _ := db.ListEvents(EventFilter{AgentID: "pc1"})
	if len(byAgent) != 2 {
		t.Fatalf("agent filter = %d events", len(byAgent))
	}
	byAction, _ := db.ListEvents(EventFilter{Action: "restart-success"})
	if len(byAction) != 1 || byAction[0].AgentID != "pc2" {
		t.Fatalf("action filter = %+v", byAction)
	}
	recent, _ := db.ListEvents(EventFilter{Since: now.Add(-time.Hour)})
	if len(recent) != 2 {
		t.Fatalf("since filter = %d, want 2 (old one excluded)", len(recent))
	}

	n, err := db.PruneEvents()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d events, want 1", n)
	}
	all, _ := db.ListEvents(EventFilter{})
	if len(all) != 2 {
		t.Fatalf("after prune = %d events, want 2", len(all))
	}
}

func TestDeleteAgentRemovesCommands(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "del.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	_ = db.UpsertAgent("pc1", "G1", now, map[string]any{"role": "agent"})
	_ = db.QueueCommand(Command{ID: "x1", AgentID: "pc1", Action: "start-app", Created: now})

	removed, err := db.DeleteAgent("pc1")
	if err != nil || !removed {
		t.Fatalf("DeleteAgent = %v, %v", removed, err)
	}
	if _, err := db.GetAgent("pc1"); err != sql.ErrNoRows {
		t.Fatalf("agent still present: %v", err)
	}
	if pending, _ := db.FetchPendingCommands("pc1", 10); len(pending) != 0 {
		t.Fatal("commands not removed with agent")
	}
	if again, _ := db.DeleteAgent("pc1"); again {
		t.Fatal("deleting a missing agent should report false")
	}
}

func TestPruneCommandsKeepsPending(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "pc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	old := time.Now().Add(-3 * 24 * time.Hour)
	_ = db.QueueCommand(Command{ID: "pending", AgentID: "pc1", Action: "start-app", Created: old})
	_ = db.QueueCommand(Command{ID: "delivered", AgentID: "pc1", Action: "start-app", Created: old})
	if _, err := db.AckCommand("delivered", "ok", "", ""); err != nil {
		t.Fatal(err)
	}
	// Force the delivered_at timestamp into the past.
	if _, err := db.sql.Exec("UPDATE commands SET delivered_at = ? WHERE id = 'delivered'",
		old.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	n, err := db.PruneCommands()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d commands, want 1 (only delivered)", n)
	}
	if pending, _ := db.FetchPendingCommands("pc1", 10); len(pending) != 1 {
		t.Fatal("pending command must survive pruning")
	}
}
