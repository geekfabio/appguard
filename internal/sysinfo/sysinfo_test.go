package sysinfo

import "testing"

func TestCollectBasics(t *testing.T) {
	info := Collect()
	if info.OS == "" || info.Arch == "" || info.CPUs < 1 {
		t.Fatalf("incomplete info: %+v", info)
	}
	if info.MachineID == "" {
		t.Fatal("machine id should never be empty (hostname fallback)")
	}
}

func TestMachineIDStable(t *testing.T) {
	if MachineID() != MachineID() {
		t.Fatal("MachineID must be stable across calls")
	}
}
