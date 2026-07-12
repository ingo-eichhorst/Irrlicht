package services

import (
	"fmt"
	"os"
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
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
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
		if !d.admitNewSession(id, &ev) {
			return
		}
		// All new sessions start as ready. Content-based detection on
		// subsequent activity events will transition to working/waiting.
		state := d.buildNewSessionState(id, ev, now)
		if !d.finalizeNewSession(id, ev, state) {
			return
		}
	} else {
		d.backfillExistingSession(id, ev, existing, now)
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

// admitNewSession runs the full new-session admission gate in order:
// recently-deleted cooldown, orphan/live-stale-transcript check, missing-cwd
// check, cached host-gate rejection, and the host-ancestry gate itself. May
// mutate ev.CWD when the stale-transcript check resolves one from the
// transcript so EnrichNewSession doesn't have to re-read it. Returns false
// (already logged) the moment any check rejects the session.
func (d *SessionDetector) admitNewSession(id agent.Identity, ev *agent.Event) bool {
	// Skip transcripts whose session was recently deleted (process exit or
	// /clear cleanup). recentlyDeleted itself allows re-creation once the
	// cooldown has passed and the transcript has fresh activity (--continue).
	if d.recentlyDeleted(*ev) {
		d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
			"skipping recently deleted session")
		return false
	}

	if !d.admitStaleTranscript(id, ev) {
		return false
	}

	// Skip zombie sessions whose worktree has been deleted from disk. A
	// long-dead session can be re-touched via `claude --resume` from another
	// worktree, which refreshes the transcript mtime and would otherwise
	// sneak past the staleness check during a daemon restart (issue #321).
	// The cwd directory missing on disk is an unambiguous orphan signal — no
	// live process can be running in a gone cwd.
	if cwdMissing(ev.CWD) {
		d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
			"skipping session with missing cwd")
		return false
	}

	// Skip sessions previously rejected by the host-ancestry gate below.
	// Checked unconditionally (before looking at id.Name) because the
	// debounce-coalesce path re-enters onNewSession with an empty Identity
	// (see processActivityWithoutIdentity) — without this cache,
	// AllowsSession("", ...) would no-op on the unrecognized adapter name and
	// let an already-rejected non-interactive session slip through on a
	// same-window retry.
	if d.wasHostRejected(ev.SessionID) {
		return false
	}

	return d.admitHost(id, *ev)
}

// recentlyDeleted reports whether ev's session was explicitly deleted
// (process exit, /clear cleanup) recently enough that a late-arriving
// transcript write should still be ignored. Once the cooldown has passed and
// the transcript shows fresh activity, it's a genuine --continue: the
// deletion record is cleared and false is returned. The cooldown prevents
// ghost sessions from late-arriving writes of a dying process.
func (d *SessionDetector) recentlyDeleted(ev agent.Event) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	deletedAt, deleted := d.deletedSessions[ev.SessionID]
	if !deleted {
		return false
	}
	if time.Since(time.Unix(deletedAt, 0)) < d.deletedCooldown || isStaleTranscript(ev.TranscriptPath) {
		return true
	}
	delete(d.deletedSessions, ev.SessionID)
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
		"previously deleted session has fresh activity (--continue), allowing re-creation")
	return false
}

// admitStaleTranscript reports whether a stale transcript should still admit
// a new session: skip orphan transcripts left by exited processes, but not
// when a live agent process still owns the transcript's cwd (issue #576:
// consent granted after sessions started makes "stale at first sight" the
// canonical backfill path). A non-stale transcript always admits. Threads
// the transcript-derived cwd into ev so EnrichNewSession and PID discovery's
// fallback don't have to re-read the transcript for it.
func (d *SessionDetector) admitStaleTranscript(id agent.Identity, ev *agent.Event) bool {
	if !isStaleTranscript(ev.TranscriptPath) {
		return true
	}
	liveCWD, live := d.isLiveStaleSession(id.Name, *ev)
	if !live {
		d.log.LogInfo(logComponentSessionDetector, ev.SessionID, "skipping orphan transcript")
		return false
	}
	// Thread the transcript-derived cwd into the event so EnrichNewSession
	// doesn't re-read the transcript for it (and PID discovery gets a usable
	// cwd for its fallback).
	if ev.CWD == "" {
		ev.CWD = liveCWD
	}
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
		"stale transcript but live process owns its cwd — creating session")
	return true
}

