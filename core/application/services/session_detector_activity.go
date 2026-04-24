package services

import (
	"fmt"
	"strings"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func (d *SessionDetector) handleTranscriptEvent(ev agent.Event) {
	// Record raw inbound event for lifecycle replay.
	switch ev.Type {
	case agent.EventNewSession:
		d.record(lifecycle.Event{Kind: lifecycle.KindTranscriptNew, SessionID: ev.SessionID, Adapter: ev.Adapter, TranscriptPath: ev.TranscriptPath, FileSize: ev.Size, ProjectDir: ev.ProjectDir, CWD: ev.CWD})
		d.onNewSession(ev)
	case agent.EventActivity:
		d.record(lifecycle.Event{Kind: lifecycle.KindTranscriptActivity, SessionID: ev.SessionID, Adapter: ev.Adapter, TranscriptPath: ev.TranscriptPath, FileSize: ev.Size})
		d.onActivity(ev)
	case agent.EventRemoved:
		d.record(lifecycle.Event{Kind: lifecycle.KindTranscriptRemoved, SessionID: ev.SessionID, Adapter: ev.Adapter, TranscriptPath: ev.TranscriptPath})
		d.onRemoved(ev)
	}
}

// onNewSession handles a new transcript file appearing.
func (d *SessionDetector) onNewSession(ev agent.Event) {
	d.log.LogInfo("session-detector", ev.SessionID,
		fmt.Sprintf("new session detected in %s (adapter=%s)", ev.ProjectDir, ev.Adapter))

	// Track project directory for parent derivation.
	d.mu.Lock()
	d.projectSessions[ev.SessionID] = ev.ProjectDir
	d.mu.Unlock()

	// Check if session already exists.
	existing, _ := d.repo.Load(ev.SessionID)
	isNew := existing == nil

	now := time.Now().Unix()

	if isNew {
		// Skip transcripts whose session was recently deleted (process exit
		// or /clear cleanup). Allow re-creation if the cooldown has passed
		// and the transcript has fresh activity (--continue scenario).
		d.mu.Lock()
		deletedAt, deleted := d.deletedSessions[ev.SessionID]
		if deleted && time.Since(time.Unix(deletedAt, 0)) >= d.deletedCooldown && !isStaleTranscript(ev.TranscriptPath) {
			delete(d.deletedSessions, ev.SessionID)
			deleted = false
			d.log.LogInfo("session-detector", ev.SessionID,
				"previously deleted session has fresh activity (--continue), allowing re-creation")
		}
		d.mu.Unlock()
		if deleted {
			d.log.LogInfo("session-detector", ev.SessionID,
				"skipping recently deleted session")
			return
		}

		// Skip orphan transcripts left by exited Claude Code processes.
		if isStaleTranscript(ev.TranscriptPath) {
			d.log.LogInfo("session-detector", ev.SessionID,
				"skipping orphan transcript")
			return
		}

		// All new sessions start as ready. Content-based detection on
		// subsequent activity events will transition to working/waiting.
		state := &session.SessionState{
			Version:         1,
			SessionID:       ev.SessionID,
			State:           session.StateReady,
			Adapter:         ev.Adapter,
			TranscriptPath:  ev.TranscriptPath,
			CWD:             ev.CWD,
			DaemonVersion:   d.version,
			ParentSessionID: deriveParentSession(ev.TranscriptPath),
			FirstSeen:       now,
			UpdatedAt:       now,
			Confidence:      "medium",
			EventCount:      1,
			LastEvent:       "transcript_new",
		}

		// Record parent-child linkage if detected.
		if state.ParentSessionID != "" {
			d.record(lifecycle.Event{Kind: lifecycle.KindParentLinked, SessionID: ev.SessionID, ParentSessionID: state.ParentSessionID})
		}

		// Resolve git metadata and compute initial metrics.
		d.enricher.EnrichNewSession(state, ev)

		if err := d.repo.Save(state); err != nil {
			d.log.LogError("session-detector", ev.SessionID,
				fmt.Sprintf("failed to save new session: %v", err))
			return
		}
		d.recordCost(state)

		// Record pre-session detection (proc-* IDs only — real transcript
		// sessions are already covered by KindTranscriptNew above).
		if strings.HasPrefix(ev.SessionID, "proc-") {
			d.record(lifecycle.Event{Kind: lifecycle.KindPreSessionCreated, SessionID: ev.SessionID, Adapter: ev.Adapter, ProjectDir: ev.ProjectDir, CWD: ev.CWD})
		}

		// Record initial state transition.
		d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, NewState: session.StateReady, Reason: "new session created"})

		d.broadcast(outbound.PushTypeCreated, state)

		// When a real transcript session arrives, remove any pre-sessions for
		// the same project. Match by projectDir first (Claude Code layout),
		// then fall back to CWD (Codex/Pi have different transcript layouts).
		if ev.TranscriptPath != "" {
			d.cleanupPreSessionsForProject(ev.ProjectDir, state.CWD, ev.Adapter)
		}
	} else {
		// Session already exists. Update transcript path if missing.
		if existing.TranscriptPath == "" {
			existing.TranscriptPath = ev.TranscriptPath
			existing.UpdatedAt = now
			if err := d.repo.Save(existing); err != nil {
				d.log.LogError("session-detector", ev.SessionID,
					fmt.Sprintf("failed to update transcript path: %v", err))
			}
		}
	}

	// PID discovery (async). Each adapter has its own strategy:
	// Claude Code uses CWD-based matching, Codex/Pi use transcript file writer.
	adapter := ev.Adapter
	if !isNew {
		adapter = existing.Adapter
	}
	cwd := ev.CWD
	if !isNew {
		cwd = existing.CWD
	}
	transcriptPath := ev.TranscriptPath
	if !isNew && transcriptPath == "" {
		transcriptPath = existing.TranscriptPath
	}
	go d.pidMgr.DiscoverPIDWithRetry(ev.SessionID, cwd, transcriptPath, adapter)
}

