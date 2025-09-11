package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"

	"irrlicht/hook/domain/event"
	"irrlicht/hook/infrastructure/container"
)

const (
	Version        = "1.0.0"
	MaxPayloadSize = 512 * 1024 // 512KB
)

func main() {
	// Initialize dependency injection container
	di, err := container.NewContainer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize application: %v\n", err)
		os.Exit(1)
	}
	defer di.Close()

	// Handle version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("irrlicht-hook version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// Check if processing is disabled
	if di.GetConfigService().IsDisabled() {
		di.GetLogger().LogInfo("", "", "Processing disabled, exiting")
		os.Exit(0)
	}

	// Read and validate input
	hookEvent, err := readAndParseEvent(os.Stdin)
	if err != nil {
		di.GetLogger().LogError("", "", fmt.Sprintf("Failed to read/parse event: %v", err))
		os.Exit(1)
	}

	// Process the event using the use case
	useCase := di.GetProcessHookEventUseCase()
	if err := useCase.Execute(hookEvent); err != nil {
		di.GetLogger().LogError(hookEvent.HookEventName, hookEvent.SessionID,
			fmt.Sprintf("Event processing failed: %v", err))
		os.Exit(1)
	}
}

// readAndParseEvent reads from stdin and parses the hook event
func readAndParseEvent(reader io.Reader) (*event.HookEvent, error) {
	// Read all input
	input, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}

	// Check payload size
	payloadSize := len(input)
	if payloadSize > MaxPayloadSize {
		return nil, fmt.Errorf("payload size %d exceeds maximum %d", payloadSize, MaxPayloadSize)
	}

	// Parse JSON
	var rawEvent map[string]interface{}
	if err := json.Unmarshal(input, &rawEvent); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Convert to domain event
	hookEvent := event.FromRawMap(rawEvent)
	if hookEvent == nil {
		return nil, fmt.Errorf("failed to convert raw event to domain event")
	}

	return hookEvent, nil
}

// Legacy types and functions for compatibility - these should be gradually removed
// as the codebase is fully migrated to hexagonal architecture

// HookEvent represents a Claude Code hook event (legacy)
type HookEvent struct {
	HookEventName string                 `json:"hook_event_name"`
	SessionID     string                 `json:"session_id"`
	Timestamp     string                 `json:"timestamp"`
	Matcher       string                 `json:"matcher,omitempty"`
	Reason        string                 `json:"reason,omitempty"`
	Data          map[string]interface{} `json:"data"`
	// Direct fields that Claude Code sends at top level
	TranscriptPath string `json:"transcript_path,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	Source         string `json:"source,omitempty"`
}

// SessionMetrics holds computed performance metrics from transcript analysis (legacy)
type SessionMetrics struct {
	ElapsedSeconds     int64   `json:"elapsed_seconds"`
	TotalTokens        int64   `json:"total_tokens"`
	ModelName          string  `json:"model_name"`
	ContextUtilization float64 `json:"context_utilization_percentage"`
	PressureLevel      string  `json:"pressure_level"`
}

// SessionState represents the current state of a session (legacy)
type SessionState struct {
	Version         int             `json:"version"`
	SessionID       string          `json:"session_id"`
	State           string          `json:"state"`
	CompactionState string          `json:"compaction_state,omitempty"`
	Model           string          `json:"model,omitempty"`
	CWD             string          `json:"cwd,omitempty"`
	TranscriptPath  string          `json:"transcript_path,omitempty"`
	GitBranch       string          `json:"git_branch,omitempty"`
	ProjectName     string          `json:"project_name,omitempty"`
	FirstSeen       int64           `json:"first_seen"`
	UpdatedAt       int64           `json:"updated_at"`
	Confidence      string          `json:"confidence"`
	EventCount      int             `json:"event_count"`
	LastEvent       string          `json:"last_event"`
	LastMatcher     string          `json:"last_matcher,omitempty"`
	Metrics         *SessionMetrics `json:"metrics,omitempty"`
	// Transcript monitoring for waiting state recovery
	LastTranscriptSize int64  `json:"last_transcript_size,omitempty"`
	WaitingStartTime   *int64 `json:"waiting_start_time,omitempty"`
}
