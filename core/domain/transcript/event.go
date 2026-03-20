// Package transcript defines domain types for Claude Code transcript file events.
package transcript

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

// TranscriptEvent carries information about a single transcript change.
type TranscriptEvent struct {
	Type           EventType
	Adapter        string // Source adapter name (e.g. "claude-code", "codex")
	SessionID      string // UUID portion of the filename (without .jsonl)
	ProjectDir     string // Leaf directory name under the watched root
	TranscriptPath string // Absolute path to the .jsonl file
	Size           int64  // Current file size in bytes (0 for removals)
}
