// session.go holds the core lifecycle types: the three-state machine
// (working/waiting/ready — see STATES.md), the yield verdict recorded once a
// session goes ready, SessionState itself, and the launcher/background-agent
// metadata attached to it. Per-pass computed metrics live in metrics.go; the
// "is the agent waiting on me" text heuristics live in waiting_cue.go.
package session

import (
	"time"
)

// State constants — three MECE states for session lifecycle.
// See STATES.md for the formal state machine specification.
const (
	StateWorking = "working" // Agent actively processing (tools, text generation, hooks, compaction, or a live Bash background process)
	StateWaiting = "waiting" // Agent finished turn, waiting for user input
	StateReady   = "ready"   // Session inactive (process exited, transcript removed, cancelled)
)

// IsCanonicalState reports whether s is one of the three valid lifecycle
// states. Anything else (empty, "cancelled", a typo) is a domain violation.
func IsCanonicalState(s string) bool {
	return s == StateWorking || s == StateWaiting || s == StateReady
}

// Yield state constants — whether a finished session's work survived in the
// repo or was reverted (#373). An independent dimension from the lifecycle
// State above: a session is always in one of the three lifecycle states, and
// separately carries one of these yield verdicts once it has gone ready.
const (
	YieldUnknown    = "unknown"    // not git-tracked, or not yet evaluated
	YieldProductive = "productive" // shipped a commit that hasn't been reverted
	YieldReverted   = "reverted"   // its HEAD commit was later git-reverted
)

// MetricsTimelinePoint is one cumulative SessionMetrics snapshot tagged with
// the transcript-relative timestamp it was observed at. A MetricsCollector can
// return an ordered timeline of these so a replay viewer can show cost/tokens
// climbing turn-by-turn instead of jumping straight to the final total.
type MetricsTimelinePoint struct {
	VirtualTime time.Time
	Metrics     *SessionMetrics
}

// subagentSummary tracks the aggregate state of all child sessions.
type subagentSummary struct {
	Total   int `json:"total"`
	Working int `json:"working"`
	Waiting int `json:"waiting"`
	Ready   int `json:"ready"`
}

// Equal reports whether two summaries carry the same counts. Nil receivers
// and arguments are handled — two nils are equal. Used to skip redundant
// parent re-broadcasts when a child event didn't change the badge (#593).
func (s *subagentSummary) Equal(o *subagentSummary) bool {
	if s == nil || o == nil {
		return s == o
	}
	return *s == *o
}

// Launcher identifies the terminal emulator or IDE that spawned the session's
// agent process. Captured once from the process env when the PID is first
// known (see processlifecycle.ReadLauncherEnv). Fields are best-effort —
// clients must treat every field as optional and fall back to the session
// CWD when nothing identifies the host.
//
// TermProgram is the primary identifier; clients map it to a platform-native
// activator (e.g. the macOS menu-bar app derives an app bundle ID from it).
// Keeping that derivation client-side avoids persisting redundant state. The
// exception is HostBundleID: when no curated TermProgram matches, the daemon
// resolves the host bundle id by process ancestry (which the client can't do)
// and carries it here.
type Launcher struct {
	TermProgram    string `json:"term_program,omitempty"`     // $TERM_PROGRAM (e.g. iTerm.app, Apple_Terminal, vscode, cursor, ghostty, WezTerm, Hyper)
	ITermSessionID string `json:"iterm_session_id,omitempty"` // $ITERM_SESSION_ID
	TermSessionID  string `json:"term_session_id,omitempty"`  // $TERM_SESSION_ID (Terminal.app)
	TmuxPane       string `json:"tmux_pane,omitempty"`        // $TMUX_PANE
	TmuxSocket     string `json:"tmux_socket,omitempty"`      // first `,`-field of $TMUX
	VSCodePID      int    `json:"vscode_pid,omitempty"`       // $VSCODE_PID (vscode/cursor/windsurf)
	TTY            string `json:"tty,omitempty"`              // controlling TTY of the agent process, e.g. "/dev/ttys021" — Terminal.app AppleScript matches tabs by this
	KittyListenOn  string `json:"kitty_listen_on,omitempty"`  // $KITTY_LISTEN_ON — kitty remote-control socket path
	KittyWindowID  string `json:"kitty_window_id,omitempty"`  // $KITTY_WINDOW_ID — kitty window identifier
	KittyPID       int    `json:"kitty_pid,omitempty"`        // $KITTY_PID — kitty.app process id (lets the activator target this specific instance when multiple kitties run)
	HostBundleID   string `json:"host_bundle_id,omitempty"`   // CFBundleIdentifier of the host app resolved by process-ancestry when no curated TermProgram matched (e.g. md.obsidian for an in-Obsidian terminal). Unlike TermProgram, this is derived server-side because the client has no map for arbitrary embedded-terminal hosts; the client builds a generic title-match activator from it.
}

