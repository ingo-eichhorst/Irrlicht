package services

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func (d *SessionDetector) handleTranscriptEvent(id agent.Identity, ev agent.Event) {
	// Workflow bookkeeping files (e.g. journal.jsonl) sit next to agent
	// transcripts in the run directory but are not sessions — drop them
	// before recording so they never surface in the UI (issue #565).
	if isWorkflowBookkeepingFile(ev.TranscriptPath) {
		return
	}

	// Workflow agents sit in a per-run directory, so the watcher reports the
	// ephemeral run id (wf_…) as their project dir. Relabel with the stable
	// layout name — mirroring the "subagents" label plain subagents carry —
	// so logs and recordings group cleanly (issue #565).
	if workflowRunRoot(filepath.Dir(ev.TranscriptPath)) != "" {
		ev.ProjectDir = "subagents/workflows"
	}

	// Record raw inbound event for lifecycle replay. Adapter identity is
	// sourced from the inbound.Watcher's Identity() — see the per-watcher
	// drain goroutine in Run() that wraps each event with its watcher's
	// identity into the merged channel.
	switch ev.Type {
	case agent.EventNewSession:
		d.record(lifecycle.Event{Kind: lifecycle.KindTranscriptNew, SessionID: ev.SessionID, Adapter: id.Name, TranscriptPath: ev.TranscriptPath, FileSize: ev.Size, ProjectDir: ev.ProjectDir, CWD: ev.CWD})
		d.onNewSession(id, ev)
	case agent.EventActivity:
		d.record(lifecycle.Event{Kind: lifecycle.KindTranscriptActivity, SessionID: ev.SessionID, Adapter: id.Name, TranscriptPath: ev.TranscriptPath, FileSize: ev.Size})
		d.onActivity(id, ev)
	case agent.EventRemoved:
		d.record(lifecycle.Event{Kind: lifecycle.KindTranscriptRemoved, SessionID: ev.SessionID, Adapter: id.Name, TranscriptPath: ev.TranscriptPath})
		d.onRemoved(ev)
	}
}

