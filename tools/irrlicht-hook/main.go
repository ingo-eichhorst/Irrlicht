package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	MaxPayloadSize = 512 * 1024 // 512KB
	AppSupportDir  = "Library/Application Support/Irrlicht"
)

// HookEvent represents a Claude Code hook event
type HookEvent struct {
	HookEventName string                 `json:"hook_event_name"`
	SessionID     string                 `json:"session_id"`
	Timestamp     string                 `json:"timestamp"`
	Data          map[string]interface{} `json:"data"`
}

// SessionState represents the current state of a session
type SessionState struct {
	Version       int                    `json:"version"`
	SessionID     string                 `json:"session_id"`
	State         string                 `json:"state"`
	Model         string                 `json:"model,omitempty"`
	CWD           string                 `json:"cwd,omitempty"`
	TranscriptPath string                `json:"transcript_path,omitempty"`
	FirstSeen     int64                  `json:"first_seen"`
	UpdatedAt     int64                  `json:"updated_at"`
	Confidence    string                 `json:"confidence"`
}

func main() {
	// Check for kill switch via environment variable
	if os.Getenv("IRRLICHT_DISABLED") == "1" {
		logEvent("Kill switch activated via IRRLICHT_DISABLED=1, exiting")
		os.Exit(0)
	}

	// Check for kill switch in settings
	if isDisabledInSettings() {
		logEvent("Kill switch activated via settings, exiting")
		os.Exit(0)
	}

	// Read event from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		logError("Failed to read stdin: %v", err)
		os.Exit(1)
	}

	// Check payload size
	if len(input) > MaxPayloadSize {
		logError("Payload size %d exceeds maximum %d", len(input), MaxPayloadSize)
		os.Exit(1)
	}

	// Parse event
	var event HookEvent
	if err := json.Unmarshal(input, &event); err != nil {
		logError("Failed to parse JSON: %v", err)
		os.Exit(1)
	}

	// Validate and sanitize
	if err := validateEvent(&event); err != nil {
		logError("Event validation failed: %v", err)
		os.Exit(1)
	}

	// Process event
	if err := processEvent(&event); err != nil {
		logError("Failed to process event: %v", err)
		os.Exit(1)
	}

	logEvent("Successfully processed %s event for session %s", event.HookEventName, event.SessionID)
}

// isDisabledInSettings checks if Irrlicht is disabled in Claude settings
func isDisabledInSettings() bool {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false // Settings file doesn't exist or can't be read
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false // Invalid JSON
	}

	// Check hooks.irrlicht.disabled
	if hooks, ok := settings["hooks"].(map[string]interface{}); ok {
		if irrlicht, ok := hooks["irrlicht"].(map[string]interface{}); ok {
			if disabled, ok := irrlicht["disabled"].(bool); ok && disabled {
				return true
			}
		}
	}

	return false
}

// validateEvent validates and sanitizes the hook event
func validateEvent(event *HookEvent) error {
	// Validate required fields
	if event.HookEventName == "" {
		return fmt.Errorf("missing hook_event_name")
	}
	if event.SessionID == "" {
		return fmt.Errorf("missing session_id")
	}

	// Validate event type
	validEvents := []string{
		"SessionStart", "UserPromptSubmit", "Notification",
		"Stop", "SubagentStop", "SessionEnd",
	}
	
	valid := false
	for _, validEvent := range validEvents {
		if event.HookEventName == validEvent {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid event type: %s", event.HookEventName)
	}

	// Sanitize paths
	if data := event.Data; data != nil {
		if transcriptPath, ok := data["transcript_path"].(string); ok {
			if err := validatePath(transcriptPath); err != nil {
				return fmt.Errorf("invalid transcript_path: %w", err)
			}
		}
		if cwd, ok := data["cwd"].(string); ok {
			if err := validatePath(cwd); err != nil {
				return fmt.Errorf("invalid cwd: %w", err)
			}
		}
	}

	return nil
}

// validatePath checks if a path is safe (within user domain)
func validatePath(path string) error {
	// Check for suspicious patterns
	suspicious := []string{
		"/etc/", "/root/", "/var/", "/usr/", "/sys/", "/dev/", "/proc/",
		"../", "..\\", "C:\\", "\\\\", "//",
	}

	for _, pattern := range suspicious {
		if strings.Contains(path, pattern) {
			return fmt.Errorf("suspicious path pattern: %s", pattern)
		}
	}

	// Must be absolute path within user home
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory")
	}

	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute")
	}

	if !strings.HasPrefix(path, homeDir) {
		return fmt.Errorf("path must be within user home directory")
	}

	return nil
}

