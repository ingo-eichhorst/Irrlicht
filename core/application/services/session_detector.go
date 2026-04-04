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
	"strings"
	"sync"
	"syscall"
	"time"

	processadapter "irrlicht/core/adapters/outbound/process"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
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
	version     string                   // daemon version stamped on new sessions
	readyTTL    time.Duration            // max idle time for ready sessions before deletion

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
	return &SessionDetector{
		watchers:        watchers,
		pw:              pw,
		repo:            repo,
		log:             log,
		git:             git,
		metrics:         metrics,
		broadcaster:     broadcaster,
		version:         version,
		readyTTL:        readyTTL,
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

	// Periodic liveness sweep: detect dead PIDs that kqueue missed.
	go d.sweepDeadPIDs(ctx)

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
		// All new sessions start as ready. Content-based detection on
		// subsequent activity events will transition to working/waiting.
		initialState := session.StateReady

		state := &session.SessionState{
			Version:         1,
			SessionID:       ev.SessionID,
			State:           initialState,
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

		// Resolve git metadata: prefer CWD (set by process scanner), fall
		// back to transcript inspection for file-based sessions.
		if ev.CWD != "" {
			state.CWD = ev.CWD
			state.GitBranch = d.git.GetBranch(ev.CWD)
			state.ProjectName = d.git.GetProjectName(ev.CWD)
		} else if ev.TranscriptPath != "" {
			if cwd := d.git.GetCWDFromTranscript(ev.TranscriptPath); cwd != "" {
				state.CWD = cwd
				state.GitBranch = d.git.GetBranch(cwd)
				state.ProjectName = d.git.GetProjectName(cwd)
			} else if b := d.git.GetBranchFromTranscript(ev.TranscriptPath); b != "" {
				state.GitBranch = b
			}
		}

		// Compute initial metrics (no-op for pre-sessions with no transcript).
		if m, _ := d.metrics.ComputeMetrics(ev.TranscriptPath); m != nil {
			state.Metrics = m
		}

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

	// Retry PID discovery if not yet known (async to avoid blocking the
	// event loop on lsof I/O).
	if state.PID == 0 && ev.TranscriptPath != "" {
		sid, tp := ev.SessionID, ev.TranscriptPath
		go d.discoverAndRegisterPID(sid, tp)
	}

	// Refresh CWD/branch/project from transcript. GetCWDFromTranscript returns
	// the LATEST cwd, which may change mid-session (e.g. worktree switch).
	if ev.TranscriptPath != "" {
		if cwd := d.git.GetCWDFromTranscript(ev.TranscriptPath); cwd != "" && cwd != state.CWD {
			state.CWD = cwd
			state.GitBranch = d.git.GetBranch(cwd)
			state.ProjectName = d.git.GetProjectName(cwd)
		}
	}

	// Refresh metrics (includes LastEventType for content-based detection).
	if m, _ := d.metrics.ComputeMetrics(ev.TranscriptPath); m != nil {
		state.Metrics = session.MergeMetrics(m, state.Metrics)
	}

	// Content-based state detection using three-way check:
	// - NeedsUserAttention: user-blocking tool open → waiting
	// - IsAgentDone: agent finished turn → ready
	// - User event while working: ESC cancellation → ready
	// - Otherwise: actively processing → working
	now := time.Now().Unix()
	if state.Metrics.NeedsUserAttention() {
		if state.State != session.StateWaiting {
			d.log.LogInfo("session-detector", ev.SessionID,
				"user-blocking tool open → waiting")
			state.State = session.StateWaiting
			state.UpdatedAt = now
			state.WaitingStartTime = &now
		}
	} else if state.Metrics.IsAgentDone() {
		if state.State == session.StateWorking || state.State == session.StateWaiting {
			d.log.LogInfo("session-detector", ev.SessionID,
				"agent finished turn → ready")
			state.State = session.StateReady
		}
	} else if (state.State == session.StateWorking || state.State == session.StateWaiting) && !state.Metrics.HasOpenToolCall && state.Metrics.LastEventType == "user" && state.Metrics.LastToolResultWasError {
		// ESC cancellation: a user event with is_error=true tool_result arrives
		// while working/waiting with no open tool calls. Normal tool completions
		// have is_error=false and don't match this check.
		d.log.LogInfo("session-detector", ev.SessionID,
			fmt.Sprintf("rejected tool result while %s → ready (cancellation)", state.State))
		state.State = session.StateReady
	} else {
		if state.State != session.StateWorking {
			d.log.LogInfo("session-detector", ev.SessionID,
				fmt.Sprintf("transcript activity (%s → working)", state.State))
			state.State = session.StateWorking
			state.LastTranscriptSize = 0
			state.WaitingStartTime = nil
		}
	}

	// Infer in-process sub-agent activity from open Agent tool calls.
	// Claude Code Explore/Plan agents run inside the parent process and
	// don't create separate transcripts, so this is the only detection path.
	if state.Metrics != nil && state.Metrics.HasOpenToolCall {
		agentCount := 0
		for _, name := range state.Metrics.LastOpenToolNames {
			if name == "Agent" {
				agentCount++
			}
		}
		if agentCount > 0 {
			state.Subagents = &session.SubagentSummary{
				Total:   agentCount,
				Working: agentCount,
			}
		}
	} else if state.Subagents != nil {
		state.Subagents = nil
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

	d.log.LogInfo("process-exit", sessionID,
		fmt.Sprintf("pid %d exited, deleting session (was %s)", pid, state.State))

	if err := d.repo.Delete(sessionID); err != nil {
		d.log.LogError("process-exit", sessionID,
			fmt.Sprintf("failed to delete session: %v", err))
		return
	}

	d.broadcast(outbound.PushTypeDeleted, state)
}

// sweepDeadPIDs periodically checks all sessions for dead processes and deletes
// them. This is a safety net for cases where kqueue misses an exit (PID not
// registered, daemon restart window, race conditions).
func (d *SessionDetector) sweepDeadPIDs(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkPIDLiveness()
		}
	}
}

