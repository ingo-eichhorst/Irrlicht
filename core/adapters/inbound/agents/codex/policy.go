package codex

import "irrlicht/core/domain/agent"

// StatePolicy returns Codex-specific state behavior.
func StatePolicy() agent.StatePolicy {
	return agent.StatePolicy{EnableStaleToolTimer: true}
}