// onNewSession handles a new transcript file appearing.
func (d *SessionDetector) onNewSession(id agent.Identity, ev agent.Event) {
	d.log.LogInfo("session-detector", ev.SessionID,
		fmt.Sprintf("new session detected in %s (adapter=%s)", ev.ProjectDir, id.Name))

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

		// Skip orphan transcripts left by exited Claude Code processes —
		// unless a live agent process still owns the transcript's cwd
		// (issue #576: consent granted after sessions started makes
		// "stale at first sight" the canonical backfill path).
		if isStaleTranscript(ev.TranscriptPath) {
			liveCWD, live := d.isLiveStaleSession(id.Name, ev)
			if !live {
				d.log.LogInfo("session-detector", ev.SessionID,
					"skipping orphan transcript")
				return
			}
			// Thread the transcript-derived cwd into the event so
			// EnrichNewSession doesn't re-read the transcript for it (and
			// PID discovery gets a usable cwd for its fallback).
			if ev.CWD == "" {
				ev.CWD = liveCWD
			}
			d.log.LogInfo("session-detector", ev.SessionID,
				"stale transcript but live process owns its cwd — creating session")
		}

		// Skip zombie sessions whose worktree has been deleted from disk.
		// A long-dead session can be re-touched via `claude --resume` from
		// another worktree, which refreshes the transcript mtime and would
		// otherwise sneak past the staleness check during a daemon restart
		// (issue #321). The cwd directory missing on disk is an unambiguous
		// orphan signal — no live process can be running in a gone cwd.
		if cwdMissing(ev.CWD) {
			d.log.LogInfo("session-detector", ev.SessionID,
				"skipping session with missing cwd")
			return
		}

		// All new sessions start as ready. Content-based detection on
		// subsequent activity events will transition to working/waiting.
		parentID := ev.ParentSessionID
		if parentID == "" {
			parentID = deriveParentSession(ev.TranscriptPath)
		}
		state := &session.SessionState{
			Version:         1,
			SessionID:       ev.SessionID,
			State:           session.StateReady,
			Adapter:         id.Name,
			TranscriptPath:  ev.TranscriptPath,
			CWD:             ev.CWD,
			DaemonVersion:   d.version,
			ParentSessionID: parentID,
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
			d.record(lifecycle.Event{Kind: lifecycle.KindPreSessionCreated, SessionID: ev.SessionID, Adapter: id.Name, ProjectDir: ev.ProjectDir, CWD: ev.CWD})
		}

		// Record initial state transition.
		d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, NewState: session.StateReady, Reason: "new session created"})

		d.broadcast(outbound.PushTypeCreated, state)

		// When a real transcript session arrives, remove any pre-sessions for
		// the same project. Match by projectDir first (Claude Code layout),
		// then fall back to CWD (Codex/Pi have different transcript layouts).
		if ev.TranscriptPath != "" {
			d.cleanupPreSessionsForProject(ev.ProjectDir, state.CWD, id.Name)
		}
	} else {
		// Session already exists. Update transcript path if missing, and
		// backfill Adapter when it's empty — this happens when an earlier
		// processActivity fallback (debounce/refresh) created the session
		// without an identity. The watcher's identity on the next real
		// transcript event is the authoritative source.
		//
		// Under the PIDManager's state lock: a discovery goroutine spawned by
		// the earlier event may still be in flight, and its assignPIDLocked
		// writes state.PID/UpdatedAt on this same pointer (issue #606).
		d.pidMgr.WithSessionStateLock(func() {
			changed := false
			if existing.TranscriptPath == "" {
				existing.TranscriptPath = ev.TranscriptPath
				changed = true
			}
			if existing.Adapter == "" && id.Name != "" {
				existing.Adapter = id.Name
				changed = true
			}
			if changed {
				existing.UpdatedAt = now
				if err := d.repo.Save(existing); err != nil {
					d.log.LogError("session-detector", ev.SessionID,
						fmt.Sprintf("failed to update existing session: %v", err))
				}
			}
		})
	}

	// PID discovery (async). Each adapter has its own strategy:
	// Claude Code uses CWD-based matching, Codex/Pi use transcript file writer.
	adapter := id.Name
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

// isLiveStaleSession reports whether a stale transcript should still produce
// a session because a live agent process owns its cwd (issue #576). Checks
// run cheapest-first:
//  1. Subagent transcripts are never rescued — they have no process of their
//     own, and finished subagents are routinely stale.
//  2. Only the newest .jsonl in its directory qualifies — the watcher's
//     initial scan emits every transcript younger than MaxSessionAge, and a
//     single live process can only correspond to the most recent one.
//  3. The transcript must yield a cwd (from the event for scanner-sourced
//     sessions, from transcript content for fswatcher events, which carry
//     no CWD).
//  4. A live process of this adapter's binary must own that cwd.
//
// The resolved cwd is returned alongside the verdict so the caller can reuse
// it instead of re-extracting it from the transcript during enrichment.
func (d *SessionDetector) isLiveStaleSession(adapter string, ev agent.Event) (string, bool) {
	if deriveParentSessionID(ev.TranscriptPath) != "" {
		return "", false
	}
	if !isNewestTranscriptInDir(ev.TranscriptPath) {
		return "", false
	}
	cwd := ev.CWD
	if cwd == "" {
		cwd = d.enricher.git.GetCWDFromTranscript(ev.TranscriptPath)
	}
	if cwd == "" {
		return "", false
	}
	return cwd, d.pidMgr.HasLiveProcessInCWD(adapter, cwd)
}

