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

import (
	"log"
	"os"
	"path/filepath"
)

// AdapterName identifies sessions originating from OpenCode.
const AdapterName = "opencode"

// ProcessName is the OS-level executable name for OpenCode, used by
// the process lifecycle scanner to detect running instances via pgrep -x.
const ProcessName = "opencode"

// dbRelPath is the path relative to $HOME where OpenCode stores its SQLite
// database. Follows XDG Base Directory conventions on macOS/Linux.
const dbRelPath = ".local/share/opencode/opencode.db"

// StorePath is the absolute path of the OpenCode SQLite database the daemon
// reads — the ONE resolver both the watcher (which tails it) and the hook
// receiver (which composes a session key from it) go through.
//
// A single function rather than two `filepath.Join(home, dbRelPath)` call sites
// for the same reason pi's Source() is a function: the hook receiver's key and
// the watcher's key must be the same string or they are two sessions, and a
// second, independently-written composition is exactly the drift #1361 records.
//
// Known limitation, stated rather than left to be discovered: this does NOT
// honour $XDG_DATA_HOME, which opencode's own Global.Path.data does
// (`join(process.env.XDG_DATA_HOME || join(homedir(), ".local", "share"),
// "opencode")`). That gap predates #1719 and is not introduced by it — a user
// with XDG_DATA_HOME set has never been observed by this adapter at all,
// because the watcher would find no database. It is stated here because this is
// now the one place to fix it.
func StorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("opencode: $HOME not set, using relative path: %v", err)
	}
	return filepath.Join(home, dbRelPath)
}

// transcriptPathFor renders the string every downstream consumer keys an
// OpenCode session on: the write-ahead log beside the database, with the
// session id carried in a query parameter (metrics.go's parseTranscriptPath
// reads it back out).
//
// The WAL rather than the database itself is deliberate and predates #1719:
// isStaleTranscript() stats whatever this names, and OpenCode flushes every
// write to the WAL while the main file only moves on checkpoint.
func transcriptPathFor(sessionID string) string {
	return StorePath() + "-wal" + sessionQueryParam + sessionID
}
