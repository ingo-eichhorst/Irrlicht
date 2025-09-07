package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	
	"transcript-tailer/pkg/tailer"
)

const (
	MaxPayloadSize = 512 * 1024 // 512KB
	AppSupportDir  = "Library/Application Support/Irrlicht"
	Version        = "1.0.0"
	MaxLogSize     = 10 * 1024 * 1024 // 10MB
	MaxLogFiles    = 5
)

// Metrics tracks performance and resource usage
type Metrics struct {
	mu             sync.Mutex
	eventsProcessed int64
	totalLatencyMs  int64
	lastEventTime   time.Time
}

var (
	metrics = &Metrics{}
	logger  *StructuredLogger
)

// HookEvent represents a Claude Code hook event
type HookEvent struct {
	HookEventName   string                 `json:"hook_event_name"`
	SessionID       string                 `json:"session_id"`
	Timestamp       string                 `json:"timestamp"`
	Data            map[string]interface{} `json:"data"`
	// Direct fields that Claude Code sends at top level
	TranscriptPath  string                 `json:"transcript_path,omitempty"`
	CWD             string                 `json:"cwd,omitempty"`
	Model           string                 `json:"model,omitempty"`
	PermissionMode  string                 `json:"permission_mode,omitempty"`
	Prompt          string                 `json:"prompt,omitempty"`
}

// SessionMetrics holds computed performance metrics from transcript analysis  
type SessionMetrics struct {
	MessagesPerMinute    float64 `json:"messages_per_minute"`
	ElapsedSeconds       int64   `json:"elapsed_seconds"`
	LastMessageAt        int64   `json:"last_message_at"`
	SessionStartAt       int64   `json:"session_start_at"`
	TotalTokens          int64   `json:"total_tokens"`
	ModelName            string  `json:"model_name"`
	ContextUtilization   float64 `json:"context_utilization_percentage"`
	PressureLevel        string  `json:"pressure_level"`
}

// SessionState represents the current state of a session
type SessionState struct {
	Version       int              `json:"version"`
	SessionID     string           `json:"session_id"`
	State         string           `json:"state"`
	Model         string           `json:"model,omitempty"`
	CWD           string           `json:"cwd,omitempty"`
	TranscriptPath string          `json:"transcript_path,omitempty"`
	GitBranch     string           `json:"git_branch,omitempty"`
	ProjectName   string           `json:"project_name,omitempty"`
	FirstSeen     int64            `json:"first_seen"`
	UpdatedAt     int64            `json:"updated_at"`
	Confidence    string           `json:"confidence"`
	EventCount    int              `json:"event_count"`
	LastEvent     string           `json:"last_event"`
	Metrics       *SessionMetrics  `json:"metrics,omitempty"`
}

