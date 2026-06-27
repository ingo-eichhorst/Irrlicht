// Package agents provides per-adapter map projections of an []agent.Agent
// slice. Callers that consume per-adapter behavior as adapter-name → function
// maps (metrics.Adapter, NewSessionDetector, NewPIDManager, the replay CLI)
// use these helpers; callers that consume per-adapter behavior structurally
// dispatch on agent.Source variants directly.
package agents

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// ParserFactory returns a fresh TranscriptParser instance. Parsers are
// stateful (Claude Code tracks pending turns; Codex tracks a cumulative
// usage cursor), so every TranscriptTailer needs its own instance.
type ParserFactory func() tailer.TranscriptParser

// SubagentCounter reports how many in-process child agents a live session
// currently has open. Adapters that model subagents as separate transcripts
// (codex, pi) don't register a counter — the domain-level summary walks
// file-based children. Claude Code, which tracks subagents inline in the
// parent transcript's metrics, provides a non-nil counter.
type SubagentCounter func(m *tailer.SessionMetrics) int

// MetricsProvider is an optional adapter-supplied function that computes
// session metrics directly from the agent's native storage format, bypassing
// the JSONL-tailer path. Used by adapters (like OpenCode) that store state
// in a database rather than append-only JSONL transcript files.
//
// transcriptPath is whatever the adapter set in agent.Event.TranscriptPath
// (e.g. the SQLite database path). sessionID is the session UUID.
// Returns nil, nil when the session has no data yet.
type MetricsProvider func(transcriptPath, sessionID string) (*session.SessionMetrics, error)

// Parsers produces the adapter-name → parser-factory map consumed by
// metrics.Adapter and the replay CLI's parserFor().
//
// JSONLineParser-shape adapters (claudecode, codex, pi) are produced
// uniformly. FilesUnderCWD (aider) and ProcessOwnedStore (opencode)
// adapters are intentionally omitted — main.go and the replay CLI
// register those entries explicitly via adapter-package imports, since
// the new RawLineParser/ProcessOwnedStore variants don't carry a parser
// factory that's compatible with ParserFactory.
func Parsers(agents []agent.Agent) map[string]ParserFactory {
	m := make(map[string]ParserFactory, len(agents))
	for _, a := range agents {
		if jp, ok := jsonLineParserOf(a); ok {
			np := jp.NewParser
			m[a.Identity.Name] = func() tailer.TranscriptParser {
				return np().(tailer.TranscriptParser)
			}
		}
	}
	return m
}

// PIDDiscoverers produces the adapter-name → PIDDiscoverFunc map consumed
// by NewSessionDetector / NewPIDManager.
func PIDDiscoverers(agents []agent.Agent) map[string]agent.PIDDiscoverFunc {
	m := make(map[string]agent.PIDDiscoverFunc, len(agents))
	for _, a := range agents {
		m[a.Identity.Name] = a.Process.PIDForSession
	}
	return m
}

// ProcessNames produces the adapter-name → OS-process-name map used by
// the startup zombie sweep. For ExactName matchers the OS process name
// IS the matcher name. For CommandPattern matchers no reliable OS
// process name exists (e.g. aider runs under python), so we fall back
// to Identity.Name; the zombie sweep does pgrep -x against this and
// finds nothing, which is correct behavior.
func ProcessNames(agents []agent.Agent) map[string]string {
	m := make(map[string]string, len(agents))
	for _, a := range agents {
		if e, ok := a.Process.Match.(agent.ExactName); ok {
			m[a.Identity.Name] = e.Name
			continue
		}
		// CommandPattern: fall back to the adapter's Identity.Name.
		m[a.Identity.Name] = a.Identity.Name
	}
	return m
}

// ArgvExcluders produces the adapter-name → Process.ExcludeArgv map consumed by
// the liveness sweep's infra re-validation: a session bound to a still-alive PID
// whose argv the adapter rejects (e.g. Claude Code's --bg-spare pool helper) is a
// ghost and must be reaped (#727). Only adapters that declare a non-nil
// Process.ExcludeArgv appear (today: claude-code); the rest get no entry, so the
// sweep leaves their sessions untouched.
func ArgvExcluders(agents []agent.Agent) map[string]func([]string) bool {
	m := make(map[string]func([]string) bool)
	for _, a := range agents {
		if a.Process.ExcludeArgv != nil {
			m[a.Identity.Name] = a.Process.ExcludeArgv
		}
	}
	return m
}

// ControlPresets produces the adapter-name → (preset id → command text) map
// consumed by the BackchannelEngine to translate a rule's agent-agnostic preset
// into the concrete command for the session's agent (issue #754). Only adapters
// that declare Control.Presets appear; for the rest a preset rule degrades
// gracefully (the engine logs and doesn't fire). The submit sequence is owned
// downstream by the controller, not these strings.
func ControlPresets(agents []agent.Agent) map[string]map[string]string {
	m := make(map[string]map[string]string)
	for _, a := range agents {
		if len(a.Control.Presets) > 0 {
			m[a.Identity.Name] = a.Control.Presets
		}
	}
	return m
}

// SubagentCounters produces the adapter-name → SubagentCounter map
// consumed by metrics.Adapter. Only adapters whose LineParser implements
// agent.SubagentCounter (currently: claudecode) appear in the map.
func SubagentCounters(agents []agent.Agent) map[string]SubagentCounter {
	m := make(map[string]SubagentCounter)
	for _, a := range agents {
		jp, ok := jsonLineParserOf(a)
		if !ok {
			continue
		}
		p := jp.NewParser()
		sc, ok := p.(agent.SubagentCounter)
		if !ok {
			continue
		}
		m[a.Identity.Name] = func(metrics *tailer.SessionMetrics) int {
			return sc.OpenSubagents(metrics)
		}
	}
	return m
}

// MetricsProviders produces the adapter-name → MetricsProvider map
// consumed by metrics.Adapter. Only ProcessOwnedStore adapters with a
// non-nil Reader appear in the map.
func MetricsProviders(agents []agent.Agent) map[string]MetricsProvider {
	m := make(map[string]MetricsProvider)
	for _, a := range agents {
		s, ok := a.Source.(agent.ProcessOwnedStore)
		if !ok || s.Reader == nil {
			continue
		}
		r := s.Reader
		m[a.Identity.Name] = func(transcriptPath, sessionID string) (*session.SessionMetrics, error) {
			return r.ComputeMetrics(transcriptPath, sessionID)
		}
	}
	return m
}

// jsonLineParserOf returns the JSONLineParser carried inside an Agent's
// FilesUnderRoot source (if any). Returns the parser and ok=true when
// the Agent declares a FilesUnderRoot source with a JSONLineParser.
func jsonLineParserOf(a agent.Agent) (agent.JSONLineParser, bool) {
	s, ok := a.Source.(agent.FilesUnderRoot)
	if !ok {
		return agent.JSONLineParser{}, false
	}
	jp, ok := s.Parser.(agent.JSONLineParser)
	return jp, ok
}
