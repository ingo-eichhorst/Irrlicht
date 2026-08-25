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

	// Drop every per-session store — a hook that fired without a clearing
	// partner (e.g. an agent crash mid-overlay), a decided-but-unpublished
	// #1366 transition, an idle-retry counter, the hook-liveness watchdog's
	// rising-edge memory (#1368). Any of them left behind is permanent, and a
	// recycled session ID would inherit it. One seam, shared with the
	// PIDManager reap path; see forgetSessionScopedState.
	d.forgetSessionScopedState(ev.SessionID)

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
		state.UpdatedAt = d.nowFn().Unix()
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
//
// Two consequences of that Glob-only-returns-existing-paths property were
// assessed in issue #1088 and both came back benign:
//
//  1. Ordering. Relying on the destination already existing when the Remove
//     arrives would break if Claude Code deleted the source before writing the
//     destination. It does not: a census of every relocated transcript on a
//     real machine (50 files, 2104 relocation markers, up to 179 moves in a
//     single session) found birthtime == the session's first-line timestamp in
//     50/50 cases, while the files kept growing for hours afterwards. The inode
//     is preserved across every move, i.e. the transcript is renamed, never
//     copied or recreated — so the destination exists at the instant the source
//     stops existing. If that ever changed, the fallback here is graceful, not
//     corrupting: the glob returns "", the session flips ready, and the next
//     activity event revives it. That fallback is pinned by
//     TestSessionDetector_Removed_TranscriptGone_FlipsReady.
//
//  2. Subagents. Dir(Dir(removedPath)) resolves a subagent path
//     (<slug>/<parent-id>/subagents/<x>.jsonl) to <slug>/<parent-id>, so
//     sibling slugs are never searched and a relocated subagent is not detected
//     as such. That path is unreachable rather than merely harmless: Claude Code
//     relocates a parent by renaming the whole <parent-id>/ subtree, and
//     renaming a directory leaves the child files' vnodes untouched, so
//     fswatcher (which only emits events for paths ending in .jsonl) never
//     delivers a Remove for the subagent at all. The watcher instead re-walks
//     the destination dir and re-emits the child as a new session at its new
//     path. onRemoved is therefore never called for a relocating subagent.
//     TestRelocatedTranscript pins the limitation so the reasoning is
//     discoverable if the watcher's dir handling ever changes.
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
	state.UpdatedAt = d.nowFn().Unix()
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

// ReconcilePreSessionBackchannel carries a still-open trust-dialog Waiting
// state from a presession onto the real session that superseded it (issue
// #997). Every reconciliation path retires the presession by deleting its row
// outright; if TerminalObserver had already forced it into Waiting via a
// terminal read-back edge (handleTerminalUISignal persists that directly onto
// the presession's own row), that fact is lost the moment the row disappears
// unless it's copied forward first. Registered as (half of) the callback
// SetSessionSupersededHandler installs — the other half re-keys
// TerminalObserver's own edge-detection cache, wired alongside this one at
// startup since SessionDetector has no reference to TerminalObserver.
//
// Runs under WithSessionStateLock, the same lock handleTerminalUISignal's own
// load-modify-save takes, so this can't race a concurrent terminal-UI signal
// for either id. A no-op when the presession was never forced into Waiting,
// or when the new session already is (e.g. its own terminal-observer poll
// already caught up).
func (d *SessionDetector) ReconcilePreSessionBackchannel(oldID, newID string) {
	d.pidMgr.WithSessionStateLock(func() {
		old, err := d.repo.Load(oldID)
		if err != nil || old == nil || old.State != session.StateWaiting {
			return // nothing to carry forward
		}
		newState, err := d.repo.Load(newID)
		if err != nil || newState == nil || newState.State == session.StateWaiting {
			return // already waiting, or gone
		}

		prevState := newState.State
		newState.State = session.StateWaiting
		newState.WaitingStartTime = old.WaitingStartTime
		newState.UpdatedAt = d.nowFn().Unix()
		if err := d.repo.Save(newState); err != nil {
			d.log.LogError(logComponentSessionDetector, newID,
				fmt.Sprintf("failed to carry forward waiting state from presession %s: %v", oldID, err))
			return
		}

		d.record(lifecycle.Event{
			Kind: lifecycle.KindStateTransition, SessionID: newID,
			PrevState: prevState, NewState: session.StateWaiting,
			Reason: fmt.Sprintf("carried forward from superseded presession %s", oldID),
		})
		d.broadcast(outbound.PushTypeUpdated, newState)
	})
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
	// session.HookSignal is the shared hook-name → SignalKind table, also read
	// by the replay harness's applyHookEvent. It used to be a switch here and a
	// second switch there, and they had drifted (issue #1320).
	if effect, ok := session.HookSignal(hookEventName); ok {
		d.signals.ApplyHook(sessionID, effect, d.nowFn())
	}

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
//
// It is also marked Terminal exactly when hookName is session.HookStop — the
// one hook name in this shared dispatch that means "the turn is authoritatively
// done," as opposed to a permission prompt or compaction signal. processActivity
// reads Terminal to decide whether a hook arriving for an already-tombstoned
// session (this call racing PIDManager's own teardown) still deserves a
// classify pass against the cached snapshot rather than being dropped
// (issue #1772).
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
		Terminal:       hookName == session.HookStop,
	}:
	default:
		d.log.LogError("hook-receiver", sessionID,
			fmt.Sprintf("debouncedEvents channel full, %s hook event dropped", hookName))
	}
}

