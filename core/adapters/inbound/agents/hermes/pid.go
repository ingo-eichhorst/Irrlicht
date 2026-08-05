package hermes

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Hermes process backing a session by matching the
// process CWD against the full command line.
//
// The command-line variant is required rather than preferred: Hermes runs as
// a Python console script, so pgrep -x "hermes" matches nothing and the
// name-based DiscoverPIDByCWD opencode uses would always return 0. The argv
// filter drops the long-lived gateway/service processes, which share the
// binary but own no session.
//
// Hermes opens the SQLite store per write rather than holding it open, so
// transcript-writer detection (codex, pi) does not apply. Two Hermes sessions
// launched from the same directory are therefore not distinguishable — the
// disambiguator picks among them.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWDAndCmdLineExcludingArgv(
		processCmdPattern, cwd, disambiguate, isServiceArgv)
}
