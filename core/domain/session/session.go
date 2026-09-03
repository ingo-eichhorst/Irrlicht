// session.go holds the core lifecycle types: the four-state machine
// (working/waiting/ready/error — see events.md), the yield verdict recorded
// once a session goes ready, SessionState itself, and the launcher/background-
// agent metadata attached to it. Per-pass computed metrics live in metrics.go;
// the "is the agent waiting on me" text heuristics live in waiting_cue.go.
package session

import (
	"slices"
	"time"
)

// State constants — four MECE states for session lifecycle.
// See events.md for the formal state machine specification.
const (
	StateWorking = "working" // Agent actively processing (tools, text generation, hooks, compaction, or a live Bash background process)
	StateWaiting = "waiting" // Agent finished turn, waiting for user input
	StateReady   = "ready"   // Session inactive (process exited, transcript removed, cancelled)
	// StateError is #1796's fourth state: the session's own machinery failed
	// — the provider refused or failed the call, credentials were rejected,
	// the agent process died mid-turn, or Irrlicht could not read the
	// session. It exists so such a session is visibly red instead of
	// silently green, which is what every one of those cases used to be.
	//
	// NOT a tool failure. A grep that matched nothing and a build that broke
	// are the agent working normally; see ParsedEvent.IsError for that half.
	//
	// Cleared by the next SUCCESSFUL turn and by nothing else. The rule and
	// its accepted cost live with the code that implements each half:
	// clearSessionErrorOnRecovery in the tailer, and graceFor in
	// state_dwell.go for why neither edge is debounced.
	StateError = "error"
)

// canonicalStates is the lifecycle-state vocabulary, declared exactly once.
//
// Order is the ladder's own: the three ordinary states in the sequence a
// session moves through them, then the failure state. Callers that render the
// list to a human (see the filesystem repository's unrecognized-state warning)
// get a stable, readable order for free, and a fifth state cannot desynchronise
// the check from the message the way a hand-typed "%s/%s/%s" did (#1798).
var canonicalStates = []string{StateWorking, StateWaiting, StateReady, StateError}

// CanonicalStates returns the lifecycle-state vocabulary, newest-state-last.
//
// It returns a copy: the backing array is package state, and a caller that
// sorted or truncated the slice in place would silently redefine what every
// other caller — IsCanonicalState included — considers a valid state.
func CanonicalStates() []string {
	return slices.Clone(canonicalStates)
}

// IsCanonicalState reports whether s is one of the valid lifecycle states.
// Anything else (empty, "cancelled", a typo) is a domain violation.
//
// Reads canonicalStates rather than spelling the values out again, so this
// predicate and every message that lists the vocabulary cannot drift apart.
func IsCanonicalState(s string) bool {
	return slices.Contains(canonicalStates, s)
}

