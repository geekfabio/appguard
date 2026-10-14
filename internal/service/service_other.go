//go:build !windows

package service

import (
	"context"
	"errors"
	"time"
)

var errWindowsOnly = errors.New("Windows service management is available only on Windows")

func IsWindowsService() (bool, error) { return false, nil }
func Run(name string, run func(context.Context) error) error { return errWindowsOnly }
func Install(name, configPath string) error { return errWindowsOnly }
func Uninstall(name string) error { return errWindowsOnly }
func Start(name string) error { return errWindowsOnly }
func Stop(name string) error { return errWindowsOnly }
func Status(name string) (string, error) { return "", errWindowsOnly }
func ScheduleRestart(name, configPath string) error { return errWindowsOnly }
func RestartWhenStopped(name string, timeout time.Duration) error { return errWindowsOnly }
