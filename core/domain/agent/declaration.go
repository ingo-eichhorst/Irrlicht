package agent

import "irrlicht/core/domain/permission"

// Agent is the registration record each inbound agent adapter exports.
// It collapses identity, process recognition, session source, and
// consent-gated permissions into four orthogonal axes. Each adapter
// package's Agent() constructor returns one value; the daemon wires
// per-adapter behavior off it via the map projections in
// core/adapters/inbound/agents (Parsers, PIDDiscoverers, ProcessNames,
// SubagentCounters, MetricsProviders) and the Source-variant dispatch
// in core/cmd/irrlichd/wiring.go.
type Agent struct {
	Identity    Identity
	Process     Process
	Source      Source
	Permissions []Permission
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
}
