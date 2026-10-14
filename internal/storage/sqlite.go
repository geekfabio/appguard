package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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

CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	agent_id TEXT,
	group_name TEXT,
	action TEXT NOT NULL,
	app_name TEXT,
	detail TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_agent ON events(agent_id, ts DESC);

CREATE TABLE IF NOT EXISTS agents (
	agent_id TEXT PRIMARY KEY,
	group_name TEXT NOT NULL,
	last_seen TEXT NOT NULL,
	status_json TEXT NOT NULL
);

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
	if _, err := d.sql.Exec(schema); err != nil {
		return err
	}
	for _, column := range []string{"result_status", "result_error", "result_output"} {
		if err := d.addColumnIfMissing("commands", column, "TEXT"); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) addColumnIfMissing(table, column, kind string) error {
	rows, err := d.sql.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = d.sql.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, kind))
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

// DeleteAgent removes an agent and its queued commands. It reports whether
// an agent row was actually removed.
func (d *DB) DeleteAgent(agentID string) (bool, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	res, err := tx.Exec("DELETE FROM agents WHERE agent_id = ?", agentID)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec("DELETE FROM commands WHERE agent_id = ?", agentID); err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
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

// AckCommand marks the command as delivered and stores the agent's result.
// It returns the command, or sql.ErrNoRows when it was already acknowledged
// (an agent re-acking after a lost response) or does not exist.
func (d *DB) AckCommand(commandID, status, errMsg, output string) (*Command, error) {
	res, err := d.sql.Exec(`
UPDATE commands SET delivered_at = ?, result_status = ?, result_error = ?, result_output = ?
WHERE id = ? AND delivered_at IS NULL
`,
		time.Now().UTC().Format(time.RFC3339Nano),
		status,
		errMsg,
		output,
		commandID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}

	var command Command
	err = d.sql.QueryRow(
		"SELECT id, agent_id, action, COALESCE(app_name, '') FROM commands WHERE id = ?",
		commandID,
	).Scan(&command.ID, &command.AgentID, &command.Action, &command.AppName)
	if err != nil {
		return nil, err
	}
	return &command, nil
}

// EventRecord is a persisted controller event.
type EventRecord struct {
	Time    time.Time `json:"time"`
	AgentID string    `json:"agentId,omitempty"`
	Group   string    `json:"group,omitempty"`
	Action  string    `json:"action"`
	AppName string    `json:"appName,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// EventFilter narrows an event query. Empty fields are ignored.
type EventFilter struct {
	AgentID string
	Group   string
	Action  string
	Since   time.Time
	Limit   int
}

// AppendEvent stores one event.
func (d *DB) AppendEvent(e EventRecord) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	_, err := d.sql.Exec(
		"INSERT INTO events(ts, agent_id, group_name, action, app_name, detail) VALUES(?,?,?,?,?,?)",
		e.Time.UTC().Format(time.RFC3339Nano), e.AgentID, e.Group, e.Action, e.AppName, e.Detail,
	)
	return err
}

// ListEvents returns matching events, newest first.
func (d *DB) ListEvents(f EventFilter) ([]EventRecord, error) {
	limit := f.Limit
	if limit <= 0 || limit > 2000 {
		limit = 300
	}
	q := strings.Builder{}
	q.WriteString("SELECT ts, COALESCE(agent_id,''), COALESCE(group_name,''), action, COALESCE(app_name,''), COALESCE(detail,'') FROM events WHERE 1=1")
	var args []any
	if f.AgentID != "" {
		q.WriteString(" AND agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.Group != "" {
		q.WriteString(" AND group_name = ?")
		args = append(args, f.Group)
	}
	if f.Action != "" {
		q.WriteString(" AND action = ?")
		args = append(args, f.Action)
	}
	if !f.Since.IsZero() {
		q.WriteString(" AND ts >= ?")
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	q.WriteString(" ORDER BY ts DESC, id DESC LIMIT ?")
	args = append(args, limit)

	rows, err := d.sql.Query(q.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var ts string
		if err := rows.Scan(&ts, &e.AgentID, &e.Group, &e.Action, &e.AppName, &e.Detail); err != nil {
			return nil, err
		}
		e.Time, _ = time.Parse(time.RFC3339Nano, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventRetention is how long controller events are kept before pruning.
const EventRetention = 48 * time.Hour

// PruneEvents deletes events older than EventRetention. Logs are kept to a
// minimum: nothing older than two days survives.
func (d *DB) PruneEvents() (int64, error) {
	cutoff := time.Now().Add(-EventRetention).UTC().Format(time.RFC3339Nano)
	res, err := d.sql.Exec("DELETE FROM events WHERE ts < ?", cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PruneCommands deletes delivered commands older than EventRetention.
// Pending (undelivered) commands are always kept so an offline agent still
// receives them when it returns.
func (d *DB) PruneCommands() (int64, error) {
	cutoff := time.Now().Add(-EventRetention).UTC().Format(time.RFC3339Nano)
	res, err := d.sql.Exec(
		"DELETE FROM commands WHERE delivered_at IS NOT NULL AND delivered_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CommandRecord is a stored command together with its delivery result, used
// by the dashboard to show outcomes (e.g. remote PowerShell runs).
type CommandRecord struct {
	ID        string     `json:"id"`
	AgentID   string     `json:"agentId"`
	Action    string     `json:"action"`
	AppName   string     `json:"appName,omitempty"`
	Created   time.Time  `json:"createdAt"`
	Delivered *time.Time `json:"deliveredAt,omitempty"`
	Status    string     `json:"status"`           // "ok", "failed" or "" (pending)
	Error     string     `json:"error,omitempty"`
	Output    string     `json:"output,omitempty"`
}

// ListCommandsByAction returns the most recent commands of an action, newest
// first, including pending ones.
func (d *DB) ListCommandsByAction(action string, limit int) ([]CommandRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.sql.Query(`
SELECT id, agent_id, action, COALESCE(app_name, ''), created_at, delivered_at,
       COALESCE(result_status, ''), COALESCE(result_error, ''), COALESCE(result_output, '')
FROM commands
WHERE action = ?
ORDER BY created_at DESC
LIMIT ?
`, action, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CommandRecord
	for rows.Next() {
		var rec CommandRecord
		var created string
		var delivered sql.NullString
		if err := rows.Scan(&rec.ID, &rec.AgentID, &rec.Action, &rec.AppName, &created,
			&delivered, &rec.Status, &rec.Error, &rec.Output); err != nil {
			return nil, err
		}
		rec.Created, _ = time.Parse(time.RFC3339Nano, created)
		if delivered.Valid && delivered.String != "" {
			if t, err := time.Parse(time.RFC3339Nano, delivered.String); err == nil {
				rec.Delivered = &t
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
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
