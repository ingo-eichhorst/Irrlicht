// SessionDetector orchestrates AgentWatchers + ProcessWatcher to detect
// and manage agent sessions from transcript file activity.
//
// It subscribes to one or more AgentWatcher event streams and:
//   - On new session: creates session state, discovers PID via lsof,
//     registers with ProcessWatcher, derives parent_session_id
//   - On activity: refreshes metrics, uses content-based detection
//     (LastEventType) to determine working/waiting state
//   - On removed: cleans up session
package services

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	processadapter "irrlicht/core/adapters/outbound/process"
	"irrlicht/core/domain/session"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
	"irrlicht/core/ports/outbound"
)

// SessionDetector watches transcript files to detect sessions and orchestrate
// ProcessWatcher for lifecycle management. Working/waiting state is determined
// by content-based detection (LastEventType from transcript parsing).
type SessionDetector struct {
	watchers    []inbound.AgentWatcher
	pw          outbound.ProcessWatcher // optional
	repo        outbound.SessionRepository
	log         outbound.Logger
	git         outbound.GitResolver
	metrics     outbound.MetricsCollector
	broadcaster outbound.PushBroadcaster // optional

	// projectSessions tracks sessionID → projectDir for parent derivation.
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
) *SessionDetector {
	return &SessionDetector{
		watchers:        watchers,
		pw:              pw,
		repo:            repo,
		log:             log,
		git:             git,
		metrics:         metrics,
		broadcaster:     broadcaster,
		projectSessions: make(map[string]string),
	}
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
		// Create a new session state from transcript discovery.
		state := &session.SessionState{
			Version:        1,
			SessionID:      ev.SessionID,
			State:          session.StateWorking,
			Adapter:        ev.Adapter,
			TranscriptPath: ev.TranscriptPath,
			FirstSeen:      now,
			UpdatedAt:      now,
			Confidence:     "medium",
			EventCount:     1,
			LastEvent:      "transcript_new",
		}

		// Derive parent_session_id from co-located sessions.
		if parentID := d.deriveParentSessionID(ev.SessionID, ev.ProjectDir); parentID != "" {
			state.ParentSessionID = parentID
			d.log.LogInfo("session-detector", ev.SessionID,
				fmt.Sprintf("derived parent_session_id=%s from project dir %s", parentID, ev.ProjectDir))
		}

		// Resolve git metadata from transcript path.
		if b := d.git.GetBranchFromTranscript(ev.TranscriptPath); b != "" {
			state.GitBranch = b
		}

		// Compute initial metrics.
		if m, _ := d.metrics.ComputeMetrics(ev.TranscriptPath); m != nil {
			state.Metrics = m
		}

		if err := d.repo.Save(state); err != nil {
			d.log.LogError("session-detector", ev.SessionID,
				fmt.Sprintf("failed to save new session: %v", err))
			return
		}

		d.broadcast(outbound.PushTypeCreated, state)
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
		// Derive parent if not already set.
		if existing.ParentSessionID == "" {
			if parentID := d.deriveParentSessionID(ev.SessionID, ev.ProjectDir); parentID != "" {
				existing.ParentSessionID = parentID
				existing.UpdatedAt = now
				if err := d.repo.Save(existing); err != nil {
					d.log.LogError("session-detector", ev.SessionID,
						fmt.Sprintf("failed to update parent_session_id: %v", err))
				} else {
					d.log.LogInfo("session-detector", ev.SessionID,
						fmt.Sprintf("derived parent_session_id=%s from project dir %s", parentID, ev.ProjectDir))
				}
			}
		}
	}

	// One-time PID discovery via lsof.
	d.discoverAndRegisterPID(ev.SessionID, ev.TranscriptPath)
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

	// Refresh metrics (includes LastEventType for content-based detection).
	if m, _ := d.metrics.ComputeMetrics(ev.TranscriptPath); m != nil {
		state.Metrics = session.MergeMetrics(m, state.Metrics)
	}

	// Explicit ready → working transition (T8).
	if state.State == session.StateReady {
		d.log.LogInfo("session-detector", ev.SessionID,
			"transcript activity on ready session → working")
		state.State = session.StateWorking
		state.LastTranscriptSize = 0
		state.WaitingStartTime = nil
	}

	// Content-based state detection: the last event type in the transcript
	// tells us whose turn it is. An assistant message with no open tool
	// calls means Claude finished and is waiting for user input.
	if state.Metrics.IsWaitingForInput() {
		if state.State == session.StateWorking {
			d.log.LogInfo("session-detector", ev.SessionID,
				"last event is assistant, no open tool calls → waiting")
			now := time.Now().Unix()
			state.State = session.StateWaiting
			state.UpdatedAt = now
			state.WaitingStartTime = &now
		}
	} else {
		if state.State == session.StateWaiting {
			d.log.LogInfo("session-detector", ev.SessionID,
				"transcript activity while waiting → working")
			state.State = session.StateWorking
			state.LastTranscriptSize = 0
			state.WaitingStartTime = nil
		}
	}

	state.UpdatedAt = time.Now().Unix()
	state.EventCount++
	state.LastEvent = "transcript_activity"

	if err := d.repo.Save(state); err != nil {
		d.log.LogError("session-detector", ev.SessionID,
			fmt.Sprintf("failed to save activity update: %v", err))
		return
	}

	d.broadcast(outbound.PushTypeUpdated, state)

	d.updateParentSubagentSummary(ev.SessionID)
}

