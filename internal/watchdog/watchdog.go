package watchdog

import (
	"context"
	"errors"
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
	pending        map[string]bool
	startedAt       time.Time
	startupHandled  map[string]bool
	// busy marks applications with a start/stop in flight outside the lock.
	busy            map[string]bool
	// generation changes on every settings update; restarts scheduled for an
	// older generation are dropped.
	generation      uint64
	events          chan Event
}

const (
	startVerifyWindow = 3 * time.Second
	stopTimeout       = 10 * time.Second
)

func New(cfg *config.Config, configPath string) *Watchdog {
	return &Watchdog{
		cfg: cfg,
		configPath: configPath,
		retries: make(map[string]int),
		stableSince: make(map[string]time.Time),
		pending: make(map[string]bool),
		startedAt: time.Now(),
		startupHandled: make(map[string]bool),
		busy: make(map[string]bool),
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
	w.startedAt = time.Now()
	w.startupHandled = make(map[string]bool)
	w.generation++

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
	var initial []config.Application

	w.mu.Lock()
	if !w.cfg.Enabled {
		w.mu.Unlock()
		return
	}

	now := time.Now()

	for _, app := range w.cfg.Apps {
		if !app.Enabled || w.busy[app.Name] {
			continue
		}

		running, _, err := proc.IsRunning(app.Path)
		if err != nil {
			slog.Error("process check failed", "app", app.Name, "error", err)
			w.emit("process-check-failed", app.Name, err.Error())
			continue
		}

		if !w.startupHandled[app.Name] {
			delay := time.Duration(app.StartupDelaySeconds) * time.Second
			elapsed := now.Sub(w.startedAt)

			if elapsed < delay {
				continue
			}

			w.startupHandled[app.Name] = true
			if !running {
				w.busy[app.Name] = true
				initial = append(initial, app)
				continue
			}
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

		go w.restartAfterDelay(app.Name, attempt, delay, w.generation)
	}
	w.mu.Unlock()

	for _, app := range initial {
		go w.initialStart(app)
	}
}

func (w *Watchdog) initialStart(app config.Application) {
	delay := time.Duration(app.StartupDelaySeconds) * time.Second
	pid, err := startApp(app)

	w.mu.Lock()
	delete(w.busy, app.Name)
	if err == nil {
		w.stableSince[app.Name] = time.Now()
	}
	w.mu.Unlock()

	if err != nil {
		slog.Error("initial application start failed", "app", app.Name, "error", err)
		w.emit("startup-failed", app.Name, err.Error())
		return
	}
	slog.Info("application started after startup delay", "app", app.Name, "delay", delay, "pid", pid)
	w.emit("startup-success", app.Name, fmt.Sprintf("iniciada após %s (PID %d)", delay, pid))
}

// restartAfterDelay re-reads the application after the delay so a restart
// scheduled before a settings change, a disable or a manual start is dropped.
func (w *Watchdog) restartAfterDelay(name string, attempt int, delay time.Duration, gen uint64) {
	time.Sleep(delay)

	w.mu.Lock()
	if gen != w.generation {
		w.mu.Unlock()
		return
	}
	app, ok := w.findApp(name)
	if !ok || !app.Enabled || !w.cfg.Enabled || w.busy[name] {
		w.pending[name] = false
		w.mu.Unlock()
		return
	}
	w.busy[name] = true
	w.mu.Unlock()

	var err error
	var pid uint32
	running, _, _ := proc.IsRunning(app.Path)
	if !running {
		pid, err = startApp(app)
	}

	w.mu.Lock()
	delete(w.busy, name)
	if gen == w.generation {
		w.pending[name] = false
		if err == nil {
			w.stableSince[name] = time.Now()
		}
	}
	w.mu.Unlock()

	switch {
	case running:
		w.emit("restart-skipped", name, "aplicação já se encontra em execução")
	case err != nil:
		slog.Error("application restart failed", "app", name, "retry", attempt, "error", err)
		w.emit("restart-failed", name, fmt.Sprintf("tentativa %d: %v", attempt, err))
	default:
		slog.Info("application restarted", "app", name, "retry", attempt, "pid", pid)
		w.emit("restart-success", name, fmt.Sprintf("tentativa %d concluída (PID %d)", attempt, pid))
	}
}

func (w *Watchdog) findApp(name string) (config.Application, bool) {
	for _, app := range w.cfg.Apps {
		if app.Name == name {
			return app, true
		}
	}
	return config.Application{}, false
}

func startApp(app config.Application) (uint32, error) {
	return proc.Start(proc.StartOptions{
		Path:      app.Path,
		Dir:       app.WorkingDirectory,
		Args:      app.Arguments,
		Env:       app.Environment,
		RunAs:     app.RunAs,
		VerifyFor: startVerifyWindow,
	})
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
			"operationPending": w.busy[app.Name],
			"runAs": app.RunAs,
			"pids": ids,
			"retries": w.retries[app.Name],
			"maxRetries": app.MaxRetries,
			"startupDelaySeconds": app.StartupDelaySeconds,
			"startupDelayPending": !w.startupHandled[app.Name],
		})
	}

	return map[string]any{
		"agentId": w.cfg.AgentID,
		"role": w.cfg.Role,
		"enabled": w.cfg.Enabled,
		"apps": apps,
	}
}

