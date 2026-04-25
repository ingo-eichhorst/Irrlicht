// Package lifecycle defines the unified event types for recording and
// replaying the full session lifecycle — not just transcript-derived state
// transitions but also process lifecycle, filesystem events, debounce
// behavior, and parent-child linking.
package lifecycle

import "time"

// Kind enumerates all recordable lifecycle signals.
type Kind string

const (
	// Transcript file events (from fswatcher → AgentWatcher).
	KindTranscriptNew      Kind = "transcript_new"
	KindTranscriptActivity Kind = "transcript_activity"
	KindTranscriptRemoved  Kind = "transcript_removed"

	// Process lifecycle events.
	KindPIDDiscovered  Kind = "pid_discovered"
	KindProcessSpawned Kind = "process_spawned"
	KindProcessExited  Kind = "process_exited"

	// File-system events on the agent's working directory. Debounced.
	// Reserved by .specs/onboard-agent/07-10-recorder-fidelity.md (WS08);
	// emission is wired by a follow-up PR.
	KindFileEvent Kind = "file_event"

	// State machine transitions (output of ClassifyState).
	KindStateTransition Kind = "state_transition"

	// Parent-child linkage.
	KindParentLinked Kind = "parent_linked"

	// Debounce: records coalescing for faithful replay.
	KindDebounceCoalesced Kind = "debounce_coalesced"

	// Agent hooks (future: issue #108).
	KindHookReceived Kind = "hook_received"

	// Pre-session lifecycle (process scanner detections).
	KindPreSessionCreated Kind = "presession_created"
	KindPreSessionRemoved Kind = "presession_removed"
)

// Event is a single recorded lifecycle signal. The Kind field discriminates
// which optional fields are populated. All events carry a monotonic sequence
// number and wall-clock timestamp for ordering.
type Event struct {
	// Ordering and timing.
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"ts"`
	Kind      Kind      `json:"kind"`

	// Session identity.
	SessionID string `json:"session_id"`
	Adapter   string `json:"adapter,omitempty"`

	// Transcript events.
	TranscriptPath string `json:"transcript_path,omitempty"`
	FileSize       int64  `json:"file_size,omitempty"`
	ProjectDir     string `json:"project_dir,omitempty"`
	CWD            string `json:"cwd,omitempty"`

	// Process lifecycle.
	PID int `json:"pid,omitempty"`

	// State transitions (recorded as output for validation during replay).
	PrevState string `json:"prev_state,omitempty"`
	NewState  string `json:"new_state,omitempty"`
	Reason    string `json:"reason,omitempty"`

	// Parent-child.
	ParentSessionID string `json:"parent_session_id,omitempty"`

	// Debounce.
	CoalescedCount int `json:"coalesced_count,omitempty"`

	// Hooks (future: issue #108).
	HookName string `json:"hook_name,omitempty"`
	HookData string `json:"hook_data,omitempty"`

	// File-system events (KindFileEvent). Reserved by WS08 — emission pending.
	Path   string `json:"path,omitempty"`
	FileOp string `json:"file_op,omitempty"` // create | write | remove | rename
}
