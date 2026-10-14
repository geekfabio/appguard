package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	AgentID    string        `json:"agentId"`
	Role       string        `json:"role"`
	Group      string        `json:"group"`
	Enabled    bool          `json:"enabled"`
	CheckEvery int           `json:"checkEverySeconds"`
	Controller Controller    `json:"controller"`
	Apps       []Application `json:"applications"`
}

type Controller struct {
	URL              string `json:"url"`
	PollEverySeconds int    `json:"pollEverySeconds"`
	Listen           string `json:"listen"`
	DatabasePath     string `json:"databasePath"`
}

type Application struct {
	Name              string `json:"name"`
	Path              string `json:"path"`
	WorkingDirectory  string `json:"workingDirectory"`
	Enabled           bool   `json:"enabled"`
	AutoRestart       bool   `json:"autoRestart"`
	MaxRetries        int    `json:"maxRetries"`
	ResetAfterSeconds  int    `json:"resetAfterSeconds"`
	StartupDelaySeconds int   `json:"startupDelaySeconds"`
	Arguments         []string          `json:"arguments,omitempty"`
	Environment       map[string]string `json:"environment,omitempty"`
	// RunAs: "auto" (default), "user" or "service". See process.StartOptions.
	RunAs             string            `json:"runAs,omitempty"`
}

type AgentSettings struct {
	Group      string        `json:"group"`
	CheckEvery int           `json:"checkEverySeconds"`
	Apps       []Application `json:"applications"`
}

var mu sync.Mutex

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil { return nil, fmt.Errorf("read config: %w", err) }
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil { return nil, fmt.Errorf("parse config: %w", err) }
	applyDefaults(&cfg)
	return &cfg, nil
}

func Normalize(cfg *Config) {
	applyDefaults(cfg)
}

func applyDefaults(cfg *Config) {
	host, _ := os.Hostname()
	if host != "" { cfg.AgentID = host }
	if cfg.Role == "" { cfg.Role = "agent" }
	if cfg.Role != "agent" && cfg.Role != "controller" { cfg.Role = "agent" }
	if cfg.Group == "" { cfg.Group = "default" }
	if cfg.CheckEvery <= 0 { cfg.CheckEvery = 10 }
	if cfg.Controller.PollEverySeconds <= 0 { cfg.Controller.PollEverySeconds = 5 }
	if cfg.Controller.Listen == "" { cfg.Controller.Listen = ":8443" }
	if cfg.Controller.DatabasePath == "" { cfg.Controller.DatabasePath = "appguard.db" }
	for i := range cfg.Apps { normalizeApplication(&cfg.Apps[i]) }
}

func normalizeApplication(app *Application) {
	if app.MaxRetries <= 0 { app.MaxRetries = 5 }
	if app.ResetAfterSeconds <= 0 { app.ResetAfterSeconds = 300 }
	if app.StartupDelaySeconds <= 0 { app.StartupDelaySeconds = 60 }
	if app.WorkingDirectory == "" && app.Path != "" { app.WorkingDirectory = filepath.Dir(app.Path) }
	switch app.RunAs = strings.ToLower(strings.TrimSpace(app.RunAs)); app.RunAs {
	case "auto", "user", "service":
	default:
		app.RunAs = "auto"
	}
}

func NormalizeSettings(s *AgentSettings) {
	if s.Group == "" { s.Group = "default" }
	if s.CheckEvery <= 0 { s.CheckEvery = 10 }
	for i := range s.Apps { normalizeApplication(&s.Apps[i]) }
}

func Save(path string, cfg *Config) error {
	mu.Lock()
	defer mu.Unlock()
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil { return err }
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil { return err }
	return os.Rename(tmp, path)
}