// HasWorkInFlight reports whether more work is coming for this session, so
// that whatever is waiting on it must keep waiting.
//
// THIS IS ONE HALF OF A PARTITION, and the half that is NOT about concurrency.
// Seven call sites spell some variant of `state == StateWorking || state ==
// StateWaiting` inline; #1798 flagged that they had silently become an
// INCOMPLETE enumeration when the fourth state arrived, and #1801 settles the
// split here rather than leaving each site to drift:
//
//   - "Does this session occupy a concurrency slot?" — working or waiting, and
//     nothing else. `error` is excluded deliberately and permanently: nothing
//     clears an error until the next successful turn, so counting it would
//     inflate the active count for the whole remaining life of the session.
//     That half stays in the concurrency tracker's own `concurrencyActive`,
//     which reads a bare state string off a persisted timeline that carries no
//     error detail at all — which is itself why the two cannot be one
//     function. events.md records the rule.
//
//   - "Is more work coming?" — this one. A session in `error` whose Phase is
//     `retrying` has another attempt scheduled: it is still working, and
//     releasing its parent in that gap paints the parent green while its
//     subagent sits red mid-retry.
//
// A TERMINAL error is not work in flight — no further attempt is coming, and
// the thing waiting is free to finish. Neither is ErrorPhaseUnknown:
// IsRetrying is false for it on purpose (see its doc), and reading "probably
// still going" out of a transcript that said nothing is exactly the invented
// verdict every pointer field on SessionError exists to prevent.
//
// ONLY hasActiveChildren (parent-ready gating) consumes this today, because it
// is the only site whose answer actually changes. Every other site was checked;
// each is named here with its reason, so the next reader inherits the decision
// instead of the question:
//
//   - isOrphanedChild and findCompletionTarget gate PROMOTION to ready.
//     Admitting a retrying session there would CLEAR a live error rather than
//     preserve it — the opposite of what this predicate is for.
//   - isUserInterruptReady leaves an errored session red after an ESC, matching
//     "cleared by the next successful turn and by nothing else".
//   - aggregateSubagentEstimate is working-only by design: an errored child
//     contributes no estimate.
//   - refreshStaleSessions is not a three-state site at all any more — #1798
//     already widened it (session_detector_activity.go's
//     `case StateWaiting, StateReady, StateError`), which is what makes
//     sessionErrorHoldTimeout reachable.
//   - pid_manager's liveness sweep keys off `== StateReady`, not off
//     working-or-waiting, so `error` behaves there exactly as `working` always
//     did. Nothing to change.
//
// ONE CAVEAT ON THE HOLD THIS CREATES, since a parent held forever would be a
// worse bug than the one being fixed. For transcript-backed children the hold
// is bounded well below the error's own 12h ceiling: reapStaleChild reaps any
// non-ready child whose transcript has been idle past orphanTranscriptAge (2
// minutes) and fires onChildDeleted, which releases the parent. DB-backed
// children (the `?session=` adapters — hermes, opencode, antigravity) are
// exempt from that sweep, so there a stuck Phase==retrying child holds its
// parent until the adapter's own maxAge or the 12h ceiling. That is latent
// rather than live: no inbound adapter emits SessionError yet, which is
// #1799/#1800's work, and it is theirs to bound.
func (s *SessionState) HasWorkInFlight() bool {
	if s == nil {
		return false
	}
	if s.State == StateWorking || s.State == StateWaiting {
		return true
	}
	if s.State != StateError || s.Metrics == nil {
		return false
	}
	return s.Metrics.SessionError.IsRetrying()
}

