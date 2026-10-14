//go:build windows

package process

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// RunPowerShell executes a script with Windows PowerShell and returns its
// combined stdout+stderr. The script is passed on stdin so there are no
// command-line length or quoting limits. It always runs hidden, in the
// service session (session 0) with the service account.
func RunPowerShell(ctx context.Context, script string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = bytes.NewBufferString(script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}

	out, err := cmd.CombinedOutput()
	return string(out), err
}