// onActivity debounces transcript activity events per session. The first event
// fires immediately (so the UI stays responsive), then subsequent events
// within a 2-second window are coalesced into a single processActivity call.
func (d *SessionDetector) onActivity(ev agent.Event) {
	sid := ev.SessionID

	d.debounceMu.Lock()
	entry, exists := d.debounce[sid]
	if !exists {
		// First event for this session — fire immediately and start cooldown.
		entry = &debounceEntry{}
		entry.timer = time.AfterFunc(activityDebounceWindow, func() {
			d.debounceMu.Lock()
			e := d.debounce[sid]
			if e != nil && e.pending {
				coalesced := e.latest
				delete(d.debounce, sid)
				d.debounceMu.Unlock()
				// Send to the event loop instead of calling processActivity
				// directly, so all processActivity calls are serialized.
				select {
				case d.debouncedEvents <- coalesced:
				default:
				}
			} else {
				delete(d.debounce, sid)
				d.debounceMu.Unlock()
			}
		})
		d.debounce[sid] = entry
		d.debounceMu.Unlock()
		d.processActivity(ev)
		return
	}

	// Subsequent event within the debounce window — coalesce.
	entry.latest = ev
	entry.pending = true
	entry.timer.Reset(activityDebounceWindow)
	d.debounceMu.Unlock()

	d.record(lifecycle.Event{Kind: lifecycle.KindDebounceCoalesced, SessionID: ev.SessionID})
}

