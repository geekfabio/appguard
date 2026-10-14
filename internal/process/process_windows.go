//go:build windows

package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
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

const noActiveSession = 0xFFFFFFFF

// Start launches the executable and only reports success once the process
// has stayed alive for opts.VerifyFor.
func Start(opts StartOptions) (uint32, error) {
	if err := validate(opts); err != nil { return 0, err }

	dir := opts.Dir
	if dir == "" { dir = filepath.Dir(opts.Path) }

	mode, err := resolveMode(opts.RunAs)
	if err != nil { return 0, fmt.Errorf("start %s: %w", opts.Path, err) }

	var pi *windows.ProcessInformation
	if mode == RunAsUser {
		pi, err = startInUserSession(opts, dir)
	} else {
		pi, err = startHidden(opts, dir)
	}
	if err != nil { return 0, fmt.Errorf("start %s: %w", opts.Path, err) }
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)

	if err := waitAlive(pi.Process, opts.VerifyFor); err != nil {
		return pi.ProcessId, fmt.Errorf("start %s: %w", opts.Path, err)
	}
	return pi.ProcessId, nil
}

func validate(opts StartOptions) error {
	if opts.Path == "" { return errors.New("caminho do executável vazio") }
	fi, err := os.Stat(opts.Path)
	if err != nil { return fmt.Errorf("executável inválido %s: %w", opts.Path, err) }
	if fi.IsDir() { return fmt.Errorf("executável inválido %s: é uma pasta", opts.Path) }
	if opts.Dir != "" {
		if fi, err := os.Stat(opts.Dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("pasta de trabalho inválida: %s", opts.Dir)
		}
	}
	return nil
}

// resolveMode decides where the process will be created. A process that is
// not in session 0 (e.g. AppGuard run from a console) can create visible
// windows directly, so "user" there means a plain CreateProcess.
func resolveMode(runAs string) (string, error) {
	switch runAs {
	case RunAsService:
		return RunAsService, nil
	case RunAsUser:
		if activeUserSession() == noActiveSession {
			return "", errors.New("nenhuma sessão de utilizador activa")
		}
		return RunAsUser, nil
	default:
		if activeUserSession() == noActiveSession {
			return RunAsService, nil
		}
		return RunAsUser, nil
	}
}

func activeUserSession() uint32 {
	id := windows.WTSGetActiveConsoleSessionId()
	if id == 0 { return noActiveSession }
	return id
}

func currentSession() uint32 {
	var id uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &id); err != nil {
		return 0
	}
	return id
}

func startHidden(opts StartOptions, dir string) (*windows.ProcessInformation, error) {
	si := &windows.StartupInfo{
		Flags:      windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
	}
	si.Cb = uint32(unsafe.Sizeof(*si))
	return createProcess(0, opts, dir, os.Environ(), si, windows.CREATE_NO_WINDOW)
}

func startInUserSession(opts StartOptions, dir string) (*windows.ProcessInformation, error) {
	si := &windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(*si))

	if currentSession() != 0 {
		return createProcess(0, opts, dir, os.Environ(), si, 0)
	}

	session := activeUserSession()
	if session == noActiveSession { return nil, errors.New("nenhuma sessão de utilizador activa") }

	var userToken windows.Token
	if err := windows.WTSQueryUserToken(session, &userToken); err != nil {
		return nil, fmt.Errorf("obter token da sessão %d: %w", session, err)
	}
	defer userToken.Close()

	var primary windows.Token
	if err := windows.DuplicateTokenEx(userToken, windows.MAXIMUM_ALLOWED, nil,
		windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return nil, fmt.Errorf("duplicar token: %w", err)
	}
	defer primary.Close()

	env, err := primary.Environ(false)
	if err != nil { return nil, fmt.Errorf("ambiente do utilizador: %w", err) }

	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si.Desktop = desktop
	return createProcess(primary, opts, dir, env, si, 0)
}