// Yield state constants — whether a finished session's work survived in the
// repo or was reverted (#373). An independent dimension from the lifecycle
// State above: a session is always in exactly one of the lifecycle states, and
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
//
// Error joins the other three in #1801. Without it an errored child was
// counted in Total and in none of the buckets, so working+waiting+ready
// silently stopped summing to total and a red subagent was invisible in the
// parent's badge — the one place a user would look to find out why the parent
// is stuck. No `omitempty`: the existing three always serialize, and a bucket
// that vanishes at zero would make "no errored children" and "this daemon
// predates the field" the same wire value.
type subagentSummary struct {
	Total   int `json:"total"`
	Working int `json:"working"`
	Waiting int `json:"waiting"`
	Ready   int `json:"ready"`
	Error   int `json:"error"`
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
//
// The Herdr* fields are the one case where the other fields are not merely
// best-effort but actively wrong *when read from the pane*, so a herdr pane
// never keeps what its own environment claims. herdr's server owns each pane's
// pty, outlives any attached client and is reparented to init, so every
// terminal-identity var a pane inherits — $TERM_PROGRAM, $TMUX, $TMUX_PANE,
// $KITTY_*, $VSCODE_PID — describes whatever environment the *server* was
// started in, frozen at that moment and handed to every pane it will ever
// spawn. Capturing them verbatim sent click-to-focus to an unrelated
// application, and, for a server started inside tmux, made the backchannel
// resolve to tmux and type into a foreign pane in a different window. This is
// the rationale the capture and control paths refer back to (#1348).
//
// The Tmux* fields are the same case, established by live measurement on tmux
// 3.6a (#1486): tmux's server daemonizes at first use, is reparented to PID 1,
// outlives every client, and hands its own launch-time environment to every
// pane it will ever spawn. A pane created minutes after the launching terminal
// was gone still carried that terminal's $ITERM_SESSION_ID and
// $TERM_SESSION_ID, while a different client was the only one attached — so a
// tmux pane keeps only TmuxPane/TmuxSocket, exactly as a herdr pane keeps only
// its address.
//
// Unlike a herdr pane, though, the address alone does not identify one: every
// descendant of a pane inherits $TMUX_PANE, so a GUI terminal or IDE launched
// from inside one carries a stale pane address next to a perfectly good host
// identity of its own. The capture keys the suppression on tmux's own
// TERM_PROGRAM marker for that reason, and leaves the ancestry fallbacks
// enabled so a descendant reporting no host itself is still resolved — see
// processlifecycle.launcherFromEnv.
//
// Such a descendant's pane ADDRESS is dropped rather than stored, which is a
// separate decision from the suppression above and was made later (#1582): the
// two fields are what control.resolveBackend routes on, so keeping them sent
// the user's input into a stranger's pane, in a window they were not looking
// at, and interrupt and read-back went to the same one. It has to be
// decided at capture, because these fields cannot carry their own provenance —
// a genuine pane that adopted its client's identity and a descendant that
// reported its own are the same struct. So a TmuxPane that reaches this type is
// the pane its process is IN, and every consumer may treat it as one.
//
// Note what tmux itself does to $TERM_PROGRAM, because it is the opposite of
// what the herdr paragraph above would lead you to expect: tmux overwrites it
// with the literal "tmux" rather than leaking the launching terminal's value.
// That does not make it usable. "tmux" names a multiplexer, not a window;
// it matches no entry in the macOS activator registry; and being non-empty it
// suppresses the TermProgram=="" guards that would otherwise reach the
// ancestry fallbacks. It is a value that can only mask, never resolve, which
// is why it is dropped rather than kept as a label.
//
// The host fields on a herdr launcher are therefore populated from a different
// process: the attached herdr *client*, which is what actually owns a window
// (#1350). Provenance is the whole distinction — the same TmuxPane that is a
// misroute when inherited by a pane is the correct selector when it is the
// client's own, because then the client really is running in that tmux pane.
// A session with no attached client keeps the two Herdr* fields alone, which
// is the honest answer: nothing is displaying it anywhere.
//
// Since #1501 a tmux pane's host fields come from the same indirection, one
// step cheaper: tmux is asked which clients are attached
// (`tmux -S <socket> list-clients -F '#{client_pid}'`) instead of the socket
// directory being scanned for them, and each client PID is then read exactly as
// a herdr client's is. A pane with no client attached keeps TmuxPane/TmuxSocket
// alone — nothing is displaying it — which is why a click on such a session
// still does nothing rather than raising a stale window.
type Launcher struct {
	TermProgram    string `json:"term_program,omitempty"`     // $TERM_PROGRAM (e.g. iTerm.app, Apple_Terminal, vscode, cursor, ghostty, WezTerm, Hyper)
	ITermSessionID string `json:"iterm_session_id,omitempty"` // $ITERM_SESSION_ID
	TermSessionID  string `json:"term_session_id,omitempty"`  // $TERM_SESSION_ID (Terminal.app)
	TmuxPane       string `json:"tmux_pane,omitempty"`        // $TMUX_PANE — the pane this process is IN. Every descendant of a pane inherits the variable (#1486), so an inherited one is dropped at capture instead of stored (#1582)
	TmuxSocket     string `json:"tmux_socket,omitempty"`      // first `,`-field of $TMUX
	VSCodePID      int    `json:"vscode_pid,omitempty"`       // $VSCODE_PID (vscode/cursor/windsurf)
	TTY            string `json:"tty,omitempty"`              // controlling TTY, e.g. "/dev/ttys021" — Terminal.app AppleScript matches tabs by this. The agent process's own, except on a herdr session with a client attached, where it is the client's: that is the tab actually displaying the pane (#1350)
	KittyListenOn  string `json:"kitty_listen_on,omitempty"`  // $KITTY_LISTEN_ON — kitty remote-control socket path
	KittyWindowID  string `json:"kitty_window_id,omitempty"`  // $KITTY_WINDOW_ID — kitty window identifier
	KittyPID       int    `json:"kitty_pid,omitempty"`        // $KITTY_PID — kitty.app process id (lets the activator target this specific instance when multiple kitties run)
	HostBundleID   string `json:"host_bundle_id,omitempty"`   // CFBundleIdentifier of the host app resolved by process-ancestry when no curated TermProgram matched (e.g. md.obsidian for an in-Obsidian terminal). Unlike TermProgram, this is derived server-side because the client has no map for arbitrary embedded-terminal hosts; the client builds a generic title-match activator from it.

	HerdrPaneID     string `json:"herdr_pane_id,omitempty"`     // $HERDR_PANE_ID — herdr pane address, e.g. "w1:p2" (workspace 1, pane 2)
	HerdrSocketPath string `json:"herdr_socket_path,omitempty"` // $HERDR_SOCKET_PATH — the herdr server's socket; the complete addressing key for that server, as $TMUX's socket field is for tmux
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
	// window/tab owns it. Derived by the daemon from the captured Launcher TTY,
	// and RE-derived whenever that TTY is repaired: an empty TTY may equally
	// mean the `ps` behind it never answered as that the process has no
	// terminal, so the first derivation can be a claim made on no evidence, and
	// freezing it made that claim permanent and persisted (#1546). The relative
	// frequency of the two is unmeasured — no such mis-stamp has been observed
	// in the wild. Name is set once; this is not.
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
		l.HostBundleID == "" && l.HerdrPaneID == "" && l.HerdrSocketPath == "")
}

