package services

import (
	"fmt"
	"strings"
	"time"

	"irrlicht/hook/domain/event"
	"irrlicht/hook/domain/metrics"
	"irrlicht/hook/domain/session"
	"irrlicht/hook/ports/outbound"
)

// EventProcessor handles the core business logic for processing hook events
type EventProcessor struct {
	sessionRepo       outbound.SessionRepository
	transcriptAnalyzer outbound.TranscriptAnalyzer
	logger            outbound.Logger
	gitService        outbound.GitService
	metricsCollector  outbound.MetricsCollector
	fileSystemService outbound.FileSystemService
	configService     outbound.ConfigurationService
}

// NewEventProcessor creates a new event processor with all required dependencies
func NewEventProcessor(
	sessionRepo outbound.SessionRepository,
	transcriptAnalyzer outbound.TranscriptAnalyzer,
	logger outbound.Logger,
	gitService outbound.GitService,
	metricsCollector outbound.MetricsCollector,
	fileSystemService outbound.FileSystemService,
	configService outbound.ConfigurationService,
) *EventProcessor {
	return &EventProcessor{
		sessionRepo:       sessionRepo,
		transcriptAnalyzer: transcriptAnalyzer,
		logger:            logger,
		gitService:        gitService,
		metricsCollector:  metricsCollector,
		fileSystemService: fileSystemService,
		configService:     configService,
	}
}

// ProcessEvent handles the complete event processing workflow
func (ep *EventProcessor) ProcessEvent(hookEvent *event.HookEvent) error {
	startTime := time.Now()
	
	// Log the event processing start
	ep.logger.LogInfo(hookEvent.HookEventName, hookEvent.SessionID, 
		fmt.Sprintf("Processing event: %s", hookEvent.HookEventName))

	// Check if processing is disabled
	if ep.configService.IsDisabled() {
		ep.logger.LogInfo(hookEvent.HookEventName, hookEvent.SessionID, "Processing disabled, skipping")
		return nil
	}

	// Load existing session state
	existingSession, err := ep.sessionRepo.GetSession(hookEvent.SessionID)
	if err != nil {
		// Check if this is a "not found" error - this is expected for new sessions
		if !ep.isNotFoundError(err) {
			ep.logger.LogError(hookEvent.HookEventName, hookEvent.SessionID, 
				fmt.Sprintf("Failed to load session: %v", err))
			return fmt.Errorf("failed to load session: %w", err)
		}
		// Session doesn't exist yet - this is fine, we'll create a new one
		existingSession = nil
	}

	// Handle session deletion requests
	if session.ShouldDeleteSession(hookEvent.HookEventName) {
		return ep.handleSessionDeletion(hookEvent, existingSession)
	}

	// Process the event to create/update session
	updatedSession, err := ep.processEventToSession(hookEvent, existingSession)
	if err != nil {
		ep.logger.LogError(hookEvent.HookEventName, hookEvent.SessionID,
			fmt.Sprintf("Failed to process event: %v", err))
		return fmt.Errorf("failed to process event: %w", err)
	}

	// Save the updated session
	if err := ep.sessionRepo.SaveSession(updatedSession); err != nil {
		ep.logger.LogError(hookEvent.HookEventName, hookEvent.SessionID,
			fmt.Sprintf("Failed to save session: %v", err))
		return fmt.Errorf("failed to save session: %w", err)
	}

	// Record metrics
	processingTime := time.Since(startTime)
	ep.metricsCollector.RecordEventProcessing(hookEvent.HookEventName, processingTime)

	ep.logger.LogInfo(hookEvent.HookEventName, hookEvent.SessionID,
		fmt.Sprintf("Successfully processed event in %dms", processingTime.Milliseconds()))

	return nil
}

// processEventToSession converts a hook event into a session update
func (ep *EventProcessor) processEventToSession(hookEvent *event.HookEvent, existingSession *session.Session) (*session.Session, error) {
	// Determine state transition using domain logic
	stateTransitioner := session.NewStateTransitioner()
	transition := stateTransitioner.DetermineStateTransition(
		hookEvent.HookEventName,
		hookEvent.Matcher,
		hookEvent.Source,
		existingSession,
		hookEvent.TranscriptPath,
	)

	// Log state transition
	oldState := "none"
	if existingSession != nil {
		oldState = string(existingSession.State)
	}
	ep.logger.LogInfo(hookEvent.HookEventName, hookEvent.SessionID,
		fmt.Sprintf("State transition: %s -> %s (reason: %s)", oldState, transition.NewState, transition.Reason))

	// Create or update session
	var sess *session.Session
	if existingSession != nil {
		sess = existingSession
		sess.Update(transition.NewState, transition.NewCompactionState, hookEvent.HookEventName, hookEvent.Matcher)
	} else {
		sess = session.NewSession(hookEvent.SessionID, transition.NewState)
		sess.CompactionState = transition.NewCompactionState
		sess.LastEvent = hookEvent.HookEventName
		sess.LastMatcher = hookEvent.Matcher
	}

	// Update session with event data
	ep.updateSessionWithEventData(sess, hookEvent)

	// Compute metrics if transcript is available
	if sess.TranscriptPath != "" {
		// Convert session metrics to domain metrics for analysis
		var existingMetrics *metrics.SessionMetrics
		if sess.Metrics != nil {
			existingMetrics = &metrics.SessionMetrics{
				ElapsedSeconds:       sess.Metrics.ElapsedSeconds,
				TotalTokens:          sess.Metrics.TotalTokens,
				ModelName:            sess.Metrics.ModelName,
				ContextUtilization:   sess.Metrics.ContextUtilization,
				PressureLevel:        sess.Metrics.PressureLevel,
			}
		}
		
		if newMetrics := ep.computeMetrics(sess.TranscriptPath, existingMetrics); newMetrics != nil {
			// Convert back to session metrics
			sess.Metrics = &session.Metrics{
				ElapsedSeconds:       newMetrics.ElapsedSeconds,
				TotalTokens:          newMetrics.TotalTokens,
				ModelName:            newMetrics.ModelName,
				ContextUtilization:   newMetrics.ContextUtilization,
				PressureLevel:        newMetrics.PressureLevel,
			}
		}
	}

	// Handle waiting state tracking
	ep.updateWaitingStateTracking(sess, transition.NewState, existingSession)

	return sess, nil
}

