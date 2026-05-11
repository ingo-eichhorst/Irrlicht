package agents

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// TEMPORARY: removed in PR3 (#159 M4).
//
// LegacyParsers / LegacyPIDDiscoverers / LegacyProcessNames /
// LegacySubagentCounters / LegacyMetricsProviders bridge []agent.Agent
// (the new declaration shape) to the legacy adapter-name → function map
// shapes that pre-Agent consumers (metrics.New, NewSessionDetector, the
// replay CLI) still expect.
//
// Each helper is a leaf: no cross-helper dependencies, no global state.
// Once the consumers switch to consuming []agent.Agent directly (PR3+),
// this file and the agents.Config struct above it both delete.

// LegacyParsers produces the adapter-name → parser-factory map used by
// metrics.New() and the replay CLI's parserFor().
//
// JSONLineParser-shape adapters (claudecode, codex, pi) are produced
// uniformly. FilesUnderCWD (aider) and ProcessOwnedStore (opencode)
// adapters are intentionally omitted — main.go and the replay CLI
// register those entries explicitly via adapter-package imports, since
// the new RawLineParser/ProcessOwnedStore variants don't carry a parser
// factory that's compatible with the legacy ParserFactory type.
func LegacyParsers(agents []agent.Agent) map[string]ParserFactory {
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

// LegacyPIDDiscoverers produces the adapter-name → PIDDiscoverFunc map
// consumed by NewSessionDetector / NewPIDManager.
func LegacyPIDDiscoverers(agents []agent.Agent) map[string]agent.PIDDiscoverFunc {
	m := make(map[string]agent.PIDDiscoverFunc, len(agents))
	for _, a := range agents {
		m[a.Identity.Name] = a.Process.PIDForSession
	}
	return m
}

// LegacyProcessNames produces the adapter-name → OS-process-name map
// used by the startup zombie sweep. For ExactName matchers, the OS
// process name IS the matcher name. For CommandPattern matchers (aider),
// no reliable OS process name exists (aider runs under python), so we
// fall back to Identity.Name — same value the legacy agents.Config used
// historically. The zombie sweep does pgrep -x against this and finds
// nothing for CommandPattern adapters, which is correct behavior.
func LegacyProcessNames(agents []agent.Agent) map[string]string {
	m := make(map[string]string, len(agents))
	for _, a := range agents {
		if e, ok := a.Process.Match.(agent.ExactName); ok {
			m[a.Identity.Name] = e.Name
			continue
		}
		// CommandPattern: preserve historical Config.ProcessName=Identity.Name.
		m[a.Identity.Name] = a.Identity.Name
	}
	return m
}

// LegacySubagentCounters produces the adapter-name → SubagentCounter map
// consumed by metrics.New. Only adapters whose LineParser implements
// agent.SubagentCounter (currently: claudecode) appear in the map.
func LegacySubagentCounters(agents []agent.Agent) map[string]SubagentCounter {
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

// LegacyMetricsProviders produces the adapter-name → MetricsProvider map
// consumed by metrics.New. Only ProcessOwnedStore adapters with a
// non-nil Reader appear in the map.
func LegacyMetricsProviders(agents []agent.Agent) map[string]MetricsProvider {
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
