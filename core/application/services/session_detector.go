// SessionDetector orchestrates AgentWatchers + ProcessWatcher to detect
// and manage agent sessions from transcript file activity.
//
// It subscribes to one or more AgentWatcher event streams and delegates to
// three focused collaborators:
//   - StateClassifier: pure functions for state transition logic
//   - MetadataEnricher: git metadata resolution and metrics computation
//   - PIDManager: process lifecycle (discovery, exit, liveness sweeps)
package services

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// orphanTranscriptAge is the maximum age of a transcript file for it to be
// considered active. Files older than this during initial scan are treated as
// orphans left by exited processes and skipped.
const orphanTranscriptAge = 2 * time.Minute

// activityDebounceWindow is the debounce window for transcript activity
// events. The first event fires immediately; subsequent events within this
// window are coalesced into a single processing when the timer expires.
const activityDebounceWindow = 2 * time.Second

// staleWorkingRefreshInterval is how often the event loop checks for working
// sessions that haven't received a transcript activity event recently. When
// all file-system watcher events for a session are dropped (e.g. subscriber
// channel overflow during concurrent bursts), the tailer's lastOffset falls
// behind and the classifier never sees the pending tool call. Re-reading the
// transcript on this interval catches the missed events.
const staleWorkingRefreshInterval = 5 * time.Second

// subagentQuietWindow is how long a subagent's transcript must have been
// silent before finishOrphanedChildren will promote it to ready.
//
// The window has to survive the worst-case normal gap between transcript
// writes for an actively-running subagent. Background Task agents routinely
// sit with no writes for 5-15 seconds while waiting on API responses —
// session b27fdaef-6de4-403a-b277-790fe8d803bb showed a 9-second gap that
// falsely tripped a 2-second window and re-created the child session on
// the very next write. 30 seconds comfortably covers normal API latency
// while still being 4× faster than the 2-minute stale-transcript sweep,
// which is the fallback cleanup path for anything this function misses.
const subagentQuietWindow = 30 * time.Second

// debounceEntry holds debounce state for a single session.
type debounceEntry struct {
	timer   *time.Timer
	latest  agent.Event
	pending bool // true when timer is running with a coalesced event
}

// SessionDetector watches transcript files to detect sessions and orchestrate
// lifecycle management. It is a thin coordinator that delegates state
// classification, metadata enrichment, and PID management to focused
// collaborators.
type SessionDetector struct {
	watchers    []inbound.AgentWatcher
	repo        outbound.SessionRepository
	log         outbound.Logger
	broadcaster outbound.PushBroadcaster // optional
	version     string                   // daemon version stamped on new sessions

	enricher *MetadataEnricher
	pidMgr   *PIDManager

	// projectSessions tracks sessionID → projectDir for pre-session cleanup.
	mu              sync.Mutex
	projectSessions map[string]string // sessionID → projectDir

	// deletedSessions tracks session IDs that were explicitly deleted (process
	// exit, /clear cleanup) with their deletion timestamp. Prevents late-
	// arriving transcript activity from re-creating a session that was
	// intentionally removed. The timestamp enables --continue detection:
	// activity arriving well after deletion (>10s) indicates a genuine
	// --continue, not ghost events from a dying process.
	deletedSessions map[string]int64

	// debounce coalesces rapid transcript activity events per session.
	debounceMu sync.Mutex
	debounce   map[string]*debounceEntry

	// debouncedEvents receives coalesced events from debounce timer callbacks.
	// Timer callbacks send here instead of calling processActivity directly,
	// so all processActivity calls run in the single event-loop goroutine
	// and never overlap for the same session.
	debouncedEvents chan agent.Event

	// deletedCooldown is the minimum time after deletion before a session
	// can be re-created from transcript activity (e.g. --continue). Prevents
	// ghost sessions from late-arriving writes of a dying process.
	deletedCooldown time.Duration

	// recorder captures lifecycle events for offline replay (optional).
	recorder    outbound.EventRecorder
	recorderSeq int64

	// permMu guards permissionPending. The map tracks sessions with an active
	// PermissionRequest hook that hasn't been cleared by PostToolUse/
	// PostToolUseFailure. Written by HandlePermissionHook (HTTP handler
	// goroutine), read by processActivity (event-loop goroutine).
	permMu            sync.Mutex
	permissionPending map[string]bool // sessionID → true
}

