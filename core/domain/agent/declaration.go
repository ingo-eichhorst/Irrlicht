package agent

import (
	"fmt"
	"strings"

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

	// Writes is non-nil for every permission whose Apply closure writes a
	// shared, user-owned file — a file that follows $HOME rather than
	// IRRLICHT_HOME, and that therefore outlives the daemon that wrote it. It
	// carries what callers OUTSIDE the adapter need: which file, and how to
	// take irrlicht's content back out.
	Writes *ManagedUserFile

	// Advisory declares paths this permission's Apply is KNOWN to cause to be
	// written, that ManagedUserFile cannot represent (issue #1748). The
	// motivating case: kiro-cli/hooks's Apply shells out to the real
	// `kiro-cli` binary, and that child process writes its OWN identity store
	// ($HOME/Library/Application Support/kiro-cli/data.sqlite3) as a side
	// effect of merely launching — measured true even when the install itself
	// fails. Also cannot hold this because Also's whole contract (#1718)
	// assumes what ManagedUserFile assumes everywhere else: the content is
	// irrlicht's, so Uninstall can remove it and a snapshot/restore can safely
	// stand in for "don't touch it". Neither holds for an agent CLI's own
	// database:
	//
	//   - It carries no irrlicht content, so Uninstall is meaningless for it.
	//   - Where it lives is platform-specific and has only been verified on
	//     one OS (kiro-cli's own resolver may put it somewhere else entirely
	//     on another).
	//   - It is a live external store; blindly snapshotting and restoring one
	//     is a corruption risk, not a protection.
	//
	// So Advisory entries are declared for VISIBILITY only:
	// --print-advisory-files reports them separately from
	// --print-managed-files, and the recording rig warns that a recording
	// touched them without attempting to back them up or restore them. See
	// agents.AdvisoryFiles and core/cmd/irrlichd/managedwrites_test.go's
	// static arm, which now recognizes an Advisory resolver as declared the
	// same way it already recognizes Writes.Path/Also.
	Advisory []AdvisoryWrite
}

// AdvisoryWrite is one path a permission's Apply is known to write that holds
// no irrlicht-authored content — see Permission.Advisory for why it is a
// separate category from ManagedUserFile rather than another Also entry.
type AdvisoryWrite struct {
	// Path resolves the absolute path the write is known to land at. Unlike
	// ManagedUserFile.Path, a resolution error here is tolerated by every
	// consumer (agents.AdvisoryFiles skips it) rather than treated as fatal:
	// an advisory declaration is explicitly allowed to be scoped to a
	// platform where the write has actually been observed, and "unverified
	// elsewhere" is not the same defect as "our own required config failed to
	// resolve".
	Path func() (string, error)
	// Reason documents what the path is and why it is advisory rather than a
	// Writes.Also entry — shown verbatim by --print-advisory-files and the
	// recording rig's warning.
	Reason string
}

// HooksPermissionKey is the permission key under which an adapter installs
// irrlicht's hooks into an agent-owned config file. It is declared here rather
// than restated per adapter because two things narrow on it from outside the
// adapters: `irrlichd --uninstall-hooks`, which must touch the hook entries and
// nothing else, and the registry-wide version-floor tripwire. A per-adapter
// spelling is a string two packages have to agree on by convention (#1383).
const HooksPermissionKey = "hooks"

// InstructionsPermissionKey is the permission key under which an adapter writes
// irrlicht's managed instruction blocks into an agent's user-level instruction
// file (~/.claude/CLAUDE.md today). Declared here for the same reason
// HooksPermissionKey is: `irrlichd --uninstall-task-eta` narrows the
// managed-file projection to it from OUTSIDE the adapter, so a per-adapter
// spelling would be a string two packages agree on by convention.
//
// The narrowing is what keeps that command's name true in both directions — it
// must not revoke the hook entries, and --uninstall-hooks must not revoke this
// (#1437, the sibling of #1425).
const InstructionsPermissionKey = "instructions"