// paneKind names which multiplexer a launcher's session lives in, as decided
// by (*Launcher).pane below.
type paneKind string

const (
	paneKindNone  paneKind = ""
	paneKindHerdr paneKind = "herdr"
	paneKindTmux  paneKind = "tmux"
)

// ownPane is the multiplexer pane a launcher's session lives IN — the address
// that is the PANE's rather than the displaying window's.
type ownPane struct {
	kind paneKind
	id   string
}

// pane reports which multiplexer pane this launcher's session lives in.
//
// The precedence is herdr-then-tmux, and it is not a preference: it is the
// SAME precedence processlifecycle.launcherFromEnv applies when it captures
// these fields, and the reason is #1348 — a herdr server started from inside a
// tmux pane hands every pane it will ever spawn a $TMUX_PANE that is the
// server's, not the agent's. A launcher carrying both addresses is therefore a
// herdr pane whose tmux address is inherited, never a tmux pane that also
// happens to be a herdr one. Deciding it here rather than at each call site is
// what stops the capture and the adoption from drifting to opposite answers.
//
// A launcher in NEITHER — a plain terminal session — reports paneKindNone, and
// callers must treat that as "this is not a pane" rather than as a default.
func (l *Launcher) pane() ownPane {
	switch {
	case l == nil:
		return ownPane{}
	case l.HerdrPaneID != "":
		return ownPane{kind: paneKindHerdr, id: l.HerdrPaneID}
	case l.TmuxPane != "":
		return ownPane{kind: paneKindTmux, id: l.TmuxPane}
	}
	return ownPane{}
}