// HandleStopHook records the authoritative turn-done signal from Claude Code's
// Stop hook (issue #1161), which fires once at true turn end and carries the
// turn's final assistant text. Holding SignalTurnDone makes the next classify
// pass overlay SessionMetrics.HookTurnDone (so IsAgentDone is authoritative,
// not inferred from the transcript tail) and overwrite LastAssistantText /
// PendingWaitingCue from the hook's message — so a turn that ended on a
// question still routes to waiting, not ready. The hold is consume-once (see
// session.signalPolicies), so it never bleeds into the following turn.
//
// Safe to call from any goroutine (e.g. the HTTP handler).
func (d *SessionDetector) HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool) {
	d.signals.Hold(sessionID, session.SignalTurnDone, session.SignalPayload{
		LastAssistantText: lastAssistantText,
		WaitingCue:        waitingCue,
	}, d.nowFn())

	// A turn that completed retires the session's failure, and the tailer is
	// the only layer that owns that sticky state (#1799). The hold above cannot
	// do it: SignalTurnDone is consume-once, so the HookTurnDone it overlays
	// suppresses the classifier's session_error rule for exactly one pass, and
	// the pass after that reads the untouched error again — an error → ready →
	// error pair in the UI and in the recorded trace. #1798 named this and
	// deferred it here, to the first producer that could reach it.
	//
	// Nil-guarded the same way the background-process purge at
	// session_detector_activity.go is: a detector constructed without a metrics
	// collector has no tailer, and therefore no sticky error to retire.
	if d.metrics != nil {
		d.metrics.ClearSessionError(transcriptPath)
	}

	// classifyAndTransition overlays HookTurnDone onto the metrics before
	// calling ClassifyState. The Stop hook fires at true turn end (after the
	// transcript flush), so a synthetic activity event is what drives the
	// re-classification now rather than waiting for the next fswatcher pass.
	d.dispatchHookActivity(sessionID, transcriptPath, session.HookStop)
}

// HandlePermissionPromptHook records gemini-cli's Notification/ToolPermission
// hook (issue #1717): a tool confirmation prompt is genuinely open and the
// user is blocked on it right now.
//
// A dedicated method rather than the generic name-keyed HandlePermissionHook,
// and that is load-bearing rather than a style choice. gemini-cli's wire name
// for this event is literally "Notification" — the SAME string Claude Code
// uses for its own Notification hook, which session.HookSignal deliberately
// has no row for (its gate is adapter-side: claudecode dispatches it to
// HandleIdlePromptHook, never through the generic table). hookSignalEffects is
// one flat map with no adapter dimension, so adding a row keyed on
// "Notification" to make THIS call hold SignalPermissionPrompt would also
// change what claudecode's and copilot's OWN Notification dispatches do —
// copilot's in particular, whose package comment explains that copilot must
// dispatch its identically-named notification WITHOUT a hold, because copilot
// emits nothing at all when the user denies a prompt and a hold with no
// release would pin the session waiting for the 12-hour ceiling
// (permissionPromptHoldTimeout). A shared-table row would silently reintroduce
// that exact bug into copilot.
//
// gemini-cli is not in copilot's position: unlike copilot, its AfterAgent
// event reliably fires after a cancelled confirmation too (a cancelled tool
// call is converted to a terminal, completed batch entry carrying a synthetic
// error functionResponse fed back to the model, so the turn completes
// normally) — source-verified against the installed CLI, not assumed. That is
// what makes holding here safe: geminicli's AfterAgent handling calls
// ReleasePermissionPromptHold unconditionally, closing the one case (a denied
// confirmation) where AfterTool's broad release never fires. See that method
// and geminicli/hooks.go's package comment for the other half of this pair.
//
// Persistent, not consume-once — matching the existing SignalPermissionPrompt
// policy row's own semantics (session.signalPolicies), which every other
// asserter of this signal (claudecode's PreToolUse, codex's PermissionRequest)
// already relies on: the hold must survive every re-evaluation between the
// prompt opening and it being resolved.
//
// Safe to call from any goroutine (e.g. the HTTP handler).
func (d *SessionDetector) HandlePermissionPromptHook(sessionID, transcriptPath, hookName string) {
	d.signals.Hold(sessionID, session.SignalPermissionPrompt, session.SignalPayload{}, d.nowFn())
	d.dispatchHookActivity(sessionID, transcriptPath, hookName)
}

