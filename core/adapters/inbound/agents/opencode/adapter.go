// Package opencode provides an inbound adapter that monitors OpenCode
// (https://github.com/anomalyco/opencode) sessions by polling its SQLite
// database at ~/.local/share/opencode/opencode.db.
//
// Unlike the other adapters (claudecode, codex, pi, aider) which tail JSONL
// transcript files, OpenCode stores all session state in a single SQLite
// database using WAL mode. The adapter watches the database file for writes
// via fsnotify and queries the `part` table to extract normalized events.
//
// Session discovery works by querying the `session` table; activity events are
// derived from new or updated rows in the `part` table. The `message` table
// provides role context (user vs. assistant) for state transitions.
package opencode

// AdapterName identifies sessions originating from OpenCode.
const AdapterName = "opencode"

// ProcessName is the OS-level executable name for OpenCode, used by
// the process lifecycle scanner to detect running instances via pgrep -x.
const ProcessName = "opencode"

// dbRelPath is the path relative to $HOME where OpenCode stores its SQLite
// database. Follows XDG Base Directory conventions on macOS/Linux.
const dbRelPath = ".local/share/opencode/opencode.db"
