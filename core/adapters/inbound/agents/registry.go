// Package agents exposes adapter-name → parser/policy lookups shared by
// any caller that needs to route a transcript to the correct format-specific
// handler without depending on each concrete adapter package directly.
//
// Today the replay harness in core/cmd/replay-session consumes this; the
// metrics collector and irrlichd main can migrate to it in a follow-up.
package agents

import (
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/tailer"
)

// ParserFor returns the format-specific TranscriptParser for an adapter name.
// Unknown names fall back to the Claude Code parser.
func ParserFor(name string) tailer.TranscriptParser {
	switch name {
	case codex.AdapterName:
		return &codex.Parser{}
	case pi.AdapterName:
		return &pi.Parser{}
	default:
		return &claudecode.Parser{}
	}
}

// PolicyFor returns the StatePolicy for an adapter name. Unknown names fall
// back to the default policy.
func PolicyFor(name string) agent.StatePolicy {
	switch name {
	case codex.AdapterName:
		return codex.StatePolicy()
	case pi.AdapterName:
		return pi.StatePolicy()
	case claudecode.AdapterName:
		return claudecode.StatePolicy()
	default:
		return agent.DefaultStatePolicy()
	}
}
