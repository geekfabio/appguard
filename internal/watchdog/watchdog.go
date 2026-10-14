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

type Event struct {
	Time    time.Time
	Action  string
	AppName string
	Detail  string
}

type Watchdog struct {
	mu          sync.RWMutex
	cfg         *config.Config
	configPath  string
	retries     map[string]int
	stableSince map[string]time.Time
	pending     map[string]bool
	events      chan Event
}

func New(cfg *config.Config, configPath string) *Watchdog {
	return &Watchdog{
		cfg: cfg,
		configPath: configPath,
		retries: make(map[string]int),
		stableSince: make(map[string]time.Time),
		pending: make(map[string]bool),
		events: make(chan Event, 128),
	}
}

func (w *Watchdog) Events() <-chan Event {
	return w.events
}

func (w *Watchdog) Settings() config.AgentSettings {
	w.mu.RLock()
	defer w.mu.RUnlock()

	apps := append([]config.Application(nil), w.cfg.Apps...)
	return config.AgentSettings{
		Group:      w.cfg.Group,
		CheckEvery: w.cfg.CheckEvery,
		Apps:       apps,
	}
}

func (w *Watchdog) ApplySettings(settings config.AgentSettings) error {
	config.NormalizeSettings(&settings)

	w.mu.Lock()
	defer w.mu.Unlock()

	w.cfg.Group = settings.Group
	w.cfg.CheckEvery = settings.CheckEvery
	w.cfg.Apps = append([]config.Application(nil), settings.Apps...)
	w.retries = make(map[string]int)
	w.stableSince = make(map[string]time.Time)
	w.pending = make(map[string]bool)

	if err := config.Save(w.configPath, w.cfg); err != nil {
		return err
	}
	w.emit("configuration-updated", "", "configuração remota aplicada")
	return nil
}

func (w *Watchdog) emit(action, appName, detail string) {
	e := Event{
		Time: time.Now(),
		Action: action,
		AppName: appName,
		Detail: detail,
	}
	select {
	case w.events <- e:
	default:
		slog.Warn("watchdog event queue full", "action", action, "app", appName)
	}
}

func (w *Watchdog) Run(ctx context.Context) error {
	w.checkAll()
	for {
		w.mu.RLock()
		seconds := w.cfg.CheckEvery
		w.mu.RUnlock()
		if seconds <= 0 {
			seconds = 10
		}

		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
			w.checkAll()
		}
	}
}

func (w *Watchdog) checkAll() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.cfg.Enabled {
		return
	}

	now := time.Now()

	for _, app := range w.cfg.Apps {
		if !app.Enabled {
			continue
		}

		running, _, err := proc.IsRunning(app.Path)
		if err != nil {
			slog.Error("process check failed", "app", app.Name, "error", err)
			w.emit("process-check-failed", app.Name, err.Error())
			continue
		}

		if running {
			if w.stableSince[app.Name].IsZero() {
				w.stableSince[app.Name] = now
			}
			if w.retries[app.Name] > 0 &&
				now.Sub(w.stableSince[app.Name]) >= time.Duration(app.ResetAfterSeconds)*time.Second {
				w.retries[app.Name] = 0
				w.emit("retry-counter-reset", app.Name, "aplicação permaneceu estável; contador reposto")
			}
			continue
		}

		w.stableSince[app.Name] = time.Time{}

		if !app.AutoRestart || w.pending[app.Name] {
			continue
		}

		if w.retries[app.Name] >= app.MaxRetries {
			slog.Error("retry limit reached", "app", app.Name, "retries", w.retries[app.Name])
			w.emit("retry-limit-reached", app.Name, fmt.Sprintf("limite de %d tentativas atingido", app.MaxRetries))
			continue
		}

		attempt := w.retries[app.Name] + 1
		delay := retryDelay(w.retries[app.Name])
		w.retries[app.Name] = attempt
		w.pending[app.Name] = true

		slog.Warn("application stopped; scheduling restart",
			"app", app.Name, "retry", attempt, "delay", delay)

		w.emit("process-crash", app.Name, "processo não encontrado")
		w.emit("retry-scheduled", app.Name, fmt.Sprintf("tentativa %d em %s", attempt, delay))

		appCopy := app
		go w.restartAfterDelay(appCopy, attempt, delay)
	}
}

func (w *Watchdog) restartAfterDelay(app config.Application, attempt int, delay time.Duration) {
	time.Sleep(delay)

	err := proc.Start(app.Path, app.WorkingDirectory)

	w.mu.Lock()
	w.pending[app.Name] = false
	if err == nil {
		w.stableSince[app.Name] = time.Now()
	}
	w.mu.Unlock()

	if err != nil {
		slog.Error("application restart failed", "app", app.Name, "retry", attempt, "error", err)
		w.emit("restart-failed", app.Name, fmt.Sprintf("tentativa %d: %v", attempt, err))
		return
	}

	slog.Info("application restarted", "app", app.Name, "retry", attempt)
	w.emit("restart-success", app.Name, fmt.Sprintf("tentativa %d concluída", attempt))
}

func retryDelay(attempt int) time.Duration {
	delays := []time.Duration{5, 10, 30, 60, 120}
	if attempt >= len(delays) {
		attempt = len(delays) - 1
	}
	return delays[attempt] * time.Second
}

func (w *Watchdog) Status() map[string]any {
	w.mu.RLock()
	defer w.mu.RUnlock()

	apps := make([]map[string]any, 0, len(w.cfg.Apps))
	for _, app := range w.cfg.Apps {
		running, pids, _ := proc.IsRunning(app.Path)
		var ids []uint32
		for _, p := range pids {
			ids = append(ids, p.PID)
		}

		apps = append(apps, map[string]any{
			"name": app.Name,
			"enabled": app.Enabled,
			"running": running,
			"restartPending": w.pending[app.Name],
			"pids": ids,
			"retries": w.retries[app.Name],
			"maxRetries": app.MaxRetries,
		})
	}

	return map[string]any{
		"agentId": w.cfg.AgentID,
		"enabled": w.cfg.Enabled,
		"apps": apps,
	}
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
		for _, app := range w.cfg.Apps {
			_ = proc.Stop(app.Path)
			w.pending[app.Name] = false
		}
	case "restart-all":
		for _, app := range w.cfg.Apps {
			if !app.Enabled {
				continue
			}
			_ = proc.Stop(app.Path)
			if err := proc.Start(app.Path, app.WorkingDirectory); err != nil {
				slog.Error("restart failed", "app", app.Name, "error", err)
				w.emit("restart-failed", app.Name, err.Error())
			} else {
				w.pending[app.Name] = false
				w.stableSince[app.Name] = time.Now()
				w.emit("restart-success", app.Name, "reinício remoto concluído")
			}
		}
	case "disable-app", "enable-app", "stop-app", "restart-app":
		idx := -1
		for i := range w.cfg.Apps {
			if w.cfg.Apps[i].Name == appName {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("unknown application %q", appName)
		}

		app := &w.cfg.Apps[idx]
		switch action {
		case "disable-app":
			app.Enabled = false
		case "enable-app":
			app.Enabled = true
		case "stop-app":
			app.Enabled = false
			w.pending[app.Name] = false
			_ = proc.Stop(app.Path)
		case "restart-app":
			_ = proc.Stop(app.Path)
			if err := proc.Start(app.Path, app.WorkingDirectory); err != nil {
				return err
			}
			w.pending[app.Name] = false
			w.stableSince[app.Name] = time.Now()
		}
	default:
		return fmt.Errorf("unknown action %q", action)
	}

	return config.Save(w.configPath, w.cfg)
}