// updateSessionWithEventData updates session fields with data from the hook event
func (ep *EventProcessor) updateSessionWithEventData(sess *session.Session, hookEvent *event.HookEvent) {
	// Extract data from event (preferring direct fields over Data map)
	if hookEvent.Model != "" {
		sess.Model = hookEvent.Model
	} else if data := hookEvent.Data; data != nil {
		if model, ok := data["model"].(string); ok {
			sess.Model = model
		}
	}

	if hookEvent.CWD != "" {
		sess.CWD = hookEvent.CWD
	} else if data := hookEvent.Data; data != nil {
		if cwd, ok := data["cwd"].(string); ok {
			sess.CWD = cwd
		}
	}

	if hookEvent.TranscriptPath != "" {
		sess.TranscriptPath = hookEvent.TranscriptPath
	} else if data := hookEvent.Data; data != nil {
		if transcriptPath, ok := data["transcript_path"].(string); ok {
			sess.TranscriptPath = transcriptPath
		}
	}

	// Extract git information and project name
	if sess.CWD != "" {
		projectName := ep.extractProjectName(sess.CWD)
		gitBranch := ep.gitService.GetCurrentBranch(sess.CWD)
		sess.UpdateMetadata(sess.TranscriptPath, sess.CWD, sess.Model, gitBranch, projectName)
	}

	// Special handling for SessionStart after clear
	if hookEvent.HookEventName == "SessionStart" && hookEvent.Source == "clear" {
		sess.Model = "New Session"
	}
}

// computeMetrics computes session metrics using the transcript analyzer
func (ep *EventProcessor) computeMetrics(transcriptPath string, existingMetrics *metrics.SessionMetrics) *metrics.SessionMetrics {
	if transcriptPath == "" {
		return existingMetrics
	}

	newMetrics, err := ep.transcriptAnalyzer.ComputeSessionMetrics(transcriptPath, existingMetrics)
	if err != nil {
		// Transcript analysis failed - keep existing metrics
		return existingMetrics
	}

	return newMetrics
}

// updateWaitingStateTracking handles the transcript monitoring for waiting state recovery
func (ep *EventProcessor) updateWaitingStateTracking(sess *session.Session, newState session.State, existingSession *session.Session) {
	if newState == session.Waiting {
		// Store transcript size when entering waiting state
		if sess.TranscriptPath != "" {
			size := ep.fileSystemService.GetFileSize(sess.TranscriptPath)
			sess.StartWaiting(size)
		}
	} else if existingSession != nil && existingSession.State == session.Waiting && newState == session.Working {
		// Clear waiting state monitoring when transitioning away from waiting
		sess.StopWaiting()
	}
}

// handleSessionDeletion handles the deletion of session files
func (ep *EventProcessor) handleSessionDeletion(hookEvent *event.HookEvent, existingSession *session.Session) error {
	reason := hookEvent.Reason
	if reason == "" && hookEvent.Data != nil {
		if r, ok := hookEvent.Data["reason"].(string); ok {
			reason = r
		}
	}

	ep.logger.LogInfo(hookEvent.HookEventName, hookEvent.SessionID,
		fmt.Sprintf("Deleting session file (reason: %s)", reason))

	if err := ep.sessionRepo.DeleteSession(hookEvent.SessionID); err != nil {
		ep.logger.LogError(hookEvent.HookEventName, hookEvent.SessionID,
			fmt.Sprintf("Failed to delete session: %v", err))
		return fmt.Errorf("failed to delete session: %w", err)
	}

	ep.logger.LogInfo(hookEvent.HookEventName, hookEvent.SessionID, "Session deleted successfully")
	return nil
}

// extractProjectName extracts the project name from a working directory path
func (ep *EventProcessor) extractProjectName(cwd string) string {
	if cwd == "" {
		return ""
	}
	return ep.fileSystemService.ExtractProjectName(cwd)
}

// isNotFoundError checks if an error is a "not found" error
func (ep *EventProcessor) isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	
	errMsg := err.Error()
	return strings.Contains(errMsg, "session not found") ||
		   strings.Contains(errMsg, "no such file or directory") ||
		   strings.Contains(errMsg, "file does not exist")
}