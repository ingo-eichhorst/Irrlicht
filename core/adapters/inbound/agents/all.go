package agents

import (
	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/domain/agent"
)

// All returns the canonical adapter slice the daemon wires up at boot.
// Order matters: the first entry's parser is the fallback for unknown
// adapter names in metrics.Registry.FallbackName. Exported so the
// agent-onboarding viewer can build the same metrics Registry during
// replay without duplicating the construction.
func All() []agent.Agent {
	return []agent.Agent{
		claudecode.Agent(),
		codex.Agent(),
		pi.Agent(),
		aider.Agent(),
		opencode.Agent(),
	}
}
