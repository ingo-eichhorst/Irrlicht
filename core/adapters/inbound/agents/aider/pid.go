package aider

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Aider process owning a session by matching command
// lines containing "/aider" whose CWD equals the session's working directory.
// Aider runs as `python /path/to/aider …` (uv/pipx wrapper), so the bare
// process name is `python` — pgrep -x aider would find nothing. We mirror
// the CommandLineMatch pattern declared in Config() so the scanner and the
// PID discoverer agree on what "an aider process" means.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWDAndCmdLine("/aider", cwd, disambiguate)
}
