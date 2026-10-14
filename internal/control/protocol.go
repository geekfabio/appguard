package control

import (
	"encoding/json"
	"time"

	"appguard/internal/config"
	"appguard/internal/update"
)

type Heartbeat struct {
	AgentID   string               `json:"agentId"`
	Group     string               `json:"group"`
	Version   string               `json:"version"`
	Timestamp time.Time            `json:"timestamp"`
	Status    map[string]any       `json:"status"`
	Settings  config.AgentSettings `json:"settings"`
	// BinarySHA256 identifies the running appguard.exe; set only by agents
	// that can update themselves (SelfUpdate).
	BinarySHA256 string `json:"binarySha256,omitempty"`
	SelfUpdate   bool   `json:"selfUpdate,omitempty"`
}

// HeartbeatResponse is returned by the controller. Update is set when the
// agent should install the controller's binary.
type HeartbeatResponse struct {
	Update *update.Manifest `json:"update,omitempty"`
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
	// Output holds captured output, e.g. from a run-powershell command.
	Output string `json:"output,omitempty"`
}

// PowerShellRequest is the payload of a run-powershell command.
type PowerShellRequest struct {
	Script         string `json:"script"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type Event struct {
	Time    time.Time `json:"time"`
	AgentID string    `json:"agentId,omitempty"`
	Group   string    `json:"group,omitempty"`
	Action  string    `json:"action"`
	AppName string    `json:"appName,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}
