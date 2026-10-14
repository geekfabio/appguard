package control

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"appguard/internal/buildinfo"
	"appguard/internal/config"
	"appguard/internal/update"
)

const (
	// updateLease is how long an agent holds an update slot.
	updateLease = 15 * time.Minute
	// updateCooldown keeps an agent whose update did not complete within its
	// lease from taking a slot again too soon.
	updateCooldown = time.Hour
)

// agentRelease offers the controller's own appguard.exe to agents. The
// controller binary is the desired version for every self-updating agent.
type agentRelease struct {
	path          string
	manifest      update.Manifest
	maxConcurrent int

	mu       sync.Mutex
	leases   map[string]time.Time // agentID -> lease expiry
	cooldown map[string]time.Time // agentID -> no new lease before
}

// newAgentRelease returns nil when agent updates are disabled or the running
// binary cannot be read.
func newAgentRelease(cfg config.AgentUpdates) *agentRelease {
	if !cfg.IsEnabled() {
		slog.Info("agent self-update disabled by configuration")
		return nil
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		slog.Warn("agent self-update unavailable", "error", err)
		return nil
	}
	sum, size, err := update.FileSHA256(exe)
	if err != nil {
		slog.Warn("agent self-update unavailable", "error", err)
		return nil
	}
	slog.Info("offering controller binary to agents", "version", buildinfo.Version, "sha256", sum)
	return &agentRelease{
		path:          exe,
		manifest:      update.Manifest{Version: buildinfo.Version, SHA256: sum, Size: size},
		maxConcurrent: cfg.MaxConcurrent,
		leases:        make(map[string]time.Time),
		cooldown:      make(map[string]time.Time),
	}
}

// offer returns the manifest when the agent should update now.
func (r *agentRelease) offer(hb Heartbeat) *update.Manifest {
	if r == nil || !hb.SelfUpdate || hb.BinarySHA256 == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if strings.EqualFold(hb.BinarySHA256, r.manifest.SHA256) {
		delete(r.leases, hb.AgentID)
		delete(r.cooldown, hb.AgentID)
		return nil
	}

	now := time.Now()
	for id, expires := range r.leases {
		if now.After(expires) {
			delete(r.leases, id)
			r.cooldown[id] = now.Add(updateCooldown)
		}
	}
	if _, ok := r.leases[hb.AgentID]; ok {
		m := r.manifest
		return &m
	}
	if now.Before(r.cooldown[hb.AgentID]) || len(r.leases) >= r.maxConcurrent {
		return nil
	}
	r.leases[hb.AgentID] = now.Add(updateLease)
	m := r.manifest
	return &m
}

// state describes an agent's version against the controller for the
// dashboard: "current", "pending" (will self-update), "manual" (agent too old
// to self-update or not running as a service) or "".
func (r *agentRelease) state(status map[string]any) string {
	if r == nil || status == nil || status["role"] == "controller" {
		return ""
	}
	sha, _ := status["binarySha256"].(string)
	self, _ := status["selfUpdate"].(bool)
	version, _ := status["version"].(string)
	switch {
	case sha != "" && strings.EqualFold(sha, r.manifest.SHA256):
		return "current"
	case self:
		return "pending"
	case version != "" && version != r.manifest.Version:
		return "manual"
	}
	return ""
}

func (r *agentRelease) serve(w http.ResponseWriter, req *http.Request) {
	if r == nil {
		http.Error(w, "agent updates disabled", http.StatusNotFound)
		return
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, err := os.Open(r.path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-AppGuard-Version", r.manifest.Version)
	w.Header().Set("X-AppGuard-SHA256", r.manifest.SHA256)
	http.ServeContent(w, req, "appguard.exe", fi.ModTime(), f)
}
