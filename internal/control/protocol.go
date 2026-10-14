package control

import (
	"encoding/json"
	"time"

	"appguard/internal/config"
)

type Heartbeat struct {
	AgentID   string               `json:"agentId"`
	Group     string               `json:"group"`
	Timestamp time.Time            `json:"timestamp"`
	Status    map[string]any       `json:"status"`
	Settings  config.AgentSettings `json:"settings"`
}

type Command struct {
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	AppName string          `json:"appName,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Event struct {
	Time    time.Time `json:"time"`
	AgentID string    `json:"agentId,omitempty"`
	Group   string    `json:"group,omitempty"`
	Action  string    `json:"action"`
	AppName string    `json:"appName,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}