// NewSessionDetector creates a SessionDetector with all required dependencies.
// pw and broadcaster may be nil (optional).
func NewSessionDetector(
	watchers []inbound.AgentWatcher,
	pw outbound.ProcessWatcher,
	repo outbound.SessionRepository,
	log outbound.Logger,
	git outbound.GitResolver,
	metrics outbound.MetricsCollector,
	broadcaster outbound.PushBroadcaster,
	version string,
	readyTTL time.Duration,
	pidDiscovers map[string]PIDDiscoverFunc,
) *SessionDetector {
	det := &SessionDetector{
		watchers:        watchers,
		repo:            repo,
		log:             log,
		broadcaster:     broadcaster,
		version:         version,
		enricher:        NewMetadataEnricher(git, metrics),
		projectSessions: make(map[string]string),
		deletedSessions: make(map[string]int64),
		debounce:        make(map[string]*debounceEntry),
		debouncedEvents: make(chan agent.Event, 64),
		deletedCooldown:   10 * time.Second,
		permissionPending: make(map[string]bool),
	}
	det.pidMgr = NewPIDManager(
		pw, repo, log, broadcaster, readyTTL,
		pidDiscovers, det.removeFromProjectSessions,
	)
	det.pidMgr.SetChildDeletedHandler(det.reevaluateParent)
	return det
}

// SetDeletedCooldown overrides the deleted-session cooldown.
// Intended for tests that need immediate re-creation.
func (d *SessionDetector) SetDeletedCooldown(dur time.Duration) {
	d.deletedCooldown = dur
}

// RunPIDLivenessSweepForTest runs one iteration of the liveness sweep
// synchronously. Intended for tests that need to exercise the sweep's
// child-cleanup path without waiting for the real 5-second ticker.
func (d *SessionDetector) RunPIDLivenessSweepForTest() {
	d.pidMgr.CheckPIDLiveness()
}

// SetRecorder enables lifecycle event recording. When set, the detector and
// its PIDManager will emit lifecycle events to the recorder for offline replay.
func (d *SessionDetector) SetRecorder(r outbound.EventRecorder) {
	d.recorder = r
	d.pidMgr.SetRecorder(r, &d.recorderSeq)
}

// record emits a lifecycle event if recording is enabled. It assigns a
// monotonic sequence number and fills in the timestamp if missing.
func (d *SessionDetector) record(ev lifecycle.Event) {
	if d.recorder == nil {
		return
	}
	ev.Seq = atomic.AddInt64(&d.recorderSeq, 1)
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	d.recorder.Record(ev)
}

// Run subscribes to all AgentWatcher event streams, fans them into a single
// channel, and processes events until ctx is cancelled. It blocks for the
// lifetime of the detector.
func (d *SessionDetector) Run(ctx context.Context) error {
	merged := make(chan agent.Event, 16)
	var wg sync.WaitGroup

	for _, w := range d.watchers {
		ch := w.Subscribe()
		wg.Add(1)
		go func(watcher inbound.AgentWatcher, ch <-chan agent.Event) {
			defer wg.Done()
			defer watcher.Unsubscribe(ch)
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					select {
					case merged <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}(w, ch)
	}

	// Close merged when all watcher goroutines exit.
	go func() {
		wg.Wait()
		close(merged)
	}()

	// Seed project sessions map from existing sessions on disk.
	d.seedFromDisk()

	// Periodic liveness sweep: detect dead PIDs that kqueue missed.
	go d.pidMgr.SweepDeadPIDs(ctx)

	d.log.LogInfo("session-detector", "", "started — listening for transcript events")

	// Periodic refresh catches missed fswatcher events. When the subscriber
	// channel overflows during concurrent bursts (multiple sessions + subagent
	// transcripts on the same watcher), events are silently dropped and the
	// tailer never sees the pending tool call. Re-reading the transcript on a
	// short interval recovers within seconds instead of stalling until the
	// next user action.
	refreshTicker := time.NewTicker(staleWorkingRefreshInterval)
	defer refreshTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-merged:
			if !ok {
				// Watcher goroutines exited (usually because ctx was cancelled).
				// Return the context error if set, nil otherwise.
				return ctx.Err()
			}
			d.handleTranscriptEvent(ev)
		case ev := <-d.debouncedEvents:
			// Coalesced events from debounce timers — process in the event
			// loop goroutine so processActivity never runs concurrently.
			d.processActivity(ev)
		case <-refreshTicker.C:
			d.refreshStaleSessions()
		}
	}
}

