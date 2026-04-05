package claudecode

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Claude Code process owning a session by matching
// "claude" processes whose CWD matches the session's working directory.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWD("claude", cwd, disambiguate)
}