// wasHostRejected reports whether sessionID was already rejected by the
// host-ancestry gate (issue #784). See admitNewSession's call site for why
// this cache is checked unconditionally, ahead of admitHost itself.
func (d *SessionDetector) wasHostRejected(sessionID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, rejected := d.hostGateRejected[sessionID]
	return rejected
}

// admitHost reports whether id's adapter may create a session for ev,
// rejecting one whose process was launched by something other than a known
// interactive host (currently antigravity only) — a synchronous, one-shot
// check so a non-interactive process never becomes a visible session in the
// first place, rather than appearing and being reaped later (issue #784).
// Adapters that don't require a known host always admit. A rejection is
// cached in hostGateRejected (see wasHostRejected).
func (d *SessionDetector) admitHost(id agent.Identity, ev agent.Event) bool {
	if !d.pidMgr.RequiresKnownHost(id.Name) {
		return true
	}
	cwd := ev.CWD
	if cwd == "" {
		cwd = d.enricher.git.GetCWDFromTranscript(ev.TranscriptPath)
	}
	if d.pidMgr.AllowsSession(ev.SessionID, id.Name, cwd, ev.TranscriptPath) {
		return true
	}
	d.mu.Lock()
	d.hostGateRejected[ev.SessionID] = struct{}{}
	d.mu.Unlock()
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
		"skipping session bound to a non-interactive host")
	return false
}

// buildNewSessionState constructs the initial SessionState for a
// just-admitted new session and enriches it with git metadata and metrics.
func (d *SessionDetector) buildNewSessionState(id agent.Identity, ev agent.Event, now int64) *session.SessionState {
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

	// A child's own first activity event may not arrive for a while — the
	// same background-workflow discovery lag that leaves its parent needing
	// holdParentWorkingForNewChild (in finalizeNewSession) below. Classify it
	// against its own freshly-computed metrics now instead of leaving it at
	// the generic "ready until proven otherwise" bootstrap: a child stuck at
	// ready doesn't count as active in hasActiveChildren, so the parent's
	// hold can't survive past the child's own next activity pass — e.g. the
	// periodic stale-session sweep would see the parent's own turn-done
	// metrics unchanged, find no active children, and flip it straight back
	// to ready (issue #889).
	if state.ParentSessionID != "" {
		if newState, _ := ClassifyState(state.State, state.Metrics); newState != state.State {
			state.State = newState
		}
	}
	return state
}

// finalizeNewSession persists a freshly built session, records its creation
// events, broadcasts it, and runs the two post-create side effects that
// depend on it already being visible: holding a stalled parent working, and
// clearing out any pre-session placeholder for the same project. Returns
// false if the save failed (already logged), so the caller stops there.
func (d *SessionDetector) finalizeNewSession(id agent.Identity, ev agent.Event, state *session.SessionState) bool {
	if err := d.repo.Save(state); err != nil {
		d.log.LogError(logComponentSessionDetector, ev.SessionID,
			fmt.Sprintf("failed to save new session: %v", err))
		return false
	}
	d.recordCost(state)

	// Record pre-session detection (proc-* IDs only — real transcript
	// sessions are already covered by KindTranscriptNew in handleTranscriptEvent).
	if strings.HasPrefix(ev.SessionID, "proc-") {
		d.record(lifecycle.Event{Kind: lifecycle.KindPreSessionCreated, SessionID: ev.SessionID, Adapter: id.Name, ProjectDir: ev.ProjectDir, CWD: ev.CWD})
	}

	// Record initial state transition.
	d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, NewState: state.State, Reason: "new session created"})

	d.broadcast(outbound.PushTypeCreated, state)

	// This child's parent may have already flipped to ready before this
	// child was discovered (e.g. a background Workflow-tool run whose
	// turn-done fired while it had no active children yet). Hold it working
	// now instead of waiting for the parent's own next transcript/hook event
	// to catch it (issue #889).
	if state.ParentSessionID != "" {
		d.holdParentWorkingForNewChild(state.ParentSessionID)
	}

	// When a real transcript session arrives, remove any pre-sessions for the
	// same project. Match by projectDir first (Claude Code layout), then
	// fall back to CWD (Codex/Pi have different transcript layouts).
	if ev.TranscriptPath != "" {
		d.cleanupPreSessionsForProject(ev.ProjectDir, state.CWD, id.Name)
	}
	return true
}

