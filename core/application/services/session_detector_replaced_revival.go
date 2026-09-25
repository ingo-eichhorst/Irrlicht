package services

import (
	"fmt"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
)

// Log lines for the revival decision (issue #2059). Before it, a hook for a
// session deleted as replaced was dropped without a trace.
const (
	revivedReplacedMsg        = "reviving replaced session: a hook arrived and adapter discovery still names its pid"
	notRevivedNoPIDMatchMsg   = "not reviving replaced session: adapter discovery does not name its old pid"
	notRevivedPIDExitedMsg    = "not reviving replaced session: its old pid has exited"
	notRevivedHolderActiveMsg = "not reviving replaced session: the session now holding its pid is active"
	notRevivedNotHookMsg      = "not reviving replaced session: the activity is not a hook"
)

// replacedSession is what a root session deleted by the same-pid cleanup
// leaves behind: the pid a new session took from it and where it ran.
type replacedSession struct {
	pid        int
	adapter    string
	cwd        string
	projectDir string
	// armed is set once the deletion that recorded this entry has written
	// its tombstone. Any later deletion of the same id drops the entry, so
	// only a same-pid replacement can ever make a session revivable.
	armed bool
}

// recordReplacement is PIDManager's OnSessionReplaced. cleanupStalePIDHolders
// calls it just before onSessionDeleted for the same id, which is why the
// entry starts unarmed and the project dir is still in projectSessions.
func (d *SessionDetector) recordReplacement(old *session.SessionState, pid int) {
	if old.ParentSessionID != "" {
		return
	}
	d.mu.Lock()
	d.replacedSessions[old.SessionID] = replacedSession{
		pid: pid, adapter: old.Adapter, cwd: old.CWD,
		projectDir: d.projectSessions[old.SessionID],
	}
	d.mu.Unlock()
}

// armOrDropReplacement runs under d.mu from removeFromProjectSessions. The
// first deletion after recordReplacement arms the entry; any other deletion
// drops it.
func (d *SessionDetector) armOrDropReplacement(sessionID string) {
	rec, ok := d.replacedSessions[sessionID]
	if !ok {
		return
	}
	if rec.armed {
		delete(d.replacedSessions, sessionID)
		return
	}
	rec.armed = true
	d.replacedSessions[sessionID] = rec
}

// tryReviveReplacedSession re-creates a session that the same-pid cleanup
// deleted, when one of its own hooks shows it is still running (issue #2059).
// Ordinary re-creation needs a main-transcript write less than
// orphanTranscriptAge old; a parent whose work runs in subagents can go much
// longer without one while its hooks keep arriving (29 min in #2042).
//
// A hook alone is not enough: /clear also ends in the same-pid cleanup, and a
// late hook can still carry the cleared transcript's path. So it revives only
// when both of these hold:
//   - the adapter's own PID discovery, run for this session, names the pid it
//     lost, and that process is alive. claudecode discovery declines after a
//     /clear on the current metadata schema (tested in #2044's
//     LiveOwnerWithUpdatedAtNotStolenByStaleGate);
//   - no other root session holding that pid has a fresh transcript, so a
//     session that is actually working keeps it.
//
// The revived session then goes through ordinary PID discovery, whose
// same-pid cleanup evicts the session that took the pid. Returns true when
// it revived the session.
func (d *SessionDetector) tryReviveReplacedSession(ev agent.Event) bool {
	d.mu.Lock()
	rec, ok := d.replacedSessions[ev.SessionID]
	deletedAt := d.deletedSessions[ev.SessionID]
	d.mu.Unlock()
	if !ok || !rec.armed || time.Since(time.Unix(deletedAt, 0)) < d.deletedCooldown {
		return false
	}
	if reason := d.revivalRejection(ev, rec); reason != "" {
		d.log.LogInfo(logComponentSessionDetector, ev.SessionID, reason)
		return false
	}

	d.mu.Lock()
	if cur, still := d.replacedSessions[ev.SessionID]; !still || cur != rec || d.deletedSessions[ev.SessionID] != deletedAt {
		d.mu.Unlock()
		return false
	}
	delete(d.deletedSessions, ev.SessionID)
	delete(d.deletedStates, ev.SessionID)
	delete(d.replacedSessions, ev.SessionID)
	d.projectSessions[ev.SessionID] = rec.projectDir
	d.mu.Unlock()

	d.log.LogInfo(logComponentSessionDetector, ev.SessionID, revivedReplacedMsg)
	d.reviveReplacedSession(ev, rec)
	return true
}

// revivalRejection returns the log line naming the first gate that rejects a
// revival, or "" when every gate passes.
func (d *SessionDetector) revivalRejection(ev agent.Event, rec replacedSession) string {
	if !ev.Synthetic {
		return notRevivedNotHookMsg
	}
	if !d.pidMgr.IsPIDAlive(rec.pid) {
		return notRevivedPIDExitedMsg
	}
	if d.pidMgr.DiscoverPIDOnly(ev.SessionID, rec.adapter, rec.cwd, ev.TranscriptPath) != rec.pid {
		return notRevivedNoPIDMatchMsg
	}
	if d.pidHolderActive(ev.SessionID, rec.pid) {
		return notRevivedHolderActiveMsg
	}
	return ""
}

// pidHolderActive reports whether a root session other than sessionID holds
// pid and has written its transcript within orphanTranscriptAge.
// isStaleTranscript is false for a path it cannot stat, so an unreadable
// holder counts as active.
func (d *SessionDetector) pidHolderActive(sessionID string, pid int) bool {
	states, err := d.repo.ListAll()
	if err != nil {
		return true
	}
	for _, s := range states {
		if s.SessionID == sessionID || s.PID != pid || s.ParentSessionID != "" {
			continue
		}
		if !isStaleTranscript(s.TranscriptPath) {
			return true
		}
	}
	return false
}

// reviveReplacedSession re-creates the session from its transcript and starts
// PID discovery for it. It skips onNewSession's admission gate on purpose:
// that gate rejects a stale transcript, and tryReviveReplacedSession's own
// checks are the evidence that replaces it. The event handed to
// finalizeNewSession has no transcript path so that it does not retire
// pre-sessions: a revived session is not a new process.
func (d *SessionDetector) reviveReplacedSession(ev agent.Event, rec replacedSession) {
	id := agent.Identity{Name: rec.adapter}
	ev.CWD = rec.cwd
	ev.ProjectDir = rec.projectDir
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
		fmt.Sprintf(NewSessionInfoFormat, ev.ProjectDir, id.Name))

	state := d.buildNewSessionState(id, ev, d.nowFn().Unix())
	finalize := ev
	finalize.TranscriptPath = ""
	if !d.finalizeNewSession(id, finalize, state) {
		return
	}
	go d.pidMgr.DiscoverPIDWithRetry(ev.SessionID, rec.cwd, ev.TranscriptPath, rec.adapter)
}