type operation struct {
	app           config.Application
	stop          bool
	start         bool
	skipIfRunning bool
}

// Apply executes a remote command. Configuration flags are changed under the
// lock; stopping and starting processes happens outside it so heartbeats and
// the supervision loop are not blocked while processes start or exit.
func (w *Watchdog) Apply(action, appName string) error {
	w.mu.Lock()

	var ops []operation
	switch action {
	case "disable-all":
		w.cfg.Enabled = false
	case "enable-all":
		w.cfg.Enabled = true
	case "stop-all":
		for _, app := range w.cfg.Apps {
			ops = append(ops, operation{app: app, stop: true})
		}
	case "start-all", "restart-all":
		for _, app := range w.cfg.Apps {
			if !app.Enabled {
				continue
			}
			restart := action == "restart-all"
			ops = append(ops, operation{app: app, stop: restart, start: true, skipIfRunning: !restart})
		}
	case "disable-app", "enable-app", "stop-app", "start-app", "restart-app":
		idx := -1
		for i := range w.cfg.Apps {
			if w.cfg.Apps[i].Name == appName {
				idx = i
				break
			}
		}
		if idx < 0 {
			w.mu.Unlock()
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
			ops = append(ops, operation{app: *app, stop: true})
		case "start-app":
			ops = append(ops, operation{app: *app, start: true, skipIfRunning: true})
		case "restart-app":
			ops = append(ops, operation{app: *app, stop: true, start: true})
		}
	default:
		w.mu.Unlock()
		return fmt.Errorf("unknown action %q", action)
	}

	var errs []error
	runnable := ops[:0]
	for _, op := range ops {
		name := op.app.Name
		if w.busy[name] {
			errs = append(errs, fmt.Errorf("%s: já existe uma operação em curso", name))
			continue
		}
		w.busy[name] = true
		w.pending[name] = false
		if op.start {
			w.retries[name] = 0
		}
		runnable = append(runnable, op)
	}

	errs = append(errs, config.Save(w.configPath, w.cfg))
	w.mu.Unlock()

	var wg sync.WaitGroup
	results := make([]error, len(runnable))
	for i, op := range runnable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = w.runOperation(op)
		}()
	}
	wg.Wait()

	return errors.Join(append(errs, results...)...)
}

func (w *Watchdog) runOperation(op operation) error {
	name := op.app.Name
	err := w.execOperation(op)

	w.mu.Lock()
	delete(w.busy, name)
	if op.start && err == nil {
		w.stableSince[name] = time.Now()
	}
	w.mu.Unlock()

	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func (w *Watchdog) execOperation(op operation) error {
	name := op.app.Name
	failed, succeeded := "stop-failed", "stopped"
	if op.start {
		failed, succeeded = "start-failed", "start-success"
		if op.stop {
			failed, succeeded = "restart-failed", "restart-success"
		}
	}

	if op.stop {
		if err := proc.Stop(op.app.Path, stopTimeout); err != nil {
			slog.Error("stop failed", "app", name, "error", err)
			w.emit(failed, name, "não foi possível parar: "+err.Error())
			return err
		}
	}
	if !op.start {
		w.emit(succeeded, name, "paragem remota concluída")
		return nil
	}

	if op.skipIfRunning {
		if running, _, _ := proc.IsRunning(op.app.Path); running {
			w.emit("start-skipped", name, "aplicação já se encontra em execução")
			return nil
		}
	}

	pid, err := startApp(op.app)
	if err != nil {
		slog.Error("remote start failed", "app", name, "error", err)
		w.emit(failed, name, err.Error())
		return err
	}
	slog.Info("application started remotely", "app", name, "pid", pid)
	w.emit(succeeded, name, fmt.Sprintf("arranque remoto concluído (PID %d)", pid))
	return nil
}