// backfillExistingSession fills in TranscriptPath/Adapter on an
// already-known session when an earlier processActivity fallback
// (debounce/refresh) created it without one — the watcher's identity on this
// transcript event is the authoritative source. Runs under the PIDManager's
// state lock: a discovery goroutine spawned by the earlier event may still
// be in flight, and its assignPIDLocked writes state.PID/UpdatedAt on this
// same pointer (issue #606).
func (d *SessionDetector) backfillExistingSession(id agent.Identity, ev agent.Event, existing *session.SessionState, now int64) {
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
		if !changed {
			return
		}
		existing.UpdatedAt = now
		if err := d.repo.Save(existing); err != nil {
			d.log.LogError(logComponentSessionDetector, ev.SessionID,
				fmt.Sprintf("failed to update existing session: %v", err))
		}
	})
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
			d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
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
		d.processActivityLocked(id, state, ev)
	})
}

// processActivityLocked performs the content-based classification and
// load-modify-save for an already-loaded existing session. It MUST be called
// under PIDManager.WithSessionStateLock so the PID-discovery goroutine it spawns
// can't write state.PID concurrently with this loop's writes (issue #606).
//
// id is the watcher's identity on the first event of a debounce window and
// empty on the coalesced/refresh paths — see processActivity.
func (d *SessionDetector) processActivityLocked(id agent.Identity, state *session.SessionState, ev agent.Event) {
	d.backfillAdapterFromIdentity(id, state, ev)
	d.applyPendingPID(state, ev)

	// Retry PID discovery if not yet known.
	if state.PID == 0 {
		go d.pidMgr.TryDiscoverPID(ev.SessionID, state.CWD, ev.TranscriptPath, state.Adapter)
	}

	// Refresh CWD/branch/project and metrics from transcript.
	d.enricher.RefreshOnActivity(state, ev.TranscriptPath)

	transcriptGrew := d.observeTranscriptGrowth(state, ev)

	// Short-circuit when this pass consumed transcript content but produced
	// no state-relevant change — e.g. Claude Code's post-turn
	// `system/away_summary` recap, which the parser marks Skip=true. The
	// force-bounce below would otherwise see the stale LastEventType from
	// the prior turn_done and flip a ready session back to working
	// (issue #329).
	skipClassification := state.Metrics != nil && state.Metrics.NoSubstantiveActivity

	// Bounce a ready session back to working on fresh activity BEFORE the
	// background-liveness probe below, which is gated on state==working
	// (issue #445) and would otherwise see this pass's stale `ready` and
	// clear/skip the probe — silently dropping the hold for a background
	// process (e.g. a retried `git push`) launched right after the session
	// had settled to ready. See issue #937. Skipped in lockstep with
	// classifyAndTransition below so a no-substantive-activity pass still
	// can't force the bounce (issue #329).
	if !skipClassification {
		d.forceReadyToWorkingIfActive(state, ev)
	}

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

	d.recordTaskDeltas(id, ev, state)

	// Still record this pass as activity (LastEvent / EventCount / UpdatedAt /
	// broadcast) below even when classification is skipped, so the UI's
	// "last activity" stays fresh — just don't re-run the state machine.
	if !skipClassification {
		d.classifyAndTransition(state, ev)
	}

	// Refresh the unified sub-agent summary (in-process from the adapter
	// plus file-based children from the repo). See ComputeSubagentSummary.
	d.refreshSubagentSummary(state)

	// Only treat this pass as an activity event when the transcript actually
	// grew. A bare refresh-tick re-read of a frozen transcript leaves the
	// activity markers (UpdatedAt / EventCount / LastEvent) alone so the
	// ready-TTL age-out can reap dead sessions (issue #667). A real state
	// transition still bumps UpdatedAt via the write inside the classify block
	// above, so #445's process-exit settle is unaffected.
	if transcriptGrew {
		state.UpdatedAt = time.Now().Unix()
		state.EventCount++
		state.LastEvent = "transcript_activity"
	}

	if err := d.repo.Save(state); err != nil {
		d.log.LogError(logComponentSessionDetector, ev.SessionID,
			fmt.Sprintf("failed to save activity update: %v", err))
		return
	}
	d.recordCost(state)

	d.broadcast(outbound.PushTypeUpdated, state)

	// When a parent session finishes, clean up all its child sessions.
	if state.State == session.StateReady && state.ParentSessionID == "" {
		d.cleanupFinishedParent(state)
	}

	// When a child session changes state, re-evaluate the parent — it may
	// be held in working solely because of this child.
	if state.ParentSessionID != "" {
		d.reevaluateParent(state.ParentSessionID)
	}
}

