package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"appguard/internal/storage"
)

const (
	maxPowerShellScript  = 256 * 1024 // 256 KiB
	defaultPSTimeoutSecs = 120
	maxPSTimeoutSecs     = 3600
)

type powerShellTarget struct {
	Type  string `json:"type"`  // "all", "group" or "agent"
	Value string `json:"value"` // group name or agent id (empty for "all")
}

type powerShellRequest struct {
	Script         string           `json:"script"`
	TimeoutSeconds int              `json:"timeoutSeconds"`
	Target         powerShellTarget `json:"target"`
}

// handlePowerShell queues a run-powershell command for the targeted agents.
// It is an admin-only endpoint (protected by the login).
func (s *controllerState) handlePowerShell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req powerShellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.Script = strings.TrimRight(req.Script, "\x00")
	if strings.TrimSpace(req.Script) == "" {
		http.Error(w, "script vazio", http.StatusBadRequest)
		return
	}
	if len(req.Script) > maxPowerShellScript {
		http.Error(w, "script demasiado grande", http.StatusRequestEntityTooLarge)
		return
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = defaultPSTimeoutSecs
	}
	if req.TimeoutSeconds > maxPSTimeoutSecs {
		req.TimeoutSeconds = maxPSTimeoutSecs
	}

	targets, err := s.resolveTargets(req.Target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(targets) == 0 {
		http.Error(w, "nenhum agente corresponde ao destino", http.StatusNotFound)
		return
	}

	payload, err := json.Marshal(PowerShellRequest{
		Script:         req.Script,
		TimeoutSeconds: req.TimeoutSeconds,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ids := make([]string, 0, len(targets))
	now := time.Now()
	for i, agentID := range targets {
		id := fmt.Sprintf("%d-%d-%s", now.UnixNano(), i, agentID)
		if err := s.db.QueueCommand(storage.Command{
			ID:      id,
			AgentID: agentID,
			Action:  "run-powershell",
			Payload: payload,
			Created: now,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ids = append(ids, id)
	}

	s.addEvent(Event{
		Time: now, Action: "run-powershell",
		Detail: fmt.Sprintf("Script PowerShell enviado a %d agente(s)", len(ids)),
	})
	writeJSON(w, map[string]any{"queued": len(ids), "ids": ids})
}

// resolveTargets returns the agent ids matching a target.
func (s *controllerState) resolveTargets(t powerShellTarget) ([]string, error) {
	switch t.Type {
	case "agent":
		if t.Value == "" {
			return nil, fmt.Errorf("agentId em falta")
		}
		return []string{t.Value}, nil
	case "all", "group", "":
		agents, err := s.db.ListAgents()
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, a := range agents {
			if role, _ := a.Status["role"].(string); role == "controller" {
				continue // never run on the controller's own node
			}
			if t.Type == "group" && a.Group != t.Value {
				continue
			}
			ids = append(ids, a.AgentID)
		}
		return ids, nil
	default:
		return nil, fmt.Errorf("tipo de destino inválido: %q", t.Type)
	}
}

// handlePowerShellResults returns recent run-powershell commands with their
// per-agent output.
func (s *controllerState) handlePowerShellResults(w http.ResponseWriter, r *http.Request) {
	records, err := s.db.ListCommandsByAction("run-powershell", 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, records)
}