// onActivity debounces transcript activity events per session. The first event
// fires immediately (so the UI stays responsive), then subsequent events
// within a 2-second window are coalesced into a single processActivity call.
// Identity is forwarded to processActivity (only needed for the rare
// startup-race fallback where state is nil and we want to bootstrap a
// session with a non-empty Adapter). Coalesced events lose identity at the
// debouncedEvents-channel boundary — see Run() for the rationale.
func (d *SessionDetector) onActivity(id agent.Identity, ev agent.Event) {
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
		d.processActivity(id, ev)
		return
	}

	// Subsequent event within the debounce window.
	if ev.Terminal {
		// Turn-end signal: short-circuit the debounce, fire immediately.
		entry.timer.Stop()
		delete(d.debounce, sid)
		d.debounceMu.Unlock()
		d.record(lifecycle.Event{Kind: lifecycle.KindDebounceTerminal, SessionID: ev.SessionID})
		d.processActivity(id, ev)
		return
	}

	// Coalesce.
	entry.latest = ev
	entry.pending = true
	entry.timer.Reset(activityDebounceWindow)
	d.debounceMu.Unlock()

	d.record(lifecycle.Event{Kind: lifecycle.KindDebounceCoalesced, SessionID: ev.SessionID})
}

// processActivityWithoutIdentity is the entry point for paths where no
// watcher identity is available — debounce-coalesced events and synthetic
// refresh events. See processActivity for what "no identity" implies.
func (d *SessionDetector) processActivityWithoutIdentity(ev agent.Event) {
	d.processActivity(agent.Identity{}, ev)
}

// processActivity handles a (possibly debounced) transcript activity
// event. It uses content-based detection to determine whether the agent
// is working or waiting for user input.
//
// id is the watcher's Identity only when invoked directly from
// handleTranscriptEvent / onActivity (the first event in a debounce
// window). For the no-identity entry points, call
// processActivityWithoutIdentity instead.
func (d *SessionDetector) processActivity(id agent.Identity, ev agent.Event) {
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
		// arrives before the initial scan). Identity carries the adapter
		// name when invoked from handleTranscriptEvent; debounce/refresh
		// paths pass an empty Identity, in which case state.Adapter starts
		// empty and gets backfilled later when handleTranscriptEvent
		// processes the next transcript event for this session (see the
		// existing-session branch in onNewSession).
		d.onNewSession(id, ev)
		return
	}

	// Run the load-modify-save of this existing session under the PIDManager's
	// state lock. processActivity spawns a PID-discovery goroutine below
	// (state.PID == 0 branch); that goroutine's assignPIDLocked writes state.PID
	// on the same *SessionState pointer this loop mutates. Sharing assignMu makes
	// the two mutually exclusive (issue #606).
	d.pidMgr.WithSessionStateLock(func() {
		d.processActivityLocked(state, ev)
	})
}

