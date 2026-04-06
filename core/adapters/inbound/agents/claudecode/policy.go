package claudecode

import "irrlicht/core/domain/agent"

// StatePolicy returns Claude Code-specific state behavior.
func StatePolicy() agent.StatePolicy {
	return agent.StatePolicy{EnableStaleToolTimer: true}
}