// ReleasePermissionPromptHold drops any held SignalPermissionPrompt for
// sessionID without requiring a name-keyed hook event to carry the release.
//
// Exists for gemini-cli's AfterAgent handler (issue #1717), which calls this
// alongside HandleStopHook rather than relying solely on AfterTool's broad
// release. The gap it closes: on a CANCELLED tool confirmation, gemini-cli's
// scheduler returns before ever running the tool, so AfterTool never fires for
// that call — source-verified against the installed CLI (the cancel path
// converts straight to a terminal batch entry without reaching the tool
// execution step AfterTool is fired from). Without this call, a denied prompt
// would stay held until SignalPermissionPrompt's own 12-hour ceiling
// (permissionPromptHoldTimeout) — technically bounded, but a needless half-day
// of false `waiting` when the turn that resolved it has, in fact, already
// completed.
//
// Safe to release unconditionally: fireAfterAgentHookSafe (gemini-cli's own
// gate on firing AfterAgent) cannot fire while a confirmation from that turn
// is still genuinely open — the agent loop is blocked awaiting the user's
// answer at that point and has not yet reached the point where AfterAgent
// could fire — so by the time AfterAgent arrives, any permission prompt opened
// during that turn has already been resolved one way or another. Release on
// an unheld signal is a no-op (SignalHolds.Release), so this costs nothing on
// the far more common case where nothing was held.
//
// Safe to call from any goroutine (e.g. the HTTP handler).
func (d *SessionDetector) ReleasePermissionPromptHold(sessionID string) {
	d.signals.Release(sessionID, session.SignalPermissionPrompt)
}

// HandleIdlePromptHook records Claude Code's Notification/idle_prompt hook
// (issue #1173): the agent has finished its turn and is now idle at the prompt
// waiting for the user. Holding SignalIdlePrompt makes every subsequent
// classify pass overlay SessionMetrics.IdlePromptPending (the idle_prompt rule)
// so the session shows waiting even when the turn ended on a plain statement
// that PendingWaitingCue's prose heuristic can't detect. The hold is persistent
// rather than consume-once (see session.signalPolicies) and lasts until the
// next turn begins, when its staleness rule drops it as IsAgentDone flips
// false — a lower-tier reclassify must not revert the corrected waiting back to
// ready.
//
// The idle_prompt hook fires after the turn goes idle (the session is typically
// already ready by then), so this is a reconcile-and-correct: the synthetic
// activity below drives a ready→waiting transition without the intermediate
// working blip forceReadyToWorkingIfActive would otherwise record (it skips the
// bounce while this flag is pending).
//
// Safe to call from any goroutine (e.g. the HTTP handler).
func (d *SessionDetector) HandleIdlePromptHook(sessionID, transcriptPath string) {
	d.signals.Hold(sessionID, session.SignalIdlePrompt, session.SignalPayload{}, d.nowFn())

	d.dispatchHookActivity(sessionID, transcriptPath, session.HookNotification)
}

// HandleCompactHook processes a Claude Code PreCompact hook for a manual
// /compact. The compaction window writes nothing to the transcript, so without
// an out-of-band push the session stays frozen in its pre-compact state instead
// of showing working (issue #657). Holding SignalCompactInProgress makes
// classifyAndTransition's Overlay set CompactInProgress so ClassifyState holds
// the session in working until the compact_boundary lands (which #656 turns
// into turn_done → ready) or the policy's timeout drops an orphaned hold.
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

	d.signals.Hold(sessionID, session.SignalCompactInProgress, session.SignalPayload{}, d.nowFn())

	// Flip the session to working immediately — there is no transcript flush
	// coming during the compaction window to trigger a natural re-evaluation.
	d.dispatchHookActivity(sessionID, transcriptPath, session.HookPreCompact)
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
	//
	// #1798: error is deliberately NOT added to the exclusion. The filter
	// exists to stop a persisted session being promoted to working on no
	// evidence — "working" is a claim that something is happening right now,
	// and at boot nothing is. An error is the opposite kind of claim: it
	// describes something that already happened, so re-asserting it at boot is
	// sound in a way re-asserting "working" is not.
	//
	// It does not actually fire today, and that is a known gap rather than a
	// contradiction of the above. RefreshMetrics ran a few lines up, and it
	// merges against a fresh tailer pass whose SessionError is nil — the tailer
	// does not persist its sticky error — so the persisted value is gone before
	// ClassifyState reads it and this path sees a healthy session. The fix is
	// LedgerState persistence in the tailer, deferred to #1800; see
	// SessionMetrics.SessionError for the full account. The filter is written
	// to accept error now so that landing the persistence is a one-file change
	// rather than also having to notice this line.
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
		state.UpdatedAt = d.nowFn().Unix()
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
// the re-creation cooldown they guard is only 10 seconds. deletedStates is
// keyed identically and pruned in lockstep — it never outlives the tombstone
// that gates whether anything ever reads it back (issue #1772).
func (d *SessionDetector) pruneDeletedSessionsCache() {
	d.mu.Lock()
	defer d.mu.Unlock()
	pruneThreshold := time.Now().Add(-1 * time.Hour).Unix()
	for id, ts := range d.deletedSessions {
		if ts < pruneThreshold {
			delete(d.deletedSessions, id)
			delete(d.deletedStates, id)
		}
	}
}