// processActivityLocked performs the content-based classification and
// load-modify-save for an already-loaded existing session. It MUST be called
// under PIDManager.WithSessionStateLock so the PID-discovery goroutine it spawns
// can't write state.PID concurrently with this loop's writes (issue #606).
func (d *SessionDetector) processActivityLocked(state *session.SessionState, ev agent.Event) {
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

	// Probe Bash background-process liveness (run_in_background). Must run
	// after RefreshOnActivity (which recomputes metrics, clearing the flag)
	// and before ClassifyState (whose IsAgentDone check reads it). Gated on
	// the transcript-derived count, so only sessions that launched a
	// background process ever shell out. The 5s refreshStaleSessions ticker
	// re-runs this path for working sessions, so a process that exits with no
	// further transcript activity still flips the session ready on re-probe.
	// See issue #445.
	d.applyBackgroundLiveness(state)

	// Drain authoritative subagent-completion signals harvested from this
	// parent's transcript (origin.kind="task-notification" lines parsed by
	// the Claude Code adapter). This is the event-based path to ready for
	// child subagents whose own JSONL ends with stop_reason=null — see
	// issue #134. The defensive finishOrphanedChildren below still runs as
	// a fallback for adapters/versions that don't emit notifications.
	if state.ParentSessionID == "" && state.Metrics != nil && len(state.Metrics.SubagentCompletions) > 0 {
		d.applySubagentCompletions(state.SessionID, state.Metrics.SubagentCompletions)
	}

	// Short-circuit when this pass consumed transcript content but produced
	// no state-relevant change — e.g. Claude Code's post-turn
	// `system/away_summary` recap, which the parser marks Skip=true. The
	// force-bounce below would otherwise see the stale LastEventType from
	// the prior turn_done and flip a ready session back to working
	// (issue #329). Still record this as activity (LastEvent / EventCount /
	// UpdatedAt / broadcast) so the UI's "last activity" stays fresh —
	// just don't re-run the state machine.
	skipClassification := state.Metrics != nil && state.Metrics.NoSubstantiveActivity

	if !skipClassification {
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

		// Overlay the transcript-based stalled-edit-tool fallback (#488).
		d.markStalledEditTool(ev.SessionID, state.Metrics, time.Now().Unix())

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
		//
		// The fast-forward also fires when the turn ends in waiting with a
		// question/cue (#593) — IsAgentDone gates it, so permission-prompt
		// waiting (open tool call) is excluded. Without this, a parent whose
		// overnight run ends by asking a question leaves its finished
		// children stuck in working until the liveness sweep. The waiting
		// branch only fast-forwards: no hold (the parent legitimately waits
		// on the user) and no cleanupChildren (background children may still
		// be running; finishOrphanedChildren's quiet-window and open-tool
		// guards leave those alone).
		parentHeldWorking := false
		if state.ParentSessionID == "" {
			switch {
			case newState == session.StateReady:
				d.finishOrphanedChildren(state.SessionID)
				if d.hasActiveChildren(state.SessionID) {
					d.log.LogInfo("session-detector", ev.SessionID,
						"holding parent working — active children still running")
					newState = session.StateWorking
					reason = ""
					parentHeldWorking = true
				}
			case newState == session.StateWaiting && state.Metrics != nil && state.Metrics.IsAgentDone():
				// Turn-done waiting (question/cue) — fast-forward only.
				// Permission-prompt waiting never reaches here: its open
				// tool call makes IsAgentDone return false.
				d.finishOrphanedChildren(state.SessionID)
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
		prevSummary := state.Subagents
		d.pidMgr.cleanupChildren(state.SessionID)
		// The broadcast above carried a summary counting children that were
		// just deleted. Refresh, persist, and re-push so the turn's final
		// parent message has the cleared badge AND the repo copy is clean —
		// hook-path transitions re-broadcast the persisted summary as-is
		// (#593). Gated on the summary changing so the common no-children
		// case adds no push traffic.
		d.refreshSubagentSummary(state)
		if !state.Subagents.Equal(prevSummary) {
			if err := d.repo.Save(state); err != nil {
				d.log.LogError("session-detector", ev.SessionID,
					fmt.Sprintf("failed to persist cleared subagent summary: %v", err))
			}
			d.broadcast(outbound.PushTypeUpdated, state)
		}
	}

	// When a child session changes state, re-evaluate the parent — it may
	// be held in working solely because of this child.
	if state.ParentSessionID != "" {
		d.reevaluateParent(state.ParentSessionID)
	}
}

// applyBackgroundLiveness sets HasLiveBackgroundProcess on the session's
// metrics from the last-known liveness of its Bash background processes
// (run_in_background), and kicks off an off-loop refresh of that knowledge.
// When true, IsAgentDone returns false and the classifier holds the session
// `working` past end_turn until the process exits.
//
// Two deliberate choices (issue #445 review):
//   - Gated on state == working. The feature only ever needs to PREVENT a
//     working→ready transition; it must never RESURRECT a session the user
//     already cancelled (ESC → ready) just because a detached process is still
//     alive. Non-working sessions clear their cache and the flag.
//   - The lsof probe runs in a goroutine, not inline, so a slow filesystem
//     can't stall the single event-loop goroutine (and thus every other
//     session). processActivity uses the last-known value — optimistically
//     "alive" on first sight so a not-yet-probed process is never prematurely
//     declared dead — and a completed probe whose verdict changed nudges the
//     event loop (via debouncedEvents) to re-classify promptly.
func (d *SessionDetector) applyBackgroundLiveness(state *session.SessionState) {
	sid := state.SessionID
	m := state.Metrics
	if m == nil || state.State != session.StateWorking ||
		m.BackgroundProcessCount == 0 || len(m.BackgroundProcessOutputs) == 0 || d.bgLiveProbe == nil {
		d.bgMu.Lock()
		delete(d.bgLive, sid)
		delete(d.bgProbing, sid)
		d.bgMu.Unlock()
		if m != nil {
			m.HasLiveBackgroundProcess = false
		}
		return
	}

	d.bgMu.Lock()
	known, seen := d.bgLive[sid]
	alive := true // optimistic on first sight — never flip to ready before probing
	if seen {
		alive = known
	}
	startProbe := !d.bgProbing[sid]
	if startProbe {
		d.bgProbing[sid] = true
	}
	d.bgMu.Unlock()

	m.HasLiveBackgroundProcess = alive
	if !startProbe {
		return
	}

	outputs := append([]string(nil), m.BackgroundProcessOutputs...) // copy: goroutine must not alias state
	transcriptPath := state.TranscriptPath
	go func() {
		live := d.bgLiveProbe(outputs)
		d.bgMu.Lock()
		prev, had := d.bgLive[sid]
		d.bgLive[sid] = live
		d.bgProbing[sid] = false
		d.bgMu.Unlock()
		// On a changed (or first) verdict, nudge the event loop to re-classify
		// now rather than waiting for the next refresh tick. The send mirrors
		// the debounce-timer path (single event-loop goroutine owns
		// processActivity); drop if the buffer is full — the 5s refresh is the
		// backstop.
		if !had || prev != live {
			select {
			case d.debouncedEvents <- agent.Event{Type: agent.EventActivity, SessionID: sid, TranscriptPath: transcriptPath}:
			default:
			}
		}
	}()
}

// markStalledEditTool maintains the per-session editToolOpenSince window and
// sets metrics.OpenToolStalled when a permission-gated edit tool
// (Edit/Write/MultiEdit/NotebookEdit) has been open past the stale-refresh
// interval — the transcript-based fallback for a held permission prompt when
// the curl-delivered PermissionRequest hook can't reach the daemon (#488).
//
// Those tools run in-process and complete near-instantly, so one still open
// after the window means the agent is blocked on the prompt, not executing.
// The window is tracked from first observation (not state.UpdatedAt), so a
// fresh tool_use write is never flagged on the spot — only the 5s
// refreshStaleSessions re-evaluation of a lingering open tool is, which also
// lets a held prompt flip without any new transcript write. The flag is
// redundant once PermissionPending fired (the classifier prefers the hook),
// so it is skipped then. now is injected for testability.
func (d *SessionDetector) markStalledEditTool(sessionID string, m *session.SessionMetrics, now int64) {
	d.permMu.Lock()
	defer d.permMu.Unlock()

	if m == nil || !m.HasOpenEditPermissionTool() {
		delete(d.editToolOpenSince, sessionID)
		return
	}
	since, ok := d.editToolOpenSince[sessionID]
	if !ok {
		since = now
		d.editToolOpenSince[sessionID] = since
	}
	if !m.PermissionPending && now-since >= int64(staleWorkingRefreshInterval.Seconds()) {
		m.OpenToolStalled = true
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
		// Consent-gated per adapter (#570): this refresh re-reads the
		// transcript independently of the (gated) watcher pipeline. After
		// a revoke, persisted working sessions would otherwise keep being
		// re-read and re-broadcast every tick — "existing sessions stop
		// updating" must hold.
		if !d.observeAllowed(state.Adapter) {
			continue
		}
		if now.Sub(time.Unix(state.UpdatedAt, 0)) < staleWorkingRefreshInterval {
			continue
		}
		d.processActivityWithoutIdentity(agent.Event{
			Type:           agent.EventActivity,
			SessionID:      state.SessionID,
			TranscriptPath: state.TranscriptPath,
		})
	}
}