// handleTranscriptEvent dispatches a transcript event to the appropriate handler.
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
	}

	// Retry PID discovery if not yet known.
	if state.PID == 0 {
		go d.pidMgr.TryDiscoverPID(ev.SessionID, state.CWD, ev.TranscriptPath, state.Adapter)
	}

	// Refresh CWD/branch/project and metrics from transcript.
	d.enricher.RefreshOnActivity(state, ev.TranscriptPath)

	// Force ready→working when metrics show activity so ClassifyState can
	// properly detect the working→ready transition. Without this, sessions
	// that start as ready (initial state) and whose first activity event
	// already shows IsAgentDone()=true would stay ready with no transition
	// broadcast — the UI would never see the "agent finished" event.
	if state.State == session.StateReady && state.Metrics != nil && state.Metrics.LastEventType != "" {
		d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: ev.SessionID, PrevState: session.StateReady, NewState: session.StateWorking, Reason: "force ready→working on first activity"})
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

// onRemoved handles transcript file deletion or pre-session expiry.
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

// removeFromProjectSessions removes a session from the projectSessions map and
// marks it as deleted. Used as a callback by PIDManager when sessions are deleted.
func (d *SessionDetector) removeFromProjectSessions(sessionID string) {
	d.mu.Lock()
	delete(d.projectSessions, sessionID)
	d.deletedSessions[sessionID] = time.Now().Unix()
	d.mu.Unlock()
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

// refreshSubagentSummary recomputes state.Subagents from the adapter-reported
// in-process count (state.Metrics.OpenSubagents) merged with file-based
// children discovered via the repository. A nil repo lookup is treated as
// "no children", matching the domain helper's contract.
func (d *SessionDetector) refreshSubagentSummary(state *session.SessionState) {
	if state == nil {
		return
	}
	children, err := d.repo.ListAll()
	if err != nil {
		children = nil
	}
	state.Subagents = session.ComputeSubagentSummary(state, children)
}

// finishOrphanedChildren walks the child sessions of parentID and promotes
// each one to ready if it has no open tool calls. Called when the parent's
// own turn is done and we're about to hold the parent in working on
// behalf of the children.
//
// This handles a Claude Code quirk: in-process subagent transcripts
// (Explore/Plan tools under <parent>/subagents/<agent-id>.jsonl) never
// emit a proper end_turn event — their final assistant message is written
// with stop_reason: null, which the classifier correctly treats as
// streaming. Without this fast-forward the child stays in working until
// the 2-minute stale-transcript sweep catches it, and the parent is
// held working for that entire window.
//
// Safety: we only promote children whose metrics show no open tool calls.
// A child that is genuinely still running a tool keeps an entry in the
// FIFO and is left alone.
func (d *SessionDetector) finishOrphanedChildren(parentID string) {
	states, err := d.repo.ListAll()
	if err != nil {
		return
	}
	now := time.Now().Unix()
	for _, s := range states {
		if s.ParentSessionID != parentID {
			continue
		}
		if s.State != session.StateWorking && s.State != session.StateWaiting {
			continue
		}
		if s.Metrics == nil || s.Metrics.HasOpenToolCall {
			continue
		}
		// Safety: a child whose transcript has been written in the last
		// subagentQuietWindow is a background agent still mid-run — we
		// don't know whether the parent's "done" means "finished the
		// subagents" or "kicked off async background work". Leaving active
		// children alone avoids the bug where background agents are
		// promoted and deleted while still writing. If the stat fails
		// (missing file), skip to be conservative — the liveness sweep
		// will fall back to its 2-minute window for anything we miss here.
		info, err := os.Stat(s.TranscriptPath)
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) < subagentQuietWindow {
			continue
		}

		prev := s.State
		s.State = session.StateReady
		s.UpdatedAt = now
		s.WaitingStartTime = nil
		d.record(lifecycle.Event{
			Kind:       lifecycle.KindStateTransition,
			SessionID:  s.SessionID,
			PrevState:  prev,
			NewState:   session.StateReady,
			Reason:     "subagent orphaned (parent turn done, no open tools)",
		})
		if err := d.repo.Save(s); err != nil {
			d.log.LogError("session-detector", s.SessionID,
				fmt.Sprintf("failed to finish orphaned child: %v", err))
			continue
		}
		d.log.LogInfo("session-detector", s.SessionID,
			fmt.Sprintf("finished orphaned subagent (%s → ready) — parent %s turn done", prev, parentID))
		d.broadcast(outbound.PushTypeUpdated, s)
	}
}