// backfillAdapterFromIdentity fills in an empty Adapter from the watcher's
// identity. A session created via the no-identity fallback (debounce-
// coalesced / synthetic refresh during the startup race, see
// processActivity → onNewSession) starts with Adapter="" and never sees
// another transcript-NEW event while it stays continuously active, so
// onNewSession's existing-session backfill never fires. Doing it here
// unblocks PID discovery (TryDiscoverPID keys off the adapter) and the ghost
// sweep on the very next identity-carrying activity event (issue #643). Runs
// under WithSessionStateLock with the other existing-session writes (issue
// #606).
func (d *SessionDetector) backfillAdapterFromIdentity(id agent.Identity, state *session.SessionState, ev agent.Event) {
	if state.Adapter != "" || id.Name == "" {
		return
	}
	state.Adapter = id.Name
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
		fmt.Sprintf("backfilled empty adapter=%s on activity", id.Name))
}

// applyPendingPID applies any deferred PID from a background discovery
// goroutine. This must happen before ClassifyState so that the PID and state
// transition are saved atomically, avoiding the race where
// HandlePIDAssigned's separate Save would overwrite a state change.
func (d *SessionDetector) applyPendingPID(state *session.SessionState, ev agent.Event) {
	pid, ok := d.pidMgr.ConsumePendingPID(ev.SessionID)
	if !ok {
		return
	}
	if state.PID != pid {
		state.PID = pid
		d.log.LogInfo(logComponentSessionDetector, ev.SessionID,
			fmt.Sprintf("applied deferred pid %d", pid))
	}
	// Capture launcher identity + background-agent marker idempotently —
	// HandlePIDAssigned normally populates them, but this path runs first in
	// startup races where the pending PID is applied before the direct save.
	// captureBackground must follow captureLauncher: it derives Detached from
	// the just-captured TTY (#744).
	d.pidMgr.captureLauncher(state, pid)
	d.pidMgr.captureBackground(state, pid)
}

// observeTranscriptGrowth reports whether this pass observed new transcript
// bytes. The 5s refreshStaleSessions ticker re-reads working sessions to
// recover missed tool-call events, but for a frozen transcript (e.g. a
// gemini-cli session whose process died mid-turn, pid=0) that re-read sees
// nothing new — yet an unconditional UpdatedAt bump would refresh it to
// wall-clock "now" every tick, defeating the ready-TTL age-out that reaps
// dead sessions (issue #667). Tracks size on the persisted
// LastTranscriptSize so UpdatedAt advances only on real activity. Fail-open:
// a stat error counts as growth so the bump is only ever suppressed when the
// transcript is positively confirmed unchanged.
//
// Extends the #667 byte-growth suppression to a second ghost shape. A PID==0
// session is transcript-first — discovered from its on-disk transcript with
// no agent process to liveness-check (an Antigravity IDE conversation is the
// canonical case). Its transcript can be a system log that keeps appending
// SYSTEM steps (CONVERSATION_HISTORY/CHECKPOINT, all parsed Skip=true) after
// the agent stopped, so the size check alone stays true and re-bumps
// UpdatedAt forever — now-UpdatedAt never crosses readyTTL and the PID==0
// sweep (PIDManager.CheckPIDLiveness) can never reap it (issue #735). When
// this pass consumed only non-substantive lines, treat it as no growth so it
// ages out like a dead-process session (see
// TestCheckPIDLiveness_DeadProcessWorking_Reaped). Scoped to PID==0: a
// PID-bound session is reaped by process liveness instead, and keeps the
// activity bump for noisy post-turn recaps so its "last activity" stays
// fresh (issue #329).
func (d *SessionDetector) observeTranscriptGrowth(state *session.SessionState, ev agent.Event) bool {
	transcriptGrew := true
	if ev.TranscriptPath != "" {
		if fi, err := os.Stat(ev.TranscriptPath); err == nil {
			transcriptGrew = fi.Size() != state.LastTranscriptSize
			state.LastTranscriptSize = fi.Size()
		}
	}
	if state.PID == 0 && state.Metrics != nil && state.Metrics.NoSubstantiveActivity {
		transcriptGrew = false
	}
	return transcriptGrew
}

// recordTaskDeltas records the task-list deltas folded in this pass — one
// task_delta lifecycle event each — so task tracking is an assertable
// observable in onboarding recordings (#662). Per-pass and transient
// (MergeMetrics copies AppliedTaskDeltas with no old-value fallback), so
// each delta is recorded exactly once. Recording-only: does not feed
// ClassifyState.
func (d *SessionDetector) recordTaskDeltas(id agent.Identity, ev agent.Event, state *session.SessionState) {
	if state.Metrics == nil {
		return
	}
	for _, td := range state.Metrics.AppliedTaskDeltas {
		d.record(lifecycle.Event{
			Kind:        lifecycle.KindTaskDelta,
			SessionID:   ev.SessionID,
			Adapter:     id.Name,
			TaskOp:      td.Op,
			TaskID:      td.ID,
			TaskSubject: td.Subject,
			TaskStatus:  td.Status,
		})
	}
}

