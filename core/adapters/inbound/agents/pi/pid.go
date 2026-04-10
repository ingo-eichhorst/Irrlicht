package pi

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Pi process owning a session by matching "pi" processes
// whose CWD equals the session's working directory. Pi's binary resolves to
// process name "pi" (despite being a Node.js script), so pgrep -x "pi" works.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWD(ProcessName, cwd, disambiguate)
}
