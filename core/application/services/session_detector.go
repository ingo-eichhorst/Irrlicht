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

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// orphanTranscriptAge is the maximum age of a transcript file for it to be
// considered active. Files older than this during initial scan are treated as
// orphans left by exited processes and skipped.
const orphanTranscriptAge = 2 * time.Minute

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
) *SessionDetector {
	det := &SessionDetector{
		watchers:        watchers,
		repo:            repo,
		log:             log,
		broadcaster:     broadcaster,
		version:         version,
		enricher:        NewMetadataEnricher(git, metrics),
		projectSessions: make(map[string]string),
	}
	det.pidMgr = NewPIDManager(
		pw, repo, log, broadcaster, readyTTL,
		processlifecycle.DiscoverPID, det.removeFromProjectSessions,
	)
	return det
}

// WithCWDDiscovery sets an optional fallback PID discovery function that finds
// a process by matching its working directory. Called when lsof on the
// transcript file fails to find a PID.
func (d *SessionDetector) WithCWDDiscovery(fn func(string, func([]int) int) (int, error)) {
	d.pidMgr.SetCWDDiscovery(fn)
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
				return nil
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
			ParentSessionID: deriveParentSessionID(ev.TranscriptPath),
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

	// PID discovery with retry and CWD fallback (async).
	cwd := ev.CWD
	if !isNew {
		cwd = existing.CWD
	}
	go d.pidMgr.DiscoverPIDWithRetry(ev.SessionID, ev.TranscriptPath, cwd)
}

// onActivity handles transcript file writes. It uses content-based detection
// to determine whether the agent is working or waiting for user input.
func (d *SessionDetector) onActivity(ev agent.Event) {
	// Load session state.
	state, err := d.repo.Load(ev.SessionID)
	if err != nil || state == nil {
		// Session not tracked yet — treat as new.
		d.onNewSession(ev)
		return
	}

	// Retry PID discovery if not yet known (async to avoid blocking the
	// event loop on lsof I/O). Includes CWD fallback.
	if state.PID == 0 && ev.TranscriptPath != "" {
		sid, tp, cwd := ev.SessionID, ev.TranscriptPath, state.CWD
		go d.pidMgr.TryDiscoverPID(sid, tp, cwd)
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

	// When a parent session finishes, clean up all its child sessions.
	if state.State == session.StateReady && state.ParentSessionID == "" {
		d.pidMgr.cleanupChildren(state.SessionID)
	}
}

// onRemoved handles transcript file deletion or pre-session expiry.
func (d *SessionDetector) onRemoved(ev agent.Event) {
	d.log.LogInfo("session-detector", ev.SessionID, "session removed")

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

	// Re-evaluate state for all non-ready sessions: recompute metrics from
	// transcript and apply the current detection logic. This ensures sessions
	// persisted with stale states are corrected on startup.
	for _, state := range states {
		if state.State == session.StateReady || state.TranscriptPath == "" {
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

// removeFromProjectSessions removes a session from the projectSessions map.
// Used as a callback by PIDManager when sessions are deleted.
func (d *SessionDetector) removeFromProjectSessions(sessionID string) {
	d.mu.Lock()
	delete(d.projectSessions, sessionID)
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
