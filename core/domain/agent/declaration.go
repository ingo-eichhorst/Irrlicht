package agent

// Agent is the registration record each inbound agent adapter exports.
// It collapses identity, process recognition, and session source into three
// orthogonal axes. Each adapter package's Agent() constructor returns one
// Agent value; the daemon will wire every per-adapter behavior off it.
//
// In PR1 of #159 these types are pure additions with no callers — the
// daemon still consumes the legacy agents.Config slice (see PR2/PR3 for
// the wiring switch and the eventual deletion of agents.Config).
//
// New variants slated for Phase B/C (HostedByEditor for Process.Match,
// EditorSharedKVStore for Source, DocumentParser for FileParser) will be
// added as additional cases on their respective sealed sums without
// breaking adapters targeting the existing shape.
type Agent struct {
	Identity Identity
	Process  Process
	Source   Source
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
}
