package services

import (
	"fmt"
	"strings"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func (d *SessionDetector) removeFromProjectSessions(sessionID string) {
	d.mu.Lock()
	delete(d.projectSessions, sessionID)
	d.deletedSessions[sessionID] = time.Now().Unix()
	d.mu.Unlock()
	// Drop the background-process liveness cache for the gone session — a
	// deleted session is never re-observed as non-working, so
	// applyBackgroundLiveness would otherwise never reclaim these entries
	// (issue #445).
	d.bgMu.Lock()
	delete(d.bgLive, sessionID)
	delete(d.bgProbing, sessionID)
	d.bgMu.Unlock()
	if d.historyTracker != nil {
		d.historyTracker.Remove(sessionID)
	}
	d.forgetSessionScopedState(sessionID)
}

// cacheDeletedSnapshot stashes state's last known in-memory value, keyed by
// session ID, alongside the deletedSessions tombstone removeFromProjectSessions
// writes for the same id. It exists so a hook that authoritatively signals
// turn-done (opencode's session.idle, hermes' on_session_end — anything that
// dispatches an agent.Event with Terminal set) but loses the race with THIS
// exact teardown — HandleProcessExit records ProcessExited and the repo row
// is gone before the plugin's own HTTP beacon can land — still has a session
// to classify against instead of nothing at all. See processActivity's
// Terminal branch and issue #1772.
//
// Wired as PIDManagerDeps.OnSessionRemoved, fired only from deleteSession —
// the single choke point every actual repo-row removal routes through
// (process exit, the ready-TTL/liveness sweep, the startup zombie sweep).
// Deliberately NOT fired from HandleProcessExit's own early onSessionDeleted
// call (no state is loaded yet at that point) and NOT fired by the /clear
// cleanup or dedup/supersession paths, which call repo.Delete directly
// without going through deleteSession — reviving a session removed on
// purpose would be exactly the ghost-session class deletedCooldown exists to
// prevent.
func (d *SessionDetector) cacheDeletedSnapshot(state *session.SessionState) {
	if state == nil {
		return
	}
	d.mu.Lock()
	d.deletedStates[state.SessionID] = state
	d.mu.Unlock()
}

// forgetSessionScopedState drops the per-session bookkeeping that only the
// session itself gives meaning to. It exists because there are TWO teardown
// paths and they are not the same one:
//
//   - onRemoved, which fires on the TRANSCRIPT FILE disappearing;
//   - removeFromProjectSessions, reached from the PIDManager's
//     OnSessionDeleted callback — dead process, duplicate-PID dedup, ready-TTL
//     age-out, parent cleanup, pre-session supersession.
//
// A dying process does not delete its transcript, so the second path is how
// most sessions actually end, and it was clearing neither the signal holds nor
// the #1366 dwell. Nothing revisits a deleted session, so an entry left behind
// is permanent for the life of the process and a recycled session ID inherits
// it. That drift is not hypothetical: this function was extracted after the
// second map was added to the first list and missed on the second, which is
// exactly how the first one was missed before it.
//
// Every store here is idempotent, so both callers may call it unconditionally.
// The rule for the next per-session map: add it HERE, not to a call site.
//
// Not everything per-session belongs in this set. deletedSessions is a
// deliberate tombstone written BY this path and aged out on its own schedule,
// and the background-liveness caches are cleared inline above with their own
// reasoning (#445).
func (d *SessionDetector) forgetSessionScopedState(sessionID string) {
	// SignalHolds, unlike StateDwell, is not nil-safe; struct-literal test
	// detectors reach this path without one.
	if d.signals != nil {
		d.signals.DropSession(sessionID)
	}
	d.dwell.DropSession(sessionID)

	// The hook-liveness watchdog's rising-edge memory (#1368): one bool per
	// session, kept for the life of the process otherwise, and a recycled
	// session ID would inherit a stale "was done" and swallow its first turn.
	// Nil-safe on its own receiver, so it needs no guard like signals above.
	d.hookLiveness.Forget(sessionID)

	d.idleMu.Lock()
	delete(d.idleProjectRetryAttempts, sessionID)
	d.idleMu.Unlock()
}

// broadcast sends a push notification if a broadcaster is configured. For
// parent sessions, the unified Subagents summary is refreshed so WebSocket
// clients see the same counts as the REST-hydration path. When a child
// session is broadcast, the parent is also re-broadcast with an updated
// summary — otherwise the badge would go stale until the parent's next
// transcript event.
func (d *SessionDetector) broadcast(msgType string, state *session.SessionState) {
	if d.broadcaster == nil {
		return
	}

	d.refreshSubagentSummary(state)
	d.broadcaster.Broadcast(outbound.PushMessage{Type: msgType, Session: state})

	// Newly-created sessions get an immediate history snapshot so any
	// connected client can hydrate the row's history bars before the first
	// tick or transition rolls in.
	if msgType == outbound.PushTypeCreated && d.historyTracker != nil {
		d.historyTracker.EmitSnapshot(state.SessionID)
	}

	if state.ParentSessionID == "" {
		return
	}
	parent, err := d.repo.Load(state.ParentSessionID)
	if err != nil || parent == nil {
		return
	}
	d.refreshSubagentSummary(parent)
	d.broadcaster.Broadcast(outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: parent})
}

