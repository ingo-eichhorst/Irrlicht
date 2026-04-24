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

// Capability describes a behavior an adapter (and its upstream agent) supports.
// The ir:onboard-agent skill uses capability lists to decide which canonical
// scenarios apply to which adapters — an adapter that does not declare
// CapSubagents is excluded from any scenario whose Requires list contains it.
type Capability string

const (
	// CapHeadlessMode — the agent CLI supports a non-interactive prompt→exit
	// mode suitable for scripted driving (claude --print, codex exec, pi --print).
	CapHeadlessMode Capability = "headless_mode"
	// CapToolCalls — the agent can invoke tools mid-session.
	CapToolCalls Capability = "tool_calls"
	// CapPermissionHooks — the adapter wires permission-pending state via
	// agent-emitted hooks (Claude Code's PermissionRequest / PostToolUse*).
	CapPermissionHooks Capability = "permission_hooks"
	// CapSubagents — the agent can spawn child agents that appear as separate
	// tracked sessions with a ParentSessionID link.
	CapSubagents Capability = "subagents"
)

// Config is the registration record each inbound agent adapter exports.
type Config struct {
	Name               string        // adapter label on events, e.g. "claude-code"
	ProcessName        string        // OS-level executable name for pgrep, e.g. "claude"
	RootDir            string        // transcript root relative to $HOME, e.g. ".claude/projects"
	NewParser          ParserFactory // fresh-per-call factory; parsers are stateful
	DiscoverPID        agent.PIDDiscoverFunc
	CountOpenSubagents SubagentCounter // optional; nil = always zero
	Capabilities       []Capability    // consumed by the onboarding-scenario matrix
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
