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
	"strings"
	"sync"
	"time"

	"irrlicht/core/domain/agent"
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

// defaultStaleToolTimeout is how long a non-user-blocking open tool call must
// remain unanswered before we assume it's permission-pending and transition to
// waiting. Empirically, 95% of auto-approved tools complete within 8 seconds.
const defaultStaleToolTimeout = 5 * time.Second

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
	// exit, /clear cleanup). Prevents late-arriving transcript activity from
	// re-creating a session that was intentionally removed.
	deletedSessions map[string]bool

	// debounce coalesces rapid transcript activity events per session.
	debounceMu sync.Mutex
	debounce   map[string]*debounceEntry

	// staleToolTimers tracks per-session timers for detecting permission-pending
	// tool calls. When a session is "working" with open non-blocking tool calls
	// and the permission mode requires approval, a timer starts. If no new
	// activity arrives before it fires, the session transitions to "waiting".
	staleToolTimeout time.Duration
	staleToolMu      sync.Mutex
	staleToolTimers  map[string]*time.Timer
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
	discoverPIDByCWD func(string, func([]int) int) (int, error),
) *SessionDetector {
	det := &SessionDetector{
		watchers:        watchers,
		repo:            repo,
		log:             log,
		broadcaster:     broadcaster,
		version:         version,
		enricher:        NewMetadataEnricher(git, metrics),
		projectSessions:  make(map[string]string),
		deletedSessions:  make(map[string]bool),
		debounce:         make(map[string]*debounceEntry),
		staleToolTimeout: defaultStaleToolTimeout,
		staleToolTimers:  make(map[string]*time.Timer),
	}
	det.pidMgr = NewPIDManager(
		pw, repo, log, broadcaster, readyTTL,
		discoverPIDByCWD, det.removeFromProjectSessions,
	)
	return det
}

// SetStaleToolTimeout overrides the default stale-tool-call timeout.
// Intended for tests that need a shorter duration.
func (d *SessionDetector) SetStaleToolTimeout(timeout time.Duration) {
	d.staleToolTimeout = timeout
}

// startStaleToolTimer starts a timer that will transition a session to "waiting"
// if no new transcript activity arrives within staleToolTimeout. Called when
// processActivity leaves the session in "working" with open non-blocking tool
// calls in a permission mode that requires user approval.
func (d *SessionDetector) startStaleToolTimer(sessionID string, expectedUpdatedAt int64) {
	d.staleToolMu.Lock()
	defer d.staleToolMu.Unlock()

	if t, ok := d.staleToolTimers[sessionID]; ok {
		t.Stop()
	}

	d.staleToolTimers[sessionID] = time.AfterFunc(d.staleToolTimeout, func() {
		d.onStaleToolTimeout(sessionID, expectedUpdatedAt)
	})
}

// cancelStaleToolTimer cancels any pending stale-tool timer for a session.
func (d *SessionDetector) cancelStaleToolTimer(sessionID string) {
	d.staleToolMu.Lock()
	defer d.staleToolMu.Unlock()
	if t, ok := d.staleToolTimers[sessionID]; ok {
		t.Stop()
		delete(d.staleToolTimers, sessionID)
	}
}

// onStaleToolTimeout fires when a session has had an open non-blocking tool call
// for staleToolTimeout without any new transcript activity. If the session state
// hasn't changed since the timer was set, transition it to "waiting".
func (d *SessionDetector) onStaleToolTimeout(sessionID string, expectedUpdatedAt int64) {
	d.staleToolMu.Lock()
	delete(d.staleToolTimers, sessionID)
	d.staleToolMu.Unlock()

	state, err := d.repo.Load(sessionID)
	if err != nil || state == nil {
		return
	}

	// Guard: only transition if the session is still in the exact state we
	// expect. If UpdatedAt changed, new activity arrived and processActivity
	// already re-evaluated.
	if state.State != session.StateWorking || state.UpdatedAt != expectedUpdatedAt {
		return
	}
	if state.Metrics == nil || !state.Metrics.HasOpenToolCall || state.Metrics.NeedsUserAttention() {
		return
	}

	d.log.LogInfo("session-detector", sessionID,
		"open tool call with no activity → waiting (likely permission-pending)")

	now := time.Now().Unix()
	state.State = session.StateWaiting
	state.UpdatedAt = now
	state.WaitingStartTime = &now
	state.LastEvent = "stale_tool_timeout"

	if err := d.repo.Save(state); err != nil {
		d.log.LogError("session-detector", sessionID,
			fmt.Sprintf("failed to save stale-tool transition: %v", err))
		return
	}

	d.broadcast(outbound.PushTypeUpdated, state)
}