// cleanupPreSessionsForProject retires any pre-session(s) (proc-<pid>) for
// the same project/cwd now that a real transcript-backed session has
// arrived. Returns whether at least one pre-session was actually retired —
// callers feed this into ShouldSynthesizeCatchUpTurn (state_classifier.go)
// as its "was this daemon already live-tracking the process" signal.
// newSessionID (the real session's id) lets it fire onSessionSuperseded so a
// subsystem holding per-session state can carry it forward before the
// pre-session row is deleted (issue #997).
func (d *SessionDetector) cleanupPreSessionsForProject(projectDir, realCWD, adapter, newSessionID string) bool {
	// Collect candidates under the lock; defer I/O (repo.Load) to outside.
	d.mu.Lock()
	var ids []string
	var cwdCandidates []string
	for sid, pdir := range d.projectSessions {
		if !strings.HasPrefix(sid, "proc-") {
			continue
		}
		if pdir == projectDir {
			ids = append(ids, sid)
			delete(d.projectSessions, sid)
			continue
		}
		if realCWD != "" {
			cwdCandidates = append(cwdCandidates, sid)
		}
	}
	d.mu.Unlock()

	// CWD fallback: match pre-sessions whose CWD equals the real session's
	// CWD. Needed for adapters whose transcript paths don't encode the
	// project directory (Codex stores by date, Pi uses double-dash encoding).
	for _, sid := range cwdCandidates {
		if state, _ := d.repo.Load(sid); state != nil && state.Adapter == adapter && state.CWD == realCWD {
			d.mu.Lock()
			delete(d.projectSessions, sid)
			d.mu.Unlock()
			ids = append(ids, sid)
		}
	}

	for _, sid := range ids {
		d.retirePreSession(sid, newSessionID, adapter, projectDir)
	}
	return len(ids) > 0
}

// retirePreSession fires the supersession hook and deletes a single retired
// pre-session, then records + logs its removal. Extracted from
// cleanupPreSessionsForProject's per-id loop to keep that function's
// cognitive complexity down — pure refactor, no behavior change (issue #997).
func (d *SessionDetector) retirePreSession(sid, newSessionID, adapter, projectDir string) {
	state, _ := d.repo.Load(sid)
	// Fire before the delete so a re-key handler's own Load(sid) is
	// guaranteed to still succeed (issue #997). Read directly off pidMgr
	// (same package) rather than keeping a second copy of the handler here.
	if d.pidMgr.onSessionSuperseded != nil {
		d.pidMgr.onSessionSuperseded(sid, newSessionID)
	}
	_ = d.repo.Delete(sid)
	adapterName := adapter
	if state != nil {
		adapterName = state.Adapter
		d.broadcast(outbound.PushTypeDeleted, state)
	}
	d.record(lifecycle.Event{Kind: lifecycle.KindPreSessionRemoved, SessionID: sid, Adapter: adapterName, Reason: "superseded by real session for project"})
	d.log.LogInfo(logComponentSessionDetector, sid,
		fmt.Sprintf("removed pre-session — real session arrived in %s", projectDir))
}