// processEvent processes the hook event and updates session state
func processEvent(event *HookEvent) error {
	// Map event to state
	state := mapEventToState(event.HookEventName)
	
	// Create session state
	now := time.Now().Unix()
	sessionState := SessionState{
		Version:    1,
		SessionID:  event.SessionID,
		State:      state,
		UpdatedAt:  now,
		Confidence: "high",
	}

	// Extract additional data
	if data := event.Data; data != nil {
		if model, ok := data["model"].(string); ok {
			sessionState.Model = model
		}
		if cwd, ok := data["cwd"].(string); ok {
			sessionState.CWD = cwd
		}
		if transcriptPath, ok := data["transcript_path"].(string); ok {
			sessionState.TranscriptPath = transcriptPath
		}
	}

	// Load existing state to preserve first_seen
	existingState, err := loadSessionState(event.SessionID)
	if err == nil && existingState.FirstSeen > 0 {
		sessionState.FirstSeen = existingState.FirstSeen
	} else {
		sessionState.FirstSeen = now
	}

	// Save session state
	return saveSessionState(&sessionState)
}

// mapEventToState maps hook events to session states
func mapEventToState(eventName string) string {
	switch eventName {
	case "SessionStart", "UserPromptSubmit":
		return "working"
	case "Notification":
		return "waiting"
	case "Stop", "SubagentStop", "SessionEnd":
		return "finished"
	default:
		return "working" // Default fallback
	}
}

// loadSessionState loads existing session state from disk
func loadSessionState(sessionID string) (*SessionState, error) {
	path := getSessionStatePath(sessionID)
	
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}

	return &state, nil
}

// saveSessionState atomically saves session state to disk
func saveSessionState(state *SessionState) error {
	// Ensure instances directory exists
	instancesDir := getInstancesDir()
	if err := os.MkdirAll(instancesDir, 0755); err != nil {
		return fmt.Errorf("failed to create instances directory: %w", err)
	}

	// Marshal state to JSON
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session state: %w", err)
	}

	// Atomic write: write to temp file then rename
	path := getSessionStatePath(state.SessionID)
	tempPath := path + ".tmp"

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath) // Clean up temp file
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// getInstancesDir returns the path to the instances directory
func getInstancesDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, AppSupportDir, "instances")
}

// getSessionStatePath returns the path to a session state file
func getSessionStatePath(sessionID string) string {
	return filepath.Join(getInstancesDir(), sessionID+".json")
}

// logEvent logs an event message
func logEvent(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	log.Printf("[irrlicht-hook] %s", message)
	
	// Also log to file if possible
	if logFile := getLogFile(); logFile != nil {
		fmt.Fprintf(logFile, "[%s] %s\n", time.Now().Format(time.RFC3339), message)
		logFile.Close()
	}
}

// logError logs an error message
func logError(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	log.Printf("[irrlicht-hook] ERROR: %s", message)
	
	// Also log to file if possible
	if logFile := getLogFile(); logFile != nil {
		fmt.Fprintf(logFile, "[%s] ERROR: %s\n", time.Now().Format(time.RFC3339), message)
		logFile.Close()
	}
}

// getLogFile opens the log file for appending
func getLogFile() *os.File {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	logsDir := filepath.Join(homeDir, AppSupportDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil
	}

	logPath := filepath.Join(logsDir, "events.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil
	}

	return file
}