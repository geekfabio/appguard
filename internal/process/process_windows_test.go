//go:build windows

package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMergeEnvOverridesCaseInsensitive(t *testing.T) {
	got := mergeEnv([]string{"Path=C:\\a", "FOO=1"}, map[string]string{"PATH": "C:\\b", "NEW": "x"})
	want := []string{"PATH=C:\\b", "FOO=1", "NEW=x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("mergeEnv = %v, want %v", got, want)
	}
}

func TestStartDetectsImmediateExit(t *testing.T) {
	cmd := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	_, err := Start(StartOptions{Path: cmd, Args: []string{"/c", "exit", "3"}, RunAs: RunAsService, VerifyFor: 2 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "código 3") {
		t.Fatalf("err = %v, want immediate-exit error with code 3", err)
	}
}

func TestStartRejectsMissingExecutable(t *testing.T) {
	if _, err := Start(StartOptions{Path: `C:\nao\existe.exe`}); err == nil {
		t.Fatal("expected error for missing executable")
	}
}
