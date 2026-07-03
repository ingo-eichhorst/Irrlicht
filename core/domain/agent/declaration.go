package agent

import "irrlicht/core/domain/permission"

// Agent is the registration record each inbound agent adapter exports.
// It collapses identity, process recognition, session source, write-back
// control, and consent-gated permissions into five orthogonal axes. Each
// adapter package's Agent() constructor returns one value; the daemon wires
// per-adapter behavior off it via the map projections in
// core/adapters/inbound/agents (Parsers, PIDDiscoverers, ProcessNames,
// SubagentCounters, MetricsProviders) and the Source-variant dispatch
// in core/cmd/irrlichd/wiring.go.
type Agent struct {
	Identity    Identity
	Process     Process
	Source      Source
	Control     Control
	Permissions []Permission
}

// Control declares whether and how the daemon may write back to a discovered
// session of this agent through its terminal backend (issue #724, the
// "backchannel"). The zero value means not controllable — an adapter must opt
// in. Controllability also requires a usable backend target on the session's
// Launcher and the user's consent; this axis only states the agent *can* take
// interactive input.
type Control struct {
	// SupportsInput is true for agents that read from an interactive TUI/REPL
	// (claude, codex, …) and false for non-interactive/headless ones
	// (opencode's process-owned store), which can never be driven by injected
	// keystrokes.
	SupportsInput bool
	// Interrupt is how an interrupt is delivered to this agent.
	Interrupt InterruptMethod
	// Presets maps a backchannel preset id (backchannel.Preset*) to this
	// agent's concrete command text (issue #754) — e.g. "compact" → "/compact".
	// The text is the literal command only; the daemon appends the submit
	// sequence the session's terminal backend needs (a CR for tmux/kitty;
	// AppleScript hosts auto-submit). An agent that omits a preset doesn't
	// support it: the rule won't fire and the UI marks it unsupported, so a
	// wrong command is never sent. Nil means no presets are supported.
	Presets map[string]string
}

// InterruptMethod is how the daemon interrupts a running turn.
type InterruptMethod int

const (
	// InterruptNone means interrupts are not supported.
	InterruptNone InterruptMethod = iota
	// InterruptCtrlC delivers an ETX (Ctrl-C) into the terminal backend.
	InterruptCtrlC
	// InterruptSignal delivers SIGINT to the agent's process (reserved for a
	// future non-backend path).
	InterruptSignal
)

// ControlPermissionKey is the consent key gating the backchannel write path.
// Must match application/services.ControlPermissionKey.
const ControlPermissionKey = "control"

// ControlPermission is the consent gate for the backchannel write path (issue
// #724), shared by every adapter that declares Control.SupportsInput. KindModify
// with nil Apply/Remove: granting only permits InputService to forward input;
// it writes nothing to disk. The backchannel master-toggle gates it further.
func ControlPermission() Permission {
	return Permission{
		Key:             ControlPermissionKey,
		Kind:            permission.KindModify,
		Title:           "Send input to sessions",
		FeatureUnlocked: "Reply to prompts, interrupt turns, and run event→action rules (backchannel)",
		Touches:         "Sends text you submit into this agent's controlling terminal via its terminal backend (e.g. tmux send-keys)",
		Detail: "When granted — and only while the Backchannel beta toggle is on — " +
			"the daemon may forward text you submit and interrupt signals into a " +
			"running session of this agent by scripting the terminal backend that " +
			"owns it (tmux/kitty/…). Input is injected as if you typed it. Only " +
			"sessions with a controllable terminal backend are affected; nothing is " +
			"sent unless you (or a rule you configured) send it. Granting changes " +
			"nothing on disk; toggling off stops all forwarding immediately.",
	}
}

// Permission declares one consent-gated capability of an adapter: what
// it touches, what feature it unlocks, and (for modify-kind entries) how
// to apply and undo the modification. The daemon exercises nothing — no
// install, no read — until the user grants the permission (issue #570).
type Permission struct {
	Key             string          // stable identifier, e.g. "hooks", "transcripts"
	Kind            permission.Kind // modify (writes a file) or observe (reads only)
	Title           string          // short label for the wizard row
	FeatureUnlocked string          // what granting enables
	Touches         string          // what the daemon reads/writes when granted
	Detail          string          // expanded (i) text: exact paths/commands, how to undo

	// Apply performs the modification on grant; Remove undoes it on
	// revoke. Nil for observe-kind permissions — their effect (starting
	// and stopping the agent's watchers) is owned by the daemon wiring.
	Apply  func() error
	Remove func() error
}

// Identity is the always-required adapter metadata served via
// GET /api/v1/agents. Frontends key off Name; DisplayName + icons are
// purely presentational.
type Identity struct {
	Name         string // adapter label on events, e.g. "claude-code"
	DisplayName  string // human-readable label, e.g. "Claude Code"
	IconSVGLight string // raw <svg>…</svg> markup, light theme
	IconSVGDark  string // raw <svg>…</svg> markup, dark theme
}

// Process bundles the two universal process-related contracts every
// adapter must declare: how to recognize the agent's OS processes, and
// how to map a session (by cwd + transcript path) back to a single PID.
type Process struct {
	Match         ProcessMatcher
	PIDForSession PIDDiscoverFunc

	// ExcludeArgv, when non-nil, lets an adapter reject a matched process by
	// inspecting its argv. The process scanner consults it after a PID
	// matches Match: returning true means "this is the agent's binary but not
	// a session" (e.g. a background daemon / wrapper that runs the same
	// binary) and no pre-session is minted. The format-specific argv shapes
	// live in the adapter package; the scanner stays generic. A nil argv
	// (unreadable, e.g. hardened-runtime) is passed through, so an adapter's
	// predicate must default to *not* excluding when it can't tell.
	ExcludeArgv func(argv []string) bool

	// RequireKnownHost, when true, gates session admission on the bound
	// process's OS-level ancestry: a candidate PID whose parent chain doesn't
	// resolve to a recognized terminal emulator or IDE is rejected, and no
	// session is created for it. Unlike ExcludeArgv (adapter-specific argv
	// shapes), the ancestry check itself is generic and shared — this is a
	// plain opt-in flag, not a predicate. Added for issue #784: a third-party
	// menu-bar app (CodexBar) kept an Antigravity `agy` CLI process running in
	// the background for quota polling, with no distinguishing argv or cwd
	// signal, so identity has to come from who launched the process instead.
	RequireKnownHost bool
}
