package muse

import (
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Muse process bound to a session by asking which
// process currently holds that session's .session.lock file open for
// writing. format-spec §1 confirms the fd is held via advisory flock only
// while the session is live — released on both a clean /exit and a SIGKILL
// (live-tested both exit paths) — while the stale "pid=<N>" text the file
// also carries survives either exit path unmodified, so the text alone is
// not a liveness signal and is never read here.
//
// This is deliberately NOT a process-name-plus-cwd scan (junie/vibe's
// shape). format-spec §2 caught a single OS process (`muse serve`, Meta's
// MSP host mode) holding open lock fds on 11 concurrent sessions at once —
// "one muse-bin pid ⇒ one session" does not hold, and a name-based scan
// would need an election among candidates the way junie's process-sidecar
// binding does (there, several sidecars naming the same pid, newest
// startedAt wins). Keying the lsof lookup on THIS session's own lock file
// sidesteps that entirely: lsof is scoped to the one path given, so the
// answer is already the right session's owner whether it came from a
// dedicated interactive invocation or a shared `serve` host holding many
// other sessions' locks at the same time.
//
// Resolving the right PID per session is not the whole story, though: under
// `muse serve` every live root resolves to the SAME PID, and the daemon's
// same-PID supersession (#169) treats a second root on one PID as replacing
// the first. OwnsSharedPID is what keeps those roots side by side (#2084).
//
// Falls back to asking the identical question of the transcript file
// itself (session.jsonl) when the lock file has no writer. UNVERIFIED
// whether Muse ever keeps session.jsonl open for the session's lifetime the
// way Codex/Pi do — format-spec's evidence is all about .session.lock, none
// about session.jsonl's own fd — but the fallback costs one extra lsof call
// only when the first comes back empty, and covers that shape for free if
// it turns out to hold. Also covers nested subagent session.jsonl files
// (<parent>/subagent/<child-id>/session.jsonl) uniformly: whatever
// transcriptPath names, the lock path is derived as its direct sibling, so
// a child session with no .session.lock of its own (UNVERIFIED whether one
// exists — format-spec §9 never inspected a subagent directory's contents
// beyond session.jsonl) still falls through to the transcript-file check.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	if lockPath := sessionLockPath(transcriptPath); lockPath != "" {
		pid, err := processlifecycle.DiscoverPIDByTranscriptWriter(lockPath)
		if err != nil {
			return 0, err
		}
		if pid != 0 {
			return pid, nil
		}
	}
	return processlifecycle.DiscoverPIDByTranscriptWriter(transcriptPath)
}

// OwnsSharedPID reports whether this session is still bound to pid by the same
// probe DiscoverPID uses — its .session.lock's writer, else its transcript's —
// asked of one root that shares pid with a newer muse root. An erroring probe,
// no writer, or a different writer does not confirm ownership
// (agent.SharedPIDOwnerFunc's contract: inconclusive is false), so the root
// falls back to the exclusive same-PID policy and is retired.
//
// Under `muse serve` this keeps every root whose lock the host still holds.
// In the dedicated one-session-per-process mode, /clear starts a new session
// in the same process and releases the old session's lock (live-probed for
// /clear only, recorded in
// replaydata/agents/muse/scenarios/1-5_session-reset/metadata.json), so the
// old root answers false and is retired as before — provided muse does not
// also hold the old session.jsonl open, which the fallback above would read
// as ownership (UNVERIFIED, as noted there). Also not measured: whether the
// lock release always lands before the new root's PID binds. If it does not,
// the old root is kept at assignment and retired by a later periodic
// same-PID sweep instead (SweepDeadPIDs ticks every 5s, backing off to 15s).
func OwnsSharedPID(cwd, transcriptPath string, pid int) bool {
	if pid <= 0 {
		return false
	}
	owner, err := DiscoverPID(cwd, transcriptPath, nil)
	return err == nil && owner == pid
}

// sessionLockPath derives a session's .session.lock path from its
// session.jsonl transcript path: the lock file is a direct sibling, in the
// same session directory (format-spec §1). Returns "" when transcriptPath
// carries no real parent directory to anchor the sibling on.
func sessionLockPath(transcriptPath string) string {
	dir := filepath.Dir(transcriptPath)
	if dir == "" || dir == "." {
		return ""
	}
	return filepath.Join(dir, sessionLockFilename)
}
