package services

import (
	"fmt"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func (d *SessionDetector) onRemoved(ev agent.Event) {
	d.log.LogInfo("session-detector", ev.SessionID, "session removed")

	// Cancel any pending debounce timer for this session.
	d.debounceMu.Lock()
	if entry, ok := d.debounce[ev.SessionID]; ok {
		entry.timer.Stop()
		delete(d.debounce, ev.SessionID)
	}
	d.debounceMu.Unlock()

	// Remove from project tracking.
	d.mu.Lock()
	delete(d.projectSessions, ev.SessionID)
	d.mu.Unlock()

	state, err := d.repo.Load(ev.SessionID)
	if err != nil || state == nil {
		return
	}

	// Pre-sessions (no transcript) are deleted entirely — the user never
	// sent a message, so there is no useful state to keep.
	if state.TranscriptPath == "" {
		_ = d.repo.Delete(ev.SessionID)
		d.broadcast(outbound.PushTypeDeleted, state)
		return
	}

	// Real sessions: transition to ready.
	if state.State == session.StateReady {
		return
	}

	prevState := state.State
	state.State = session.StateReady
	state.UpdatedAt = time.Now().Unix()
	state.Confidence = "high"
	state.LastEvent = "transcript_removed"

	d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, PrevState: prevState, NewState: session.StateReady, Reason: "transcript removed"})

	if err := d.repo.Save(state); err != nil {
		d.log.LogError("session-detector", ev.SessionID,
			fmt.Sprintf("failed to save removal state: %v", err))
		return
	}

	d.broadcast(outbound.PushTypeUpdated, state)

	if d.historyTracker != nil {
		d.historyTracker.Remove(ev.SessionID)
	}
}

// HandleProcessExit deletes a session when its process exits.
func (d *SessionDetector) HandleProcessExit(pid int, sessionID string) {
	d.pidMgr.HandleProcessExit(pid, sessionID)
}

// HandlePIDAssigned records a newly-discovered PID for a session.
func (d *SessionDetector) HandlePIDAssigned(pid int, sessionID string) {
	d.pidMgr.HandlePIDAssigned(pid, sessionID)
}

// HandlePermissionHook processes a Claude Code PermissionRequest, PostToolUse,
// or PostToolUseFailure hook event. It updates the in-memory permission-pending
// flag and injects a synthetic activity event to trigger re-classification.
//
// Safe to call from any goroutine (e.g. HTTP handler).
func (d *SessionDetector) HandlePermissionHook(sessionID, transcriptPath, hookEventName string) {
	d.permMu.Lock()
	switch hookEventName {
	case "PermissionRequest":
		d.permissionPending[sessionID] = true
	case "PostToolUse", "PostToolUseFailure":
		delete(d.permissionPending, sessionID)
	}
	d.permMu.Unlock()

	d.record(lifecycle.Event{
		Kind:      lifecycle.KindHookReceived,
		SessionID: sessionID,
		HookName:  hookEventName,
	})

	// Inject synthetic activity event so the event loop re-evaluates the
	// session. processActivity will overlay PermissionPending onto the
	// metrics before calling ClassifyState.
	select {
	case d.debouncedEvents <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      sessionID,
		TranscriptPath: transcriptPath,
		Adapter:        "claude-code",
	}:
	default:
		d.log.LogError("hook-receiver", sessionID,
			"debouncedEvents channel full, permission event dropped")
	}
}

// seedFromDisk populates the projectSessions map from existing sessions,
// re-evaluates stale states, backfills metadata, and cleans up dead PIDs.
func (d *SessionDetector) seedFromDisk() {
	states, err := d.repo.ListAll()
	if err != nil {
		return
	}

	// Populate projectSessions map.
	d.mu.Lock()
	for _, state := range states {
		if state.TranscriptPath != "" {
			if pdir := extractProjectDir(state.TranscriptPath); pdir != "" {
				d.projectSessions[state.SessionID] = pdir
			}
		}
	}
	d.mu.Unlock()

	// Re-evaluate state for sessions with transcripts: recompute metrics
	// and apply the current detection logic. This ensures sessions persisted
	// with stale states are corrected on startup (e.g. ready sessions whose
	// last assistant message ends with a question should be waiting), and
	// that stale persisted metrics from an older daemon version (e.g. pre-
	// PR #110 codex cumulative token counts) are overwritten with a fresh
	// recomputation under the current parser.
	for _, state := range states {
		if state.TranscriptPath == "" {
			continue
		}
		d.enricher.RefreshMetrics(state)

		newState, reason := ClassifyState(state.State, state.Metrics)
		// Only apply transitions to waiting or ready (not working promotion)
		// because seed is re-evaluating persisted state, not responding to
		// new activity.
		if newState != state.State && newState != session.StateWorking {
			if reason != "" {
				d.log.LogInfo("session-detector-seed", state.SessionID,
					fmt.Sprintf("re-evaluated %s on startup", reason))
			}
			state.State = newState
		}
		// Always persist after RefreshMetrics — stale metrics from an
		// older daemon version would otherwise linger on disk indefinitely
		// for idle sessions that never get another transcript_activity
		// event to trigger RefreshOnActivity + Save.
		_ = d.repo.Save(state)
		if d.historyTracker != nil {
			d.historyTracker.OnTransition(state.SessionID, state.State, time.Now())
		}
	}

	// Backfill ProjectName / CWD / GitBranch for sessions that were saved
	// before these fields were populated.
	for _, state := range d.enricher.BackfillMetadata(states) {
		state.UpdatedAt = time.Now().Unix()
		if err := d.repo.Save(state); err != nil {
			d.log.LogError("session-detector-seed", state.SessionID,
				fmt.Sprintf("failed to backfill metadata: %v", err))
		} else {
			d.log.LogInfo("session-detector-seed", state.SessionID,
				fmt.Sprintf("backfilled project=%s cwd=%s", state.ProjectName, state.CWD))
			d.broadcast(outbound.PushTypeUpdated, state)
		}
	}

	// Clean up dead sessions and register alive PIDs with ProcessWatcher.
	d.pidMgr.SeedPIDs(states)

	// Prune stale deletedSessions entries from previous daemon runs.
	// Entries older than 1 hour serve no purpose — the cooldown window
	// is only 10 seconds.
	d.mu.Lock()
	pruneThreshold := time.Now().Add(-1 * time.Hour).Unix()
	for id, ts := range d.deletedSessions {
		if ts < pruneThreshold {
			delete(d.deletedSessions, id)
		}
	}
	d.mu.Unlock()
}