func (d *SessionDetector) checkPIDLiveness() {
	states, err := d.repo.ListAll()
	if err != nil {
		return
	}
	for _, state := range states {
		if state.PID > 0 {
			if err := syscall.Kill(state.PID, 0); err == syscall.ESRCH {
				d.HandleProcessExit(state.PID, state.SessionID)
			}
		}
	}

	// Sweep stale sessions that can't be cleaned up via PID liveness:
	// - Ready sessions (idle beyond TTL)
	// - Working/waiting sessions with PID=0 (zombies where PID discovery
	//   failed and no kqueue/sweep cleanup path can fire)
	if d.readyTTL > 0 {
		for _, state := range states {
			if !state.IsStale(d.readyTTL) {
				continue
			}
			if state.State == session.StateReady || state.PID == 0 {
				d.log.LogInfo("session-detector", state.SessionID,
					fmt.Sprintf("%s session (pid=%d) idle for >%v, deleting",
						state.State, state.PID, d.readyTTL))
				_ = d.repo.Delete(state.SessionID)
				d.broadcast(outbound.PushTypeDeleted, state)
			}
		}
	}
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

	d.HandlePIDAssigned(pid, sessionID)
}

// HandlePIDAssigned records a newly-discovered PID for a session, registers it
// with the ProcessWatcher, and cleans up old sessions that shared the same PID.
// This handles the /clear scenario: the CLI process stays alive (same PID) but
// starts a new transcript, making the old session obsolete.
func (d *SessionDetector) HandlePIDAssigned(pid int, sessionID string) {
	if pid <= 0 {
		return
	}

	state, _ := d.repo.Load(sessionID)
	if state == nil || state.PID == pid {
		return
	}

	state.PID = pid
	state.UpdatedAt = time.Now().Unix()
	_ = d.repo.Save(state)

	// Register with ProcessWatcher for exit monitoring.
	if d.pw != nil {
		if err := d.pw.Watch(pid, sessionID); err != nil {
			d.log.LogError("session-detector", sessionID,
				fmt.Sprintf("failed to watch pid %d: %v", pid, err))
		}
	}

	// Clean up old sessions that had the same PID (e.g. /clear).
	// Subagent sessions share the parent's PID, so skip cleanup when
	// either side is a subagent.
	if state.ParentSessionID != "" {
		return
	}

	states, err := d.repo.ListAll()
	if err != nil {
		return
	}

	for _, old := range states {
		if old.SessionID == sessionID || old.PID != pid {
			continue
		}
		if old.ParentSessionID != "" || strings.HasPrefix(old.SessionID, "proc-") {
			continue
		}

		d.log.LogInfo("session-detector", old.SessionID,
			fmt.Sprintf("replaced by new session %s (same pid %d) — deleting", sessionID, pid))

		d.mu.Lock()
		delete(d.projectSessions, old.SessionID)
		d.mu.Unlock()

		_ = d.repo.Delete(old.SessionID)
		d.broadcast(outbound.PushTypeDeleted, old)
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

	// Re-evaluate state for all non-ready sessions: recompute metrics from
	// transcript and apply the current detection logic. This ensures sessions
	// persisted with stale states (e.g. from a previous logic version) are
	// corrected on startup.
	for _, state := range states {
		if state.State == session.StateReady {
			continue
		}
		if state.TranscriptPath == "" {
			continue
		}
		if m, _ := d.metrics.ComputeMetrics(state.TranscriptPath); m != nil {
			state.Metrics = session.MergeMetrics(m, state.Metrics)
		}
		changed := false
		if state.Metrics.NeedsUserAttention() {
			if state.State != session.StateWaiting {
				state.State = session.StateWaiting
				changed = true
			}
		} else if state.Metrics.IsAgentDone() {
			if state.State != session.StateReady {
				d.log.LogInfo("session-detector-seed", state.SessionID,
					fmt.Sprintf("re-evaluated %s → ready on startup", state.State))
				state.State = session.StateReady
				changed = true
			}
		} else if (state.State == session.StateWorking || state.State == session.StateWaiting) && state.Metrics != nil && !state.Metrics.HasOpenToolCall && state.Metrics.LastEventType == "user" && state.Metrics.LastToolResultWasError {
			d.log.LogInfo("session-detector-seed", state.SessionID,
				fmt.Sprintf("re-evaluated %s → ready on startup (user event, no open tools)", state.State))
			state.State = session.StateReady
			changed = true
		}
		if changed {
			_ = d.repo.Save(state)
		}
	}

	// Backfill ProjectName / CWD / GitBranch for sessions that were saved
	// before these fields were populated. Derive CWD from the transcript when
	// it is missing, then fill the remaining fields from CWD.
	for _, state := range states {
		if state.ProjectName != "" {
			continue
		}
		changed := false
		if state.CWD == "" && state.TranscriptPath != "" {
			if cwd := d.git.GetCWDFromTranscript(state.TranscriptPath); cwd != "" {
				state.CWD = cwd
				changed = true
			}
		}
		if state.CWD != "" {
			if state.ProjectName == "" {
				state.ProjectName = d.git.GetProjectName(state.CWD)
				changed = true
			}
			if state.GitBranch == "" {
				state.GitBranch = d.git.GetBranch(state.CWD)
				changed = true
			}
		}
		if changed {
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
	}

	// Check PID liveness and register alive PIDs with ProcessWatcher.
	// Dead processes are cleaned up synchronously (no async ESRCH race).
	for _, state := range states {
		if state.PID <= 0 {
			continue
		}
		if err := syscall.Kill(state.PID, 0); err == syscall.ESRCH {
			d.log.LogInfo("session-detector-seed", state.SessionID,
				fmt.Sprintf("pid %d dead, deleting session", state.PID))
			_ = d.repo.Delete(state.SessionID)
			d.broadcast(outbound.PushTypeDeleted, state)
			continue
		}
		if d.pw != nil {
			if err := d.pw.Watch(state.PID, state.SessionID); err != nil {
				d.log.LogError("session-detector-seed", state.SessionID,
					fmt.Sprintf("failed to watch existing pid %d: %v", state.PID, err))
			}
		}
	}
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

// deriveParentSessionID extracts a parent session ID from a subagent transcript path.
// Claude Code subagent transcripts live at .../<parent-session-id>/subagents/<agent-id>.jsonl.
// Returns "" if the path doesn't match the subagent pattern.
func deriveParentSessionID(transcriptPath string) string {
	dir := filepath.Dir(transcriptPath) // .../subagents
	if filepath.Base(dir) != "subagents" {
		return ""
	}
	return filepath.Base(filepath.Dir(dir)) // parent session ID
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