// hasActiveChildren returns true if any child session of the given parent is
// still working or waiting. Used to prevent a parent from transitioning to
// ready while background/foreground subagents are still processing.
func (d *SessionDetector) hasActiveChildren(parentID string) bool {
	states, err := d.repo.ListAll()
	if err != nil {
		return false
	}
	for _, s := range states {
		if s.ParentSessionID == parentID &&
			(s.State == session.StateWorking || s.State == session.StateWaiting) {
			return true
		}
	}
	return false
}

// reevaluateParent checks whether a parent session should transition now that
// a child's state has changed. If the parent's own turn is done (IsAgentDone)
// and no more active children remain, the parent transitions to ready (or
// waiting if ending with a question).
func (d *SessionDetector) reevaluateParent(parentID string) {
	parent, err := d.repo.Load(parentID)
	if err != nil || parent == nil {
		return
	}
	// Only re-evaluate parents that are being held in working.
	if parent.State != session.StateWorking {
		return
	}
	// The parent's own turn must be done for it to transition.
	if parent.Metrics == nil || !parent.Metrics.IsAgentDone() {
		return
	}
	// Fast-forward orphaned subagents (finished work but no stop_reason)
	// — see finishOrphanedChildren doc for rationale.
	d.finishOrphanedChildren(parentID)

	// Still have active children — stay working.
	if d.hasActiveChildren(parentID) {
		return
	}

	// All children are done and the parent's turn is complete.
	newState, reason := ClassifyState(parent.State, parent.Metrics)
	if newState == parent.State {
		return
	}

	now := time.Now().Unix()
	if reason != "" {
		d.log.LogInfo("session-detector", parentID,
			fmt.Sprintf("children done, parent re-evaluated: %s", reason))
	}
	d.record(lifecycle.Event{Kind: lifecycle.KindStateTransition, SessionID: parentID, PrevState: parent.State, NewState: newState, Reason: reason})
	parent.State = newState
	parent.UpdatedAt = now
	if newState == session.StateWaiting {
		parent.WaitingStartTime = &now
	}

	if err := d.repo.Save(parent); err != nil {
		d.log.LogError("session-detector", parentID,
			fmt.Sprintf("failed to save parent re-evaluation: %v", err))
		return
	}
	d.broadcast(outbound.PushTypeUpdated, parent)

	// If the parent transitioned to ready, clean up its children.
	if parent.State == session.StateReady {
		d.pidMgr.cleanupChildren(parentID)
	}
}

// cleanupPreSessionsForProject removes all synthetic pre-sessions (proc-*)
// in the given project. It matches by projectDir first (Claude Code layout
// where the transcript path encodes the project directory), then falls back
// to CWD comparison for adapters whose transcript paths use different layouts
// (Codex stores by date, Pi uses double-dash encoding).
func (d *SessionDetector) cleanupPreSessionsForProject(projectDir, realCWD, adapter string) {
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
		d.record(lifecycle.Event{Kind: lifecycle.KindPreSessionRemoved, SessionID: sid, Adapter: adapterName})
		d.log.LogInfo("session-detector", sid,
			fmt.Sprintf("removed pre-session — real session arrived in %s", projectDir))
	}
}
