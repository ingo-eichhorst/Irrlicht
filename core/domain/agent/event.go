// Package agent defines domain types for coding agent transcript file events.
package agent

// EventType classifies transcript file-system events.
type EventType string

const (
	// EventNewSession fires when a new .jsonl transcript file appears.
	EventNewSession EventType = "new_session"
	// EventActivity fires when an existing .jsonl transcript file is written to.
	EventActivity EventType = "activity"
	// EventRemoved fires when a .jsonl transcript file is deleted.
	EventRemoved EventType = "removed"
)

// Event carries information about a single agent transcript change.
// Adapter identity is not on the event itself — it flows from the
// emitting watcher via inbound.Watcher.Identity() and is attached to the
// event by the per-watcher drain goroutine in session_detector.Run().
type Event struct {
	Type            EventType
	SessionID       string // UUID portion of the filename (without .jsonl)
	ProjectDir      string // Leaf directory name under the watched root
	TranscriptPath  string // Absolute path or DB path; for DB-backed adapters includes "?session=<id>"
	Size            int64  // Current file size in bytes (0 for removals)
	CWD             string // Working directory of the agent process (set by process scanner)
	ParentSessionID string // Parent session ID for subagent sessions (empty for top-level)
	Terminal        bool   // true when this activity event ends the agent's turn (fast-path debounce)
	Synthetic       bool   // true when injected by a hook handler rather than a real transcript write (issue #905)
}