// hasOnlyAgentTools returns true if all open tool names are "Agent".
// Agent tool calls are in-process subagents that legitimately run for minutes
// and should not trigger the stale-tool timer.
func hasOnlyAgentTools(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if n != "Agent" {
			return false
		}
	}
	return true
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
		}
	}
}

// handleTranscriptEvent dispatches a transcript event to the appropriate handler.
func (d *SessionDetector) handleTranscriptEvent(ev agent.Event) {
	switch ev.Type {
	case agent.EventNewSession:
		d.onNewSession(ev)
	case agent.EventActivity:
		d.onActivity(ev)
	case agent.EventRemoved:
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
		// or /clear cleanup). This prevents late-arriving file events from
		// re-creating a session that was intentionally removed.
		d.mu.Lock()
		deleted := d.deletedSessions[ev.SessionID]
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

		// Resolve git metadata and compute initial metrics.
		d.enricher.EnrichNewSession(state, ev)

		if err := d.repo.Save(state); err != nil {
			d.log.LogError("session-detector", ev.SessionID,
				fmt.Sprintf("failed to save new session: %v", err))
			return
		}

		d.broadcast(outbound.PushTypeCreated, state)

		// When a real transcript session arrives, remove any pre-sessions for
		// the same project directory (they are now superseded).
		if ev.TranscriptPath != "" {
			d.cleanupPreSessionsForProject(ev.ProjectDir)
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

	// PID discovery (async). Only for claude-code sessions — the CWD-based
	// discovery searches for "claude" processes and would assign the wrong
	// PID to Codex/Pi sessions that share the same directory.
	adapter := ev.Adapter
	if !isNew {
		adapter = existing.Adapter
	}
	if adapter == "claude-code" {
		cwd := ev.CWD
		if !isNew {
			cwd = existing.CWD
		}
		go d.pidMgr.DiscoverPIDWithRetry(ev.SessionID, cwd)
	}
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
				d.processActivity(coalesced)
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
}

// processActivity handles a (possibly debounced) transcript activity event.
// It uses content-based detection to determine whether the agent is working
// or waiting for user input.
func (d *SessionDetector) processActivity(ev agent.Event) {
	// Load session state.
	state, err := d.repo.Load(ev.SessionID)
	if err != nil || state == nil {
		// If the session was explicitly deleted (process exit, /clear cleanup),
		// don't re-create it from a late-arriving transcript write.
		d.mu.Lock()
		deleted := d.deletedSessions[ev.SessionID]
		d.mu.Unlock()
		if deleted {
			return
		}
		// Session not tracked yet — treat as new (startup race where activity
		// arrives before the initial scan).
		d.onNewSession(ev)
		return
	}

	// Cancel any pending stale-tool timer — new activity arrived.
	d.cancelStaleToolTimer(ev.SessionID)

	// Retry PID discovery if not yet known (claude-code only).
	if state.PID == 0 && state.CWD != "" && state.Adapter == "claude-code" {
		sid, cwd := ev.SessionID, state.CWD
		go d.pidMgr.TryDiscoverPID(sid, cwd)
	}

	// Refresh CWD/branch/project and metrics from transcript.
	d.enricher.RefreshOnActivity(state, ev.TranscriptPath)

	// Content-based state detection.
	now := time.Now().Unix()
	newState, reason := ClassifyState(state.State, state.Metrics)
	if newState != state.State {
		if reason != "" {
			d.log.LogInfo("session-detector", ev.SessionID, reason)
		}
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

	// Infer in-process sub-agent activity from open Agent tool calls.
	state.Subagents = InferSubagents(state.Metrics)

	state.UpdatedAt = time.Now().Unix()
	state.EventCount++
	state.LastEvent = "transcript_activity"

	if err := d.repo.Save(state); err != nil {
		d.log.LogError("session-detector", ev.SessionID,
			fmt.Sprintf("failed to save activity update: %v", err))
		return
	}

	d.broadcast(outbound.PushTypeUpdated, state)

	// If the session is still "working" with open non-blocking tool calls
	// in a permission mode that requires approval, start a timer to detect
	// permission-pending state.
	if state.State == session.StateWorking &&
		state.Metrics != nil &&
		state.Metrics.HasOpenToolCall &&
		!state.Metrics.NeedsUserAttention() &&
		!hasOnlyAgentTools(state.Metrics.LastOpenToolNames) &&
		state.Metrics.PermissionMode != "bypassPermissions" {
		d.startStaleToolTimer(ev.SessionID, state.UpdatedAt)
	}

	// When a parent session finishes, clean up all its child sessions.
	if state.State == session.StateReady && state.ParentSessionID == "" {
		d.pidMgr.cleanupChildren(state.SessionID)
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

	// Cancel any pending stale-tool timer for this session.
	d.cancelStaleToolTimer(ev.SessionID)

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

	state.State = session.StateReady
	state.UpdatedAt = time.Now().Unix()
	state.Confidence = "high"
	state.LastEvent = "transcript_removed"

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
	// last assistant message ends with a question should be waiting).
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
			_ = d.repo.Save(state)
		}

		// Start stale-tool timer for sessions that are working with open
		// non-blocking tool calls — they may be permission-pending from a
		// previous daemon run.
		if state.State == session.StateWorking &&
			state.Metrics != nil &&
			state.Metrics.HasOpenToolCall &&
			!state.Metrics.NeedsUserAttention() &&
			!hasOnlyAgentTools(state.Metrics.LastOpenToolNames) &&
			state.Metrics.PermissionMode != "bypassPermissions" {
			d.startStaleToolTimer(state.SessionID, state.UpdatedAt)
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
}

// removeFromProjectSessions removes a session from the projectSessions map and
// marks it as deleted. Used as a callback by PIDManager when sessions are deleted.
func (d *SessionDetector) removeFromProjectSessions(sessionID string) {
	d.mu.Lock()
	delete(d.projectSessions, sessionID)
	d.deletedSessions[sessionID] = true
	d.mu.Unlock()
}

// broadcast sends a push notification if a broadcaster is configured.
func (d *SessionDetector) broadcast(msgType string, state *session.SessionState) {
	if d.broadcaster != nil {
		d.broadcaster.Broadcast(outbound.PushMessage{Type: msgType, Session: state})
	}
}

// cleanupPreSessionsForProject removes all synthetic pre-sessions (proc-*)
// in the given project directory. Called when a real transcript session arrives
// so the pre-session doesn't linger alongside the real one.
func (d *SessionDetector) cleanupPreSessionsForProject(projectDir string) {
	d.mu.Lock()
	var ids []string
	for sid, pdir := range d.projectSessions {
		if pdir == projectDir && strings.HasPrefix(sid, "proc-") {
			ids = append(ids, sid)
			delete(d.projectSessions, sid)
		}
	}
	d.mu.Unlock()

	for _, sid := range ids {
		state, _ := d.repo.Load(sid)
		_ = d.repo.Delete(sid)
		if state != nil {
			d.broadcast(outbound.PushTypeDeleted, state)
		}
		d.log.LogInfo("session-detector", sid,
			fmt.Sprintf("removed pre-session — real session arrived in %s", projectDir))
	}
}
