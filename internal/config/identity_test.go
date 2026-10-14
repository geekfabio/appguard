package config

import (
	"path/filepath"
	"testing"

	"appguard/internal/sysinfo"
)

func TestAgentIDPriority(t *testing.T) {
	// Env override wins over everything.
	t.Setenv("APPGUARD_AGENT_ID", "fixed-id")
	cfg := &Config{AgentID: "from-config"}
	applyDefaults(cfg)
	if cfg.AgentID != "fixed-id" {
		t.Fatalf("env override ignored: %q", cfg.AgentID)
	}
}

func TestAgentIDKeepsConfiguredValue(t *testing.T) {
	t.Setenv("APPGUARD_AGENT_ID", "")
	cfg := &Config{AgentID: "explicit"}
	applyDefaults(cfg)
	if cfg.AgentID != "explicit" {
		t.Fatalf("configured id overwritten: %q", cfg.AgentID)
	}
}

func TestAgentIDFallsBackToMachineID(t *testing.T) {
	t.Setenv("APPGUARD_AGENT_ID", "")
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.AgentID == "" || cfg.AgentID != sysinfo.MachineID() {
		t.Fatalf("empty id should become machine id, got %q", cfg.AgentID)
	}
}

func TestLoadDoesNotRewriteIdentityOnReload(t *testing.T) {
	t.Setenv("APPGUARD_AGENT_ID", "")
	path := filepath.Join(t.TempDir(), "cfg.json")
	cfg := &Config{AgentID: "pc-stable", Role: "agent"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AgentID != "pc-stable" {
		t.Fatalf("identity changed across reload: %q", loaded.AgentID)
	}
}
