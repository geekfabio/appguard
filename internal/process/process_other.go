//go:build !windows

package process

import "errors"

type Info struct {
	PID  uint32
	Name string
}

var ErrWindowsOnly = errors.New("AppGuard process supervision is supported only on Windows")

func FindByExecutable(path string) ([]Info, error) { return nil, ErrWindowsOnly }
func IsRunning(path string) (bool, []Info, error)  { return false, nil, ErrWindowsOnly }
func Start(path, dir string) error                 { return ErrWindowsOnly }
func Stop(path string) error                       { return ErrWindowsOnly }
