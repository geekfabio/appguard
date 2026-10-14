package control

import (
	"path/filepath"
	"sort"
	"testing"
	"time"

	"appguard/internal/storage"
)

func stateWithAgents(t *testing.T) *controllerState {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "ps.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.UpsertAgent("ctrl", "Controllers", now, map[string]any{"role": "controller"}))
	must(db.UpsertAgent("pc1", "Loja-01", now, map[string]any{"role": "agent"}))
	must(db.UpsertAgent("pc2", "Loja-01", now, map[string]any{"role": "agent"}))
	must(db.UpsertAgent("pc3", "Loja-02", now, map[string]any{"role": "agent"}))
	return &controllerState{db: db}
}

func TestResolveTargets(t *testing.T) {
	st := stateWithAgents(t)

	all, err := st.resolveTargets(powerShellTarget{Type: "all"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(all)
	if got := join(all); got != "pc1,pc2,pc3" {
		t.Fatalf("all = %s (controller must be excluded)", got)
	}

	grp, _ := st.resolveTargets(powerShellTarget{Type: "group", Value: "Loja-01"})
	sort.Strings(grp)
	if join(grp) != "pc1,pc2" {
		t.Fatalf("group = %v", grp)
	}

	one, _ := st.resolveTargets(powerShellTarget{Type: "agent", Value: "pc3"})
	if join(one) != "pc3" {
		t.Fatalf("agent = %v", one)
	}

	if _, err := st.resolveTargets(powerShellTarget{Type: "agent"}); err == nil {
		t.Fatal("agent target without id should error")
	}
	if _, err := st.resolveTargets(powerShellTarget{Type: "bogus"}); err == nil {
		t.Fatal("invalid target type should error")
	}
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
