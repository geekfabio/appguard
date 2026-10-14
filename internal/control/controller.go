package control

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"appguard/internal/buildinfo"
	"appguard/internal/config"
	"appguard/internal/storage"
)

const offlineAfter = 20 * time.Second

type controllerState struct {
	mu      sync.RWMutex
	db      *storage.DB
	release *agentRelease
	guard   *authGuard
}

type agentView struct {
	AgentID   string         `json:"agentId"`
	Group     string         `json:"group"`
	LastSeen  time.Time      `json:"lastSeen"`
	Online    bool           `json:"online"`
	Status    map[string]any `json:"status"`
	// Update is the agent's version state against the controller binary.
	Update    string         `json:"update,omitempty"`
	// Pending is the number of commands queued but not yet delivered.
	Pending   int            `json:"pending,omitempty"`
}

func RunController(cfg *config.Config) error {
	db, err := storage.Open(cfg.Controller.DatabasePath)
	if err != nil {
		return fmt.Errorf("open controller database: %w", err)
	}
	defer db.Close()

	st := &controllerState{
		db: db,
		release: newAgentRelease(cfg.Controller.AgentUpdates),
		guard: newAuthGuard(cfg.Controller.Auth),
	}

	if cfg.Role == "controller" {
		status := map[string]any{
			"role": "controller",
			"version": buildinfo.Version,
			"enabled": cfg.Enabled,
			"apps": []any{},
		}
		_ = st.db.UpsertAgent(cfg.AgentID, cfg.Group, time.Now(), status)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"status": "ok",
			"service": "appguard-controller",
			"time": time.Now().UTC(),
		})
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.db.Ping(); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{
			"status": "ready",
			"database": "ok",
		})
	})

	mux.HandleFunc("/login", st.guard.handleLogin)
	mux.HandleFunc("/logout", st.guard.handleLogout)

	mux.HandleFunc("/", st.guard.protectPage(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := strings.ReplaceAll(dashboardHTML, "{{VERSION}}", "v"+buildinfo.Version)
		_, _ = w.Write([]byte(page))
	}))

	mux.HandleFunc("/agent/", st.guard.protectPage(func(w http.ResponseWriter, r *http.Request) {
		agentID := strings.TrimPrefix(r.URL.Path, "/agent/")
		if agentID == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := strings.ReplaceAll(agentHTML, "{{AGENT_ID}}", templateJS(agentID))
		_, _ = w.Write([]byte(page))
	}))

	mux.HandleFunc("/powershell", st.guard.protectPage(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(powershellHTML))
	}))

	mux.HandleFunc("/api/v1/enroll", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req Enrollment
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.AgentID == "" {
			http.Error(w, "missing agentId", http.StatusBadRequest)
			return
		}
		if req.Group == "" {
			req.Group = "default"
		}

		status := map[string]any{
			"version": req.Version,
			"enrolled": true,
			"apps": []any{},
		}
		if err := st.db.UpsertAgent(req.AgentID, req.Group, time.Now(), status); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		st.addEvent(Event{
			Time: time.Now(),
			AgentID: req.AgentID,
			Group: req.Group,
			Action: "agent-enrolled",
			Detail: "Agente registado manualmente no controlador",
		})
		writeJSON(w, map[string]any{
			"enrolled": true,
			"agentId": req.AgentID,
			"version": req.Version,
		})
	}))

	mux.HandleFunc("/api/v1/heartbeat", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var hb Heartbeat
		if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if hb.AgentID == "" {
			http.Error(w, "missing agentId", http.StatusBadRequest)
			return
		}
		if hb.Group == "" {
			hb.Group = "default"
		}

		hb.Timestamp = time.Now()
		if hb.Status == nil {
			hb.Status = map[string]any{}
		}
		hb.Status["version"] = hb.Version
		hb.Status["settings"] = hb.Settings
		hb.Status["binarySha256"] = hb.BinarySHA256
		hb.Status["selfUpdate"] = hb.SelfUpdate

		previous, previousErr := st.db.GetAgent(hb.AgentID)
		if err := st.db.UpsertAgent(hb.AgentID, hb.Group, hb.Timestamp, hb.Status); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if previousErr == sql.ErrNoRows {
			st.addEvent(Event{
				Time: time.Now(), AgentID: hb.AgentID, Group: hb.Group,
				Action: "agent-online", Detail: "Agente registado no controlador",
			})
		} else if previous != nil && time.Since(previous.LastSeen) > offlineAfter {
			st.addEvent(Event{
				Time: time.Now(), AgentID: hb.AgentID, Group: hb.Group,
				Action: "agent-reconnected", Detail: "Agente voltou a comunicar com o controlador",
			})
		}

		if m := st.release.offer(hb); m != nil {
			writeJSON(w, HeartbeatResponse{Update: m})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/update/agent", st.auth(st.release.serve))

	mux.HandleFunc("/api/v1/agent-events", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if e.Action == "" {
			http.Error(w, "missing action", http.StatusBadRequest)
			return
		}

		st.addEvent(Event{
			Time: e.Time,
			AgentID: e.AgentID,
			Group: e.Group,
			Action: e.Action,
			AppName: e.AppName,
			Detail: e.Detail,
		})
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/commands/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/commands/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}

		stored, err := st.db.FetchPendingCommands(agentID, 50)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		cmds := make([]Command, 0, len(stored))
		for _, item := range stored {
			cmds = append(cmds, Command{
				ID: item.ID,
				Action: item.Action,
				AppName: item.AppName,
				Payload: item.Payload,
			})
		}
		writeJSON(w, cmds)
	}))

	mux.HandleFunc("/api/v1/commands/ack/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		commandID := strings.TrimPrefix(r.URL.Path, "/api/v1/commands/ack/")
		if commandID == "" {
			http.Error(w, "missing command id", http.StatusBadRequest)
			return
		}
		// Agents older than this release send no body: treat as success.
		result := CommandResult{Status: CommandOK}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil && err != io.EOF {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if result.Status != CommandFailed {
			result.Status = CommandOK
		}

		cmd, err := st.db.AckCommand(commandID, result.Status, result.Error, result.Output)
		if err == sql.ErrNoRows {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		event := Event{
			Time: time.Now(), AgentID: cmd.AgentID, AppName: cmd.AppName,
			Action: "command-completed", Detail: cmd.Action + ": concluído",
		}
		if a, err := st.db.GetAgent(cmd.AgentID); err == nil {
			event.Group = a.Group
		}
		if result.Status == CommandFailed {
			event.Action = "remote-command-failed"
			event.Detail = cmd.Action + ": " + result.Error
		}
		st.addEvent(event)
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/admin/agents", st.admin(func(w http.ResponseWriter, r *http.Request) {
		agents, err := st.db.ListAgents()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		now := time.Now()
		result := make([]agentView, 0, len(agents))
		for _, a := range agents {
			pending, _ := st.db.PendingCommandCount(a.AgentID)
			result = append(result, agentView{
				AgentID: a.AgentID,
				Group: a.Group,
				LastSeen: a.LastSeen,
				Online: now.Sub(a.LastSeen) <= offlineAfter,
				Status: a.Status,
				Update: st.release.state(a.Status),
				Pending: pending,
			})
		}
		writeJSON(w, result)
	}))

	mux.HandleFunc("/api/v1/admin/agent-detail/", st.admin(func(w http.ResponseWriter, r *http.Request) {
		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agent-detail/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}

		a, err := st.db.GetAgent(agentID)
		if err == sql.ErrNoRows {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		events := st.listEvents(agentID, 200)

		writeJSON(w, map[string]any{
			"agent": agentView{
				AgentID: a.AgentID,
				Group: a.Group,
				LastSeen: a.LastSeen,
				Online: time.Since(a.LastSeen) <= offlineAfter,
				Status: a.Status,
				Update: st.release.state(a.Status),
			},
			"events": events,
		})
	}))

	mux.HandleFunc("/api/v1/admin/events", st.admin(st.handleEvents))

	mux.HandleFunc("/api/v1/admin/agent-remove/", st.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agent-remove/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}
		removed, err := st.db.DeleteAgent(agentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !removed {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		st.addEvent(Event{
			Time: time.Now(), AgentID: agentID, Action: "agent-removed",
			Detail: "Agente removido do controlador",
		})
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/admin/broadcast/", st.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		action := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/broadcast/")
		if !validAction(action) {
			http.Error(w, "invalid action", http.StatusBadRequest)
			return
		}

		agents, err := st.db.ListAgents()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		st.mu.Lock()
		for _, a := range agents {
			st.enqueueLocked(a.AgentID, action, "")
		}
		st.mu.Unlock()

		st.addEvent(Event{
			Time: time.Now(), Action: action, Detail: "Comando global enviado",
		})
		w.WriteHeader(http.StatusAccepted)
	}))

	mux.HandleFunc("/api/v1/admin/agent/", st.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agent/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || !validAction(parts[1]) {
			http.Error(w, "expected /agent/{id}/{action}", http.StatusBadRequest)
			return
		}

		agentID, action := parts[0], parts[1]
		a, err := st.db.GetAgent(agentID)
		if err == sql.ErrNoRows {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		st.mu.Lock()
		st.enqueueLocked(agentID, action, "")
		st.mu.Unlock()

		st.addEvent(Event{
			Time: time.Now(), AgentID: agentID, Group: a.Group,
			Action: action, Detail: "Comando enviado ao agente",
		})

		w.WriteHeader(http.StatusAccepted)
	}))

	mux.HandleFunc("/api/v1/admin/group/", st.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/group/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || !validAction(parts[1]) {
			http.Error(w, "expected /group/{group}/{action}", http.StatusBadRequest)
			return
		}

		group, action := parts[0], parts[1]
		agents, err := st.db.ListAgents()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		count := 0
		st.mu.Lock()
		for _, a := range agents {
			if a.Group == group {
				st.enqueueLocked(a.AgentID, action, "")
				count++
			}
		}
		st.mu.Unlock()

		st.addEvent(Event{
			Time: time.Now(), Group: group, Action: action,
			Detail: fmt.Sprintf("Comando enviado a %d agente(s)", count),
		})

		writeJSON(w, map[string]any{"accepted": count})
	}))

	mux.HandleFunc("/api/v1/admin/agent-app/", st.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		rest := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agent-app/")
		parts := strings.Split(rest, "/")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || !validAppAction(parts[2]) {
			http.Error(w, "expected /agent-app/{agentId}/{appName}/{action}", http.StatusBadRequest)
			return
		}

		agentID, appName, action := parts[0], parts[1], parts[2]
		a, err := st.db.GetAgent(agentID)
		if err == sql.ErrNoRows {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := st.db.QueueCommand(storage.Command{
			ID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), agentID),
			AgentID: agentID,
			Action: action,
			AppName: appName,
			Created: time.Now(),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		st.addEvent(Event{
			Time: time.Now(),
			AgentID: agentID,
			Group: a.Group,
			Action: action,
			AppName: appName,
			Detail: "Comando por aplicação colocado na fila",
		})

		w.WriteHeader(http.StatusAccepted)
	}))

	mux.HandleFunc("/api/v1/admin/agent-settings/", st.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/agent-settings/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}
		if _, err := st.db.GetAgent(agentID); err == sql.ErrNoRows {
			http.Error(w, "unknown agent", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var settings config.AgentSettings
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		config.NormalizeSettings(&settings)

		payload, err := json.Marshal(settings)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := st.db.QueueCommand(storage.Command{
			ID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), agentID),
			AgentID: agentID,
			Action: "apply-settings",
			Payload: payload,
			Created: time.Now(),
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		st.addEvent(Event{
			Time: time.Now(), AgentID: agentID, Group: settings.Group,
			Action: "apply-settings", Detail: "Configuração remota colocada na fila",
		})

		writeJSON(w, map[string]any{"queued": true})
	}))

	mux.HandleFunc("/api/v1/admin/powershell", st.admin(st.handlePowerShell))
	mux.HandleFunc("/api/v1/admin/powershell/results", st.admin(st.handlePowerShellResults))

	go st.pruneLoop()

	slog.Info("AppGuard controller listening",
		"addr", cfg.Controller.Listen,
		"database", cfg.Controller.DatabasePath)

	server := &http.Server{
		Addr: cfg.Controller.Listen,
		Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

// pruneLoop keeps stored logs small: events and delivered commands older
// than two days are deleted on start and every six hours.
func (s *controllerState) pruneLoop() {
	prune := func() {
		if n, err := s.db.PruneEvents(); err != nil {
			slog.Error("prune events failed", "error", err)
		} else if n > 0 {
			slog.Debug("pruned old events", "count", n)
		}
		if n, err := s.db.PruneCommands(); err != nil {
			slog.Error("prune commands failed", "error", err)
		} else if n > 0 {
			slog.Debug("pruned old commands", "count", n)
		}
	}
	prune()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for range t.C {
		prune()
	}
}

func (s *controllerState) addEvent(event Event) {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	if err := s.db.AppendEvent(storage.EventRecord{
		Time: event.Time, AgentID: event.AgentID, Group: event.Group,
		Action: event.Action, AppName: event.AppName, Detail: event.Detail,
	}); err != nil {
		slog.Error("persist event failed", "action", event.Action, "error", err)
	}
}

func (s *controllerState) listEvents(agentID string, limit int) []Event {
	records, err := s.db.ListEvents(storage.EventFilter{AgentID: agentID, Limit: limit})
	if err != nil {
		slog.Error("list events failed", "error", err)
		return nil
	}
	return toEvents(records)
}

func toEvents(records []storage.EventRecord) []Event {
	out := make([]Event, 0, len(records))
	for _, r := range records {
		out = append(out, Event{
			Time: r.Time, AgentID: r.AgentID, Group: r.Group,
			Action: r.Action, AppName: r.AppName, Detail: r.Detail,
		})
	}
	return out
}

func (s *controllerState) enqueueLocked(agentID, action, appName string) {
	_ = s.db.QueueCommand(storage.Command{
		ID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), agentID),
		AgentID: agentID,
		Action: action,
		AppName: appName,
		Created: time.Now(),
	})
}

