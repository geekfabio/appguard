//go:build !windows

package setup

import "errors"

func pickExecutable() (string, error) {
	return "", errors.New("native file selection is only available on Windows")
}
