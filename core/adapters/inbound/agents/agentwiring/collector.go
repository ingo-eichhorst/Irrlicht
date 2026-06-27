// Package agentwiring composes the canonical metrics collector shared by
// the daemon (core/cmd/irrlichd) and the agent-onboarding replay viewer.
//
// It lives in its own package on purpose: core/adapters/outbound/metrics
// imports core/adapters/inbound/agents for the Registry value types, so a
// constructor that calls metrics.New cannot live in the agents package
// itself without an import cycle (agents → metrics → agents). Pulling the
// composition one level out keeps a single source of truth for the parser
// map + collector wiring without breaking that graph.
package agentwiring

import (
	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/outbound/compaction"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// ParserFactories returns the complete adapter-name → parser-factory map
// for the given adapter slice: agents.Parsers (JSONLineParser-shape
// adapters) plus the FilesUnderCWD (aider) and ProcessOwnedStore
// (opencode) overrides that agents.Parsers intentionally omits, because
// the RawLineParser/ProcessOwnedStore source variants don't carry a
// factory compatible with agents.ParserFactory.
//
// This is the one place those two overrides are declared; both the daemon
// and the viewer build their collector from it, so a new adapter can never
// be wired in one and silently dropped in the other. The companion test
// asserts every adapter in agents.All() is covered here.
func ParserFactories(adapters []agent.Agent) map[string]agents.ParserFactory {
	parserFactories := agents.Parsers(adapters)
	parserFactories[aider.AdapterName] = func() tailer.TranscriptParser { return &aider.Parser{} }
	parserFactories[opencode.AdapterName] = func() tailer.TranscriptParser { return &opencode.Parser{} }
	return parserFactories
}

// BuildMetricsCollector wires the metrics collector the daemon uses at
// boot from the given adapter slice. Claude Code's parser is the
// documented fallback for unknown adapter names. Single source of truth
// for core/cmd/irrlichd and the agent-onboarding viewer.
func BuildMetricsCollector(adapters []agent.Agent) outbound.MetricsCollector {
	return metrics.New(metrics.Registry{
		Parsers:          ParserFactories(adapters),
		SubagentCounters: agents.SubagentCounters(adapters),
		MetricsProviders: agents.MetricsProviders(adapters),
		FallbackName:     claudecode.AdapterName,
		// Default headline compaction (issue #759). A future setting or LLM
		// adapter swaps this one line.
		Compactor: compaction.DeterministicCompactor{},
	})
}
