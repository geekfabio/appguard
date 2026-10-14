//go:build windows

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func IsWindowsService() (bool, error) {
	return svc.IsWindowsService()
}

type handler struct {
	run func(context.Context) error
}

func (h *handler) Execute(args []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case c := <-requests:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
				changes <- svc.Status{State: svc.Stopped}
				return false, 0
			}
		case err := <-done:
			if err != nil { return true, 1 }
			return false, 0
		}
	}
}

func Run(name string, run func(context.Context) error) error {
	return svc.Run(name, &handler{run: run})
}

func Install(name, configPath string) error {
	exe, err := os.Executable()
	if err != nil { return err }
	m, err := mgr.Connect()
	if err != nil { return err }
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", name)
	}

	s, err = m.CreateService(name, exe, mgr.Config{
		DisplayName: "AppGuard Process Supervisor",
		Description: "Supervises configured background processes and restarts them when required.",
		StartType: mgr.StartAutomatic,
	}, "-config", configPath, "run")
	if err != nil { return err }
	defer s.Close()
	// Restart on failure, as scripts/install.ps1 does; self-update and its
	// rollback rely on the service coming back after a crash.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 3600); err != nil { return err }
	return s.SetRecoveryActionsOnNonCrashFailures(true)
}

// ScheduleRestart launches a detached "service-restart" helper that starts
// the service again once the current instance has stopped.
func ScheduleRestart(name, configPath string) error {
	exe, err := os.Executable()
	if err != nil { return err }
	cmd := exec.Command(exe, "-config", configPath, "-service", name, "service-restart")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil { return err }
	return cmd.Process.Release()
}

// RestartWhenStopped waits for the service to stop and starts it again.
func RestartWhenStopped(name string, timeout time.Duration) error {
	m, err := mgr.Connect()
	if err != nil { return err }
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil { return err }
	defer s.Close()

	deadline := time.Now().Add(timeout)
	for {
		st, err := s.Query()
		if err != nil { return err }
		if st.State == svc.Stopped { break }
		if time.Now().After(deadline) { return fmt.Errorf("service %s did not stop within %s", name, timeout) }
		time.Sleep(500 * time.Millisecond)
	}

	for attempt := 0; ; attempt++ {
		err = s.Start()
		if err == nil || err == windows.ERROR_SERVICE_ALREADY_RUNNING { return nil }
		if attempt >= 5 { return err }
		time.Sleep(2 * time.Second)
	}
}

func Uninstall(name string) error {
	m, err := mgr.Connect()
	if err != nil { return err }
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil { return err }
	defer s.Close()
	return s.Delete()
}

func Start(name string) error {
	m, err := mgr.Connect()
	if err != nil { return err }
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil { return err }
	defer s.Close()
	return s.Start()
}

func Stop(name string) error {
	m, err := mgr.Connect()
	if err != nil { return err }
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil { return err }
	defer s.Close()
	_, err = s.Control(svc.Stop)
	return err
}

func Status(name string) (string, error) {
	m, err := mgr.Connect()
	if err != nil { return "", err }
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil { return "", err }
	defer s.Close()
	st, err := s.Query()
	if err != nil { return "", err }
	return fmt.Sprintf("state=%v", st.State), nil
}
