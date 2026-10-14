package control

import "time"

type Heartbeat struct {
	AgentID   string         `json:"agentId"`
	Timestamp time.Time      `json:"timestamp"`
	Status    map[string]any `json:"status"`
}

type Command struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	AppName string `json:"appName,omitempty"`
}
