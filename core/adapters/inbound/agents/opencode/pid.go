package opencode

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/pkg/sqlitero"
)

// DiscoverPID finds the OpenCode process for a given session: the client
// whose cwd is the session's directory, else — for a session no client runs —
// the opencode 2.x service that hosts it (servicePIDFor, #2107). OpenCode
// opens and closes the database for each write (WAL mode) rather than holding
// it open continuously, so lsof-based transcript-writer detection (used by
// Codex) is not reliable here.
//
// A client is a process processCmdPattern matches that is not the opencode
// 2.x service (IsServiceArgv): the service hosts every session of its user
// from $HOME, so binding by cwd would hand it any session started there
// (#2106).
//
// Trade-off: Two OpenCode instances in the same working directory are not
// distinguishable, because OpenCode does not hold the database open for the
// lifetime of the session. For the same reason a client-less session in a
// directory a client runs in binds to that client.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	pid, err := processlifecycle.DiscoverPIDByCWDAndCmdLineExcludingArgv(
		processCmdPattern, cwd, disambiguate, IsServiceArgv)
	if err != nil || pid > 0 {
		return pid, err
	}
	return servicePIDFor(transcriptPath)
}

// servicePIDFor returns the PID of the opencode 2.x service hosting the
// session transcriptPath keys: the one running service, while the session's
// session_v2 row is live (v2SessionLive). It returns 0 when no service runs;
// when more than one does — the stable service binds a fixed port (49374),
// which lets one run at a time (#2104), but a second, such as another release
// channel's, is not ruled out, and no row says which one hosts a session, so
// it binds none rather than guess; and when the row is archived, deleted, or
// not a 2.x row at all — a 1.x store's session is no service's.
//
// Never through the service's service.json, which would name it directly: that
// file carries the service's password, and no consent covers reading it.
func servicePIDFor(transcriptPath string) (int, error) {
	pids, err := ServicePIDs()
	if err != nil || len(pids) != 1 {
		return 0, err
	}
	if !v2SessionLive(transcriptPath) {
		return 0, nil
	}
	return pids[0], nil
}

// OwnsSharedPID is opencode's Process.SharedPIDOwner (#2107): pid still hosts
// the session transcriptPath keys when pid is the 2.x service
// (IsServiceArgv over its argv) and the session's session_v2 row is live. So
// any number of client-less roots share the service's PID while their sessions
// last, and an archived or deleted one gives it up. A client's PID is never
// shared: two roots on one TUI keep 1.x's exclusive supersession. Every read
// that fails — an unreadable argv, a store that will not open — answers false,
// as the contract requires.
//
// time_suspended plays no part: 2.x sets it on a TUI's session for its whole
// working turn (watcher_v2.go's note on it), so it does not mean "closed".
func OwnsSharedPID(cwd, transcriptPath string, pid int) bool {
	return IsServiceArgv(processlifecycle.ReadArgv(pid)) && v2SessionLive(transcriptPath)
}

// v2SessionLive reports whether transcriptPath keys a session whose
// session_v2 row exists and is not archived. A key carrying no session id, a
// store that will not open, and a store with no session_v2 table (a 1.x one)
// all answer false.
func v2SessionLive(transcriptPath string) bool {
	dbPath, sid := parseTranscriptPath(transcriptPath, "")
	if dbPath == "" || sid == "" {
		return false
	}
	db, err := sqlitero.Open(dbPath)
	if err != nil {
		return false
	}
	defer db.Close()
	gone, err := v2SessionGone(db, sid)
	return err == nil && !gone
}

// LiveClientCWDs returns the working directories of the live OpenCode
// clients — DiscoverPID's processes, the service excluded. The watcher admits
// a session in one of them on its activity.
func LiveClientCWDs() (map[string]struct{}, error) {
	return processlifecycle.LiveCWDsByCmdline(processCmdPattern, IsServiceArgv)
}

// ServicePIDs returns the PIDs of the running opencode 2.x services: the
// processes processCmdPattern matches whose argv IsServiceArgv recognizes.
func ServicePIDs() ([]int, error) {
	return processlifecycle.PIDsByCmdlineKeepingArgv(processCmdPattern, IsServiceArgv)
}
