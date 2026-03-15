package services

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"irrlicht/core/domain/event"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// approvalProneTools lists tool names that commonly require user approval.
// PreToolUse events for these tools trigger speculative waiting.
var approvalProneTools = map[string]bool{
	"Bash":      true,
	"Write":     true,
	"Edit":      true,
	"MultiEdit": true,
}

// EventService orchestrates hook event processing.
// It implements ports/inbound.EventHandler.
type EventService struct {
	repo      outbound.SessionRepository
	log       outbound.Logger
	git       outbound.GitResolver
	metrics   outbound.MetricsCollector
	pathValid outbound.PathValidator

	// broadcaster is optional; when set, Broadcast is called after every state save.
	broadcaster outbound.PushBroadcaster

	// SpeculativeWaitDelay is how long the background timer waits before
	// speculatively transitioning to "waiting". Exported for test overrides.
	SpeculativeWaitDelay time.Duration

	// pendingTimers maps sessionID -> *time.Timer for in-process speculative waits.
	pendingTimers sync.Map
}

// NewEventService creates an EventService with all dependencies wired.
func NewEventService(
	repo outbound.SessionRepository,
	log outbound.Logger,
	git outbound.GitResolver,
	metrics outbound.MetricsCollector,
	pathValid outbound.PathValidator,
) *EventService {
	return &EventService{
		repo:                 repo,
		log:                  log,
		git:                  git,
		metrics:              metrics,
		pathValid:            pathValid,
		SpeculativeWaitDelay: 2 * time.Second,
	}
}

// SetBroadcaster wires in an optional broadcaster for push notifications.
func (s *EventService) SetBroadcaster(b outbound.PushBroadcaster) {
	s.broadcaster = b
}

// HandleEvent validates and processes a hook event, updating session state on disk.
func (s *EventService) HandleEvent(evt *event.HookEvent) error {
	// Cancel any pending speculative-wait timer for this session.
	if v, loaded := s.pendingTimers.LoadAndDelete(evt.SessionID); loaded {
		v.(*time.Timer).Stop()
	}

	// Validate event
	if err := evt.Validate(s.pathValid.Validate); err != nil {
		return fmt.Errorf("event validation failed: %w", err)
	}

	s.log.LogInfo(evt.HookEventName, evt.SessionID,
		fmt.Sprintf("Event matcher=%s source=%s", evt.Matcher, evt.Source))

	existing, _ := s.repo.Load(evt.SessionID)
	isNewSession := existing == nil

	// Determine whether the transcript has grown since we entered waiting state.
	transcriptActivity := s.detectTranscriptActivity(existing)
	if transcriptActivity {
		s.log.LogInfo(evt.HookEventName, evt.SessionID, "Transcript activity detected")
	}

	// Compute new state via pure domain function.
	result := session.SmartStateTransition(
		evt.HookEventName,
		evt.Matcher,
		evt.Source,
		evt.ResolveReason(),
		existing,
		transcriptActivity,
	)

	// Handle session deletion.
	if result.NewState == session.StateDeleteSession {
		s.log.LogInfo(evt.HookEventName, evt.SessionID,
			fmt.Sprintf("Deleting session file (reason: %s)", result.Reason))
		if err := s.repo.Delete(evt.SessionID); err != nil {
			return fmt.Errorf("failed to delete session file: %w", err)
		}
		s.log.LogInfo(evt.HookEventName, evt.SessionID, "Session file deleted")
		if s.broadcaster != nil {
			tombstone := &session.SessionState{SessionID: evt.SessionID, State: session.StateDeleteSession}
			s.broadcaster.Broadcast(outbound.PushMessage{Type: outbound.PushTypeDeleted, Session: tombstone})
		}
		return nil
	}

	prevStateStr := "none"
	if existing != nil {
		prevStateStr = existing.StringState()
	}
	s.log.LogInfo(evt.HookEventName, evt.SessionID,
		fmt.Sprintf("State transition: %s -> %s (compaction: %s, reason: %s, matcher: %s)",
			prevStateStr, result.NewState, result.NewCompactionState, result.Reason, evt.Matcher))

	now := time.Now().Unix()
	state := &session.SessionState{
		Version:         1,
		SessionID:       evt.SessionID,
		State:           result.NewState,
		CompactionState: result.NewCompactionState,
		UpdatedAt:       now,
		Confidence:      "high",
		LastEvent:       evt.HookEventName,
		LastMatcher:     evt.Matcher,
	}

	// Extract model, CWD, transcript path — prefer direct fields, fall back to Data map.
	s.populateFromEvent(state, evt)

	// Resolve git metadata.
	if state.CWD != "" {
		state.ProjectName = s.git.GetProjectName(state.CWD)
		state.GitBranch = s.git.GetBranch(state.CWD)
	}
	if state.TranscriptPath != "" {
		if b := s.git.GetBranchFromTranscript(state.TranscriptPath); b != "" {
			state.GitBranch = b
		}
	}

	// Compute metrics.
	if state.TranscriptPath != "" {
		if m, _ := s.metrics.ComputeMetrics(state.TranscriptPath); m != nil {
			state.Metrics = m
		}
	}

	// Capture PID on SessionStart.
	if evt.HookEventName == "SessionStart" {
		state.PID = os.Getppid()
		if evt.Source == "clear" || existing == nil {
			state.Model = "New Session"
		}
	}

	// Carry forward fields from existing state.
	if existing != nil && existing.FirstSeen > 0 {
		state.FirstSeen = existing.FirstSeen
		state.EventCount = existing.EventCount + 1
		s.inheritFromExisting(state, existing)
	} else {
		state.FirstSeen = now
		state.EventCount = 1
	}

	// Handle waiting-state transcript monitoring.
	s.updateWaitingMonitoring(state, existing, result.NewState, now)

	// Schedule speculative wait for approval-prone tools.
	if evt.HookEventName == "PreToolUse" && approvalProneTools[evt.ToolName] {
		s.scheduleSpeculativeWait(evt.SessionID)
	}

	if err := s.repo.Save(state); err != nil {
		return err
	}
	if s.broadcaster != nil {
		msgType := outbound.PushTypeUpdated
		if isNewSession {
			msgType = outbound.PushTypeCreated
		}
		s.broadcaster.Broadcast(outbound.PushMessage{Type: msgType, Session: state})
	}
	return nil
}

