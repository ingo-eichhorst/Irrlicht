package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// sessionsDir is the directory Claude Code writes per-process metadata files to.
// Each live Claude process owns ~/.claude/sessions/<pid>.json containing
// {"pid":N,"sessionId":"<uuid>","cwd":"...","startedAt":...}.
// Overridable in tests via the test helper in pid_test.go.
var sessionsDir = defaultSessionsDir()

func defaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

// claudeSessionMeta mirrors the on-disk schema of ~/.claude/sessions/<pid>.json.
// Only the fields we consume are declared; json.Unmarshal ignores the rest.
type claudeSessionMeta struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
}

// DiscoverPID finds the Claude Code process owning a session. It prefers an
// authoritative lookup in ~/.claude/sessions/<pid>.json (where Claude writes a
// direct PID↔sessionId mapping for every live process) and falls back to
// CWD-based matching only when that metadata is missing.
//
// The CWD fallback is restricted: if more than one live Claude process matches
// the CWD after excluding PIDs that belong to other sessions (per metadata),
// DiscoverPID returns 0 (unknown — retry later) rather than guessing. This
// prevents the flap loop in issue #109 where cwd-only matching would bind a
// new transcript to a PID already legitimately owned by another session,
// triggering destructive duplicate-PID cleanup.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	wantSessionID := sessionIDFromTranscript(transcriptPath)

	// Layer 1: authoritative metadata lookup.
	// Scan ~/.claude/sessions/*.json once, collecting PIDs that some metadata
	// file owns (for negative-filtering the fallback) and looking for an
	// exact sessionId match.
	claimedByOthers := make(map[int]bool)
	if wantSessionID != "" && sessionsDir != "" {
		entries, err := os.ReadDir(sessionsDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
					continue
				}
				meta, ok := readSessionMeta(filepath.Join(sessionsDir, e.Name()))
				if !ok {
					continue
				}
				if !pidAlive(meta.PID) {
					continue
				}
				if meta.SessionID == wantSessionID {
					return meta.PID, nil
				}
				// A live Claude process owns this PID for a different sessionId;
				// exclude it from the cwd fallback below.
				claimedByOthers[meta.PID] = true
			}
		}
	}

	// Layer 2: restricted CWD fallback.
	// Used when the metadata file for this session hasn't appeared yet (brief
	// startup window) or when transcriptPath is empty. Never guesses between
	// competing candidates — DiscoverPIDWithRetry will retry shortly.
	//
	// The disambiguate arg from the caller (PIDManager.TryDiscoverPID) is
	// deliberately ignored: its "prefer unclaimed, else highest PID" strategy
	// is the exact behavior that lets a new session steal a rival's PID.
	// The wrapped callback below enforces the stricter unambiguous rule.
	wrapped := func(pids []int) int {
		filtered := make([]int, 0, len(pids))
		for _, p := range pids {
			if claimedByOthers[p] {
				continue
			}
			filtered = append(filtered, p)
		}
		switch len(filtered) {
		case 0:
			return 0
		case 1:
			return filtered[0]
		default:
			// Ambiguous: multiple live claude processes share this cwd and
			// none are disambiguated by metadata. Returning 0 causes the
			// caller to retry, giving Claude time to write its metadata file.
			return 0
		}
	}
	return discoverByCWD(ProcessName, cwd, wrapped)
}

// sessionIDFromTranscript extracts Claude's canonical session UUID from a
// transcript path of the form .../<sessionID>.jsonl. Returns "" when the path
// is empty or doesn't look like a Claude transcript.
func sessionIDFromTranscript(path string) string {
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".jsonl") {
		return ""
	}
	return strings.TrimSuffix(base, ".jsonl")
}

// readSessionMeta reads and parses a single ~/.claude/sessions/<pid>.json file.
// Returns ok=false on any I/O or decode error — stale/garbage files are simply
// skipped.
func readSessionMeta(path string) (claudeSessionMeta, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return claudeSessionMeta{}, false
	}
	var meta claudeSessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return claudeSessionMeta{}, false
	}
	if meta.PID <= 0 || meta.SessionID == "" {
		return claudeSessionMeta{}, false
	}
	return meta, true
}

// pidAlive returns true if signal 0 can be delivered to pid (i.e. the process
// exists and the caller has permission). Overridable in tests.
var pidAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// discoverByCWD is the fallback CWD-matching implementation. It's a package
// variable so tests can inject a stub in place of the real pgrep+lsof call.
var discoverByCWD = processlifecycle.DiscoverPIDByCWD
