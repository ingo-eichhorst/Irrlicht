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

	// Drop any leftover permission-pending flag — otherwise a hook that
	// fired without a clearing partner (e.g. agent crash mid-overlay) would
	// keep the entry forever, and a recycled session ID would inherit it.
	d.permMu.Lock()
	delete(d.permissionPending, ev.SessionID)
	d.permMu.Unlock()

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

	// Drop the in-memory tailer cache and the on-disk ledger file —
	// the transcript is gone, so this state will never change again.
	d.enricher.PruneMetrics(state.TranscriptPath)
}

// rotateOnNewTranscript handles the claudecode /clear pattern: when a
// new transcript file appears in a projectDir that already hosts a
// real session, the existing session was abandoned in place. Emits
// transcript_removed + dashboard delete for the old session BEFORE
// the new one is registered, so events.jsonl and the dashboard show
// clean rotation rather than the brief overlap the same-PID cleanup
// path produces (issue #169 round 2).
//
// Heuristic: same projectDir + state=ready + last-activity within
// 60s = /clear. Stale orphans (>60s idle) are handled separately by
// the staleness check. Multi-session-same-cwd is rare for claudecode
// (different sessions typically open in different cwds) and the
// PIDManager same-PID cleanup at pid_discovered remains the
// authoritative safety net — this path only optimizes the timing.
//
// Gated to claudecode adapter because /clear's UUID-rotation semantics
// is adapter-specific. Other adapters either don't have /clear or
// don't rotate transcript files.
func (d *SessionDetector) rotateOnNewTranscript(id agent.Identity, ev agent.Event) {
	if id.Name != "claudecode" {
		return
	}
	states, err := d.repo.ListAll()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	for _, old := range states {
		if old.SessionID == ev.SessionID {
			continue
		}
		// Same project dir (claudecode hashes cwd into the path) AND a
		// real UUID-keyed session (skip pre-sessions, those are
		// handled by their own removal path).
		if old.TranscriptPath == "" {
			continue
		}
		// Project dir derived from transcript path == new ev.ProjectDir
		// would be cleaner, but the same projectDir tag from agent.Event
		// is propagated through fswatcher and matches what onRemoved
		// uses to scope cleanup. Compare via projectSessions instead so
		// the dependency on the tag's exact string format is internal.
		d.mu.Lock()
		oldDir, ok := d.projectSessions[old.SessionID]
		d.mu.Unlock()
		if !ok || oldDir != ev.ProjectDir {
			continue
		}
		if old.State != session.StateReady {
			continue
		}
		if now-old.UpdatedAt > 60 {
			continue // stale orphan, not a /clear
		}
		if old.ParentSessionID != "" {
			continue // subagent — not a /clear target
		}

		d.log.LogInfo("session-detector", old.SessionID,
			fmt.Sprintf("rotated by new session %s in same project dir — /clear detected", ev.SessionID))

		// Record the lifecycle event FIRST so events.jsonl shows the
		// old session ending before the new one's transcript_new.
		d.record(lifecycle.Event{
			Kind:           lifecycle.KindTranscriptRemoved,
			SessionID:      old.SessionID,
			Adapter:        old.Adapter,
			TranscriptPath: old.TranscriptPath,
		})

		// Mirror the same-PID cleanup's repo + broadcast path so the
		// dashboard sees the old session disappear immediately.
		d.mu.Lock()
		delete(d.projectSessions, old.SessionID)
		d.deletedSessions[old.SessionID] = now
		d.mu.Unlock()
		_ = d.repo.Delete(old.SessionID)
		d.broadcast(outbound.PushTypeDeleted, old)
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

// HandlePermissionHook processes a Claude Code PermissionRequest, PreToolUse,
// PostToolUse, or PostToolUseFailure hook event. It updates the in-memory
// permission-pending flag and injects a synthetic activity event to trigger
// re-classification.
//
// PreToolUse fires synchronously when the model emits a tool_use block, before
// the assistant message is persisted to JSONL. For AskUserQuestion and
// ExitPlanMode (matched by the installer), this lets the daemon flip
// working → waiting without depending on transcript flush latency (issue #307).
//
// Safe to call from any goroutine (e.g. HTTP handler).
func (d *SessionDetector) HandlePermissionHook(sessionID, transcriptPath, hookEventName string) {
	d.permMu.Lock()
	switch hookEventName {
	case "PermissionRequest", "PreToolUse":
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
