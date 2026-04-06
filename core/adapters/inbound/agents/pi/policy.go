package pi

import "irrlicht/core/domain/agent"

// StatePolicy returns Pi-specific state behavior.
//
// Pi transcripts currently don't expose a permission-pending signal,
// and normal tool calls can run for >5s. Disable stale-tool waiting to
// avoid false waiting transitions.
func StatePolicy() agent.StatePolicy {
	return agent.StatePolicy{EnableStaleToolTimer: false}
}
