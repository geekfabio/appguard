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
	proc "appguard/internal/process"
	"appguard/internal/update"
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

// AgentOptions enables self-update. Restart must restart the service so the
// newly installed binary runs.
type AgentOptions struct {
	Updater *update.Updater
	Restart func()
}

func RunAgentClient(ctx context.Context, cfg *config.Config, w *watchdog.Watchdog, opts AgentOptions) {
	client := &http.Client{Timeout: 5 * time.Second}
	every := time.Duration(cfg.Controller.PollEverySeconds) * time.Second

	go forwardWatchdogEvents(ctx, client, cfg, w.Events())

	// Heartbeats run on their own loop so a slow command (stop + start can
	// take several seconds) never makes the agent look offline.
	go runEvery(ctx, every, func() {
		resp, ok := sendHeartbeat(client, cfg, w, opts.Updater)
		if ok {
			handleSelfUpdate(ctx, client, cfg, w, opts, resp)
		}
	})

	done := newCommandLog(200)
	runEvery(ctx, every, func() { fetchCommands(ctx, client, cfg, w, done) })
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

func sendHeartbeat(client *http.Client, cfg *config.Config, w *watchdog.Watchdog, upd *update.Updater) (HeartbeatResponse, bool) {
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
	if upd != nil {
		hb.BinarySHA256 = upd.SHA256()
		hb.SelfUpdate = true
	}
	b, _ := json.Marshal(hb)

	req, _ := http.NewRequest(
		http.MethodPost,
		strings.TrimRight(cfg.Controller.URL, "/")+"/api/v1/heartbeat",
		bytes.NewReader(b),
	)
	req.Header.Set("Content-Type", "application/json")

	var out HeartbeatResponse
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("controller heartbeat failed", "error", err)
		return out, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, false
	}
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&out)
	}
	return out, true
}

// handleSelfUpdate reports the outcome of a previous update and installs the
// binary offered by the controller.
func handleSelfUpdate(ctx context.Context, client *http.Client, cfg *config.Config, w *watchdog.Watchdog, opts AgentOptions, resp HeartbeatResponse) {
	upd := opts.Updater
	if upd == nil {
		return
	}

	upd.ConfirmHealthy()
	if r := upd.TakeResult(); r != nil {
		sendEvent(client, cfg, Event{
			Time: time.Now(), AgentID: cfg.AgentID, Group: w.Settings().Group,
			Action: r.Action, Detail: r.Detail,
		})
	}

	m := resp.Update
	if m == nil || !upd.Claim(*m) {
		return
	}

	go func() {
		group := w.Settings().Group
		sendEvent(client, cfg, Event{
			Time: time.Now(), AgentID: cfg.AgentID, Group: group,
			Action: "agent-update-started", Detail: fmt.Sprintf("a descarregar versão %s", m.Version),
		})

		download := &http.Client{Timeout: 10 * time.Minute}
		url := strings.TrimRight(cfg.Controller.URL, "/") + "/api/v1/update/agent"
		if err := upd.Install(ctx, download, url, *m); err != nil {
			slog.Error("self-update failed", "version", m.Version, "error", err)
			sendEvent(client, cfg, Event{
				Time: time.Now(), AgentID: cfg.AgentID, Group: group,
				Action: "agent-update-failed", Detail: fmt.Sprintf("versão %s: %v", m.Version, err),
			})
			return
		}

		slog.Info("self-update installed; restarting service", "version", m.Version)
		sendEvent(client, cfg, Event{
			Time: time.Now(), AgentID: cfg.AgentID, Group: group,
			Action: "agent-update-restarting", Detail: fmt.Sprintf("versão %s instalada; a reiniciar o serviço", m.Version),
		})
		if opts.Restart != nil {
			opts.Restart()
		}
	}()
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

func fetchCommands(ctx context.Context, client *http.Client, cfg *config.Config, w *watchdog.Watchdog, done *commandLog) {
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

		result := executeCommand(ctx, w, cmd)
		if result.Status == CommandFailed {
			slog.Error("remote command failed", "command", cmd.Action, "app", cmd.AppName, "error", result.Error)
		} else {
			slog.Info("remote command applied", "command", cmd.Action, "app", cmd.AppName)
		}
		done.put(cmd.ID, result)
		ackCommand(client, cfg, cmd.ID, result)
	}
}

func executeCommand(ctx context.Context, w *watchdog.Watchdog, cmd Command) CommandResult {
	switch cmd.Action {
	case "run-powershell":
		return runPowerShellCommand(ctx, cmd)
	case "apply-settings":
		var settings config.AgentSettings
		if err := json.Unmarshal(cmd.Payload, &settings); err != nil {
			return CommandResult{Status: CommandFailed, Error: err.Error()}
		}
		return resultFromErr(w.ApplySettings(settings))
	default:
		return resultFromErr(w.Apply(cmd.Action, cmd.AppName))
	}
}

func runPowerShellCommand(ctx context.Context, cmd Command) CommandResult {
	var req PowerShellRequest
	if err := json.Unmarshal(cmd.Payload, &req); err != nil {
		return CommandResult{Status: CommandFailed, Error: "payload inválido: " + err.Error()}
	}
	if strings.TrimSpace(req.Script) == "" {
		return CommandResult{Status: CommandFailed, Error: "script vazio"}
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	output, err := proc.RunPowerShell(ctx, req.Script, timeout)
	result := CommandResult{Status: CommandOK, Output: output}
	if err != nil {
		result.Status = CommandFailed
		result.Error = err.Error()
	}
	return result
}

func resultFromErr(err error) CommandResult {
	if err != nil {
		return CommandResult{Status: CommandFailed, Error: err.Error()}
	}
	return CommandResult{Status: CommandOK}
}
