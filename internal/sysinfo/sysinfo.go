// Package sysinfo collects stable identity and descriptive information about
// the computer an agent runs on.
package sysinfo

import (
	"net"
	"os"
	"runtime"
	"sort"
)

// Info describes the computer an agent is connected from.
type Info struct {
	MachineID  string   `json:"machineId,omitempty"`
	Hostname   string   `json:"hostname,omitempty"`
	OS         string   `json:"os,omitempty"`
	OSVersion  string   `json:"osVersion,omitempty"`
	Arch       string   `json:"arch,omitempty"`
	CPUs       int      `json:"cpus,omitempty"`
	TotalRAMMB uint64   `json:"totalRamMb,omitempty"`
	IPv4       []string `json:"ipv4,omitempty"`
	LoggedUser string   `json:"loggedUser,omitempty"`
	UptimeSec  uint64   `json:"uptimeSeconds,omitempty"`
}

// Collect gathers a snapshot of the current computer. It is best-effort:
// fields that cannot be read are left empty rather than failing.
func Collect() Info {
	host, _ := os.Hostname()
	info := Info{
		MachineID: MachineID(),
		Hostname:  host,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPUs:      runtime.NumCPU(),
		IPv4:      localIPv4(),
	}
	collectPlatform(&info)
	return info
}

// localIPv4 returns the machine's non-loopback IPv4 addresses, sorted.
func localIPv4() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			out = append(out, ip4.String())
		}
	}
	sort.Strings(out)
	return out
}
