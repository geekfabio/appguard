package control

import (
	"encoding/json"
	"time"

	"appguard/internal/config"
)

type Heartbeat struct {
	AgentID   string               `json:"agentId"`
	Group     string               `json:"group"`
	Version   string               `json:"version"`
	Timestamp time.Time            `json:"timestamp"`
	Status    map[string]any       `json:"status"`
	Settings  config.AgentSettings `json:"settings"`
}

type Enrollment struct {
	AgentID string `json:"agentId"`
	Group   string `json:"group"`
	Version string `json:"version"`
}

type Command struct {
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	AppName string          `json:"appName,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

const (
	CommandOK     = "ok"
	CommandFailed = "failed"
)

// CommandResult is sent by the agent when acknowledging a command.
type CommandResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type Event struct {
	Time    time.Time `json:"time"`
	AgentID string    `json:"agentId,omitempty"`
	Group   string    `json:"group,omitempty"`
	Action  string    `json:"action"`
	AppName string    `json:"appName,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}
