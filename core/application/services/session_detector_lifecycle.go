package services

import (
	"fmt"
	"path/filepath"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func (d *SessionDetector) onRemoved(ev agent.Event) {
	// A .jsonl "removal" is often a *relocation*, not a deletion. Claude Code
	// derives a session's project-dir slug from its cwd, so when a session cd's
	// into a git worktree it moves its transcript to a new slug (same session
	// id, new path). fsnotify reports the old path's rename as a deletion
	// (fswatcher collapses Rename→Removed), but the session is alive and still
	// working. Re-point tracking at the surviving copy instead of forcing the
	// session to ready (issue #877).
	if newPath := relocatedTranscript(ev.TranscriptPath); newPath != "" {
		d.onRelocated(ev, newPath)
		return
	}

	d.log.LogInfo(logComponentSessionDetector, ev.SessionID, "session removed")

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
	delete(d.compactPending, ev.SessionID)
	delete(d.editToolOpenSince, ev.SessionID)
	d.permMu.Unlock()

	state, err := d.repo.Load(ev.SessionID)
	if err != nil || state == nil {
		return
	}

	// Run the load-modify-save under the PIDManager's state lock — a
	// PID-discovery goroutine spawned for this session may still be in
	// flight, and its assignPIDLocked writes state.PID/UpdatedAt on the same
	// *SessionState pointer this path mutates (issue #606).
	d.pidMgr.WithSessionStateLock(func() {
		d.onRemovedLocked(state, ev)
	})
}

// onRelocated handles a transcript that moved to a new project-dir slug rather
// than being deleted (see relocatedTranscript). The session is alive and keeps
// its current state — the fix for a session spuriously flipping to ready when
// it cd's into a git worktree (issue #877). Tracking is re-pointed at the
// surviving file so subsequent activity events and metric refreshes follow it.
func (d *SessionDetector) onRelocated(ev agent.Event, newPath string) {
	newProjectDir := filepath.Base(filepath.Dir(newPath))
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
		fmt.Sprintf("transcript relocated to %s — session still alive, not marking ready", newProjectDir))

	// Keep the project-dir tracking current for parent derivation.
	d.mu.Lock()
	d.projectSessions[ev.SessionID] = newProjectDir
	d.mu.Unlock()

	state, err := d.repo.Load(ev.SessionID)
	if err != nil || state == nil {
		return
	}

	// Re-point the session (and the metrics tailer) at the surviving file. Under
	// the PIDManager's state lock — a PID-discovery goroutine may still be in
	// flight for this session, writing state.PID/UpdatedAt on this same pointer
	// (issue #606).
	d.pidMgr.WithSessionStateLock(func() {
		if state.TranscriptPath == newPath {
			return // an activity event already followed the move
		}
		// Drop the tailer cache for the now-gone path. The new-path tailer
		// re-reads the full (moved) file on the next activity event, so
		// cumulative metrics are rebuilt intact.
		d.enricher.PruneMetrics(state.TranscriptPath)
		state.TranscriptPath = newPath
		state.UpdatedAt = time.Now().Unix()
		if err := d.repo.Save(state); err != nil {
			d.log.LogError(logComponentSessionDetector, ev.SessionID,
				fmt.Sprintf("failed to save relocated transcript path: %v", err))
			return
		}
		d.broadcast(outbound.PushTypeUpdated, state)
	})
}

// relocatedTranscript reports whether a transcript that fsnotify flagged as
// removed actually still exists under a *different* project-dir slug — i.e. it
// was moved, not deleted. Returns the surviving path, or "" for a genuine
// removal.
//
// The scan is scoped to the sibling project dirs of the removed path
// (<projectsRoot>/*/<file>), so it stays adapter-agnostic: layouts that don't
// place transcripts exactly one level under a shared root simply find no match
// and fall through to the normal removal path. filepath.Glob only returns paths
// that exist, and the removed path no longer does, so any other match is a live
// relocated copy.
func relocatedTranscript(removedPath string) string {
	if removedPath == "" {
		return ""
	}
	projectsRoot := filepath.Dir(filepath.Dir(removedPath))
	matches, err := filepath.Glob(filepath.Join(projectsRoot, "*", filepath.Base(removedPath)))
	if err != nil {
		return ""
	}
	for _, m := range matches {
		if m != removedPath {
			return m
		}
	}
	return ""
}