// processActivity handles a (possibly debounced) transcript activity event.
// It uses content-based detection to determine whether the agent is working
// or waiting for user input.
func (d *SessionDetector) processActivity(ev agent.Event) {
	// Load session state.
	state, err := d.repo.Load(ev.SessionID)
	if err != nil || state == nil {
		// If the session was explicitly deleted (process exit, /clear cleanup),
		// don't re-create it from a late-arriving transcript write. However,
		// if activity arrives well after deletion (>10s) with a fresh
		// transcript, it's a genuine --continue — clear the flag and allow
		// re-creation. The 10s cooldown prevents ghost sessions from
		// late-arriving writes of a dying process.
		d.mu.Lock()
		deletedAt, deleted := d.deletedSessions[ev.SessionID]
		if deleted && time.Since(time.Unix(deletedAt, 0)) >= d.deletedCooldown && !isStaleTranscript(ev.TranscriptPath) {
			delete(d.deletedSessions, ev.SessionID)
			deleted = false
			d.log.LogInfo("session-detector", ev.SessionID,
				"previously deleted session has fresh activity (--continue), allowing re-creation")
		}
		d.mu.Unlock()
		if deleted {
			return
		}
		// Session not tracked yet — treat as new (startup race where activity
		// arrives before the initial scan).
		d.onNewSession(ev)
		return
	}

	// Apply any deferred PID from background discovery goroutines.
	// This must happen before ClassifyState so that the PID and state
	// transition are saved atomically, avoiding the race where
	// HandlePIDAssigned's separate Save would overwrite a state change.
	if pid, ok := d.pidMgr.ConsumePendingPID(ev.SessionID); ok {
		if state.PID != pid {
			state.PID = pid
			d.log.LogInfo("session-detector", ev.SessionID,
				fmt.Sprintf("applied deferred pid %d", pid))
		}
		// Capture launcher identity idempotently — HandlePIDAssigned
		// normally populates it, but this path runs first in startup
		// races where the pending PID is applied before the direct save.
		d.pidMgr.captureLauncher(state, pid)
	}

	// Retry PID discovery if not yet known.
	if state.PID == 0 {
		go d.pidMgr.TryDiscoverPID(ev.SessionID, state.CWD, ev.TranscriptPath, state.Adapter)
	}

	// Refresh CWD/branch/project and metrics from transcript.
	d.enricher.RefreshOnActivity(state, ev.TranscriptPath)

	// Drain authoritative subagent-completion signals harvested from this
	// parent's transcript (origin.kind="task-notification" lines parsed by
	// the Claude Code adapter). This is the event-based path to ready for
	// child subagents whose own JSONL ends with stop_reason=null — see
	// issue #134. The defensive finishOrphanedChildren below still runs as
	// a fallback for adapters/versions that don't emit notifications.
	if state.ParentSessionID == "" && state.Metrics != nil && len(state.Metrics.SubagentCompletions) > 0 {
		d.applySubagentCompletions(state.SessionID, state.Metrics.SubagentCompletions)
	}

	// Force ready→working when metrics show activity so ClassifyState can
	// properly detect the working→ready transition. Without this, sessions
	// that start as ready (initial state) and whose first activity event
	// already shows IsAgentDone()=true would stay ready with no transition
	// broadcast — the UI would never see the "agent finished" event.
	if state.State == session.StateReady && state.Metrics != nil && state.Metrics.LastEventType != "" {
		d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, PrevState: session.StateReady, NewState: session.StateWorking, Reason: ForceReadyToWorkingReason})
		state.State = session.StateWorking
	}

	// Overlay hook-based permission-pending signal onto metrics. Must happen
	// after RefreshOnActivity (which recomputes metrics from the transcript)
	// and before ClassifyState (which reads the flag). The flag persists in
	// the map until PostToolUse/PostToolUseFailure clears it, so it survives
	// fswatcher re-evaluations while the permission prompt is shown.
	d.permMu.Lock()
	if d.permissionPending[ev.SessionID] && state.Metrics != nil {
		if state.Metrics.LastWasToolDenial {
			// Permission was denied — Claude Code doesn't fire
			// PostToolUseFailure on denial, so clear from transcript
			// evidence. The denial text "[Request interrupted by user
			// for tool use]" sets LastWasToolDenial in the parser.
			delete(d.permissionPending, ev.SessionID)
		} else {
			state.Metrics.PermissionPending = true
		}
	}
	d.permMu.Unlock()

	// Content-based state detection.
	now := time.Now().Unix()
	newState, reason := ClassifyState(state.State, state.Metrics)

	// Parent-child propagation: if a parent session would transition to
	// ready but still has active children (working/waiting), hold it in
	// working. The parent will be re-evaluated when children finish.
	//
	// Before holding, fast-forward any "orphaned" children — subagents
	// whose own tail has no open tool calls but whose transcript ends
	// with `stop_reason: null` (Claude Code never writes end_turn for
	// in-process subagents). Since the parent's own turn is done,
	// those subagents' work is definitionally complete: the parent's
	// final assistant message already incorporated their results.
	parentHeldWorking := false
	if newState == session.StateReady && state.ParentSessionID == "" {
		d.finishOrphanedChildren(state.SessionID)
		if d.hasActiveChildren(state.SessionID) {
			d.log.LogInfo("session-detector", ev.SessionID,
				"holding parent working — active children still running")
			newState = session.StateWorking
			reason = ""
			parentHeldWorking = true
		}
	}

	// Same-pass user-blocking tool collapse (issue #150): when fswatcher
	// coalesces the AskUserQuestion / ExitPlanMode tool_use with its
	// tool_result, the tailer observes both in one pass and HasOpenToolCall
	// is already false by the time the classifier runs — the brief waiting
	// episode collapses and observers never see it. Emit a synthetic
	// working→waiting, then reclassify from waiting so the next transition
	// carries the correct "while waiting" phrasing.
	//
	// Skip when the parent-hold branch above rewrote newState: that parent
	// has active children and must stay working. Running the synth path
	// would reclassify from waiting, let rule 3 fire, and transition the
	// parent to ready despite children still running — undoing the hold.
	if !parentHeldWorking && ShouldSynthesizeCollapsedWaiting(state.State, newState, state.Metrics) {
		d.log.LogInfo("session-detector", ev.SessionID, SyntheticWaitingReason)
		d.record(lifecycle.Event{
			Kind:      lifecycle.KindStateTransition,
			SessionID: ev.SessionID,
			PrevState: session.StateWorking,
			NewState:  session.StateWaiting,
			Reason:    SyntheticWaitingReason,
		})
		state.State = session.StateWaiting
		newState, reason = ClassifyState(state.State, state.Metrics)
	}

	if newState != state.State {
		if reason != "" {
			d.log.LogInfo("session-detector", ev.SessionID, reason)
		}
		d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, PrevState: state.State, NewState: newState, Reason: reason})
		state.State = newState
		state.UpdatedAt = now

		// Side effects for specific transitions.
		if newState == session.StateWaiting {
			state.WaitingStartTime = &now
		} else if newState == session.StateWorking {
			state.LastTranscriptSize = 0
			state.WaitingStartTime = nil
		}
	}

	// Refresh the unified sub-agent summary (in-process from the adapter
	// plus file-based children from the repo). See ComputeSubagentSummary.
	d.refreshSubagentSummary(state)

	state.UpdatedAt = time.Now().Unix()
	state.EventCount++
	state.LastEvent = "transcript_activity"

	if err := d.repo.Save(state); err != nil {
		d.log.LogError("session-detector", ev.SessionID,
			fmt.Sprintf("failed to save activity update: %v", err))
		return
	}
	d.recordCost(state)

	d.broadcast(outbound.PushTypeUpdated, state)

	// When a parent session finishes, clean up all its child sessions.
	if state.State == session.StateReady && state.ParentSessionID == "" {
		d.pidMgr.cleanupChildren(state.SessionID)
	}

	// When a child session changes state, re-evaluate the parent — it may
	// be held in working solely because of this child.
	if state.ParentSessionID != "" {
		d.reevaluateParent(state.ParentSessionID)
	}
}

// refreshStaleSessions re-reads transcripts for working sessions that haven't
// received a file-system watcher event recently. This catches tool calls
// (AskUserQuestion, ExitPlanMode) that were missed because the subscriber
// channel was full during concurrent bursts.
func (d *SessionDetector) refreshStaleSessions() {
	sessions, err := d.repo.ListAll()
	if err != nil {
		return
	}
	now := time.Now()
	for _, state := range sessions {
		if state.State != session.StateWorking {
			continue
		}
		if state.TranscriptPath == "" {
			continue
		}
		if now.Sub(time.Unix(state.UpdatedAt, 0)) < staleWorkingRefreshInterval {
			continue
		}
		d.processActivity(agent.Event{
			Type:           agent.EventActivity,
			Adapter:        state.Adapter,
			SessionID:      state.SessionID,
			TranscriptPath: state.TranscriptPath,
		})
	}
}
