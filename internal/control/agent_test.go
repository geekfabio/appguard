package control

import "testing"

func TestCommandLogEvictsOldest(t *testing.T) {
	l := newCommandLog(2)
	l.put("a", CommandResult{Status: CommandOK})
	l.put("b", CommandResult{Status: CommandFailed, Error: "x"})
	l.put("b", CommandResult{Status: CommandFailed, Error: "y"})
	l.put("c", CommandResult{Status: CommandOK})

	if _, ok := l.get("a"); ok {
		t.Fatal("oldest entry should be evicted")
	}
	if r, ok := l.get("b"); !ok || r.Error != "y" {
		t.Fatalf("b = %+v, %v", r, ok)
	}
	if _, ok := l.get("c"); !ok {
		t.Fatal("c missing")
	}
}