// ManagedUserFile is the outside-the-adapter half of a permission that writes a
// shared user-owned file (#1357, widened by #1383).
//
// Two places have to enumerate those files. The daemon's `--uninstall-hooks`
// must leave nothing behind pointing at a dead port. And the onboarding
// recorder backs them up before running a grant-all daemon against the user's
// real $HOME — grant-all exercises EVERY modify permission's Apply closure, so
// the recorder's protected set is "every managed user file", not "every hook
// config": it is what stops a recording leaving irrlicht's managed blocks in
// the user's own ~/.claude/CLAUDE.md and ~/.config/kitty/kitty.conf.
//
// Both lists used to be hand-maintained literals, correct only for as long as
// the adapter set stayed frozen. Declaring the file here — next to the
// Apply/Remove closures that write it — makes both a projection of the consent
// catalog, so a new adapter is covered by existing over being remembered.
//
// The two consumers read DIFFERENT slices of the same declaration and that is
// deliberate: the recorder protects everything, while `--uninstall-hooks`
// narrows to HooksPermissionKey so it never revokes a capability the user did
// not ask it to touch.
type ManagedUserFile struct {
	// Path resolves the ABSOLUTE path of the file this permission writes,
	// honoring the same env overrides the adapter's own installer honors
	// (e.g. CODEX_HOME, XDG_CONFIG_HOME). It must resolve the real path
	// rather than a display string: the recorder backs up and restores
	// exactly what it returns.
	Path func() (string, error)
	// Uninstall removes irrlicht's content from that file, reporting whether
	// it modified anything. Required, not optional: a permission that can
	// write a user-owned file it cannot take itself back out of is the state
	// this declaration exists to make impossible.
	Uninstall func() (bool, error)

	// Verify reports what our entries look like in that file RIGHT NOW,
	// without writing anything (issue #1372).
	//
	// It exists because the install is not a one-shot fact. An agent whose own
	// UI rewrites its settings can delete our entries between the grant and
	// the next hook: gemini-cli's writer is sync-by-omission — it drops every
	// key absent from the snapshot the CLI took at startup, recursively — and
	// one of its write paths (saveModelChange) is not even user-initiated. The
	// symptom is indistinguishable from every other silent hook failure: a
	// permission still reading `granted`, no error anywhere, and waiting
	// detection quietly degraded.
	//
	// Read-only is the contract, not an implementation detail. The repair is a
	// separate step run through the permission service, so that the write
	// stays behind the same consent gate the original install passed, and so
	// that a verifier can never become a way to write to a config the user
	// withdrew consent for (#570).
	Verify func() (HookEntryStatus, error)

	// Version declares the upstream CLI version floor this install requires
	// (issue #1365). Nil means the permission declares no floor and installs
	// unconditionally — which is what Claude Code's hooks did for their whole
	// life, and what a new hook adapter would inherit by default, so a nil on a
	// HooksPermissionKey permission is a decision worth stating rather than an
	// omission (TestEveryHookInstallDeclaresAVersionFloor enforces it there).
	// Permissions that patch a config file irrlicht owns end-to-end — the
	// CLAUDE.md instruction blocks, the kitty remote-control block — depend on
	// no upstream CLI feature and leave it nil.
	Version *VersionGate

	// Also resolves zero or more ADDITIONAL shared, user-owned files this
	// permission's single Apply closure writes, beyond Path (issue #1718).
	//
	// Path stays what every existing narrowed projection (agents.HookConfigs,
	// `--uninstall-hooks`) means by "the file this permission writes" — adding
	// this field changes that meaning for nobody, and all eight declaration
	// sites that existed before it compile and behave unchanged with it left
	// nil. It exists because mistral-vibe's hooks install is the first whose
	// Apply genuinely spans two files: the hook entries themselves
	// ($VIBE_HOME/hooks.toml) and a separate boolean gate
	// (enable_experimental_hooks in $VIBE_HOME/config.toml) that must also be
	// true or the entries are never read. Neither #1449's grant-all refusal nor
	// the recorder's full backup/restore sweep (agents.ManagedUserFiles) can see
	// a write this field does not name — an undeclared secondary write is
	// invisible to both, which is the exact shape of the incident #1449 exists
	// to prevent, so both MUST resolve every entry here with the same rigor as
	// Path (core/application/services/permission_shared_config_gate.go,
	// core/adapters/inbound/agents/managedfiles.go).
	//
	// Deliberately not folded into Path becoming a slice: Path is read by name
	// in enough places (log lines, the narrowed uninstall commands' per-file
	// reporting) that widening its type would touch every one of them for a
	// property only two adapters need. An adapter with a second file appends
	// one resolver here instead.
	Also []func() (string, error)

	// RealFixture declares this permission's committed, real-world config
	// document (issue #1756) — a document CAPTURED from the agent's own
	// output (or the shared file it manages), not one any test constructed.
	//
	// #1753 shipped because every hook-install test in the tree graded
	// against a config its OWN test wrote: a document whose shape the test
	// author already knew how to handle. mistral-vibe's real config.toml
	// (core/adapters/inbound/agents/vibe/testdata/real-config-2.19.1.toml,
	// captured for #1755) carries twelve multi-line arrays no hand-written
	// fixture ever had; hooktoml's splicer refused all of them, Apply failed,
	// and consent kept reading granted. Nil means no such document is
	// declared — which TestEveryHookInstallDeclaresARealConfigFixture
	// (core/adapters/inbound/agents/hookrealfixture_test.go) flags for any
	// hooks-installing adapter not named in that test's own explicit,
	// reasoned gap list, so an omission is a loud failure rather than a
	// silent one.
	RealFixture *RealConfigFixture
}