// BackgroundAgent marks a session as a background agent spawned by the agent's
// own orchestration (Claude Code Agent View). Such an agent keeps running
// detached in the `claude daemon run` pool after its window/terminal is closed,
// so it shows up as a live session with no terminal the user can see (#744).
// Nil for normal interactive sessions. Clients render a "background" badge when
// present and emphasize "detached" when the agent has no controlling terminal.
type BackgroundAgent struct {
	// Name is Claude's human-readable label for the background job
	// (e.g. "Add guiding colors to quest cards"); may be empty.
	Name string `json:"name,omitempty"`
	// Detached is true when the agent has no controlling terminal — i.e. no
	// window/tab owns it. Computed by the daemon from the captured Launcher TTY.
	Detached bool `json:"detached,omitempty"`
}

// IsEmpty reports whether the launcher carries no identifying information
// — i.e. every field is zero. Capture helpers use this to decide whether to
// return nil rather than attach a meaningless struct to the session.
func (l *Launcher) IsEmpty() bool {
	return l == nil || (l.TermProgram == "" && l.ITermSessionID == "" &&
		l.TermSessionID == "" && l.TmuxPane == "" &&
		l.TmuxSocket == "" && l.VSCodePID == 0 && l.TTY == "" &&
		l.KittyListenOn == "" && l.KittyWindowID == "" && l.KittyPID == 0 &&
		l.HostBundleID == "")
}

// SessionState represents the current state of a Claude Code or Copilot session.
type SessionState struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	// Adapter identifies the source agent (e.g. "claude-code", "codex").
	// Empty means Claude Code (for backwards compatibility).
	Adapter        string `json:"adapter,omitempty"`
	Model          string `json:"model,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	GitBranch      string `json:"git_branch,omitempty"`
	ProjectName    string `json:"project_name,omitempty"`

	// HeadCommit is the full SHA of the session's working-directory HEAD,
	// captured when the session transitions to ready. Empty when the CWD is
	// not a git repo. The yield sweep correlates `git revert` commits back to
	// the session that authored the reverted work via this SHA (#373).
	HeadCommit string `json:"head_commit,omitempty"`
	// YieldState records whether the session's work survived: one of
	// YieldProductive / YieldReverted / YieldUnknown (default unknown). Set on
	// the ready transition and flipped to reverted by the yield sweep (#373).
	YieldState string `json:"yield_state,omitempty"`

	FirstSeen   int64           `json:"first_seen"`
	UpdatedAt   int64           `json:"updated_at"`
	Confidence  string          `json:"confidence"`
	EventCount  int             `json:"event_count"`
	LastEvent   string          `json:"last_event"`
	LastMatcher string          `json:"last_matcher,omitempty"`
	Metrics     *SessionMetrics `json:"metrics,omitempty"`

	// PID of the Claude Code process that owns this session (set on SessionStart).
	PID int `json:"pid,omitempty"`

	// Launcher identifies the terminal/IDE that spawned the agent process.
	// Captured once when PID is first assigned; nil if env capture failed
	// or no recognized env vars were present.
	Launcher *Launcher `json:"launcher,omitempty"`

	// Background marks a detached background agent (e.g. a Claude Code Agent
	// View bg agent living in the daemon pool). Nil for normal sessions (#744).
	Background *BackgroundAgent `json:"background,omitempty"`

	// ParentSessionID links a subagent session to its spawning parent session.
	// Derived from file path or heuristic matching in SessionDetector.
	ParentSessionID string `json:"parent_session_id,omitempty"`

	// Subagents holds the aggregate state of all child sessions.
	// Nil when this session has no children.
	Subagents *subagentSummary `json:"subagents,omitempty"`

	// DaemonVersion records which irrlichd version created this session,
	// enabling future data migrations when the schema evolves.
	DaemonVersion string `json:"daemon_version,omitempty"`

	// Transcript monitoring for waiting-state recovery.
	LastTranscriptSize int64  `json:"last_transcript_size,omitempty"`
	WaitingStartTime   *int64 `json:"waiting_start_time,omitempty"`
}

// IsStale reports whether the session's last update is older than maxAge.
// A zero or negative maxAge disables the check (always returns false).
func (s *SessionState) IsStale(maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	return time.Since(time.Unix(s.UpdatedAt, 0)) > maxAge
}
