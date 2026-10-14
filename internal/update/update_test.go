package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setup(t *testing.T, newBinary []byte) (*Updater, string, Manifest) {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "appguard.exe")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	u, err := NewAt(exe, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	u.verify = func(context.Context, string, string) error { return nil }
	sum := sha256.Sum256(newBinary)
	return u, exe, Manifest{Version: "1.1.0", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(newBinary))}
}

func serve(body []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallSwapsBinaryAndCommitsWhenHealthy(t *testing.T) {
	body := []byte("new binary")
	u, exe, m := setup(t, body)
	srv := serve(body)
	defer srv.Close()

	if !u.Claim(m) {
		t.Fatal("expected claim")
	}
	if err := u.Install(context.Background(), srv.Client(), srv.URL, m); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "new binary" || read(t, exe+".old") != "old binary" {
		t.Fatal("binaries not swapped")
	}

	// Service restarts with the new binary.
	u2, err := NewAt(exe, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack, err := u2.Startup(); rolledBack || err != nil {
		t.Fatalf("Startup = %v, %v", rolledBack, err)
	}
	u2.ConfirmHealthy() // too early: still on trial
	if u2.st.Pending == nil {
		t.Fatal("committed before HealthyAfter")
	}
	u2.startedAt = time.Now().Add(-HealthyAfter)
	u2.ConfirmHealthy()
	if u2.st.Pending != nil {
		t.Fatal("update not committed")
	}
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Fatal(".old should be removed after commit")
	}
	if r := u2.TakeResult(); r == nil || r.Action != "agent-update-installed" {
		t.Fatalf("result = %+v", r)
	}
	if u2.TakeResult() != nil {
		t.Fatal("result must be reported once")
	}
}

func TestCrashLoopRollsBackAndBlocksVersion(t *testing.T) {
	body := []byte("broken binary")
	u, exe, m := setup(t, body)
	srv := serve(body)
	defer srv.Close()

	u.Claim(m)
	if err := u.Install(context.Background(), srv.Client(), srv.URL, m); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= MaxTrialStarts+1; i++ {
		u2, err := NewAt(exe, "1.1.0")
		if err != nil {
			t.Fatal(err)
		}
		rolledBack, err := u2.Startup()
		if err != nil {
			t.Fatal(err)
		}
		if rolledBack != (i > MaxTrialStarts) {
			t.Fatalf("start %d: rolledBack = %v", i, rolledBack)
		}
	}

	if read(t, exe) != "old binary" {
		t.Fatal("previous binary not restored")
	}
	u3, err := NewAt(exe, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack, _ := u3.Startup(); rolledBack {
		t.Fatal("restored binary must not roll back")
	}
	if r := u3.TakeResult(); r == nil || r.Action != "agent-update-rolled-back" {
		t.Fatalf("result = %+v", r)
	}
	if u3.Claim(m) {
		t.Fatal("rolled-back binary must not be offered again")
	}
}

func TestInstallRejectsHashMismatch(t *testing.T) {
	u, exe, m := setup(t, []byte("expected"))
	srv := serve([]byte("tampered"))
	defer srv.Close()

	u.Claim(m)
	if err := u.Install(context.Background(), srv.Client(), srv.URL, m); err == nil {
		t.Fatal("expected error")
	}
	if read(t, exe) != "old binary" {
		t.Fatal("binary must stay untouched")
	}
	if u.Claim(m) {
		t.Fatal("failed install must not be retried immediately")
	}
}

func TestClaimSkipsCurrentBinary(t *testing.T) {
	u, _, _ := setup(t, nil)
	if u.Claim(Manifest{Version: "1.0.0", SHA256: u.SHA256()}) {
		t.Fatal("must not update to the running binary")
	}
}
