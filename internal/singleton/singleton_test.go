package singleton

import "testing"

func TestAcquireReleases(t *testing.T) {
	release, err := Acquire("appguard-test-lock")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if release == nil {
		t.Fatal("release must not be nil")
	}
	release()
}
