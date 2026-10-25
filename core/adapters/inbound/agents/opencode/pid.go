package opencode

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the OpenCode client for a given session by matching the
// process CWD. OpenCode opens and closes the database for each write (WAL
// mode) rather than holding it open continuously, so lsof-based transcript-
// writer detection (used by Codex) is not reliable here.
//
// A client is a process processCmdPattern matches that is not the opencode
// 2.x service (IsServiceArgv): the service hosts every session of its user
// from $HOME, so binding by cwd would hand it any session started there
// (#2106; service-hosted sessions are #2104's P3).
//
// Trade-off: Two OpenCode instances in the same working directory are not
// distinguishable, because OpenCode does not hold the database open for the
// lifetime of the session.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWDAndCmdLineExcludingArgv(
		processCmdPattern, cwd, disambiguate, IsServiceArgv)
}

// LiveClientCWDs returns the working directories of the live OpenCode
// clients — DiscoverPID's processes, the service excluded. The watcher admits
// a session only while a client owns its directory.
func LiveClientCWDs() (map[string]struct{}, error) {
	return processlifecycle.LiveCWDsByCmdline(processCmdPattern, IsServiceArgv)
}