// SamePaneAs reports whether other describes the same multiplexer pane as l.
//
// It exists for the one check that stands between a periodic host refresh and
// a PID-reuse misroute: a PID can be recycled by an unrelated process, and a
// fresh read of it describes whatever that process is in. Comparing the pane
// address — kind AND id, so a pane that changed multiplexer is not "the same
// pane with a new address" — is what tells a client that MOVED (the thing the
// refresh exists to observe) from a session that is no longer there at all.
//
// Two launchers in no pane at all are not "the same pane": paneKindNone is an
// absence, and answering true for it would let a refresh adopt a stranger's
// identity onto a session whose own address had gone.
func (l *Launcher) SamePaneAs(other *Launcher) bool {
	mine := l.pane()
	if mine.kind == paneKindNone {
		return false
	}
	return mine == other.pane()
}

// AdoptHostIdentity copies every host-window field of from onto l, leaving
// l's own pane address untouched, and reports whether anything changed. It is
// how a multiplexer pane acquires the identity of the client that displays it:
// the pane supplies the address to focus, the client supplies the window to
// raise.
//
// A nil or empty from is a no-op, so "no client attached" degrades to the
// address-only launcher rather than to a half-populated one. Callers must only
// pass a launcher they resolved from the attached client — the point of #1348
// is that the pane's own environment is not that.
func (l *Launcher) AdoptHostIdentity(from *Launcher) bool {
	if l == nil || from.IsEmpty() {
		return false
	}
	// Copy wholesale and put back the pane's own address, rather than listing
	// the host fields one by one. The host set grows (it has eleven members and
	// gains one per terminal integration) while a pane address is closed at two
	// fields, so enumerating the closed set is what keeps a newly added host
	// field from being silently left behind here.
	merged := *from
	l.putBackOwnPaneAddress(&merged)
	// TTY needs both sides, and the guard is symmetric on purpose:
	// BackgroundAgent.Detached is computed from this field (#744), so a client
	// resolved without a controlling tty must not erase the pane's own — and,
	// in the other direction, an agent that genuinely has no controlling
	// terminal (a background agent detached into a pool, which inherits the
	// pane's herdr env) must not be handed the client's tty and stop looking
	// detached. The client's tty describes the client's window, not a terminal
	// this process has.
	if l.TTY == "" || from.TTY == "" {
		merged.TTY = l.TTY
	}
	if merged == *l {
		return false
	}
	*l = merged
	return true
}

// putBackOwnPaneAddress restores onto merged the address that is l's PANE's
// rather than the displaying window's — the closed set AdoptHostIdentity's
// wholesale copy is allowed to overwrite and must not.
//
// WHICH closed set is l.pane()'s answer, and #1501 is why that is a question at
// all. Until then the only pane that adopted anything was a herdr one, so the
// put-back could name Herdr* unconditionally. A tmux pane adopts too now, and
// its address has to survive the copy for exactly the same reason:
// TmuxPane/TmuxSocket are what the backchannel routes on
// (control.resolveBackend) and what the macOS TmuxActivator selects with, and
// the attached client carries neither — it is a GUI terminal, not a pane.
// Leaving them behind would resolve the window and lose the pane inside it,
// which is a worse outcome than the nil it replaced: a click would raise the
// right window and select nothing, and control would fall through to a backend
// addressing a different process.
//
// Only the pane's OWN address is put back. A herdr client that is itself
// running inside tmux contributes real TmuxPane/TmuxSocket fields — the client
// really is in that pane — and those must pass through, which is what
// control.resolveBackend's herdr-before-tmux ordering depends on.
func (l *Launcher) putBackOwnPaneAddress(merged *Launcher) {
	switch l.pane().kind {
	case paneKindHerdr:
		merged.HerdrPaneID = l.HerdrPaneID
		merged.HerdrSocketPath = l.HerdrSocketPath
	case paneKindTmux:
		merged.TmuxPane = l.TmuxPane
		merged.TmuxSocket = l.TmuxSocket
	case paneKindNone:
		// Not reachable from either production call site — both resolve a
		// client only for a launcher that IS a pane — and pinned as a lock
		// rather than left to be inferred. There is no own address to keep, so
		// from stands whole.
	}
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