// auth leaves agent-facing endpoints open (agents hold no credentials).
func (s *controllerState) auth(next http.HandlerFunc) http.HandlerFunc {
	return next
}

// admin protects an endpoint used only by the dashboard operator.
func (s *controllerState) admin(next http.HandlerFunc) http.HandlerFunc {
	return s.guard.protectAPI(next)
}

func validAction(a string) bool {
	switch a {
	case "disable-all", "enable-all", "stop-all", "start-all", "restart-all":
		return true
	default:
		return false
	}
}

func validAppAction(a string) bool {
	switch a {
	case "disable-app", "enable-app", "stop-app", "start-app", "restart-app":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func templateJS(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

const dashboardHTML = `<!doctype html>
<html lang="pt">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AppGuard Control Center</title>
<style>
:root{font-family:Inter,ui-sans-serif,system-ui,-apple-system,Segoe UI,sans-serif;color:#e8edf7;background:#07111f}
*{box-sizing:border-box}body{margin:0;background:linear-gradient(135deg,#07111f,#0c1d34 55%,#07111f);min-height:100vh}
header{padding:24px 30px;border-bottom:1px solid #20344e;background:#091522e8;position:sticky;top:0;z-index:3}
h1{font-size:22px;margin:0}.sub{color:#8fa5bf;font-size:13px;margin-top:4px}
main{padding:26px 30px;max-width:1500px;margin:auto}.cards{display:grid;grid-template-columns:repeat(5,minmax(130px,1fr));gap:14px;margin-bottom:22px}
.card,.panel{background:#0d1c2d;border:1px solid #20344e;border-radius:14px;box-shadow:0 8px 28px #0004}
.card{padding:18px}.metric{font-size:30px;font-weight:700}.label{font-size:11px;color:#8fa5bf;text-transform:uppercase;letter-spacing:.08em}
.toolbar{display:flex;gap:9px;flex-wrap:wrap;margin:16px 0}.btn{background:#183451;color:#fff;border:1px solid #2a4b6b;border-radius:9px;padding:8px 12px;cursor:pointer}.btn:hover{background:#214665}
.btn.warn{background:#5b2c22;border-color:#824034}.btn.good{background:#164733;border-color:#246849}
.panel{overflow:hidden;margin-bottom:20px}.panel h2{font-size:15px;margin:0;padding:15px 18px;border-bottom:1px solid #20344e}
table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:12px 14px;border-bottom:1px solid #172b42;font-size:13px}th{color:#8fa5bf}
.badge{display:inline-flex;align-items:center;gap:7px;padding:5px 8px;border-radius:999px;background:#17304a}.dot{width:8px;height:8px;border-radius:50%;background:#ef5d5d}.online .dot{background:#46d887}
.events{max-height:330px;overflow:auto}.event{padding:10px 16px;border-bottom:1px solid #172b42;font-size:12px;color:#b9c8dc}.muted{color:#70869f}
input,select,textarea{background:#07111f;border:1px solid #2b4564;color:#fff;border-radius:8px;padding:9px 11px}input{min-width:240px}textarea{width:100%;min-height:260px;font-family:Consolas,monospace}a{color:#8fc7ff;text-decoration:none}
@media(max-width:900px){.cards{grid-template-columns:1fr 1fr}main,header{padding-left:15px;padding-right:15px}table{display:block;overflow:auto}}
</style>
</head>
<body>
<header><a href="/logout" class="btn" style="float:right">Sair</a><a href="/powershell" class="btn" style="float:right;margin-right:8px">PowerShell</a><h1>AppGuard Control Center <span class="muted">{{VERSION}}</span></h1><div class="sub">Supervisão central e estado em tempo real de processos Windows</div></header>
<main>
<div class="toolbar"><input id="search" placeholder="Pesquisar computador ou grupo" oninput="renderAgents()"><select id="groupFilter" onchange="renderAgents()"><option value="">Todos os grupos</option></select><select id="stateFilter" onchange="renderAgents()"><option value="">Todos os estados</option><option value="online">Online</option><option value="offline">Offline</option></select></div>
<div class="cards">
<div class="card"><div class="label">Computadores</div><div class="metric" id="total">0</div></div>
<div class="card"><div class="label">Online</div><div class="metric" id="online">0</div></div>
<div class="card"><div class="label">Offline</div><div class="metric" id="offline">0</div></div>
<div class="card"><div class="label">Processos activos</div><div class="metric" id="running">0</div></div>
<div class="card"><div class="label">Falhas recentes</div><div class="metric" id="failures">0</div></div>
</div>
<div class="toolbar">
<button class="btn good" onclick="broadcast('enable-all')">Activar todos</button>
<button class="btn good" onclick="broadcast('start-all')">Abrir todos</button>
<button class="btn" onclick="broadcast('restart-all')">Reiniciar todos</button>
<button class="btn warn" onclick="broadcast('stop-all')">Parar todos</button>
<button class="btn warn" onclick="broadcast('disable-all')">Desactivar watchdog</button>
</div>
<section class="panel"><h2>Agentes</h2>
<table><thead><tr><th>Computador</th><th>Grupo</th><th>Versão</th><th>Estado</th><th>Processos</th><th>Retries</th><th>Fila</th><th>Último contacto</th><th>Acções</th></tr></thead><tbody id="agents"></tbody></table>
</section>
<section class="panel"><h2>Eventos</h2>
<div class="toolbar" style="padding:0 16px">
<select id="evAction" onchange="renderEvents()"><option value="">Todas as acções</option></select>
<select id="evHours" onchange="renderEvents()"><option value="">Qualquer altura</option><option value="1">Última hora</option><option value="24">Últimas 24h</option><option value="48">Últimos 2 dias</option></select>
<a class="btn" id="csv" href="#" target="_blank">Exportar CSV</a>
<span class="muted" style="align-self:center">Guardados 2 dias</span>
</div>
<div class="events" id="events"></div></section>
</main>
<script>
let cachedAgents=[];
async function api(path,options={}){options.headers={...(options.headers||{})};const r=await fetch(path,options);if(!r.ok)throw new Error(await r.text());if(r.status===204)return null;return r.json()}
async function commandAgent(id,action){await api('/api/v1/admin/agent/'+encodeURIComponent(id)+'/'+action,{method:'POST'});setTimeout(refresh,500)}
async function broadcast(action){if(!confirm('Executar '+action+' em todos os agentes?'))return;await api('/api/v1/admin/broadcast/'+action,{method:'POST'});setTimeout(refresh,500)}
function updateBadge(u){return u==='pending'?' <span class="muted">⟳ a actualizar</span>':u==='manual'?' <span class="muted">(actualização manual)</span>':''}
function esc(s){return String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]))}
function renderAgents(){
const q=(document.getElementById('search')?.value||'').toLowerCase();
const group=document.getElementById('groupFilter')?.value||'';
const state=document.getElementById('stateFilter')?.value||'';
const agents=cachedAgents.filter(a=>(!q||(a.agentId+' '+a.group+' '+(a.status?.machine?.hostname||'')).toLowerCase().includes(q))&&(!group||a.group===group)&&(!state||(state==='online')===a.online));
document.getElementById('agents').innerHTML=agents.map(a=>{const apps=a.status?.apps||[];const rc=apps.filter(p=>p.running).length;const rt=apps.reduce((n,p)=>n+Number(p.retries||0),0);const pend=Number(a.pending||0);const hn=a.status?.machine?.hostname||a.agentId;return '<tr><td><a href="/agent/'+encodeURIComponent(a.agentId)+'"><strong>'+esc(hn)+'</strong></a>'+(hn!==a.agentId?'<br><span class="muted" style="font-size:11px">'+esc(a.agentId)+'</span>':'')+'</td><td>'+esc(a.group)+'</td><td>'+esc(a.status?.version||'-')+updateBadge(a.update)+'</td><td><span class="badge '+(a.online?'online':'')+'"><span class="dot"></span>'+(a.online?'Online':'Offline')+'</span></td><td>'+rc+' / '+apps.length+'</td><td>'+rt+'</td><td>'+(pend?'<span class="muted">'+pend+' pend.</span>':'—')+'</td><td>'+new Date(a.lastSeen).toLocaleString()+'</td><td><button class="btn agent-action" data-agent="'+encodeURIComponent(a.agentId)+'" data-action="restart-all">Reiniciar</button> <button class="btn warn agent-action" data-agent="'+encodeURIComponent(a.agentId)+'" data-action="stop-all">Parar</button> <button class="btn warn agent-remove" data-agent="'+encodeURIComponent(a.agentId)+'">Remover</button></td></tr>'}).join('');
}
let cachedEvents=[];
function renderEvents(){
const action=document.getElementById('evAction').value;
const hours=document.getElementById('evHours').value;
const params=new URLSearchParams();if(action)params.set('action',action);if(hours)params.set('sinceHours',hours);
document.getElementById('csv').href='/api/v1/admin/events?format=csv&'+params.toString();
const list=cachedEvents.filter(e=>!action||e.action===action);
document.getElementById('events').innerHTML=list.slice(0,200).map(e=>'<div class="event"><strong>'+esc(e.action)+'</strong> '+(e.appName?'· '+esc(e.appName):'')+' <span class="muted">'+new Date(e.time).toLocaleString()+'</span><br>'+esc(e.agentId||e.group||'global')+' — '+esc(e.detail||'')+'</div>').join('')||'<div class="event muted">Sem eventos.</div>';
}
async function removeAgent(id){if(!confirm('Remover o agente "'+id+'" do controlador? (não desinstala o serviço no PC)'))return;await api('/api/v1/admin/agent-remove/'+encodeURIComponent(id),{method:'POST'});setTimeout(refresh,300)}
async function refresh(){try{
const hours=document.getElementById('evHours').value;
const evURL='/api/v1/admin/events'+(hours?('?sinceHours='+hours):'');
const [agents,events]=await Promise.all([api('/api/v1/admin/agents'),api(evURL)]);
document.getElementById('total').textContent=agents.length;
document.getElementById('online').textContent=agents.filter(a=>a.online).length;
document.getElementById('offline').textContent=agents.filter(a=>!a.online).length;
let running=0,retries=0;for(const a of agents){for(const p of (a.status?.apps||[])){if(p.running)running++;retries+=Number(p.retries||0)}}
document.getElementById('running').textContent=running;
document.getElementById('failures').textContent=events.filter(e=>['process-crash','restart-failed','start-failed','startup-failed','retry-limit-reached','remote-command-failed'].includes(e.action)).length;
cachedAgents=agents;cachedEvents=events;
const groups=[...new Set(agents.map(a=>a.group))].sort();
const gf=document.getElementById('groupFilter'), oldGroup=gf.value;
gf.innerHTML='<option value="">Todos os grupos</option>'+groups.map(g=>'<option value="'+esc(g)+'">'+esc(g)+'</option>').join('');
gf.value=oldGroup;
const actions=[...new Set(events.map(e=>e.action))].sort();
const af=document.getElementById('evAction'), oldAction=af.value;
af.innerHTML='<option value="">Todas as acções</option>'+actions.map(a=>'<option value="'+esc(a)+'">'+esc(a)+'</option>').join('');
af.value=oldAction;
renderAgents();renderEvents();
}catch(e){console.error(e)}}
document.addEventListener('click',e=>{const r=e.target.closest('.agent-remove');if(r){removeAgent(decodeURIComponent(r.dataset.agent));return}const b=e.target.closest('.agent-action');if(b){commandAgent(decodeURIComponent(b.dataset.agent),b.dataset.action)}});
refresh();setInterval(refresh,5000);
</script></body></html>`

const agentHTML = `<!doctype html>
<html lang="pt"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AppGuard Agent</title>
<style>
body{font-family:Inter,system-ui;background:#07111f;color:#e8edf7;margin:0}.wrap{max-width:1100px;margin:auto;padding:28px}.panel{background:#0d1c2d;border:1px solid #20344e;border-radius:14px;margin:16px 0;overflow:hidden}.panel h2{font-size:16px;padding:14px 18px;margin:0;border-bottom:1px solid #20344e}.body{padding:18px}.btn{background:#183451;color:#fff;border:1px solid #2a4b6b;border-radius:9px;padding:8px 12px;cursor:pointer}.warn{background:#5b2c22}.good{background:#164733}.muted{color:#8fa5bf}table{width:100%;border-collapse:collapse}th,td{padding:11px;border-bottom:1px solid #172b42;text-align:left;font-size:13px}input,textarea{background:#07111f;color:#fff;border:1px solid #2b4564;border-radius:8px;padding:9px}textarea{width:100%;min-height:300px;font-family:Consolas,monospace}a{color:#8fc7ff}
</style></head><body><div class="wrap"><a href="/">← Dashboard</a><h1 id="title">Agente</h1>
<div class="panel"><h2>Estado</h2><div class="body" id="summary">A carregar...</div></div>
<div class="panel"><h2>Sistema</h2><div class="body" id="system">—</div></div>
<div class="panel"><h2>Processos</h2><div class="body"><table><thead><tr><th>Aplicação</th><th>Estado</th><th>Retries</th><th>PIDs</th><th>Acções</th></tr></thead><tbody id="apps"></tbody></table></div></div>
<div class="panel"><h2>Configuração remota</h2><div class="body"><p class="muted">Altere apenas grupo, intervalo e aplicações. Não são executados comandos shell.</p><textarea id="settings"></textarea><br><br><button class="btn good" onclick="saveSettings()">Enviar configuração</button></div></div>
<div class="panel"><h2>Histórico</h2><div class="body" id="events"></div></div>
</div><script>
const id=decodeURIComponent('{{AGENT_ID}}');document.getElementById('title').textContent='Agente '+id;
async function api(path,opts={}){opts.headers={...(opts.headers||{})};const r=await fetch(path,opts);if(!r.ok)throw new Error(await r.text());if(r.status===204)return null;return r.json()}
function esc(s){return String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]))}
async function command(action){await api('/api/v1/admin/agent/'+encodeURIComponent(id)+'/'+action,{method:'POST'});setTimeout(refresh,500)}
async function commandApp(name,action){await api('/api/v1/admin/agent-app/'+encodeURIComponent(id)+'/'+encodeURIComponent(name)+'/'+action,{method:'POST'});setTimeout(refresh,500)}
async function saveSettings(){let body;try{body=JSON.parse(document.getElementById('settings').value)}catch(e){alert('JSON inválido');return}await api('/api/v1/admin/agent-settings/'+encodeURIComponent(id),{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});alert('Configuração enviada para o agente')}
async function refresh(){try{const d=await api('/api/v1/admin/agent-detail/'+encodeURIComponent(id));const a=d.agent;document.getElementById('summary').innerHTML='<b>'+esc(a.agentId)+'</b> · '+esc(a.group)+' · '+(a.online?'Online':'Offline')+' · último contacto '+new Date(a.lastSeen).toLocaleString()+'<br><br><button class="btn good node-action" data-action="enable-all">Activar</button> <button class="btn good node-action" data-action="start-all">Abrir</button> <button class="btn node-action" data-action="restart-all">Reiniciar</button> <button class="btn warn node-action" data-action="stop-all">Parar</button> <button class="btn warn node-action" data-action="disable-all">Desactivar</button>';const m=a.status?.machine||{};const srows=[['Nome',m.hostname],['ID (máquina)',a.agentId],['Sistema',m.osVersion||m.os],['Arquitectura',m.arch],['CPUs',m.cpus],['RAM',m.totalRamMb?Math.round(m.totalRamMb/1024*10)/10+' GB':''],['IP',(m.ipv4||[]).join(', ')],['Utilizador',m.loggedUser],['Uptime',m.uptimeSeconds?Math.floor(m.uptimeSeconds/3600)+'h '+Math.floor((m.uptimeSeconds%3600)/60)+'m':'']];document.getElementById('system').innerHTML='<table>'+srows.filter(r=>r[1]!==undefined&&r[1]!=='').map(r=>'<tr><td class="muted" style="width:170px">'+esc(r[0])+'</td><td>'+esc(r[1])+'</td></tr>').join('')+'</table>';document.getElementById('apps').innerHTML=(a.status?.apps||[]).map(p=>'<tr><td>'+esc(p.name)+'</td><td>'+(p.running?'Running':(p.operationPending?'A executar comando':(p.restartPending?'Retry pendente':'Stopped')))+'</td><td>'+Number(p.retries||0)+' / '+Number(p.maxRetries||0)+'</td><td>'+esc((p.pids||[]).join(', '))+'</td><td><button class="btn good app-action" data-app="'+encodeURIComponent(p.name)+'" data-action="start-app">Abrir</button> <button class="btn good app-action" data-app="'+encodeURIComponent(p.name)+'" data-action="enable-app">Activar</button> <button class="btn app-action" data-app="'+encodeURIComponent(p.name)+'" data-action="restart-app">Reiniciar</button> <button class="btn warn app-action" data-app="'+encodeURIComponent(p.name)+'" data-action="stop-app">Parar</button> <button class="btn warn app-action" data-app="'+encodeURIComponent(p.name)+'" data-action="disable-app">Desactivar</button></td></tr>').join('');const ta=document.getElementById('settings');if(ta&&!ta.matches(':focus'))ta.value=JSON.stringify(a.status?.settings||{},null,2);document.getElementById('events').innerHTML=d.events.map(e=>'<p><b>'+esc(e.action)+'</b> '+(e.appName?'· '+esc(e.appName):'')+' <span class="muted">'+new Date(e.time).toLocaleString()+'</span><br>'+esc(e.detail||'')+'</p>').join('')}catch(e){document.getElementById('summary').textContent=e.message}}
document.addEventListener('click',e=>{const n=e.target.closest('.node-action');if(n){command(n.dataset.action);return}const a=e.target.closest('.app-action');if(a){commandApp(decodeURIComponent(a.dataset.app),a.dataset.action)}});
refresh();setInterval(refresh,5000);
</script></body></html>`

const powershellHTML = `<!doctype html>
<html lang="pt"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>AppGuard — PowerShell</title>
<style>
body{font-family:Inter,system-ui,-apple-system,Segoe UI,sans-serif;background:#07111f;color:#e8edf7;margin:0}
.wrap{max-width:1100px;margin:auto;padding:26px}a{color:#8fc7ff;text-decoration:none}
.panel{background:#0d1c2d;border:1px solid #20344e;border-radius:14px;margin:16px 0;overflow:hidden}
.panel h2{font-size:15px;padding:14px 18px;margin:0;border-bottom:1px solid #20344e}.body{padding:18px}
textarea{width:100%;min-height:200px;background:#060d18;border:1px solid #2b4564;color:#d6f5c9;border-radius:8px;padding:12px;font-family:Consolas,monospace;font-size:13px;box-sizing:border-box}
select,input{background:#07111f;border:1px solid #2b4564;color:#fff;border-radius:8px;padding:9px 11px}
.row{display:flex;gap:10px;flex-wrap:wrap;align-items:center;margin-top:12px}
.btn{background:#1f6feb;color:#fff;border:0;border-radius:9px;padding:10px 16px;cursor:pointer}.btn:hover{background:#2d7dfb}
.btn.warn{background:#5b2c22}.muted{color:#8fa5bf;font-size:12px}
.result{border-bottom:1px solid #172b42;padding:12px 16px}.result pre{white-space:pre-wrap;word-break:break-word;background:#060d18;border-radius:8px;padding:10px;margin:8px 0 0;max-height:280px;overflow:auto;font-size:12px}
.badge{display:inline-block;padding:3px 8px;border-radius:999px;font-size:11px}.ok{background:#164733}.fail{background:#5b2c22}.pend{background:#2a3b52}
.warnbox{background:#3a2a12;border:1px solid #6b4a1f;border-radius:8px;padding:10px 12px;font-size:13px;color:#ffd9a8}
</style></head><body><div class="wrap">
<a href="/">← Dashboard</a><h1>Executar PowerShell</h1>
<div class="warnbox">⚠ O script corre como SYSTEM em todos os computadores escolhidos. Reveja antes de enviar.</div>
<div class="panel"><h2>Novo comando</h2><div class="body">
<textarea id="script" placeholder="Get-Service | Where-Object Status -eq 'Running'"></textarea>
<div class="row">
<label class="muted">Destino</label>
<select id="ttype" onchange="onTarget()"><option value="all">Todos os agentes</option><option value="group">Grupo</option><option value="agent">Um agente</option></select>
<select id="tvalue" style="display:none"></select>
<label class="muted">Timeout (s)</label><input id="timeout" type="number" value="120" style="width:90px">
<button class="btn" onclick="send()">Enviar</button>
<span class="muted" id="status"></span>
</div></div></div>
<div class="panel"><h2>Resultados recentes</h2><div class="body" style="padding:0" id="results"></div></div>
</div><script>
let agents=[];
function esc(s){return String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]))}
async function api(path,opts={}){const r=await fetch(path,opts);if(!r.ok)throw new Error(await r.text());if(r.status===204)return null;return r.json()}
function onTarget(){const t=document.getElementById('ttype').value,sel=document.getElementById('tvalue');
if(t==='all'){sel.style.display='none';return}sel.style.display='';
if(t==='group'){const gs=[...new Set(agents.map(a=>a.group))].sort();sel.innerHTML=gs.map(g=>'<option>'+esc(g)+'</option>').join('')}
else{sel.innerHTML=agents.filter(a=>a.status?.role!=='controller').map(a=>'<option>'+esc(a.agentId)+'</option>').join('')}}
async function send(){const st=document.getElementById('status');st.textContent='';
const script=document.getElementById('script').value;if(!script.trim()){st.textContent='Script vazio';return}
const t=document.getElementById('ttype').value;const body={script,timeoutSeconds:Number(document.getElementById('timeout').value)||120,target:{type:t,value:t==='all'?'':document.getElementById('tvalue').value}};
if(!confirm('Enviar este script PowerShell para o destino seleccionado?'))return;
try{const r=await api('/api/v1/admin/powershell',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});st.textContent='Enviado a '+r.queued+' agente(s)';setTimeout(loadResults,800)}catch(e){st.textContent=e.message}}
function cls(s){return s==='ok'?'ok':s==='failed'?'fail':'pend'}
function label(s){return s==='ok'?'OK':s==='failed'?'Falhou':'Pendente'}
async function loadResults(){try{const recs=await api('/api/v1/admin/powershell/results');
document.getElementById('results').innerHTML=(recs||[]).map(r=>'<div class="result"><span class="badge '+cls(r.status)+'">'+label(r.status)+'</span> <b>'+esc(r.agentId)+'</b> <span class="muted">'+new Date(r.createdAt).toLocaleString()+'</span>'+(r.error?'<pre>'+esc(r.error)+'</pre>':'')+(r.output?'<pre>'+esc(r.output)+'</pre>':(r.status==='ok'?'<pre class="muted">(sem saída)</pre>':''))+'</div>').join('')||'<div class="result muted">Sem resultados ainda.</div>'}catch(e){document.getElementById('results').innerHTML='<div class="result">'+esc(e.message)+'</div>'}}
async function init(){try{agents=await api('/api/v1/admin/agents')||[]}catch(_){}onTarget();loadResults();setInterval(loadResults,4000)}
init();
</script></body></html>`