// LogEntry represents a structured log entry
type LogEntry struct {
	Timestamp       string `json:"timestamp"`
	Level           string `json:"level"`
	EventType       string `json:"event_type,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ProcessingTimeMs int64  `json:"processing_time_ms,omitempty"`
	PayloadSizeBytes int    `json:"payload_size_bytes,omitempty"`
	Result          string `json:"result"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`
}

// StructuredLogger handles JSON-formatted logging with rotation
type StructuredLogger struct {
	logFile     *os.File
	logPath     string
	currentSize int64
	mu          sync.Mutex
}


// computeSessionMetrics analyzes transcript and computes performance metrics using enhanced tailer
func computeSessionMetrics(transcriptPath string) *SessionMetrics {
	if transcriptPath == "" {
		return nil
	}
	
	// Use the enhanced transcript tailer for analysis
	transcriptTailer := tailer.NewTranscriptTailer(transcriptPath)
	metrics, err := transcriptTailer.TailAndProcess()
	if err != nil || metrics == nil {
		// Transcript doesn't exist yet or can't be read - not an error
		return nil
	}
	
	// Convert from transcript tailer metrics to hook metrics format
	hookMetrics := &SessionMetrics{
		MessagesPerMinute:    metrics.MessagesPerMinute,
		ElapsedSeconds:       metrics.ElapsedSeconds,
		LastMessageAt:        metrics.LastMessageAt.Unix(),
		SessionStartAt:       metrics.SessionStartAt.Unix(),
		TotalTokens:          metrics.TotalTokens,
		ModelName:            metrics.ModelName,
		ContextUtilization:   metrics.ContextUtilization,
		PressureLevel:        metrics.PressureLevel,
	}
	
	// Set defaults if values are missing
	if hookMetrics.ModelName == "" {
		hookMetrics.ModelName = "unknown"
	}
	if hookMetrics.PressureLevel == "" {
		hookMetrics.PressureLevel = "unknown"
	}
	
	return hookMetrics
}

// extractGitBranch tries to get git branch from CWD
func extractGitBranch(cwd string) string {
	if cwd == "" {
		return ""
	}
	
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	
	branch := strings.TrimSpace(string(output))
	if branch == "" || branch == "HEAD" {
		return ""
	}
	
	return branch
}

// extractProjectName extracts project name from CWD path
func extractProjectName(cwd string) string {
	if cwd == "" {
		return ""
	}
	
	// Get the last directory name from the path
	projectName := filepath.Base(cwd)
	
	// Handle edge cases
	if projectName == "." || projectName == "/" || projectName == "" {
		return ""
	}
	
	return projectName
}

// extractGitBranchFromTranscript tries to extract gitBranch from transcript data  
func extractGitBranchFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	
	// Read the last few lines of the transcript to find gitBranch
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()
	
	// Read the file and look for gitBranch in recent lines
	scanner := bufio.NewScanner(file)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		// Keep only last 10 lines to avoid excessive memory usage
		if len(lines) > 10 {
			lines = lines[1:]
		}
	}
	
	// Search recent lines for gitBranch field
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if strings.Contains(line, "gitBranch") {
			// Try to parse this line as JSON
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(line), &data); err == nil {
				if branch, ok := data["gitBranch"].(string); ok && branch != "" {
					return branch
				}
			}
		}
	}
	
	return ""
}


func main() {
	startTime := time.Now()
	
	// Initialize structured logger
	var err error
	logger, err = NewStructuredLogger()
	if err != nil {
		log.Printf("Failed to initialize logger: %v", err)
		os.Exit(1)
	}
	defer logger.Close()

	// Check for version flag
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("irrlicht-hook version %s\n", Version)
		fmt.Printf("Built with %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// Check for kill switch via environment variable
	if os.Getenv("IRRLICHT_DISABLED") == "1" {
		logger.LogInfo("", "", "Kill switch activated via IRRLICHT_DISABLED=1, exiting")
		os.Exit(0)
	}

	// Check for kill switch in settings
	if isDisabledInSettings() {
		logger.LogInfo("", "", "Kill switch activated via settings, exiting")
		os.Exit(0)
	}

	// Read event from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		logger.LogError("", "", fmt.Sprintf("Failed to read stdin: %v", err))
		os.Exit(1)
	}

	payloadSize := len(input)

	// Check payload size
	if payloadSize > MaxPayloadSize {
		logger.LogError("", "", fmt.Sprintf("Payload size %d exceeds maximum %d", payloadSize, MaxPayloadSize))
		os.Exit(1)
	}

	// Parse event
	var event HookEvent
	if err := json.Unmarshal(input, &event); err != nil {
		logger.LogError("", "", fmt.Sprintf("Failed to parse JSON: %v", err))
		os.Exit(1)
	}

	// Log raw event for debugging Claude Code's actual payload
	logger.LogInfo(event.HookEventName, event.SessionID, fmt.Sprintf("Raw event data: %s", string(input)))

	// Validate and sanitize
	if err := validateEvent(&event); err != nil {
		logger.LogError(event.HookEventName, event.SessionID, fmt.Sprintf("Event validation failed: %v", err))
		os.Exit(1)
	}

	// Process event with performance tracking
	processStart := time.Now()
	if err := processEvent(&event); err != nil {
		processingTime := time.Since(processStart).Milliseconds()
		logger.LogError(event.HookEventName, event.SessionID, fmt.Sprintf("Failed to process event: %v", err))
		logger.LogProcessingTime(event.HookEventName, event.SessionID, processingTime, payloadSize, "error")
		os.Exit(1)
	}

	// Log successful processing with metrics
	processingTime := time.Since(processStart).Milliseconds()
	totalTime := time.Since(startTime).Milliseconds()
	
	// Update metrics
	metrics.mu.Lock()
	metrics.eventsProcessed++
	metrics.totalLatencyMs += totalTime
	metrics.lastEventTime = time.Now()
	metrics.mu.Unlock()

	logger.LogProcessingTime(event.HookEventName, event.SessionID, processingTime, payloadSize, "success")
	logger.LogInfo(event.HookEventName, event.SessionID, fmt.Sprintf("Successfully processed event in %dms", totalTime))
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
		"PreToolUse", "PostToolUse", "PreCompact",
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

	// Sanitize paths from Data field
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
	
	// Sanitize paths from direct fields
	if event.TranscriptPath != "" {
		if err := validatePath(event.TranscriptPath); err != nil {
			return fmt.Errorf("invalid transcript_path: %w", err)
		}
	}
	if event.CWD != "" {
		if err := validatePath(event.CWD); err != nil {
			return fmt.Errorf("invalid cwd: %w", err)
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
		LastEvent:  event.HookEventName,
	}

	// Extract additional data from both Data field and direct fields
	// Check Data field first (legacy format)
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
	
	// Check direct fields (current Claude Code format)
	if event.Model != "" {
		sessionState.Model = event.Model
	}
	if event.CWD != "" {
		sessionState.CWD = event.CWD
	}
	if event.TranscriptPath != "" {
		sessionState.TranscriptPath = event.TranscriptPath
	}
	
	// Extract git branch and project name
	if sessionState.CWD != "" {
		sessionState.ProjectName = extractProjectName(sessionState.CWD)
		sessionState.GitBranch = extractGitBranch(sessionState.CWD)
	}
	
	// Try to get git branch from transcript if we have it (more reliable)
	if sessionState.TranscriptPath != "" {
		if transcriptBranch := extractGitBranchFromTranscript(sessionState.TranscriptPath); transcriptBranch != "" {
			sessionState.GitBranch = transcriptBranch
		}
	}
	
	// Compute metrics if we have a transcript path
	if sessionState.TranscriptPath != "" {
		if metrics := computeSessionMetrics(sessionState.TranscriptPath); metrics != nil {
			sessionState.Metrics = metrics
		}
	}

	// Load existing state to preserve first_seen and event_count
	existingState, err := loadSessionState(event.SessionID)
	if err == nil && existingState.FirstSeen > 0 {
		sessionState.FirstSeen = existingState.FirstSeen
		sessionState.EventCount = existingState.EventCount + 1
		
		// Preserve existing data if new event doesn't have it (fallback logic)
		if sessionState.Model == "" && existingState.Model != "" {
			sessionState.Model = existingState.Model
		}
		if sessionState.CWD == "" && existingState.CWD != "" {
			sessionState.CWD = existingState.CWD
		}
		if sessionState.GitBranch == "" && existingState.GitBranch != "" {
			sessionState.GitBranch = existingState.GitBranch
		}
		if sessionState.ProjectName == "" && existingState.ProjectName != "" {
			sessionState.ProjectName = existingState.ProjectName
		}
		
		// Re-extract git branch and project if we have CWD but missing these fields
		if sessionState.CWD != "" {
			if sessionState.GitBranch == "" {
				sessionState.GitBranch = extractGitBranch(sessionState.CWD)
			}
			if sessionState.ProjectName == "" {
				sessionState.ProjectName = extractProjectName(sessionState.CWD)
			}
		}
		if sessionState.TranscriptPath == "" && existingState.TranscriptPath != "" {
			sessionState.TranscriptPath = existingState.TranscriptPath
			
			// Recompute metrics if we have transcript path but no metrics yet
			if sessionState.Metrics == nil {
				if metrics := computeSessionMetrics(sessionState.TranscriptPath); metrics != nil {
					sessionState.Metrics = metrics
				}
			}
		}
		
		// Preserve existing metrics if we couldn't compute new ones
		if sessionState.Metrics == nil && existingState.Metrics != nil {
			sessionState.Metrics = existingState.Metrics
		}
	} else {
		sessionState.FirstSeen = now
		sessionState.EventCount = 1
	}

	// Save session state
	return saveSessionState(&sessionState)
}

// mapEventToState maps hook events to session states
func mapEventToState(eventName string) string {
	switch eventName {
	case "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact":
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

// NewStructuredLogger creates a new structured logger with rotation
func NewStructuredLogger() (*StructuredLogger, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	logsDir := filepath.Join(homeDir, AppSupportDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	logPath := filepath.Join(logsDir, "events.log")
	
	// Open or create log file
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Get current file size
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to stat log file: %w", err)
	}

	sl := &StructuredLogger{
		logFile:     file,
		logPath:     logPath,
		currentSize: stat.Size(),
	}

	// Check if rotation is needed
	if sl.currentSize > MaxLogSize {
		if err := sl.rotate(); err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to rotate log: %w", err)
		}
	}

	return sl, nil
}

// Close closes the log file
func (sl *StructuredLogger) Close() error {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	
	if sl.logFile != nil {
		return sl.logFile.Close()
	}
	return nil
}

// LogInfo logs an info-level structured log entry
func (sl *StructuredLogger) LogInfo(eventType, sessionID, message string) {
	entry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     "info",
		EventType: eventType,
		SessionID: sessionID,
		Result:    "success",
		Message:   message,
	}
	sl.writeEntry(entry)
}

// LogError logs an error-level structured log entry
func (sl *StructuredLogger) LogError(eventType, sessionID, errorMsg string) {
	entry := LogEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Level:     "error",
		EventType: eventType,
		SessionID: sessionID,
		Result:    "error",
		Error:     errorMsg,
	}
	sl.writeEntry(entry)
}

// LogProcessingTime logs processing performance metrics
func (sl *StructuredLogger) LogProcessingTime(eventType, sessionID string, processingTimeMs int64, payloadSize int, result string) {
	entry := LogEntry{
		Timestamp:       time.Now().Format(time.RFC3339Nano),
		Level:           "info",
		EventType:       eventType,
		SessionID:       sessionID,
		ProcessingTimeMs: processingTimeMs,
		PayloadSizeBytes: payloadSize,
		Result:          result,
		Message:         "Event processing completed",
	}
	sl.writeEntry(entry)
}

// writeEntry writes a log entry to the file with rotation check
func (sl *StructuredLogger) writeEntry(entry LogEntry) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	// Serialize entry to JSON
	jsonData, err := json.Marshal(entry)
	if err != nil {
		log.Printf("Failed to marshal log entry: %v", err)
		return
	}

	// Add newline
	jsonData = append(jsonData, '\n')

	// Check if rotation is needed before writing
	if sl.currentSize+int64(len(jsonData)) > MaxLogSize {
		if err := sl.rotate(); err != nil {
			log.Printf("Failed to rotate log: %v", err)
			return
		}
	}

	// Write to current log file
	n, err := sl.logFile.Write(jsonData)
	if err != nil {
		log.Printf("Failed to write log entry: %v", err)
		return
	}

	sl.currentSize += int64(n)
}

// rotate rotates the log files
func (sl *StructuredLogger) rotate() error {
	// Close current file
	if sl.logFile != nil {
		sl.logFile.Close()
	}

	// Rotate existing files
	for i := MaxLogFiles - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", sl.logPath, i)
		newPath := fmt.Sprintf("%s.%d", sl.logPath, i+1)
		
		if _, err := os.Stat(oldPath); err == nil {
			if i == MaxLogFiles-1 {
				// Remove the oldest file
				os.Remove(newPath)
			}
			os.Rename(oldPath, newPath)
		}
	}

	// Move current log to .1
	if _, err := os.Stat(sl.logPath); err == nil {
		os.Rename(sl.logPath, sl.logPath+".1")
	}

	// Create new log file
	file, err := os.OpenFile(sl.logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create new log file: %w", err)
	}

	sl.logFile = file
	sl.currentSize = 0

	return nil
}