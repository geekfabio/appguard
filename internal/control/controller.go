package control

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"appguard/internal/config"
)

type controllerState struct {
	mu       sync.RWMutex
	agents   map[string]Heartbeat
	commands map[string][]Command
	token    string
}

func RunController(cfg *config.Config) error {
	st := &controllerState{
		agents: make(map[string]Heartbeat),
		commands: make(map[string][]Command),
		token: cfg.Controller.Token,
	}

	mux := http.NewServeMux()

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
		hb.Timestamp = time.Now()
		st.mu.Lock()
		st.agents[hb.AgentID] = hb
		st.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))

	mux.HandleFunc("/api/v1/commands/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		agentID := strings.TrimPrefix(r.URL.Path, "/api/v1/commands/")
		if agentID == "" {
			http.Error(w, "missing agent id", http.StatusBadRequest)
			return
		}
		st.mu.Lock()
		cmds := st.commands[agentID]
		delete(st.commands, agentID)
		st.mu.Unlock()
		_ = json.NewEncoder(w).Encode(cmds)
	}))

	mux.HandleFunc("/api/v1/admin/agents", st.auth(func(w http.ResponseWriter, r *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		_ = json.NewEncoder(w).Encode(st.agents)
	}))

	mux.HandleFunc("/api/v1/admin/broadcast/", st.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		action := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/broadcast/")
		if !validAction(action) {
			http.Error(w, "invalid action", http.StatusBadRequest)
			return
		}
		st.mu.Lock()
		for id := range st.agents {
			st.commands[id] = append(st.commands[id], Command{
				ID: fmt.Sprintf("%d-%s", time.Now().UnixNano(), id),
				Action: action,
			})
		}
		st.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))

	slog.Info("AppGuard controller listening", "addr", cfg.Controller.Listen)
	server := &http.Server{
		Addr: cfg.Controller.Listen,
		Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return server.ListenAndServe()
}

func (s *controllerState) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func validAction(a string) bool {
	switch a {
	case "disable-all", "enable-all", "stop-all", "restart-all":
		return true
	default:
		return false
	}
}
