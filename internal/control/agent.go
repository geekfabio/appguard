package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"appguard/internal/buildinfo"
	"appguard/internal/config"
	"appguard/internal/watchdog"
)

func Enroll(cfg *config.Config) error {
	payload, _ := json.Marshal(Enrollment{
		AgentID: cfg.AgentID,
		Group: cfg.Group,
		Version: buildinfo.Version,
	})
	req, _ := http.NewRequest(
		http.MethodPost,
		strings.TrimRight(cfg.Controller.URL, "/")+"/api/v1/enroll",
		bytes.NewReader(payload),
	)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("enrollment failed: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func RunAgentClient(ctx context.Context, cfg *config.Config, w *watchdog.Watchdog) {
	client := &http.Client{Timeout: 5 * time.Second}
	every := time.Duration(cfg.Controller.PollEverySeconds) * time.Second

	go forwardWatchdogEvents(ctx, client, cfg, w.Events())

	// Heartbeats run on their own loop so a slow command (stop + start can
	// take several seconds) never makes the agent look offline.
	go runEvery(ctx, every, func() { sendHeartbeat(client, cfg, w) })

	done := newCommandLog(200)
	runEvery(ctx, every, func() { fetchCommands(client, cfg, w, done) })
}

func runEvery(ctx context.Context, every time.Duration, fn func()) {
	t := time.NewTicker(every)
	defer t.Stop()

	fn()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

func forwardWatchdogEvents(ctx context.Context, client *http.Client, cfg *config.Config, events <-chan watchdog.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-events:
			sendEvent(client, cfg, Event{
				Time: e.Time,
				AgentID: cfg.AgentID,
				Group: cfg.Group,
				Action: e.Action,
				AppName: e.AppName,
				Detail: e.Detail,
			})
		}
	}
}

func sendEvent(client *http.Client, cfg *config.Config, event Event) {
	b, _ := json.Marshal(event)
	req, _ := http.NewRequest(
		http.MethodPost,
		strings.TrimRight(cfg.Controller.URL, "/")+"/api/v1/agent-events",
		bytes.NewReader(b),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("controller event send failed", "error", err)
		return
	}
	_ = resp.Body.Close()
}

func sendHeartbeat(client *http.Client, cfg *config.Config, w *watchdog.Watchdog) {
	settings := w.Settings()
	status := w.Status()
	status["settings"] = settings

	hb := Heartbeat{
		AgentID: cfg.AgentID,
		Group: settings.Group,
		Version: buildinfo.Version,
		Timestamp: time.Now(),
		Status: status,
		Settings: settings,
	}
	b, _ := json.Marshal(hb)

	req, _ := http.NewRequest(
		http.MethodPost,
		strings.TrimRight(cfg.Controller.URL, "/")+"/api/v1/heartbeat",
		bytes.NewReader(b),
	)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("controller heartbeat failed", "error", err)
		return
	}
	_ = resp.Body.Close()
}

func ackCommand(client *http.Client, cfg *config.Config, commandID string, result CommandResult) bool {
	if commandID == "" {
		return true
	}
	b, _ := json.Marshal(result)
	req, _ := http.NewRequest(
		http.MethodPost,
		strings.TrimRight(cfg.Controller.URL, "/")+"/api/v1/commands/ack/"+url.PathEscape(commandID),
		bytes.NewReader(b),
	)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("command ack failed", "commandId", commandID, "error", err)
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// commandLog remembers results of recently executed commands so a command
// whose ack was lost is acknowledged again instead of being executed twice.
type commandLog struct {
	max     int
	order   []string
	results map[string]CommandResult
}

func newCommandLog(max int) *commandLog {
	return &commandLog{max: max, results: make(map[string]CommandResult)}
}

func (l *commandLog) get(id string) (CommandResult, bool) {
	r, ok := l.results[id]
	return r, ok
}

func (l *commandLog) put(id string, r CommandResult) {
	if _, ok := l.results[id]; !ok {
		l.order = append(l.order, id)
		if len(l.order) > l.max {
			delete(l.results, l.order[0])
			l.order = l.order[1:]
		}
	}
	l.results[id] = r
}

func fetchCommands(client *http.Client, cfg *config.Config, w *watchdog.Watchdog, done *commandLog) {
	url := strings.TrimRight(cfg.Controller.URL, "/") + "/api/v1/commands/" + cfg.AgentID
	req, _ := http.NewRequest(http.MethodGet, url, nil)

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return
	}

	var cmds []Command
	if err := json.NewDecoder(resp.Body).Decode(&cmds); err != nil {
		return
	}

	for _, cmd := range cmds {
		if result, ok := done.get(cmd.ID); ok {
			ackCommand(client, cfg, cmd.ID, result)
			continue
		}

		err := executeCommand(w, cmd)
		result := CommandResult{Status: CommandOK}
		if err != nil {
			slog.Error("remote command failed", "command", cmd.Action, "app", cmd.AppName, "error", err)
			result = CommandResult{Status: CommandFailed, Error: err.Error()}
		} else {
			slog.Info("remote command applied", "command", cmd.Action, "app", cmd.AppName)
		}
		done.put(cmd.ID, result)
		ackCommand(client, cfg, cmd.ID, result)
	}
}

func executeCommand(w *watchdog.Watchdog, cmd Command) error {
	if cmd.Action != "apply-settings" {
		return w.Apply(cmd.Action, cmd.AppName)
	}
	var settings config.AgentSettings
	if err := json.Unmarshal(cmd.Payload, &settings); err != nil {
		return err
	}
	return w.ApplySettings(settings)
}
