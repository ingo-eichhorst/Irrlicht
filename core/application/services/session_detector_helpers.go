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

	// Newly-created sessions get an immediate history_snapshot so any
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
func (d *SessionDetector) cleanupPreSessionsForProject(projectDir, realCWD, adapter string) bool {
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
		state, _ := d.repo.Load(sid)
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
	return len(ids) > 0
}