// classifyAndTransition runs the content-based state-detection pass: it
// overlays the hook/transcript-derived signals ClassifyState reads, computes
// the candidate next state, applies the parent-child and same-pass-collapse
// corrections, and — if the corrected state differs from the current one —
// records and applies the transition with its side effects. Only called
// when this pass's metrics show substantive activity (see
// processActivityLocked's skipClassification guard).
func (d *SessionDetector) classifyAndTransition(state *session.SessionState, ev agent.Event) {
	// Ready→working (if applicable) already ran in processActivityLocked,
	// before applyBackgroundLiveness — see that call site.

	// Overlay hook-based permission-pending signal onto metrics. Must happen
	// after RefreshOnActivity (which recomputes metrics from the transcript)
	// and before ClassifyState (which reads the flag). The flag persists in
	// the map until PostToolUse/PostToolUseFailure clears it, so it survives
	// fswatcher re-evaluations while the permission prompt is shown.
	d.overlayPermissionPending(state)

	// Overlay the PreCompact force-working hold (#657).
	d.applyCompactHold(ev.SessionID, state.Metrics, time.Now().Unix())

	// Overlay the transcript-based stalled-edit-tool fallback (#488).
	d.markStalledEditTool(ev.SessionID, state.Metrics, time.Now().Unix())

	// Content-based state detection.
	now := time.Now().Unix()
	newState, reason := ClassifyState(state.State, state.Metrics)
	newState, reason, parentHeldWorking := d.holdParentForActiveChildren(state, ev, newState, reason)
	newState, reason = d.synthesizeCollapsedWaitingIfNeeded(state, ev, collapsedWaitingCandidate{
		NewState:          newState,
		Reason:            reason,
		ParentHeldWorking: parentHeldWorking,
	})

	if newState != state.State {
		d.applyStateTransition(state, ev, stateTransitionUpdate{
			NewState: newState,
			Reason:   reason,
			Now:      now,
		})
	}

	// Cache-creation regression detection (#374). Driven every substantive
	// pass; the detector itself recognises turn boundaries (rising edge of
	// IsAgentDone) and is a no-op otherwise or when disabled.
	if d.cacheBloat != nil {
		d.cacheBloat.OnActivity(state)
	}
}

// forceReadyToWorkingIfActive bounces a ready session straight to working
// when its freshly refreshed metrics show activity, so ClassifyState can
// then detect a genuine working→ready transition on this same pass, and so
// the background-liveness probe (see processActivityLocked's call site)
// evaluates against this pass's effective state rather than the prior
// pass's stale `ready`.
func (d *SessionDetector) forceReadyToWorkingIfActive(state *session.SessionState, ev agent.Event) {
	if state.State != session.StateReady || state.Metrics == nil || state.Metrics.LastEventType == "" {
		return
	}
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID, ForceReadyToWorkingReason)
	d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, PrevState: session.StateReady, NewState: session.StateWorking, Reason: ForceReadyToWorkingReason})
	state.State = session.StateWorking
}

// overlayPermissionPending folds the hook-based permission-pending signal
// onto metrics. See classifyAndTransition's call site for the ordering
// requirement relative to RefreshOnActivity and ClassifyState.
func (d *SessionDetector) overlayPermissionPending(state *session.SessionState) {
	d.permMu.Lock()
	defer d.permMu.Unlock()

	if !d.permissionPending[state.SessionID] || state.Metrics == nil {
		return
	}
	if state.Metrics.LastWasToolDenial {
		// Permission was denied — Claude Code doesn't fire PostToolUseFailure
		// on denial, so clear from transcript evidence. The denial text
		// "[Request interrupted by user for tool use]" sets LastWasToolDenial
		// in the parser.
		delete(d.permissionPending, state.SessionID)
		return
	}
	state.Metrics.PermissionPending = true
}

