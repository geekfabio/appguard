package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"appguard/internal/config"
	"appguard/internal/watchdog"
)

func RunAgentClient(ctx context.Context, cfg *config.Config, w *watchdog.Watchdog) {
	client := &http.Client{Timeout: 5 * time.Second}
	t := time.NewTicker(time.Duration(cfg.Controller.PollEverySeconds) * time.Second)
	defer t.Stop()

	go forwardWatchdogEvents(ctx, client, cfg, w.Events())

	runOnce := func() {
		sendHeartbeat(client, cfg, w)
		fetchCommands(client, cfg, w)
	}
	runOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runOnce()
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
	req.Header.Set("Authorization", "Bearer "+cfg.Controller.Token)
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
	req.Header.Set("Authorization", "Bearer "+cfg.Controller.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("controller heartbeat failed", "error", err)
		return
	}
	_ = resp.Body.Close()
}

func ackCommand(client *http.Client, cfg *config.Config, commandID string) {
	if commandID == "" {
		return
	}
	req, _ := http.NewRequest(
		http.MethodPost,
		strings.TrimRight(cfg.Controller.URL, "/")+"/api/v1/commands/ack/"+commandID,
		nil,
	)
	req.Header.Set("Authorization", "Bearer "+cfg.Controller.Token)
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("command ack failed", "commandId", commandID, "error", err)
		return
	}
	_ = resp.Body.Close()
}

func fetchCommands(client *http.Client, cfg *config.Config, w *watchdog.Watchdog) {
	url := strings.TrimRight(cfg.Controller.URL, "/") + "/api/v1/commands/" + cfg.AgentID
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Controller.Token)

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
		var err error
		switch cmd.Action {
		case "apply-settings":
			var settings config.AgentSettings
			if e := json.Unmarshal(cmd.Payload, &settings); e != nil {
				err = e
			} else {
				err = w.ApplySettings(settings)
			}
		default:
			err = w.Apply(cmd.Action, cmd.AppName)
		}

		if err != nil {
			slog.Error("remote command failed", "command", cmd.Action, "error", err)
			sendEvent(client, cfg, Event{
				Time: time.Now(),
				AgentID: cfg.AgentID,
				Group: w.Settings().Group,
				Action: "remote-command-failed",
				AppName: cmd.AppName,
				Detail: cmd.Action + ": " + err.Error(),
			})
		} else {
			slog.Info("remote command applied", "command", cmd.Action, "app", cmd.AppName)
		}
		ackCommand(client, cfg, cmd.ID)
	}
}