// RealConfigFixture is one committed, real-world config fixture (issue
// #1756): a maintainer-captured copy of a document the AGENT ITSELF
// produced (or manages, for a shared settings file), secrets redacted,
// exercised by the declaring adapter's own install/uninstall tests in
// addition to whatever synthetic documents those tests construct by hand.
type RealConfigFixture struct {
	// Path is the fixture file, relative to the declaring adapter's own
	// package directory — e.g. "testdata/real-config-2.19.1.toml". Read by
	// TestEveryHookInstallDeclaresARealConfigFixture to confirm the file
	// actually exists and is not a stub, and referenced (as a literal) by
	// the adapter's own guard test that reads it.
	Path string

	// CLIVersion is the upstream CLI version that produced the captured
	// document, so a later reader can tell how stale the evidence is — the
	// same staleness problem docs/replay-testing.md names for replay
	// goldens. Required whenever Path is set: an unstamped fixture is
	// evidence nobody can date.
	CLIVersion string
}

// HookEntryStatus is one read-only look at an install (issue #1372): which of
// the events the adapter installs are no longer represented in the config file
// the way we would write them today.
//
// The two buckets are kept apart because they are different accidents, even
// though the repair for both is the same call. Missing means nothing carrying
// our sentinel is in that event's array at all — somebody deleted it, which is
// the gemini-cli sync-by-omission case. Stale means an entry of ours IS there
// but no longer canonical — most often pointing at a port this daemon is not
// listening on (#1178), which a user reading the file would not spot as broken.
type HookEntryStatus struct {
	// Missing are event names with no irrlicht entry left in the file.
	Missing []string
	// Stale are event names whose irrlicht entry is present but not in the
	// shape (or at the endpoint) we would install today.
	Stale []string
}

// Intact reports whether the install needs nothing done to it.
func (s HookEntryStatus) Intact() bool {
	return len(s.Missing) == 0 && len(s.Stale) == 0
}

// Damage renders the finding for a log line, e.g. "missing: Stop, Notification;
// stale: PreToolUse". Empty for an intact install, so a caller never logs a
// verdict with nothing behind it.
func (s HookEntryStatus) Damage() string {
	var parts []string
	if len(s.Missing) > 0 {
		parts = append(parts, "missing: "+strings.Join(s.Missing, ", "))
	}
	if len(s.Stale) > 0 {
		parts = append(parts, "stale: "+strings.Join(s.Stale, ", "))
	}
	return strings.Join(parts, "; ")
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
	// The unknown-fails-open decision is AtLeast's and is encoded there once:
	// it already reports ok=true for anything it could not read. Re-deriving it
	// here from `known` would mean two places decide the direction and the
	// contract arm that claims to pin it would only be pinning this copy.
	if ok, _ := cliversion.AtLeast(installed, g.Min); ok {
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
