//go:build windows

package singleton

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Acquire takes a machine-wide named mutex so only one AppGuard agent runs
// per computer. The returned release frees it. If another instance already
// holds it, Acquire returns ErrAlreadyRunning.
func Acquire(name string) (func(), error) {
	// Global\ scope so it is shared across all sessions, not per-session.
	p, err := windows.UTF16PtrFromString(`Global\` + name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, p)
	if h != 0 && err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		return nil, ErrAlreadyRunning
	}
	if err != nil && h == 0 {
		return nil, fmt.Errorf("create mutex: %w", err)
	}
	return func() { windows.CloseHandle(h) }, nil
}