// holdParentForActiveChildren fast-forwards a parent's own transition when
// it would otherwise land on ready, or on a turn-done "waiting"
// (question/cue), while a child subagent is still working or waiting —
// mirroring the ready case so the dashboard doesn't read "nothing happening"
// while a subagent runs (issue #897). Before holding, it fast-forwards any
// "orphaned" children — subagents whose own tail has no open tool calls but
// whose transcript ends with `stop_reason: null` (Claude Code never writes
// end_turn for in-process subagents) — via holdIfChildrenActive, so a merely
// stale child doesn't hold the parent forever. Returns the (possibly
// overridden) newState/reason and whether the hold fired, so the caller can
// skip the same-pass collapsed-waiting synthesis: that path would otherwise
// reclassify from waiting and undo the hold.
func (d *SessionDetector) holdParentForActiveChildren(state *session.SessionState, ev agent.Event, newState, reason string) (string, string, bool) {
	if state.ParentSessionID != "" {
		return newState, reason, false
	}
	// The waiting case only counts as turn-done (question/cue), not a
	// permission prompt: IsAgentDone gates it, and an open tool call
	// (permission prompt) makes IsAgentDone return false.
	turnDoneWaiting := newState == session.StateWaiting && state.Metrics != nil && state.Metrics.IsAgentDone()
	if newState != session.StateReady && !turnDoneWaiting {
		return newState, reason, false
	}
	if !d.holdIfChildrenActive(state.SessionID) {
		return newState, reason, false
	}
	logMsg := "holding parent working — active children still running"
	if turnDoneWaiting {
		logMsg = "holding parent working — active children still running (turn ended in waiting cue)"
	}
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID, logMsg)
	return session.StateWorking, "", true
}

// collapsedWaitingCandidate carries the classifier's candidate next
// state/reason plus whether holdParentForActiveChildren already overrode it
// — keeping synthesizeCollapsedWaitingIfNeeded's parameter list small
// (go:S107) instead of threading each field through individually.
type collapsedWaitingCandidate struct {
	NewState          string
	Reason            string
	ParentHeldWorking bool
}

// synthesizeCollapsedWaitingIfNeeded emits a synthetic working→waiting
// transition when fswatcher coalesces a user-blocking tool_use
// (AskUserQuestion / ExitPlanMode) with its tool_result into a single pass —
// HasOpenToolCall is already false by the time the classifier runs, so the
// brief waiting episode would otherwise never be observed (issue #150). It
// then reclassifies from waiting so the caller's next transition carries the
// correct "while waiting" phrasing. Skipped when
// holdParentForActiveChildren already rewrote newState: that parent has
// active children and must stay working, and reclassifying from waiting
// would let rule 3 fire and transition it to ready despite children still
// running — undoing the hold.
func (d *SessionDetector) synthesizeCollapsedWaitingIfNeeded(state *session.SessionState, ev agent.Event, candidate collapsedWaitingCandidate) (string, string) {
	newState, reason := candidate.NewState, candidate.Reason
	if candidate.ParentHeldWorking || !ShouldSynthesizeCollapsedWaiting(state.State, newState, state.Metrics) {
		return newState, reason
	}
	d.log.LogInfo(logComponentSessionDetector, ev.SessionID, SyntheticWaitingReason)
	d.record(lifecycle.Event{
		Kind:      lifecycle.KindStateTransition,
		SessionID: ev.SessionID,
		PrevState: session.StateWorking,
		NewState:  session.StateWaiting,
		Reason:    SyntheticWaitingReason,
		Inputs:    classifierInputs(state.Metrics),
	})
	state.State = session.StateWaiting
	return ClassifyState(state.State, state.Metrics)
}

// stateTransitionUpdate carries a state transition already determined to
// differ from state.State, plus the timestamp to stamp it with — keeping
// applyStateTransition's parameter list small (go:S107) instead of
// threading each field through individually.
type stateTransitionUpdate struct {
	NewState string
	Reason   string
	Now      int64
}

// applyStateTransition records and applies a state transition already
// determined to differ from state.State, plus the per-target-state side
// effects: waiting stamps WaitingStartTime, working clears it, and ready
// captures the yield verdict for the revert-correlation sweep (#373).
func (d *SessionDetector) applyStateTransition(state *session.SessionState, ev agent.Event, update stateTransitionUpdate) {
	newState, reason, now := update.NewState, update.Reason, update.Now
	if reason != "" {
		d.log.LogInfo(logComponentSessionDetector, ev.SessionID, reason)
	}
	d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, PrevState: state.State, NewState: newState, Reason: reason, Inputs: classifierInputs(state.Metrics)})
	state.State = newState
	state.UpdatedAt = now

	switch newState {
	case session.StateWaiting:
		state.WaitingStartTime = &now
	case session.StateWorking:
		state.WaitingStartTime = nil
	case session.StateReady:
		// Stamp HEAD + yield verdict on the turn-done → ready edge so the
		// yield sweep can correlate reverts back to it (#373).
		d.enricher.CaptureYieldOnReady(state)
	}
}