// onRemoved handles transcript file deletion.
func (d *SessionDetector) onRemoved(ev agent.Event) {
	d.log.LogInfo("session-detector", ev.SessionID, "transcript removed")

	// Remove from project tracking.
	d.mu.Lock()
	delete(d.projectSessions, ev.SessionID)
	d.mu.Unlock()

	// Transition to ready if the session still exists.
	state, err := d.repo.Load(ev.SessionID)
	if err != nil || state == nil {
		return
	}

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

	d.updateParentSubagentSummary(ev.SessionID)
}

// HandleProcessExit transitions a session to "ready" when its process exits.
func (d *SessionDetector) HandleProcessExit(pid int, sessionID string) {
	// Remove from project tracking.
	d.mu.Lock()
	delete(d.projectSessions, sessionID)
	d.mu.Unlock()

	state, err := d.repo.Load(sessionID)
	if err != nil || state == nil {
		d.log.LogInfo("process-exit", sessionID,
			fmt.Sprintf("pid %d exited but session not found (already cleaned up)", pid))
		return
	}

	// Already in a terminal state — nothing to do.
	if state.State == session.StateReady {
		return
	}

	d.log.LogInfo("process-exit", sessionID,
		fmt.Sprintf("pid %d exited, transitioning %s → ready", pid, state.State))

	state.State = session.StateReady
	state.UpdatedAt = time.Now().Unix()
	state.Confidence = "high"
	state.LastEvent = "process_exit"

	if err := d.repo.Save(state); err != nil {
		d.log.LogError("process-exit", sessionID,
			fmt.Sprintf("failed to save ready state: %v", err))
		return
	}

	d.broadcast(outbound.PushTypeUpdated, state)

	d.updateParentSubagentSummary(sessionID)
}

// deriveParentSessionID finds a likely parent session for a new session in the
// same project directory. A parent is an existing working session with an open
// tool call (typically the Agent tool that spawned the subagent).
func (d *SessionDetector) deriveParentSessionID(childID, projectDir string) string {
	d.mu.Lock()
	candidates := make([]string, 0, 4)
	for sid, pdir := range d.projectSessions {
		if pdir == projectDir && sid != childID {
			candidates = append(candidates, sid)
		}
	}
	d.mu.Unlock()

	if len(candidates) == 0 {
		return ""
	}

	// Look for a candidate that is working with an open tool call.
	for _, sid := range candidates {
		state, err := d.repo.Load(sid)
		if err != nil || state == nil {
			continue
		}
		if state.State == session.StateWorking &&
			state.Metrics != nil && state.Metrics.HasOpenToolCall {
			return sid
		}
	}

	// Fallback: if exactly one other working session exists in the same project
	// dir, assume it's the parent (common case for first subagent).
	var workingCandidate string
	workingCount := 0
	for _, sid := range candidates {
		state, err := d.repo.Load(sid)
		if err != nil || state == nil {
			continue
		}
		if state.State == session.StateWorking {
			workingCandidate = sid
			workingCount++
		}
	}
	if workingCount == 1 {
		return workingCandidate
	}

	return ""
}

