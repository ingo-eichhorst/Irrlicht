// Package agents defines the unified registration record for inbound agent
// adapters. Each adapter package (claudecode, codex, pi) exports a constructor
// that returns a Config; the daemon's wiring in cmd/irrlichd/main.go derives
// all per-adapter plumbing from one []Config slice — fswatcher roots, process
// scanner targets, the parser factory map consumed by the metrics collector,
// and the PID-discovery map consumed by the SessionDetector.
//
// This reverses the former dependency direction: the metrics adapter and the
// replay CLI used to import each concrete adapter package to build their own
// parser registries. With Config, callers receive the map from main.go and the
// agents package holds no imports of sibling adapter packages.
package agents

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/tailer"
)

// ParserFactory returns a fresh TranscriptParser instance. Parsers are
// stateful (Claude Code tracks pending turns; Codex tracks a cumulative
// usage cursor), so every TranscriptTailer needs its own instance.
type ParserFactory func() tailer.TranscriptParser

// SubagentCounter reports how many in-process child agents a live session
// currently has open. Adapters that model subagents as separate transcripts
// (codex, pi) leave this nil — the domain-level summary walks file-based
// children. Claude Code, which tracks subagents inline in the parent
// transcript's metrics, provides a non-nil counter.
type SubagentCounter func(m *tailer.SessionMetrics) int

// Config is the registration record each inbound agent adapter exports.
//
// Per-adapter capability declarations live in
// replaydata/agents/<name>/capabilities.json (keyed against
// replaydata/agents/features.json). They are not Go data anymore — the
// ir:onboard-agent skill reads the JSON to compute scenario × adapter
// applicability.
type Config struct {
	Name               string        // adapter label on events, e.g. "claude-code"
	ProcessName        string        // OS-level executable name for pgrep -x, e.g. "claude"
	RootDir            string        // transcript root relative to $HOME, e.g. ".claude/projects"
	NewParser          ParserFactory // fresh-per-call factory; parsers are stateful
	DiscoverPID        agent.PIDDiscoverFunc
	CountOpenSubagents SubagentCounter // optional; nil = always zero

	// CommandLineMatch is an optional regex pattern fed to `pgrep -f` instead
	// of `pgrep -x ProcessName`. Use this for agents whose process name on
	// disk doesn't match their CLI name — e.g. Python tools where the OS
	// process is `python` and the agent script is in argv[1]. A pattern like
	// "/aider($| )" matches the binary path while excluding wrapper scripts
	// (tmux, sh) that mention the agent name in their own argv. Leave empty
	// for exact-match agents (claude, codex, pi).
	CommandLineMatch string

	// TranscriptFilename is an optional fixed filename the scanner looks for
	// in each detected process's CWD (e.g. ".aider.chat.history.md"). When
	// set, the scanner emits a per-PID transcript_new event with the real
	// path on the first poll where the file exists in CWD. Use this for
	// agents that write transcripts per-project (in CWD) rather than under
	// a fixed RootDir under $HOME — the fswatcher's "watch one fixed root"
	// model doesn't fit them. Leave empty for fswatcher-friendly agents
	// (claude-code, codex, pi).
	TranscriptFilename string
}

// ParserMap collapses a slice of Configs into a name → factory map. Callers
// build a fresh parser for each transcript path they start tailing.
func ParserMap(cfgs []Config) map[string]ParserFactory {
	m := make(map[string]ParserFactory, len(cfgs))
	for _, c := range cfgs {
		m[c.Name] = c.NewParser
	}
	return m
}

// PIDDiscoverMap collapses a slice of Configs into the adapter → discovery
// function map consumed by the SessionDetector.
func PIDDiscoverMap(cfgs []Config) map[string]agent.PIDDiscoverFunc {
	m := make(map[string]agent.PIDDiscoverFunc, len(cfgs))
	for _, c := range cfgs {
		m[c.Name] = c.DiscoverPID
	}
	return m
}
