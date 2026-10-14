//go:build windows

package sysinfo

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// MachineID returns the Windows MachineGuid, a stable identifier created when
// Windows is installed. It survives renaming the PC and reinstalling
// AppGuard, so an agent keeps the same identity. Falls back to the hostname
// if the registry value cannot be read.
func MachineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err == nil {
		defer k.Close()
		if guid, _, err := k.GetStringValue("MachineGuid"); err == nil {
			if guid = strings.TrimSpace(guid); guid != "" {
				return guid
			}
		}
	}
	host, _ := os.Hostname()
	return host
}

func collectPlatform(info *Info) {
	info.OSVersion = osVersion()
	info.TotalRAMMB = totalRAMMB()
	info.UptimeSec = uptimeSeconds()
	info.LoggedUser = consoleUser()
}

func osVersion() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "Windows"
	}
	defer k.Close()

	product, _, _ := k.GetStringValue("ProductName")
	build, _, _ := k.GetStringValue("CurrentBuild")
	display, _, _ := k.GetStringValue("DisplayVersion")
	if product == "" {
		product = "Windows"
	}
	parts := []string{product}
	if display != "" {
		parts = append(parts, display)
	}
	if build != "" {
		parts = append(parts, "build "+build)
	}
	return strings.Join(parts, " ")
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func totalRAMMB() uint64 {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0
	}
	return m.TotalPhys / (1024 * 1024)
}

func uptimeSeconds() uint64 {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount64")
	r, _, _ := proc.Call()
	return uint64(r) / 1000
}

// consoleUser returns DOMAIN\user of the session on the physical console, or
// "" when nobody is logged in.
func consoleUser() string {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF {
		return ""
	}
	user := wtsQuery(session, 5)   // WTSUserName
	domain := wtsQuery(session, 7) // WTSDomainName
	if user == "" {
		return ""
	}
	if domain != "" {
		return domain + "\\" + user
	}
	return user
}

func wtsQuery(session uint32, infoClass uintptr) string {
	proc := windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSQuerySessionInformationW")
	var buf *uint16
	var n uint32
	r, _, _ := proc.Call(
		0, // WTS_CURRENT_SERVER_HANDLE
		uintptr(session),
		infoClass,
		uintptr(unsafe.Pointer(&buf)),
		uintptr(unsafe.Pointer(&n)),
	)
	if r == 0 || buf == nil {
		return ""
	}
	defer windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSFreeMemory").Call(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf)
}
