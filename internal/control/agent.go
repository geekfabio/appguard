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

func sendHeartbeat(client *http.Client, cfg *config.Config, w *watchdog.Watchdog) {
	hb := Heartbeat{AgentID: cfg.AgentID, Timestamp: time.Now(), Status: w.Status()}
	b, _ := json.Marshal(hb)
	req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.Controller.URL, "/")+"/api/v1/heartbeat", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+cfg.Controller.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("controller heartbeat failed", "error", err)
		return
	}
	_ = resp.Body.Close()
}

func fetchCommands(client *http.Client, cfg *config.Config, w *watchdog.Watchdog) {
	url := strings.TrimRight(cfg.Controller.URL, "/") + "/api/v1/commands/" + cfg.AgentID
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Controller.Token)
	resp, err := client.Do(req)
	if err != nil { return }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return
	}
	var cmds []Command
	if err := json.NewDecoder(resp.Body).Decode(&cmds); err != nil { return }
	for _, cmd := range cmds {
		if err := w.Apply(cmd.Action, cmd.AppName); err != nil {
			slog.Error("remote command failed", "command", cmd.Action, "error", err)
		} else {
			slog.Info("remote command applied", "command", cmd.Action)
		}
	}
}