// cleanupFinishedParent deletes all child sessions of a parent that just
// reached ready, then refreshes and re-persists/re-broadcasts the subagent
// summary so the turn's final parent message has the cleared badge AND the
// repo copy is clean — hook-path transitions re-broadcast the persisted
// summary as-is (#593). Gated on the summary actually changing so the common
// no-children case adds no push traffic.
func (d *SessionDetector) cleanupFinishedParent(state *session.SessionState) {
	prevSummary := state.Subagents
	d.pidMgr.cleanupChildren(state.SessionID)
	d.refreshSubagentSummary(state)
	if state.Subagents.Equal(prevSummary) {
		return
	}
	if err := d.repo.Save(state); err != nil {
		d.log.LogError(logComponentSessionDetector, state.SessionID,
			fmt.Sprintf("failed to persist cleared subagent summary: %v", err))
	}
	d.broadcast(outbound.PushTypeUpdated, state)
}

// applyBackgroundLiveness sets HasLiveBackgroundProcess on the session's
// metrics from the last-known liveness of its background processes — Claude
// Code's Bash run_in_background launches (probed via lsof on their output
// files) and Gemini CLI's backgrounded shell commands (probed by signalling
// their reported PID, issue #661) — and kicks off an off-loop refresh of that
// knowledge. When true, IsAgentDone returns false and the classifier holds the
// session `working` past end_turn until the process exits.
//
// Two deliberate choices (issue #445 review):
//   - Gated on state == working. The feature only ever needs to PREVENT a
//     working→ready transition; it must never RESURRECT a session the user
//     already cancelled (ESC → ready) just because a detached process is still
//     alive. Non-working sessions clear their cache and the flag.
//   - The probe runs in a goroutine, not inline, so a slow filesystem (lsof)
//     can't stall the single event-loop goroutine (and thus every other
//     session). processActivity uses the last-known value — optimistically
//     "alive" on first sight so a not-yet-probed process is never prematurely
//     declared dead — and a completed probe whose verdict changed nudges the
//     event loop (via debouncedEvents) to re-classify promptly.
func (d *SessionDetector) applyBackgroundLiveness(state *session.SessionState) {
	sid := state.SessionID
	m := state.Metrics

	if m == nil || state.State != session.StateWorking ||
		m.BackgroundProcessCount == 0 || !d.hasBackgroundProbe(m) {
		d.clearBackgroundTracking(sid, m)
		return
	}

	alive, startProbe := d.beginBackgroundProbe(sid)
	m.HasLiveBackgroundProcess = alive
	if !startProbe {
		return
	}

	outputs := append([]string(nil), m.BackgroundProcessOutputs...) // copy: goroutine must not alias state
	pids := append([]string(nil), m.BackgroundProcessPIDs...)
	go d.runBackgroundLivenessProbe(sid, state.TranscriptPath, outputs, pids)
}

// hasBackgroundProbe reports whether m has a background process AND the
// matching liveness probe to check it: output files (Claude Code, via lsof)
// or PIDs (Gemini CLI, via kill -0). A session with neither has no detached
// process to hold it `working`. Only called once m's non-nilness is
// established by the caller's short-circuiting guard.
func (d *SessionDetector) hasBackgroundProbe(m *session.SessionMetrics) bool {
	hasOutputProbe := len(m.BackgroundProcessOutputs) > 0 && d.bgLiveProbe != nil
	hasPIDProbe := len(m.BackgroundProcessPIDs) > 0 && d.bgPIDProbe != nil
	return hasOutputProbe || hasPIDProbe
}

// clearBackgroundTracking drops sid's liveness/in-flight-probe bookkeeping
// and clears its HasLiveBackgroundProcess flag — the session no longer has a
// background process worth tracking (it finished, left working, or lost its
// last probeable process).
func (d *SessionDetector) clearBackgroundTracking(sid string, m *session.SessionMetrics) {
	d.bgMu.Lock()
	delete(d.bgLive, sid)
	delete(d.bgProbing, sid)
	d.bgMu.Unlock()
	if m != nil {
		m.HasLiveBackgroundProcess = false
	}
}

