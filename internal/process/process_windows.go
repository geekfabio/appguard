//go:build windows

package process

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Info struct {
	PID  uint32
	Name string
}

func FindByExecutable(path string) ([]Info, error) {
	target := strings.ToLower(filepath.Base(path))
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil { return nil, err }
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil { return nil, err }

	var found []Info
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if strings.EqualFold(name, target) {
			found = append(found, Info{PID: entry.ProcessID, Name: name})
		}
		err := windows.Process32Next(snap, &entry)
		if err != nil {
			if err == syscall.ERROR_NO_MORE_FILES { break }
			return found, err
		}
	}
	return found, nil
}

func IsRunning(path string) (bool, []Info, error) {
	list, err := FindByExecutable(path)
	return len(list) > 0, list, err
}

func Start(path, dir string) error {
	cmd := exec.Command(path)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
	if err := cmd.Start(); err != nil { return fmt.Errorf("start %s: %w", path, err) }
	return nil
}

func Stop(path string) error {
	list, err := FindByExecutable(path)
	if err != nil { return err }
	for _, p := range list {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, p.PID)
		if err != nil { continue }
		_ = windows.TerminateProcess(h, 1)
		windows.CloseHandle(h)
	}
	return nil
}