func createProcess(token windows.Token, opts StartOptions, dir string, baseEnv []string, si *windows.StartupInfo, flags uint32) (*windows.ProcessInformation, error) {
	appName, err := windows.UTF16PtrFromString(opts.Path)
	if err != nil { return nil, err }
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{opts.Path}, opts.Args...)))
	if err != nil { return nil, err }
	cwd, err := windows.UTF16PtrFromString(dir)
	if err != nil { return nil, err }
	env, err := envBlock(mergeEnv(baseEnv, opts.Env))
	if err != nil { return nil, err }

	flags |= windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP
	pi := &windows.ProcessInformation{}
	if token != 0 {
		err = windows.CreateProcessAsUser(token, appName, cmdLine, nil, nil, false, flags, env, cwd, si, pi)
	} else {
		err = windows.CreateProcess(appName, cmdLine, nil, nil, false, flags, env, cwd, si, pi)
	}
	if err != nil { return nil, err }
	return pi, nil
}

// waitAlive fails if the process exits within d.
func waitAlive(h windows.Handle, d time.Duration) error {
	if d <= 0 { return nil }
	ev, err := windows.WaitForSingleObject(h, uint32(d/time.Millisecond))
	if err != nil { return err }
	if ev == uint32(windows.WAIT_TIMEOUT) { return nil }
	var code uint32
	_ = windows.GetExitCodeProcess(h, &code)
	return fmt.Errorf("processo terminou logo após arrancar (código %d)", code)
}

func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 { return base }
	idx := make(map[string]int, len(base))
	out := append([]string(nil), base...)
	for i, kv := range out {
		if k, _, ok := strings.Cut(kv, "="); ok && k != "" {
			idx[strings.ToUpper(k)] = i
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra { keys = append(keys, k) }
	sort.Strings(keys)
	for _, k := range keys {
		kv := k + "=" + extra[k]
		if i, ok := idx[strings.ToUpper(k)]; ok {
			out[i] = kv
		} else {
			out = append(out, kv)
		}
	}
	return out
}

func envBlock(env []string) (*uint16, error) {
	var block []uint16
	for _, kv := range env {
		if strings.IndexByte(kv, 0) >= 0 { return nil, fmt.Errorf("variável de ambiente inválida: %q", kv) }
		block = append(block, windows.StringToUTF16(kv)...)
	}
	block = append(block, 0)
	if len(block) == 1 { block = append(block, 0) }
	return &block[0], nil
}

// Stop terminates every instance of the executable and waits up to timeout
// for them to exit, so a following Start does not race the old instance.
func Stop(path string, timeout time.Duration) error {
	list, err := FindByExecutable(path)
	if err != nil { return err }

	var errs []error
	type opened struct {
		h    windows.Handle
		info Info
	}
	var handles []opened
	for _, p := range list {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, p.PID)
		if err != nil {
			if err == windows.ERROR_INVALID_PARAMETER { continue } // already gone
			errs = append(errs, fmt.Errorf("abrir PID %d: %w", p.PID, err))
			continue
		}
		if err := windows.TerminateProcess(h, 1); err != nil && err != windows.ERROR_ACCESS_DENIED {
			errs = append(errs, fmt.Errorf("terminar PID %d: %w", p.PID, err))
		}
		handles = append(handles, opened{h, p})
	}

	deadline := time.Now().Add(timeout)
	for _, o := range handles {
		wait := time.Until(deadline)
		if wait < 0 { wait = 0 }
		ev, err := windows.WaitForSingleObject(o.h, uint32(wait/time.Millisecond))
		windows.CloseHandle(o.h)
		if err == nil && ev != uint32(windows.WAIT_TIMEOUT) { continue }
		if err == nil { err = errors.New("tempo esgotado") }
		errs = append(errs, fmt.Errorf("aguardar fim do PID %d (%s): %w", o.info.PID, o.info.Name, err))
	}
	return errors.Join(errs...)
}