// beginBackgroundProbe reads the last-known liveness verdict for sid
// (optimistically alive on first sight, issue #445, so a not-yet-probed
// process is never prematurely declared dead) and claims the per-session
// in-flight probe slot if none is already running.
func (d *SessionDetector) beginBackgroundProbe(sid string) (alive, startProbe bool) {
	d.bgMu.Lock()
	defer d.bgMu.Unlock()

	known, seen := d.bgLive[sid]
	alive = true
	if seen {
		alive = known
	}
	startProbe = !d.bgProbing[sid]
	if startProbe {
		d.bgProbing[sid] = true
	}
	return alive, startProbe
}

// runBackgroundLivenessProbe shells out (lsof / kill -0) off the event-loop
// goroutine to answer whether any of sid's background processes are still
// alive — a session can carry both a Claude-Code output-file process and a
// Gemini PID process, and stays held while EITHER is alive — purges any
// that probed dead, and nudges the event loop to re-classify when the
// verdict changed (issues #649, #661). Must not alias state: outputs/pids
// are copies taken by the caller before this runs on its own goroutine.
func (d *SessionDetector) runBackgroundLivenessProbe(sid, transcriptPath string, outputs, pids []string) {
	liveOut := len(outputs) > 0 && d.bgLiveProbe != nil && d.bgLiveProbe(outputs)
	livePID := len(pids) > 0 && d.bgPIDProbe != nil && d.bgPIDProbe(pids)
	live := liveOut || livePID

	d.purgeDeadBackgroundProcesses(backgroundProbeResult{
		TranscriptPath: transcriptPath,
		Outputs:        outputs,
		PIDs:           pids,
		LiveOut:        liveOut,
		LivePID:        livePID,
	})

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
	if had && prev == live {
		return
	}
	select {
	case d.debouncedEvents <- agent.Event{Type: agent.EventActivity, SessionID: sid, TranscriptPath: transcriptPath}:
	default:
	}
}

// backgroundProbeResult carries one runBackgroundLivenessProbe pass's probed
// outputs/PIDs and their liveness verdicts — keeping
// purgeDeadBackgroundProcesses's parameter list small (go:S107) instead of
// threading each field through individually.
type backgroundProbeResult struct {
	TranscriptPath string
	Outputs        []string
	PIDs           []string
	LiveOut        bool
	LivePID        bool
}

// purgeDeadBackgroundProcesses drops probed-dead outputs/PIDs from the
// tailer's open set and the metrics ledger. A dead process died without a
// transcript-observable termination (e.g. it exited with its parent shell),
// so the ledger entry would otherwise resurrect it as a phantom open process
// on every daemon restart, re-running this probe forever. Scoped to this
// probe's snapshot — a process spawned since must survive and be judged by
// its own probe. Outputs and PIDs are purged independently so a still-live
// one of either kind is kept. See issues #649, #661.
func (d *SessionDetector) purgeDeadBackgroundProcesses(result backgroundProbeResult) {
	if d.metrics == nil {
		return
	}
	transcriptPath, outputs, pids := result.TranscriptPath, result.Outputs, result.PIDs
	if !result.LiveOut && len(outputs) > 0 {
		d.metrics.PurgeDeadBackgroundProcs(transcriptPath, outputs)
	}
	if !result.LivePID && len(pids) > 0 {
		d.metrics.PurgeDeadBackgroundPIDs(transcriptPath, pids)
	}
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
// applyCompactHold maintains the PreCompact force-working hold (#657) for one
// session. While a manual /compact is in flight the transcript receives no
// writes, so this overlays CompactInProgress to keep the session in working
// (ClassifyState rule 0b) through that silent window.
//
// The hold clears on the first of:
//   - the manual compact_boundary landing (SawManualCompactBoundary): the
//     normal path — compaction finished, release working → ready (#656);
//   - compactHoldTimeout elapsing since the PreCompact hook fired: the safety
//     net for a /compact that was interrupted or errored with no boundary ever
//     written. Without it an orphaned hold would be re-armed on every
//     refreshStaleSessions tick and strand the session in working forever — the
//     very failure #656 fixed.
//
// now is injected for testability. Mirrors markStalledEditTool's shape.
func (d *SessionDetector) applyCompactHold(sessionID string, m *session.SessionMetrics, now int64) {
	if m == nil {
		return
	}
	d.permMu.Lock()
	defer d.permMu.Unlock()

	since, ok := d.compactPending[sessionID]
	if !ok {
		return
	}
	if m.SawManualCompactBoundary || now-since >= int64(compactHoldTimeout.Seconds()) {
		delete(d.compactPending, sessionID)
		return
	}
	m.CompactInProgress = true
}

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
