package junie

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// defaultProcessesDir is the path relative to $HOME where Junie writes one
// process sidecar per (process, session) pair:
//
//	~/.junie/processes/<pid>-<session-id>-<hash>.json
//	{"pid":64871,"sessionId":"session-...","projectPath":"/path","startedAt":...}
//
// It is a direct session→PID binding — no CWD scan needed while a sidecar is
// fresh. Sidecars are not cleaned up when the process exits, so a PID read
// from one is only trusted after a liveness + command-pattern check.
const defaultProcessesDir = ".junie/processes"

// processSidecar mirrors the JSON body of a ~/.junie/processes/ sidecar.
// Unrecognized fields (future additions) are ignored. StartedAt (epoch ms)
// orders sidecars naming the same PID: one junie process serves every
// session of its IDE window over its lifetime and writes one sidecar per
// session, all naming its own PID — only the newest binding is current.
type processSidecar struct {
	PID         int    `json:"pid"`
	SessionID   string `json:"sessionId"`
	ProjectPath string `json:"projectPath"`
	StartedAt   int64  `json:"startedAt"`
}

// DiscoverPID finds the Junie process for a session, preferring the direct
// binding in ~/.junie/processes/ and falling back to CWD/cmdline discovery.
//
// The sidecar path: parse the session ID from the transcript path, find the
// sidecar naming that session, and return its PID if the PID is alive AND its
// command line still matches processCmdRegex — a stale sidecar whose PID the
// OS has reused for an unrelated process must not be bound (the same reason
// vibe liveness-checks nothing it hasn't pattern-matched).
//
// A live sidecar PID is NOT sufficient: one junie process serves every
// session of its IDE window over its lifetime and leaves one sidecar per
// session, all naming its own PID (captured live: three sidecars for three
// sequential sessions of one project, every one naming the same live PID).
// Only the session whose sidecar carries the newest startedAt is the one
// actually running on that process; binding the PID for a finished sibling
// too made the sessions delete each other through the core's
// one-session-per-PID reconciliation in an endless loop that reset each
// session's model to "unknown" and double-counted cost on every lap. A
// session that is not the current owner gets no PID at all — it is a
// transcript-only session until Junie writes it a fresh sidecar (resume).
//
// The fallback mirrors aider/vibe: match live command lines against
// processCmdPattern and narrow by working directory. The transcript carries
// no cwd; when the caller has none yet, the sidecar's projectPath stands in
// — useful even when its PID is stale, because the project directory
// outlives any one process. The ownership gate applies here too: the scan
// finds sibling sessions' processes by shared project directory, so a PID
// some newer session's sidecar names must not be stolen.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	sessionID := sessionIDFromPath(transcriptPath)
	sc := sidecarForSession(sessionID)
	if sc != nil && liveJuniePID(sc.PID) {
		if sessionOwnsPID(sc.PID, sessionID) {
			return sc.PID, nil
		}
		return 0, nil
	}
	if cwd == "" && sc != nil {
		cwd = sc.ProjectPath
	}
	pid, err := processlifecycle.DiscoverPIDByCWDAndCmdLine(processCmdPattern, cwd, disambiguate)
	if err != nil || pid == 0 || !sessionOwnsPID(pid, sessionID) {
		return 0, err
	}
	return pid, nil
}

// sessionOwnsPID reports whether sessionID is the current owner of pid per
// the sidecar election — or the pid is unowned (no sidecar names it, e.g. a
// process Junie wrote no sidecar for), which any session may bind.
func sessionOwnsPID(pid int, sessionID string) bool {
	dir, err := agentpaths.AbsRoot(defaultProcessesDir)
	if err != nil {
		return true
	}
	owner := currentSessionOnPID(dir, pid)
	return owner == "" || owner == sessionID
}

// currentSessionOnPID elects the session currently running on pid: among all
// parseable sidecars in dir whose body names pid, the greatest startedAt
// wins (a strictly-greater comparison over ReadDir's sorted order keeps ties
// deterministic on the lexically-first file). Returns "" when no sidecar
// names the pid or the dir is unreadable — the caller treats that as
// unowned. The filename also embeds a PID, but as everywhere else in this
// file the JSON body is authoritative.
func currentSessionOnPID(dir string, pid int) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var owner string
	newest := int64(-1)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		var sc processSidecar
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || json.Unmarshal(data, &sc) != nil {
			continue
		}
		if sc.PID != pid || sc.SessionID == "" {
			continue
		}
		if sc.StartedAt > newest {
			newest = sc.StartedAt
			owner = sc.SessionID
		}
	}
	return owner
}

// sidecarForSession locates the process sidecar for sessionID under the
// default ~/.junie/processes/ directory, or nil when there is none.
func sidecarForSession(sessionID string) *processSidecar {
	dir, err := agentpaths.AbsRoot(defaultProcessesDir)
	if err != nil {
		return nil
	}
	return sidecarIn(dir, sessionID)
}

// sidecarIn scans dir for the sidecar naming sessionID. The filename embeds
// the session ID (<pid>-<session-id>-<hash>.json) but the JSON body is
// authoritative: a file is accepted only when its sessionId field matches and
// its pid is positive. Malformed or unreadable files are skipped — a sidecar
// the adapter can't read must cost that candidate, not the discovery.
// When several sidecars name the same session (a resumed session gets a new
// process and a new sidecar; the old one lingers), the caller's liveness gate
// decides — this returns the first parseable match with a live PID, else the
// first parseable match.
func sidecarIn(dir, sessionID string) *processSidecar {
	if sessionID == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var first *processSidecar
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || !strings.Contains(name, sessionID) {
			continue
		}
		var sc processSidecar
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || json.Unmarshal(data, &sc) != nil {
			continue
		}
		if sc.SessionID != sessionID || sc.PID <= 0 {
			continue
		}
		if liveJuniePID(sc.PID) {
			return &sc
		}
		if first == nil {
			first = &sc
		}
	}
	return first
}

// liveJuniePID reports whether pid names a live process whose command line
// matches the junie pattern. Both halves matter: liveness alone accepts a
// reused PID, and a pattern match alone accepts a dead one. An unreadable
// argv (hardened-runtime process, race against exit) fails the check —
// a PID the adapter can't verify is a PID it must not bind.
func liveJuniePID(pid int) bool {
	if !processlifecycle.IsAlive(pid) {
		return false
	}
	argv, err := processlifecycle.Observer().ArgvOf(pid)
	if err != nil || len(argv) == 0 {
		return false
	}
	return processCmdRegex.MatchString(strings.Join(argv, " "))
}