// RunSpeculativeWait sleeps SpeculativeWaitDelay then attempts to transition
// the session to "waiting" if still in working state after a PreToolUse.
// Used by the irrlicht-hook --speculative-wait subprocess mode.
func (s *EventService) RunSpeculativeWait(sessionID string) {
	time.Sleep(s.SpeculativeWaitDelay)
	s.applySpeculativeWait(sessionID)
}

// applySpeculativeWait performs the speculative-wait state transition without
// any additional sleep. Called by both RunSpeculativeWait and the in-process timer.
func (s *EventService) applySpeculativeWait(sessionID string) {
	state, err := s.repo.Load(sessionID)
	if err != nil || state == nil {
		return
	}
	if state.LastEvent != "PreToolUse" || state.State != session.StateWorking {
		return
	}

	now := time.Now().Unix()
	state.State = session.StateWaiting
	state.UpdatedAt = now
	state.WaitingStartTime = &now

	if state.TranscriptPath != "" {
		if stat, err := os.Stat(state.TranscriptPath); err == nil {
			state.LastTranscriptSize = stat.Size()
		}
	}
	if err := s.repo.Save(state); err != nil {
		s.log.LogError("speculative-wait", sessionID,
			fmt.Sprintf("failed to save speculative state: %v", err))
		return
	}
	if s.broadcaster != nil {
		s.broadcaster.Broadcast(outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: state})
	}
	s.log.LogInfo("speculative-wait", sessionID,
		"Speculatively transitioned to waiting (PreToolUse pending approval)")
}

// CleanupOrphanedSessions scans all session files and removes those whose
// Claude Code process has exited. Called opportunistically on each invocation.
func (s *EventService) CleanupOrphanedSessions() {
	const orphanTTL = int64(3600) // 1 hour

	states, err := s.repo.ListAll()
	if err != nil {
		return
	}

	now := time.Now().Unix()
	for _, state := range states {
		if state.State == session.StateCancelledByUser {
			continue
		}
		var shouldDelete bool
		var reason string

		if state.PID > 0 {
			if !isProcessAlive(state.PID) {
				shouldDelete = true
				reason = fmt.Sprintf("process pid=%d exited", state.PID)
			}
		} else {
			if state.State == session.StateWorking || state.State == session.StateWaiting {
				age := now - state.UpdatedAt
				if age > orphanTTL {
					shouldDelete = true
					reason = fmt.Sprintf("no pid, %s session stale for %ds (TTL=%ds)",
						state.State, age, orphanTTL)
				}
			}
		}

		if shouldDelete {
			if err := s.repo.Delete(state.SessionID); err != nil {
				s.log.LogError("cleanup", state.SessionID,
					fmt.Sprintf("failed to remove orphaned session: %v", err))
			} else {
				s.log.LogInfo("cleanup", state.SessionID,
					fmt.Sprintf("removed orphaned session: %s", reason))
			}
		}
	}
}

