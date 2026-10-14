package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"appguard/internal/config"
	proc "appguard/internal/process"
)

type Watchdog struct {
	mu         sync.RWMutex
	cfg        *config.Config
	configPath string
	retries    map[string]int
	lastOK     map[string]time.Time
}

func New(cfg *config.Config, configPath string) *Watchdog {
	return &Watchdog{
		cfg: cfg, configPath: configPath,
		retries: make(map[string]int),
		lastOK: make(map[string]time.Time),
	}
}

func (w *Watchdog) Run(ctx context.Context) error {
	t := time.NewTicker(time.Duration(w.cfg.CheckEvery) * time.Second)
	defer t.Stop()
	w.checkAll()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			w.checkAll()
		}
	}
}

func (w *Watchdog) checkAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.cfg.Enabled { return }

	for _, app := range w.cfg.Apps {
		if !app.Enabled { continue }
		running, _, err := proc.IsRunning(app.Path)
		if err != nil {
			slog.Error("process check failed", "app", app.Name, "error", err)
			continue
		}
		if running {
			if since := time.Since(w.lastOK[app.Name]); since > time.Duration(app.ResetAfterSeconds)*time.Second {
				w.retries[app.Name] = 0
			}
			w.lastOK[app.Name] = time.Now()
			continue
		}
		if !app.AutoRestart { continue }
		if w.retries[app.Name] >= app.MaxRetries {
			slog.Error("retry limit reached", "app", app.Name, "retries", w.retries[app.Name])
			continue
		}
		delay := retryDelay(w.retries[app.Name])
		w.retries[app.Name]++
		slog.Warn("application stopped; scheduling restart", "app", app.Name, "retry", w.retries[app.Name], "delay", delay)
		appCopy := app
		go func() {
			time.Sleep(delay)
			if err := proc.Start(appCopy.Path, appCopy.WorkingDirectory); err != nil {
				slog.Error("application restart failed", "app", appCopy.Name, "error", err)
			} else {
				slog.Info("application restarted", "app", appCopy.Name)
			}
		}()
	}
}

func retryDelay(attempt int) time.Duration {
	delays := []time.Duration{5, 10, 30, 60, 120}
	if attempt >= len(delays) { attempt = len(delays)-1 }
	return delays[attempt] * time.Second
}

func (w *Watchdog) Status() map[string]any {
	w.mu.RLock()
	defer w.mu.RUnlock()
	apps := make([]map[string]any, 0, len(w.cfg.Apps))
	for _, app := range w.cfg.Apps {
		running, pids, _ := proc.IsRunning(app.Path)
		var ids []uint32
		for _, p := range pids { ids = append(ids, p.PID) }
		apps = append(apps, map[string]any{
			"name": app.Name, "enabled": app.Enabled, "running": running, "pids": ids,
		})
	}
	return map[string]any{"agentId": w.cfg.AgentID, "enabled": w.cfg.Enabled, "apps": apps}
}

func (w *Watchdog) Apply(action, appName string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch action {
	case "disable-all":
		w.cfg.Enabled = false
	case "enable-all":
		w.cfg.Enabled = true
	case "stop-all":
		for _, app := range w.cfg.Apps { _ = proc.Stop(app.Path) }
	case "restart-all":
		for _, app := range w.cfg.Apps {
			if !app.Enabled { continue }
			_ = proc.Stop(app.Path)
			if err := proc.Start(app.Path, app.WorkingDirectory); err != nil {
				slog.Error("restart failed", "app", app.Name, "error", err)
			}
		}
	case "disable-app", "enable-app", "stop-app", "restart-app":
		idx := -1
		for i := range w.cfg.Apps {
			if w.cfg.Apps[i].Name == appName { idx = i; break }
		}
		if idx < 0 { return fmt.Errorf("unknown application %q", appName) }
		app := &w.cfg.Apps[idx]
		switch action {
		case "disable-app":
			app.Enabled = false
		case "enable-app":
			app.Enabled = true
		case "stop-app":
			app.Enabled = false
			_ = proc.Stop(app.Path)
		case "restart-app":
			_ = proc.Stop(app.Path)
			if err := proc.Start(app.Path, app.WorkingDirectory); err != nil { return err }
		}
	default:
		return fmt.Errorf("unknown action %q", action)
	}

	return config.Save(w.configPath, w.cfg)
}
