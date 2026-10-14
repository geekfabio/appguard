//go:build !windows

package process

import (
	"context"
	"time"
)

func RunPowerShell(ctx context.Context, script string, timeout time.Duration) (string, error) {
	return "", ErrWindowsOnly
}
