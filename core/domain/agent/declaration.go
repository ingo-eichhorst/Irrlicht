package agent

import (
	"fmt"

	"irrlicht/core/domain/permission"
	"irrlicht/core/pkg/cliversion"
)

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
// At most ONE observe-kind permission per adapter is meaningful: the gate that
// enforces observe consent resolves an adapter to its first observe-kind
// permission, so a second would never be consulted. An adapter that reads an
// additional file names it in the existing observe permission's Touches/Detail
// (the user-visible contract) rather than declaring another one.
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

	// Hooks is non-nil exactly for the permission that installs irrlicht's
	// hooks into an agent-owned config file. It carries what callers OUTSIDE
	// the adapter need — which file, and how to take our entries back out.
	Hooks *HookInstall
}

// HookInstall is the outside-the-adapter half of a hooks permission (#1357).
//
// Two places have to enumerate every agent config file irrlicht installs hooks
// into: the daemon's `--uninstall-hooks`, which must leave nothing behind
// pointing at a dead port, and the onboarding recorder, which backs those files
// up before running a grant-all daemon against the user's real $HOME. Both used
// to carry a hand-maintained two-element literal list, correct only for as long
// as there were exactly two hook adapters. Declaring the file here — next to the
// Apply/Remove closures that write it — makes both lists a projection of the
// registry, so a third adapter is covered by existing over being remembered.
type HookInstall struct {
	// ConfigPath resolves the ABSOLUTE path of the agent config file the
	// hooks are written into, honoring the same env overrides the adapter's
	// own installer honors (e.g. CODEX_HOME). It must resolve the real path
	// rather than a display string: the recorder backs up and restores
	// exactly what it returns.
	ConfigPath func() (string, error)
	// Uninstall removes irrlicht's entries from that file, reporting whether
	// it modified anything.
	Uninstall func() (bool, error)

	// Version declares the upstream CLI version floor this install requires
	// (issue #1365). Nil means the adapter declares no floor and installs
	// unconditionally — which is what Claude Code did for its whole life, and
	// what a third adapter would inherit by default, so a nil here is a
	// decision worth stating rather than an omission.
	Version *VersionGate
}

// VersionGate is a declared minimum upstream-CLI version for a hook install
// (issue #1365).
//
// It is data, not behaviour, and that is the whole point. Codex used to carry
// a private codexSupportsHooks/parseCodexVersion pair and its own
// minHookMajor/Minor/Patch constants; Claude Code carried nothing and wrote
// seven hook entries into ~/.claude/settings.json whatever version was
// installed. A third adapter joins the scheme by declaring two literals here —
// a version string and the argv that prints one — and PermissionService
// enforces it for every adapter identically.
type VersionGate struct {
	// Min is the lowest upstream CLI version at which EVERY event this
	// permission installs is known to the CLI. It is a whole-install floor,
	// not a per-event one: see the package comment on why the installed event
	// set must not vary with the version.
	Min string

	// Probe is the argv that asks the installed CLI its version, e.g.
	// {"claude", "--version"}. Used when Observed is nil or yields nothing.
	// Must be a compile-time literal — it is executed.
	Probe []string

	// Observed, when non-nil, supplies the installed version WITHOUT running
	// anything. Codex sets it: the adapter already captures cli_version from
	// every session header, so the newest transcript answers the question for
	// free and works even when the binary is not on the daemon's PATH. It is
	// consulted first; an empty return falls through to Probe. An adapter with
	// no such signal leaves it nil and gets the probe.
	Observed func() string
}

// Permits reports whether an install may proceed at the given installed
// version, and when it may not, why.
//
// Pure comparison — resolving `installed` (reading a transcript, running a
// binary) belongs to the caller, which keeps process execution out of the
// domain. An unknown or unparseable version permits the install: see
// cliversion.AtLeast for why unknown fails open rather than closed.
func (g *VersionGate) Permits(installed string) (bool, string) {
	if g == nil || g.Min == "" {
		return true, ""
	}
	ok, known := cliversion.AtLeast(installed, g.Min)
	if ok || !known {
		return true, ""
	}
	return false, fmt.Sprintf(
		"installed CLI is %s, which is older than the %s required by the hook events this installs; "+
			"nothing was written — upgrade the CLI and grant again",
		installed, g.Min)
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
