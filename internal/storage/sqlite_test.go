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

	cmd, err := db.AckCommand("c1", "failed", "boom")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.AgentID != "pc1" || cmd.Action != "start-app" || cmd.AppName != "app" {
		t.Fatalf("unexpected command: %+v", cmd)
	}

	var status, msg string
	if err := db.sql.QueryRow("SELECT result_status, result_error FROM commands WHERE id = 'c1'").Scan(&status, &msg); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || msg != "boom" {
		t.Fatalf("stored result = %q, %q", status, msg)
	}

	if _, err := db.AckCommand("c1", "ok", ""); err != sql.ErrNoRows {
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