// --- internal helpers ---------------------------------------------------------

func (s *EventService) populateFromEvent(state *session.SessionState, evt *event.HookEvent) {
	// Legacy Data map
	if evt.Data != nil {
		if v, ok := evt.Data["model"].(string); ok {
			state.Model = v
		}
		if v, ok := evt.Data["cwd"].(string); ok {
			state.CWD = v
		}
		if v, ok := evt.Data["transcript_path"].(string); ok {
			state.TranscriptPath = v
		}
		if v, ok := evt.Data["parent_session_id"].(string); ok {
			state.ParentSessionID = v
		}
	}
	// Direct fields override
	if evt.Model != "" {
		state.Model = evt.Model
	}
	if evt.CWD != "" {
		state.CWD = evt.CWD
	}
	if evt.TranscriptPath != "" {
		state.TranscriptPath = evt.TranscriptPath
	}
	if evt.ParentSessionID != "" {
		state.ParentSessionID = evt.ParentSessionID
	}
}

func (s *EventService) inheritFromExisting(state *session.SessionState, existing *session.SessionState) {
	if state.Model == "" && existing.Model != "" {
		state.Model = existing.Model
	}
	if state.CWD == "" && existing.CWD != "" {
		state.CWD = existing.CWD
	}
	if state.GitBranch == "" && existing.GitBranch != "" {
		state.GitBranch = existing.GitBranch
	}
	if state.ProjectName == "" && existing.ProjectName != "" {
		state.ProjectName = existing.ProjectName
	}
	// Re-extract git metadata if CWD is available but fields are missing.
	if state.CWD != "" {
		if state.GitBranch == "" {
			state.GitBranch = s.git.GetBranch(state.CWD)
		}
		if state.ProjectName == "" {
			state.ProjectName = s.git.GetProjectName(state.CWD)
		}
	}
	if state.PID == 0 && existing.PID > 0 {
		state.PID = existing.PID
	}
	if state.ParentSessionID == "" && existing.ParentSessionID != "" {
		state.ParentSessionID = existing.ParentSessionID
	}
	if state.TranscriptPath == "" && existing.TranscriptPath != "" {
		state.TranscriptPath = existing.TranscriptPath
		if state.Metrics == nil {
			if m, _ := s.metrics.ComputeMetrics(state.TranscriptPath); m != nil {
				state.Metrics = m
			}
		}
	}
	state.Metrics = session.MergeMetrics(state.Metrics, existing.Metrics)
	// Preserve transcript monitoring fields when state is unchanged.
	if existing.State == state.State {
		state.LastTranscriptSize = existing.LastTranscriptSize
		state.WaitingStartTime = existing.WaitingStartTime
	}
}

func (s *EventService) updateWaitingMonitoring(
	state *session.SessionState,
	existing *session.SessionState,
	newState string,
	now int64,
) {
	if newState == session.StateWaiting {
		if state.TranscriptPath != "" {
			if stat, err := os.Stat(state.TranscriptPath); err == nil {
				state.LastTranscriptSize = stat.Size()
				wt := now
				state.WaitingStartTime = &wt
			}
		}
	} else if existing != nil && existing.State == session.StateWaiting && newState == session.StateWorking {
		state.LastTranscriptSize = 0
		state.WaitingStartTime = nil
	}
}

func (s *EventService) detectTranscriptActivity(existing *session.SessionState) bool {
	if existing == nil || existing.State != session.StateWaiting ||
		existing.TranscriptPath == "" || existing.WaitingStartTime == nil {
		return false
	}
	stat, err := os.Stat(existing.TranscriptPath)
	if err != nil {
		return false
	}
	return stat.Size() > existing.LastTranscriptSize
}

// scheduleSpeculativeWait starts an in-process timer that transitions the session
// to "waiting" if no new event arrives within SpeculativeWaitDelay.
// Any previously pending timer for the same session is cancelled.
func (s *EventService) scheduleSpeculativeWait(sessionID string) {
	// Cancel existing timer for this session, if any.
	if v, loaded := s.pendingTimers.LoadAndDelete(sessionID); loaded {
		v.(*time.Timer).Stop()
	}

	t := time.AfterFunc(s.SpeculativeWaitDelay, func() {
		s.pendingTimers.Delete(sessionID)
		s.applySpeculativeWait(sessionID)
	})
	s.pendingTimers.Store(sessionID, t)
	s.log.LogInfo("PreToolUse", sessionID,
		fmt.Sprintf("Scheduled speculative wait in %v for approval-prone tool", s.SpeculativeWaitDelay))
}

// isProcessAlive checks whether the process with the given PID is still running.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
