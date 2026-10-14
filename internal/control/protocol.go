package control

import "time"

type Heartbeat struct {
	AgentID   string         \`json:"agentId"\`
	Group     string         \`json:"group"\`
	Timestamp time.Time      \`json:"timestamp"\`
	Status    map[string]any \`json:"status"\`
}

type Command struct {
	ID      string \`json:"id"\`
	Action  string \`json:"action"\`
	AppName string \`json:"appName,omitempty"\`
}

type Event struct {
	Time    time.Time \`json:"time"\`
	AgentID string    \`json:"agentId,omitempty"\`
	Group   string    \`json:"group,omitempty"\`
	Action  string    \`json:"action"\`
	Detail  string    \`json:"detail,omitempty"\`
}
