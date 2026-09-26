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

// revivalRecheckInterval bounds how often one replaced session's gates are
// evaluated. A parent whose work runs in subagents sends a hook per tool call,
// and an evaluation can run adapter PID discovery on the event loop.
const revivalRecheckInterval = 5 * time.Second

// replacedSession is what a root session deleted by the same-pid cleanup
// leaves behind: the pid a new session took from it and where it ran.
type replacedSession struct {
	pid     int
	adapter string
	cwd     string
	// checkedAt is when the gates last rejected a revival; see
	// revivalRecheckInterval.
	checkedAt time.Time
}

// recordReplacement is PIDManager's OnSessionReplaced. cleanupStalePIDHolders
// calls it right after onSessionDeleted (removeFromProjectSessions) for the
// same id, which drops any earlier record, so a record only ever describes
// the latest deletion.
func (d *SessionDetector) recordReplacement(old *session.SessionState, pid int) {
	if old.ParentSessionID != "" {
		return
	}
	d.mu.Lock()
	d.replacedSessions[old.SessionID] = &replacedSession{pid: pid, adapter: old.Adapter, cwd: old.CWD}
	d.mu.Unlock()
}

// tryReviveReplacedSession re-creates a session that the same-pid cleanup
// deleted, when one of its own hooks shows it is still running (issue #2059).
// Ordinary re-creation needs a main-transcript write less than
// orphanTranscriptAge old; a parent whose work runs in subagents can go much
// longer without one while its hooks keep arriving (29 min in #2042).
//
// A hook alone is not enough: /clear also ends in the same-pid cleanup, and a
// late hook can still carry the cleared transcript's path. So it revives only
// when the old pid is alive, no other root session holding it has a fresh
// transcript (so a session that is actually working keeps it), and the
// adapter's own PID discovery, run for this session, names that pid.
// claudecode discovery declines after a /clear on the current metadata schema
// (tested in #2044's LiveOwnerWithUpdatedAtNotStolenByStaleGate).
//
// The revived session then goes through ordinary PID discovery, whose
// same-pid cleanup evicts the session that took the pid. Returns true when it
// revived the session.
func (d *SessionDetector) tryReviveReplacedSession(ev agent.Event) bool {
	d.mu.Lock()
	rec := d.replacedSessions[ev.SessionID]
	deletedAt := d.deletedSessions[ev.SessionID]
	var checkedAt time.Time
	if rec != nil {
		checkedAt = rec.checkedAt
	}
	d.mu.Unlock()
	if rec == nil || time.Since(time.Unix(deletedAt, 0)) < d.deletedCooldown {
		return false
	}
	if time.Since(checkedAt) < revivalRecheckInterval {
		return false
	}
	if reason := d.revivalRejection(ev, rec); reason != "" {
		d.mu.Lock()
		rec.checkedAt = time.Now()
		d.mu.Unlock()
		d.log.LogInfo(logComponentSessionDetector, ev.SessionID, reason)
		return false
	}

	// The gates ran without d.mu; a concurrent deletion or re-creation may
	// have replaced or dropped the record since.
	d.mu.Lock()
	if d.replacedSessions[ev.SessionID] != rec {
		d.mu.Unlock()
		return false
	}
	delete(d.deletedSessions, ev.SessionID)
	delete(d.deletedStates, ev.SessionID)
	delete(d.replacedSessions, ev.SessionID)
	d.mu.Unlock()

	d.log.LogInfo(logComponentSessionDetector, ev.SessionID, revivedReplacedMsg)
	d.reviveReplacedSession(ev, rec)
	return true
}

// revivalRejection returns the log line naming the first gate that rejects a
// revival, or "" when every gate passes. Cheapest gates first: adapter
// discovery can scan metadata files and processes. rec's pid, adapter and cwd
// never change after recordReplacement, so reading them without d.mu is safe.
func (d *SessionDetector) revivalRejection(ev agent.Event, rec *replacedSession) string {
	if !ev.Synthetic {
		return notRevivedNotHookMsg
	}
	if !d.pidMgr.IsPIDAlive(rec.pid) {
		return notRevivedPIDExitedMsg
	}
	if d.pidHolderActive(ev.SessionID, rec.pid) {
		return notRevivedHolderActiveMsg
	}
	if d.pidMgr.DiscoverPIDOnly(ev.SessionID, rec.adapter, rec.cwd, ev.TranscriptPath) != rec.pid {
		return notRevivedNoPIDMatchMsg
	}
	return ""
}

// pidHolderActive reports whether a root session other than sessionID holds
// pid and has written its transcript within orphanTranscriptAge.
// isStaleTranscript is false for a path it cannot stat, so an unreadable
// holder counts as active.
func (d *SessionDetector) pidHolderActive(sessionID string, pid int) bool {
	for _, s := range d.pidMgr.rootPIDHolders(pid, sessionID) {
		if !isStaleTranscript(s.TranscriptPath) {
			return true
		}
	}
	return false
}

// reviveReplacedSession re-creates the session from its transcript and starts
// PID discovery for it. It skips onNewSession's admission gate on purpose:
// that gate rejects a stale transcript, and tryReviveReplacedSession's own
// checks are the evidence that replaces it. It does not retire pre-sessions: a
// revived session is not a new process.
func (d *SessionDetector) reviveReplacedSession(ev agent.Event, rec *replacedSession) {
	id := agent.Identity{Name: rec.adapter}
	ev.CWD = rec.cwd
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
		fmt.Sprintf(NewSessionInfoFormat, ev.ProjectDir, id.Name))

	state := d.buildNewSessionState(id, ev, d.nowFn().Unix())
	if !d.finalizeNewSession(id, ev, state, false) {
		return
	}
	go d.pidMgr.DiscoverPIDWithRetry(ev.SessionID, rec.cwd, ev.TranscriptPath, rec.adapter)
}
