package aider

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Aider process owning a session by matching "aider"
// processes whose CWD equals the session's working directory. Same pattern
// as pi.DiscoverPID — Aider's binary resolves to process name "aider" and
// is one-CWD-per-session in normal usage.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWD(ProcessName, cwd, disambiguate)
}
