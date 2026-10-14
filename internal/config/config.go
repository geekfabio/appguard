package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	AgentID    string        `json:"agentId"`
	Group      string        `json:"group"`
	Enabled    bool          `json:"enabled"`
	CheckEvery int           `json:"checkEverySeconds"`
	Controller Controller    `json:"controller"`
	Apps       []Application `json:"applications"`
}

type Controller struct {
	URL              string `json:"url"`
	Token            string `json:"token"`
	PollEverySeconds int    `json:"pollEverySeconds"`
	Listen           string `json:"listen"`
}

type Application struct {
	Name              string `json:"name"`
	Path              string `json:"path"`
	WorkingDirectory  string `json:"workingDirectory"`
	Enabled           bool   `json:"enabled"`
	AutoRestart       bool   `json:"autoRestart"`
	MaxRetries        int    `json:"maxRetries"`
	ResetAfterSeconds int    `json:"resetAfterSeconds"`
}

var mu sync.Mutex

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.AgentID == "" {
		host, _ := os.Hostname()
		cfg.AgentID = host
	}
	if cfg.Group == "" {
		cfg.Group = "default"
	}
	if cfg.CheckEvery <= 0 {
		cfg.CheckEvery = 10
	}
	if cfg.Controller.PollEverySeconds <= 0 {
		cfg.Controller.PollEverySeconds = 5
	}
	if cfg.Controller.Listen == "" {
		cfg.Controller.Listen = ":8443"
	}
	for i := range cfg.Apps {
		if cfg.Apps[i].MaxRetries <= 0 {
			cfg.Apps[i].MaxRetries = 5
		}
		if cfg.Apps[i].ResetAfterSeconds <= 0 {
			cfg.Apps[i].ResetAfterSeconds = 300
		}
		if cfg.Apps[i].WorkingDirectory == "" {
			cfg.Apps[i].WorkingDirectory = filepath.Dir(cfg.Apps[i].Path)
		}
	}
	return &cfg, nil
}

func Save(path string, cfg *Config) error {
	mu.Lock()
	defer mu.Unlock()

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
