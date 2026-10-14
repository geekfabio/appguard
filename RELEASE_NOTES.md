# AppGuard v1.1.2

This release removes persistent log/event storage from SQLite.

## Changes

- Removed the persistent `events` table from SQLite.
- Existing `events` tables are dropped automatically when the Controller starts.
- Agent events and watchdog events are kept only in memory.
- The dashboard still shows recent events while the Controller is running.
- Recent events disappear after the Controller restarts.
- SQLite is now used only for operational state such as agents and pending commands.

## Why

This keeps the AppGuard database smaller and avoids retaining historical logs on disk.

## Unchanged

- Agent status remains available.
- Pending offline commands remain persistent.
- ACK command delivery remains persistent.
- Controller local watchdog behavior remains unchanged.
- First-run setup remains unchanged.
