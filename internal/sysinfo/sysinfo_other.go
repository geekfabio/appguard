//go:build !windows

package sysinfo

import "os"

// MachineID returns a stable identifier for this machine. Off Windows there
// is no universal machine GUID, so the hostname is used.
func MachineID() string {
	host, _ := os.Hostname()
	return host
}

func collectPlatform(info *Info) {}
