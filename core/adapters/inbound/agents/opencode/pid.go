package opencode

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the OpenCode process for a given session by matching the
// process CWD. OpenCode opens and closes the database for each write (WAL
// mode) rather than holding it open continuously, so lsof-based transcript-
// writer detection (used by Codex) is not reliable here.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWD(ProcessName, cwd, disambiguate)
}