// onRemovedLocked finishes removal handling for an already-loaded session. It
// MUST be called under PIDManager.WithSessionStateLock so a still-running
// PID-discovery goroutine can't write state.PID concurrently with this path's
// writes (issue #606).
func (d *SessionDetector) onRemovedLocked(state *session.SessionState, ev agent.Event) {
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

	// Stamp the session's HEAD commit + yield verdict now that its work is
	// done, so the yield sweep can later correlate reverts back to it (#373).
	d.enricher.CaptureYieldOnReady(state)

	d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, PrevState: prevState, NewState: session.StateReady, Reason: "transcript removed"})

	if err := d.repo.Save(state); err != nil {
		d.log.LogError(logComponentSessionDetector, ev.SessionID,
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

// HandleProcessExit deletes a session when its process exits. reason describes
// the triggering edge for the recorded lifecycle trace (issue #757).
func (d *SessionDetector) HandleProcessExit(pid int, sessionID, reason string) {
	d.pidMgr.HandleProcessExit(pid, sessionID, reason)
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

	// processActivity overlays PermissionPending onto the metrics before
	// calling ClassifyState.
	d.dispatchHookActivity(sessionID, transcriptPath, hookEventName)
}

// dispatchHookActivity records a hook-received lifecycle event and injects a
// synthetic activity event so the event loop re-classifies the session now —
// without waiting on a transcript flush. Shared by the permission and
// compaction hook handlers, whose only differences are which pending map they
// set (done by the caller) and the hook name.
//
// The injected event is marked Synthetic so forceReadyToWorkingIfActive still
// bounces a ready session on it despite the transcript not having grown yet
// (PreToolUse fires before the write) — while a real fswatcher pass with no
// transcript growth (e.g. mistral-vibe's content-less slash-command touch)
// does not force the bounce. See issue #905.
func (d *SessionDetector) dispatchHookActivity(sessionID, transcriptPath, hookName string) {
	d.record(lifecycle.Event{
		Kind:      lifecycle.KindHookReceived,
		SessionID: sessionID,
		HookName:  hookName,
	})

	select {
	case d.debouncedEvents <- agent.Event{
		Type:           agent.EventActivity,
		SessionID:      sessionID,
		TranscriptPath: transcriptPath,
		Synthetic:      true,
	}:
	default:
		d.log.LogError("hook-receiver", sessionID,
			fmt.Sprintf("debouncedEvents channel full, %s hook event dropped", hookName))
	}
}

// HandleCompactHook processes a Claude Code PreCompact hook for a manual
// /compact. The compaction window writes nothing to the transcript, so without
// an out-of-band push the session stays frozen in its pre-compact state instead
// of showing working (issue #657). Marking compactPending makes processActivity
// overlay CompactInProgress so ClassifyState holds the session in working until
// the compact_boundary lands (which #656 turns into turn_done → ready).
//
// Only manual compaction is handled: auto-compaction fires mid-turn while the
// session is already working, so a forced working blip would be spurious. The
// HTTP handler already gates on trigger=="manual"; this re-checks defensively.
//
// Safe to call from any goroutine (e.g. HTTP handler).
func (d *SessionDetector) HandleCompactHook(sessionID, transcriptPath, trigger string) {
	if trigger != "manual" {
		return
	}

	d.permMu.Lock()
	d.compactPending[sessionID] = time.Now().Unix()
	d.permMu.Unlock()

	// Flip the session to working immediately — there is no transcript flush
	// coming during the compaction window to trigger a natural re-evaluation.
	d.dispatchHookActivity(sessionID, transcriptPath, "PreCompact")
}

// seedFromDisk populates the projectSessions map from existing sessions,
// re-evaluates stale states, backfills metadata, and cleans up dead PIDs.
func (d *SessionDetector) seedFromDisk() {
	states, err := d.repo.ListAll()
	if err != nil {
		return
	}

	d.seedProjectSessions(states)
	d.seedReevaluateStates(states)
	d.seedBackfillMetadata(states)

	// Clean up dead sessions and register alive PIDs with ProcessWatcher.
	d.pidMgr.SeedPIDs(states)

	d.pruneDeletedSessionsCache()
}

// seedProjectSessions populates the projectSessions map from persisted
// sessions' transcript paths, so parent derivation works for sessions that
// existed before this daemon start.
func (d *SessionDetector) seedProjectSessions(states []*session.SessionState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, state := range states {
		if state.TranscriptPath == "" {
			continue
		}
		if pdir := extractProjectDir(state.TranscriptPath); pdir != "" {
			d.projectSessions[state.SessionID] = pdir
		}
	}
}

// seedReevaluateStates re-evaluates state for sessions with transcripts:
// recompute metrics and apply the current detection logic. This ensures
// sessions persisted with stale states are corrected on startup (e.g. ready
// sessions whose last assistant message ends with a question should be
// waiting), and that stale persisted metrics from an older daemon version
// (e.g. pre-PR #110 codex cumulative token counts) are overwritten with a
// fresh recomputation under the current parser.
//
// Consent-gated per adapter (#570): RefreshMetrics re-reads the transcript
// file, which a pending/denied observe permission forbids — the upgrade
// contract is that previously monitored agents pause until the wizard is
// answered. Un-consented sessions stay persisted as-is.
func (d *SessionDetector) seedReevaluateStates(states []*session.SessionState) {
	for _, state := range states {
		if state.TranscriptPath == "" || !d.observeAllowed(state.Adapter) {
			continue
		}
		d.seedReevaluateOne(state)
	}
}

// seedReevaluateOne refreshes one persisted session's metrics, applies any
// resulting waiting/ready correction, and always re-persists — stale metrics
// from an older daemon version would otherwise linger on disk indefinitely
// for idle sessions that never get another transcript_activity event to
// trigger RefreshOnActivity + Save.
func (d *SessionDetector) seedReevaluateOne(state *session.SessionState) {
	d.enricher.RefreshMetrics(state)

	// Probe background-process liveness before re-classifying so a session
	// persisted as `working` solely because a Bash run_in_background
	// process is still alive (its open set rehydrated from the ledger) is
	// not wrongly demoted to ready on startup. Without this, IsAgentDone
	// would return true (count alone doesn't gate) and the session would
	// flip to ready and never re-probe (refreshStaleSessions is
	// working-only). See issue #445.
	d.applyBackgroundLiveness(state)

	// Only apply transitions to waiting or ready (not working promotion)
	// because seed is re-evaluating persisted state, not responding to
	// new activity.
	newState, reason := ClassifyState(state.State, state.Metrics)
	if newState != state.State && newState != session.StateWorking {
		if reason != "" {
			d.log.LogInfo(logComponentSessionDetectorSeed, state.SessionID,
				fmt.Sprintf("re-evaluated %s on startup", reason))
		}
		state.State = newState
	}
	_ = d.repo.Save(state)
	if d.historyTracker != nil {
		d.historyTracker.OnTransition(state.SessionID, state.State, time.Now())
	}
}

// seedBackfillMetadata fills ProjectName / CWD / GitBranch for sessions that
// were saved before these fields were populated. Same consent gate as
// seedReevaluateStates — BackfillMetadata reads transcripts
// (GetCWDFromTranscript).
func (d *SessionDetector) seedBackfillMetadata(states []*session.SessionState) {
	allowed := states[:0:0]
	for _, state := range states {
		if d.observeAllowed(state.Adapter) {
			allowed = append(allowed, state)
		}
	}
	for _, state := range d.enricher.BackfillMetadata(allowed) {
		state.UpdatedAt = time.Now().Unix()
		if err := d.repo.Save(state); err != nil {
			d.log.LogError(logComponentSessionDetectorSeed, state.SessionID,
				fmt.Sprintf("failed to backfill metadata: %v", err))
			continue
		}
		d.log.LogInfo(logComponentSessionDetectorSeed, state.SessionID,
			fmt.Sprintf("backfilled project=%s cwd=%s", state.ProjectName, state.CWD))
		d.broadcast(outbound.PushTypeUpdated, state)
	}
}

// pruneDeletedSessionsCache drops deletedSessions entries older than 1 hour,
// left over from a previous daemon run. Entries that old serve no purpose —
// the re-creation cooldown they guard is only 10 seconds.
func (d *SessionDetector) pruneDeletedSessionsCache() {
	d.mu.Lock()
	defer d.mu.Unlock()
	pruneThreshold := time.Now().Add(-1 * time.Hour).Unix()
	for id, ts := range d.deletedSessions {
		if ts < pruneThreshold {
			delete(d.deletedSessions, id)
		}
	}
}