// discoverAndRegisterPID uses lsof to find the PID that has a transcript file
// open, then registers it with the ProcessWatcher for exit monitoring.
func (d *SessionDetector) discoverAndRegisterPID(sessionID, transcriptPath string) {
	if d.pw == nil || transcriptPath == "" {
		return
	}

	// Check if session already has a PID.
	state, _ := d.repo.Load(sessionID)
	if state != nil && state.PID > 0 {
		return
	}

	pid, err := processadapter.DiscoverPID(transcriptPath)
	if err != nil || pid <= 0 {
		return
	}

	d.log.LogInfo("session-detector", sessionID,
		fmt.Sprintf("lsof discovered pid %d", pid))

	// Store PID in session state.
	if state != nil {
		state.PID = pid
		state.UpdatedAt = time.Now().Unix()
		_ = d.repo.Save(state)
	}

	// Register with ProcessWatcher.
	if err := d.pw.Watch(pid, sessionID); err != nil {
		d.log.LogError("session-detector", sessionID,
			fmt.Sprintf("failed to watch pid %d: %v", pid, err))
	}
}

// seedFromDisk populates the projectSessions map from existing sessions and
// registers PIDs of active sessions with the ProcessWatcher.
func (d *SessionDetector) seedFromDisk() {
	states, err := d.repo.ListAll()
	if err != nil {
		return
	}

	d.mu.Lock()
	for _, state := range states {
		if state.TranscriptPath != "" {
			// Extract project dir from transcript path.
			// Path format: ~/<agent-root>/<project-dir>/<session-id>.jsonl
			if pdir := extractProjectDir(state.TranscriptPath); pdir != "" {
				d.projectSessions[state.SessionID] = pdir
			}
		}
	}
	d.mu.Unlock()

	// Register known PIDs with ProcessWatcher. PID discovery via lsof for
	// sessions without PIDs is deferred to onNewSession/onActivity events
	// to avoid blocking the event loop at startup.
	if d.pw != nil {
		for _, state := range states {
			if state.State == session.StateReady {
				continue
			}
			if state.PID > 0 {
				if err := d.pw.Watch(state.PID, state.SessionID); err != nil {
					d.log.LogError("session-detector-seed", state.SessionID,
						fmt.Sprintf("failed to watch existing pid %d: %v", state.PID, err))
				}
			}
		}
	}
}

// updateParentSubagentSummary recomputes the SubagentSummary on the parent
// session when a child session changes state.
func (d *SessionDetector) updateParentSubagentSummary(childSessionID string) {
	child, _ := d.repo.Load(childSessionID)
	if child == nil || child.ParentSessionID == "" {
		return
	}

	parent, _ := d.repo.Load(child.ParentSessionID)
	if parent == nil {
		return
	}

	allSessions, err := d.repo.ListAll()
	if err != nil {
		return
	}

	summary := &session.SubagentSummary{}
	for _, s := range allSessions {
		if s.ParentSessionID == parent.SessionID {
			summary.Total++
			switch s.State {
			case session.StateWorking:
				summary.Working++
			case session.StateWaiting:
				summary.Waiting++
			case session.StateReady:
				summary.Ready++
			}
		}
	}

	parent.Subagents = summary
	parent.UpdatedAt = time.Now().Unix()
	if err := d.repo.Save(parent); err != nil {
		d.log.LogError("session-detector", child.ParentSessionID,
			fmt.Sprintf("failed to update subagent summary: %v", err))
		return
	}
	d.broadcast(outbound.PushTypeUpdated, parent)
}

// broadcast sends a push notification if a broadcaster is configured.
func (d *SessionDetector) broadcast(msgType string, state *session.SessionState) {
	if d.broadcaster != nil {
		d.broadcaster.Broadcast(outbound.PushMessage{Type: msgType, Session: state})
	}
}

// extractProjectDir extracts the project directory name from a transcript path.
// Expected format: .../<project-dir>/<session-id>.jsonl
func extractProjectDir(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	// filepath.Dir gives us the directory containing the file,
	// filepath.Base of that gives us the project directory name.
	dir := filepath.Dir(transcriptPath)
	if dir == "." || dir == "/" {
		return ""
	}
	return filepath.Base(dir)
}
