package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

type Agent struct {
	AgentID  string         `json:"agentId"`
	Group    string         `json:"group"`
	LastSeen time.Time      `json:"lastSeen"`
	Status   map[string]any `json:"status"`
}

type Event struct {
	ID      int64     `json:"id"`
	Time    time.Time `json:"time"`
	AgentID string    `json:"agentId,omitempty"`
	Group   string    `json:"group,omitempty"`
	Action  string    `json:"action"`
	AppName string    `json:"appName,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

type Command struct {
	ID      string          `json:"id"`
	AgentID string          `json:"agentId"`
	Action  string          `json:"action"`
	AppName string          `json:"appName,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Created time.Time       `json:"createdAt"`
}

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	s := &DB{sql: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) migrate() error {
	const schema = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;

CREATE TABLE IF NOT EXISTS agents (
	agent_id TEXT PRIMARY KEY,
	group_name TEXT NOT NULL,
	last_seen TEXT NOT NULL,
	status_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event_time TEXT NOT NULL,
	agent_id TEXT,
	group_name TEXT,
	action TEXT NOT NULL,
	app_name TEXT,
	detail TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_agent_time
ON events(agent_id, event_time DESC);

CREATE INDEX IF NOT EXISTS idx_events_action_time
ON events(action, event_time DESC);

CREATE TABLE IF NOT EXISTS commands (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL,
	action TEXT NOT NULL,
	app_name TEXT,
	payload_json TEXT,
	created_at TEXT NOT NULL,
	delivered_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_commands_pending
ON commands(agent_id, delivered_at, created_at);
`
	_, err := d.sql.Exec(schema)
	return err
}

func (d *DB) UpsertAgent(agentID, group string, lastSeen time.Time, status map[string]any) error {
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}

	_, err = d.sql.Exec(`
INSERT INTO agents(agent_id, group_name, last_seen, status_json)
VALUES(?, ?, ?, ?)
ON CONFLICT(agent_id) DO UPDATE SET
	group_name = excluded.group_name,
	last_seen = excluded.last_seen,
	status_json = excluded.status_json
`, agentID, group, lastSeen.UTC().Format(time.RFC3339Nano), string(raw))
	return err
}

func (d *DB) ListAgents() ([]Agent, error) {
	rows, err := d.sql.Query(`
SELECT agent_id, group_name, last_seen, status_json
FROM agents
ORDER BY agent_id
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Agent
	for rows.Next() {
		var a Agent
		var ts, raw string
		if err := rows.Scan(&a.AgentID, &a.Group, &ts, &raw); err != nil {
			return nil, err
		}
		a.LastSeen, _ = time.Parse(time.RFC3339Nano, ts)
		if err := json.Unmarshal([]byte(raw), &a.Status); err != nil {
			a.Status = map[string]any{"decodeError": err.Error()}
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func (d *DB) GetAgent(agentID string) (*Agent, error) {
	var a Agent
	var ts, raw string
	err := d.sql.QueryRow(`
SELECT agent_id, group_name, last_seen, status_json
FROM agents WHERE agent_id = ?
`, agentID).Scan(&a.AgentID, &a.Group, &ts, &raw)
	if err != nil {
		return nil, err
	}
	a.LastSeen, _ = time.Parse(time.RFC3339Nano, ts)
	if err := json.Unmarshal([]byte(raw), &a.Status); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	return &a, nil
}

func (d *DB) AddEvent(e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	_, err := d.sql.Exec(`
INSERT INTO events(event_time, agent_id, group_name, action, app_name, detail)
VALUES(?, ?, ?, ?, ?, ?)
`,
		e.Time.UTC().Format(time.RFC3339Nano),
		e.AgentID,
		e.Group,
		e.Action,
		e.AppName,
		e.Detail,
	)
	return err
}

func (d *DB) ListEvents(agentID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}

	query := `
SELECT id, event_time, agent_id, group_name, action, app_name, detail
FROM events
`
	args := []any{}
	if agentID != "" {
		query += " WHERE agent_id = ?"
		args = append(args, agentID)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Event
	for rows.Next() {
		var e Event
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.AgentID, &e.Group, &e.Action, &e.AppName, &e.Detail); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, ts)
		result = append(result, e)
	}
	return result, rows.Err()
}


func (d *DB) QueueCommand(command Command) error {
	if command.Created.IsZero() {
		command.Created = time.Now()
	}
	_, err := d.sql.Exec(`
INSERT INTO commands(id, agent_id, action, app_name, payload_json, created_at)
VALUES(?, ?, ?, ?, ?, ?)
`,
		command.ID,
		command.AgentID,
		command.Action,
		command.AppName,
		string(command.Payload),
		command.Created.UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (d *DB) FetchPendingCommands(agentID string, limit int) ([]Command, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := d.sql.Query(`
SELECT id, agent_id, action, COALESCE(app_name, ''), COALESCE(payload_json, ''), created_at
FROM commands
WHERE agent_id = ? AND delivered_at IS NULL
ORDER BY created_at ASC
LIMIT ?
`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commands []Command
	for rows.Next() {
		var command Command
		var payload string
		var created string
		if err := rows.Scan(&command.ID, &command.AgentID, &command.Action, &command.AppName, &payload, &created); err != nil {
			return nil, err
		}
		if payload != "" {
			command.Payload = json.RawMessage(payload)
		}
		command.Created, _ = time.Parse(time.RFC3339Nano, created)
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func (d *DB) AckCommand(commandID string) error {
	_, err := d.sql.Exec(
		"UPDATE commands SET delivered_at = ? WHERE id = ? AND delivered_at IS NULL",
		time.Now().UTC().Format(time.RFC3339Nano),
		commandID,
	)
	return err
}

func (d *DB) PendingCommandCount(agentID string) (int, error) {
	var count int
	err := d.sql.QueryRow(
		"SELECT COUNT(*) FROM commands WHERE agent_id = ? AND delivered_at IS NULL",
		agentID,
	).Scan(&count)
	return count, err
}


func (d *DB) Ping() error {
	return d.sql.Ping()
}
