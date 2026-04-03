package tailer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/pkg/capacity"
)

// Helper functions for Go versions that don't have built-in min/max
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// normalizeModelName normalizes model names by removing date suffixes, extended
// context markers, and handling aliases. The returned name is used as the key
// for capacity lookups.
func normalizeModelName(rawModel string) string {
	if rawModel == "" {
		return ""
	}

	// Strip extended context suffix (e.g. "claude-opus-4-6[1m]")
	rawModel = strings.TrimSuffix(rawModel, "[1m]")

	// Handle common aliases first
	aliases := map[string]string{
		"opusplan": "claude-opus-4-1",
		"sonnet":   "claude-sonnet-4-6",
		"haiku":    "claude-haiku-4-5",
	}

	if normalized, exists := aliases[rawModel]; exists {
		return normalized
	}

	// Remove date suffixes (e.g., "claude-opus-4-6-20250715" -> "claude-opus-4-6")
	datePattern := regexp.MustCompile(`-\d{8}$`)
	normalized := datePattern.ReplaceAllString(rawModel, "")

	// Match most-specific patterns first (longer model IDs before shorter)
	if strings.Contains(normalized, "claude-opus-4-6") {
		return "claude-opus-4-6"
	}
	if strings.Contains(normalized, "claude-sonnet-4-6") {
		return "claude-sonnet-4-6"
	}
	if strings.Contains(normalized, "claude-sonnet-4-5") {
		return "claude-sonnet-4-5"
	}
	if strings.Contains(normalized, "claude-haiku-4-5") {
		return "claude-haiku-4-5"
	}
	if strings.Contains(normalized, "claude-opus-4-1") {
		return "claude-opus-4-1"
	}
	// Generic claude-sonnet-4 fallback (e.g. claude-sonnet-4-20250514)
	if strings.Contains(normalized, "claude-sonnet-4") {
		return "claude-4-sonnet"
	}
	if strings.Contains(normalized, "claude-3.5-sonnet") {
		return "claude-3.5-sonnet"
	}
	if strings.Contains(normalized, "claude-3.5-haiku") {
		return "claude-3.5-haiku"
	}

	return normalized
}

// getDefaultModelFromSettings reads the default model from Claude settings.json
func getDefaultModelFromSettings() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return ""
	}
	
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	
	if model, ok := settings["model"].(string); ok {
		return normalizeModelName(model)
	}
	
	return ""
}

// MessageEvent represents a single message event from transcript
type MessageEvent struct {
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Content   string    `json:"content,omitempty"`
}

// SessionMetrics holds computed performance metrics
type SessionMetrics struct {
	MessagesPerMinute float64      `json:"messages_per_minute"`
	ElapsedSeconds    int64        `json:"elapsed_seconds"`
	LastMessageAt     time.Time    `json:"last_message_at"`
	MessageHistory    []MessageEvent `json:"-"` // Sliding window, not serialized
	SessionStartAt    time.Time    `json:"session_start_at"`
	TotalTokens       int64        `json:"total_tokens,omitempty"`
	ModelName         string       `json:"model_name,omitempty"`
	ContextUtilization float64     `json:"context_utilization_percentage,omitempty"`
	PressureLevel     string       `json:"pressure_level,omitempty"` // "safe", "caution", "warning", "critical"
	
	// Raw event data for real-time client-side calculations
	TotalEventCount           int64     `json:"total_event_count,omitempty"`           // Total events since session start
	RecentEventCount          int64     `json:"recent_event_count,omitempty"`          // Events in last 5 minutes
	RecentEventWindowStart    time.Time `json:"recent_event_window_start,omitempty"`  // Start of 5-minute window

	// Tool call tracking — count unmatched tool_use/tool_result pairs
	HasOpenToolCall  bool `json:"has_open_tool_call"`            // True when OpenToolCallCount > 0
	OpenToolCallCount int `json:"open_tool_call_count,omitempty"` // Number of tool_use events without matching tool_result

	// LastEventType is the event type of the most recent message event in
	// the transcript (e.g. "assistant", "user", "tool_use", "tool_result").
	// Used for content-based working/waiting detection.
	LastEventType string `json:"last_event_type,omitempty"`

	// LastOpenToolNames holds the tool names from the most recent assistant
	// message that called tools. Cleared when a user message appears.
	// Used to detect user-blocking tools (AskUserQuestion, ExitPlanMode).
	LastOpenToolNames []string `json:"last_open_tool_names,omitempty"`
}

// TranscriptTailer monitors transcript files and computes metrics
type TranscriptTailer struct {
	path        string
	lastOffset  int64
	metrics     *SessionMetrics
	windowSize  time.Duration // Default 60 seconds
	capacityMgr *capacity.CapacityManager

	// Context window override from transcript (context_management.context_window)
	// or extended context model suffix ([1m]). Takes priority over capacity lookup.
	contextWindowOverride int64

	// Tool call pairing counters — accumulated across parsed lines.
	toolUseCount    int // tool_use / tool_call events seen
	toolResultCount int // tool_result events seen

	// lastOpenToolNames holds tool names from the most recent assistant
	// message with tool_use blocks. Cleared on user messages.
	lastOpenToolNames []string
}

// NewTranscriptTailer creates a new tailer for the given transcript path
func NewTranscriptTailer(path string) *TranscriptTailer {
	return &TranscriptTailer{
		path:        path,
		lastOffset:  0,
		capacityMgr: capacity.DefaultCapacityManager(),
		metrics: &SessionMetrics{
			MessageHistory: make([]MessageEvent, 0),
			SessionStartAt: time.Time{}, // Will be set from the first actual message timestamp
		},
		windowSize: 60 * time.Second,
	}
}

// SetSessionStartTime allows preserving the session start time across multiple invocations
func (t *TranscriptTailer) SetSessionStartTime(startTime time.Time) {
	if t.metrics != nil {
		t.metrics.SessionStartAt = startTime
	}
}

// TailAndProcess reads the last ~64KB of transcript and processes new entries
func (t *TranscriptTailer) TailAndProcess() (*SessionMetrics, error) {
	file, err := os.Open(t.path)
	if err != nil {
		return nil, fmt.Errorf("failed to open transcript: %w", err)
	}
	defer file.Close()

	// Get file size
	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat transcript: %w", err)
	}
	fileSize := stat.Size()

	// Calculate start position for ~64KB tail
	const maxTailSize = 64 * 1024 // 64KB
	startPos := int64(0)
	if fileSize > maxTailSize {
		startPos = fileSize - maxTailSize
	}

	// Only process new content since last offset
	if t.lastOffset > startPos {
		startPos = t.lastOffset
	}

	// Seek to start position
	_, err = file.Seek(startPos, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("failed to seek transcript: %w", err)
	}

	// Process new lines
	scanner := bufio.NewScanner(file)
	currentOffset := startPos

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		currentOffset += int64(len(scanner.Bytes()) + 1) // +1 for newline
		
		if line == "" {
			continue
		}

		// Parse JSONL entry defensively
		event, err := t.parseTranscriptLine(line)
		if err != nil {
			// Log error but continue processing
			fmt.Printf("Warning: failed to parse line: %v\n", err)
			fmt.Printf("Problematic line: %s\n", line)
			continue
		}

		if event != nil {
			t.addMessageEvent(*event)
		}
	}

	t.lastOffset = currentOffset
	
	// Compute current metrics
	t.computeMetrics()
	
	// Use settings fallback if no model was found in transcript
	if t.metrics.ModelName == "" {
		if defaultModel := getDefaultModelFromSettings(); defaultModel != "" {
			t.metrics.ModelName = defaultModel
		}
	}
	
	// Compute context utilization if we have capacity manager and model info
	t.computeContextUtilization()
	
	return t.metrics, scanner.Err()
}

// parseTranscriptLine attempts to parse a JSONL line into a message event
func (t *TranscriptTailer) parseTranscriptLine(line string) (*MessageEvent, error) {
	// Skip empty or whitespace-only lines
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	
	// Skip lines that don't look like valid JSON (quick check)
	if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
		// More detailed check for common partial line patterns
		if strings.Contains(line, "input_tokens") || strings.Contains(line, "output_tokens") || 
		   strings.Contains(line, "timestamp") || strings.Contains(line, "requestId") ||
		   strings.Contains(line, "Sidechain") || strings.Contains(line, "userType") ||
		   strings.Contains(line, "sessionId") || strings.Contains(line, "gitBranch") {
			return nil, fmt.Errorf("detected partial JSON line (concurrent write): %s", 
				string(line[0:min(50, len(line))]))
		}
		return nil, fmt.Errorf("invalid JSON format: %s", 
			string(line[0:min(30, len(line))]))
	}
	
	// Parse as generic JSON first
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, fmt.Errorf("JSON unmarshal error: %w", err)
	}

	// Extract timestamp
	var timestamp time.Time
	if ts, ok := raw["timestamp"]; ok {
		if tsStr, ok := ts.(string); ok {
			if parsed, err := time.Parse(time.RFC3339, tsStr); err == nil {
				timestamp = parsed
			} else if parsed, err := time.Parse("2006-01-02T15:04:05.000Z", tsStr); err == nil {
				timestamp = parsed
			}
		}
	}
	
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	// Extract event type from various possible fields
	eventType := "unknown"
	if et, ok := raw["event_type"].(string); ok {
		eventType = et
	} else if et, ok := raw["type"].(string); ok {
		eventType = et
	} else if _, ok := raw["user_input"]; ok {
		eventType = "user_message"
	} else if _, ok := raw["assistant_output"]; ok {
		eventType = "assistant_message"
	} else if _, ok := raw["tool_call"]; ok {
		eventType = "tool_call"
	}

	// Extract model information
	t.extractModelInfo(raw)

	// Extract token information
	t.extractTokenInfo(raw)

	// Only track message-related events
	if !t.isMessageEvent(eventType) {
		return nil, nil
	}

	// Claude Code embeds tool_use inside assistant messages and tool_result
	// inside user messages. Count them from message.content[] so open tool
	// call tracking works correctly. Also track tool names for user-blocking
	// tool detection (AskUserQuestion, ExitPlanMode).
	if msg, ok := raw["message"].(map[string]interface{}); ok {
		if contentArr, ok := msg["content"].([]interface{}); ok {
			var toolNames []string
			for _, item := range contentArr {
				if block, ok := item.(map[string]interface{}); ok {
					switch block["type"] {
					case "tool_use":
						t.toolUseCount++
						if name, ok := block["name"].(string); ok {
							toolNames = append(toolNames, name)
						}
					case "tool_result":
						t.toolResultCount++
					}
				}
			}
			// Assistant message with tools: store the tool names
			if eventType == "assistant" && len(toolNames) > 0 {
				t.lastOpenToolNames = toolNames
			}
		}
	}

	// User message (including tool results) clears open tool names
	if eventType == "user" {
		t.lastOpenToolNames = nil
	}

	return &MessageEvent{
		Timestamp: timestamp,
		EventType: eventType,
		Content:   line,
	}, nil
}

// isMessageEvent determines if an event type should be counted as a message
func (t *TranscriptTailer) isMessageEvent(eventType string) bool {
	messageEvents := map[string]bool{
		"user_message":      true,
		"assistant_message": true,
		"tool_call":         true,
		"tool_result":       true,
		"user_input":        true,
		"assistant_output":  true,
		// Add support for Claude Code transcript event types
		"user":              true,
		"assistant":         true,
		"tool_use":          true,
		"message":           true,
	}
	return messageEvents[eventType]
}

// addMessageEvent adds a new message event and maintains sliding window
func (t *TranscriptTailer) addMessageEvent(event MessageEvent) {
	t.metrics.MessageHistory = append(t.metrics.MessageHistory, event)
	t.metrics.LastMessageAt = event.Timestamp
	t.metrics.LastEventType = event.EventType

	// Update session start time if this is earlier
	if t.metrics.SessionStartAt.IsZero() || event.Timestamp.Before(t.metrics.SessionStartAt) {
		t.metrics.SessionStartAt = event.Timestamp
	}

	// Tool call pairing for non-Claude-Code formats where tool_use/tool_result
	// are top-level event types (e.g. Codex). Claude Code counting happens in
	// parseTranscriptLine via message.content[] inspection.
	switch event.EventType {
	case "tool_use", "tool_call":
		t.toolUseCount++
	case "tool_result":
		t.toolResultCount++
	}
}

// computeMetrics calculates messages per minute and elapsed time
func (t *TranscriptTailer) computeMetrics() {
	if len(t.metrics.MessageHistory) == 0 {
		t.metrics.MessagesPerMinute = 0
		t.metrics.ElapsedSeconds = 0
		t.metrics.TotalEventCount = 0
		t.metrics.RecentEventCount = 0
		t.metrics.RecentEventWindowStart = time.Time{}
		return
	}
	
	// Use current time as reference for real-time calculations
	currentTime := time.Now()
	
	// Use the latest message timestamp as our reference point for legacy calculations
	latestTime := t.metrics.LastMessageAt
	if latestTime.IsZero() {
		latestTime = currentTime
	}
	
	// Calculate elapsed time since session start
	if !t.metrics.SessionStartAt.IsZero() {
		t.metrics.ElapsedSeconds = int64(latestTime.Sub(t.metrics.SessionStartAt).Seconds())
	}
	
	// Calculate raw event counts for client-side real-time calculations
	t.metrics.TotalEventCount = int64(len(t.metrics.MessageHistory))
	
	// For recent events: use 5-minute window from current time
	fiveMinutesAgo := currentTime.Add(-5 * time.Minute)
	recentEventCount := int64(0)
	
	// Set window start to the later of session start or 5 minutes ago
	windowStart := fiveMinutesAgo
	if t.metrics.SessionStartAt.After(fiveMinutesAgo) {
		windowStart = t.metrics.SessionStartAt
	}
	t.metrics.RecentEventWindowStart = windowStart
	
	// Count events in the recent window
	for _, msg := range t.metrics.MessageHistory {
		if msg.Timestamp.After(windowStart) || msg.Timestamp.Equal(windowStart) {
			recentEventCount++
		}
	}
	t.metrics.RecentEventCount = recentEventCount
	
	// Compute open tool call count from pairing counters.
	openCalls := t.toolUseCount - t.toolResultCount
	if openCalls < 0 {
		openCalls = 0 // Guard against starting mid-stream where we see results without uses
	}
	t.metrics.OpenToolCallCount = openCalls
	t.metrics.HasOpenToolCall = openCalls > 0
	t.metrics.LastOpenToolNames = t.lastOpenToolNames

	// Legacy calculation: For messages per minute, use a sliding window from the latest timestamp
	legacyWindowStart := latestTime.Add(-t.windowSize)
	messageCount := 0
	
	// Filter messages to sliding window and count
	filteredHistory := make([]MessageEvent, 0, len(t.metrics.MessageHistory))
	for _, msg := range t.metrics.MessageHistory {
		if msg.Timestamp.After(legacyWindowStart) || msg.Timestamp.Equal(legacyWindowStart) {
			filteredHistory = append(filteredHistory, msg)
			messageCount++
		}
	}
	
	// Update history to only keep recent messages
	t.metrics.MessageHistory = filteredHistory
	
	// Convert to messages per minute (legacy calculation)
	if messageCount > 0 {
		// Calculate actual time span of messages
		if len(filteredHistory) > 1 {
			timeSpan := latestTime.Sub(filteredHistory[0].Timestamp)
			if timeSpan > 0 {
				t.metrics.MessagesPerMinute = float64(messageCount) / timeSpan.Minutes()
			} else {
				t.metrics.MessagesPerMinute = float64(messageCount) // All messages at same time
			}
		} else {
			// Single message - use window size
			t.metrics.MessagesPerMinute = float64(messageCount) / t.windowSize.Minutes()
		}
	} else {
		t.metrics.MessagesPerMinute = 0
	}
}

// GetMetrics returns current computed metrics
func (t *TranscriptTailer) GetMetrics() *SessionMetrics {
	if t.metrics == nil {
		return &SessionMetrics{}
	}
	return t.metrics
}

// ResetOffset resets the file offset (useful for testing or file rotation)
func (t *TranscriptTailer) ResetOffset() {
	t.lastOffset = 0
}

// extractModelInfo extracts model name from transcript entry
func (t *TranscriptTailer) extractModelInfo(raw map[string]interface{}) {
	// Look for model name in various possible fields
	modelName := ""
	
	// Check for model field directly
	if model, ok := raw["model"].(string); ok {
		modelName = model
	} else if request, ok := raw["request"].(map[string]interface{}); ok {
		if model, ok := request["model"].(string); ok {
			modelName = model
		}
	} else if metadata, ok := raw["metadata"].(map[string]interface{}); ok {
		if model, ok := metadata["model"].(string); ok {
			modelName = model
		}
	}
	
	// Check for message.model field (Claude Code format for assistant messages)
	if modelName == "" {
		if message, ok := raw["message"].(map[string]interface{}); ok {
			if model, ok := message["model"].(string); ok {
				modelName = model
			}
		}
	}
	
	// If this is an assistant message, prioritize its model info (most recent)
	if typeField, ok := raw["type"].(string); ok && typeField == "assistant" {
		if message, ok := raw["message"].(map[string]interface{}); ok {
			if model, ok := message["model"].(string); ok {
				modelName = model
			}
		}
	}
	
	if modelName != "" {
		// Detect extended context suffix before normalization
		if strings.Contains(modelName, "[1m]") {
			t.contextWindowOverride = 1000000
		}
		// Normalize the model name before storing
		t.metrics.ModelName = normalizeModelName(modelName)
	}

	// Extract context_management.context_window (most accurate source)
	if cm, ok := raw["context_management"].(map[string]interface{}); ok {
		if cw, ok := cm["context_window"].(float64); ok && cw > 0 {
			t.contextWindowOverride = int64(cw)
		}
	}
}

// extractTokenInfo extracts token count information from transcript entry
func (t *TranscriptTailer) extractTokenInfo(raw map[string]interface{}) {
	// Look for token usage in various possible fields
	var totalTokens int64 = 0
	
	// Check usage field (Claude API format)
	if usage, ok := raw["usage"].(map[string]interface{}); ok {
		if inputTokens, ok := usage["input_tokens"].(float64); ok {
			totalTokens += int64(inputTokens)
		}
		if outputTokens, ok := usage["output_tokens"].(float64); ok {
			totalTokens += int64(outputTokens)
		}
		if cacheReadTokens, ok := usage["cache_read_input_tokens"].(float64); ok {
			totalTokens += int64(cacheReadTokens)
		}
		if cacheCreationTokens, ok := usage["cache_creation_input_tokens"].(float64); ok {
			totalTokens += int64(cacheCreationTokens)
		}
		// Also check for total_tokens directly
		if total, ok := usage["total_tokens"].(float64); ok {
			totalTokens = int64(total)
		}
	}
	
	// Check message.usage field (Claude Code format)
	if message, ok := raw["message"].(map[string]interface{}); ok {
		if usage, ok := message["usage"].(map[string]interface{}); ok {
			if inputTokens, ok := usage["input_tokens"].(float64); ok {
				totalTokens += int64(inputTokens)
			}
			if outputTokens, ok := usage["output_tokens"].(float64); ok {
				totalTokens += int64(outputTokens)
			}
			if cacheReadTokens, ok := usage["cache_read_input_tokens"].(float64); ok {
				totalTokens += int64(cacheReadTokens)
			}
			if cacheCreationTokens, ok := usage["cache_creation_input_tokens"].(float64); ok {
				totalTokens += int64(cacheCreationTokens)
			}
			// Also check for total_tokens directly
			if total, ok := usage["total_tokens"].(float64); ok {
				totalTokens = int64(total)
			}
		}
	}
	
	// Check for token count in response metadata
	if response, ok := raw["response"].(map[string]interface{}); ok {
		if usage, ok := response["usage"].(map[string]interface{}); ok {
			if total, ok := usage["total_tokens"].(float64); ok {
				totalTokens = int64(total)
			}
		}
	}
	
	// Check for token count string fields
	if tokenStr, ok := raw["token_count"].(string); ok {
		if tokens, err := strconv.ParseInt(tokenStr, 10, 64); err == nil {
			totalTokens = tokens
		}
	} else if tokenFloat, ok := raw["token_count"].(float64); ok {
		totalTokens = int64(tokenFloat)
	}
	
	// Update to latest token count (current context window, not cumulative)
	if totalTokens > 0 {
		t.metrics.TotalTokens = totalTokens
	}
}

// computeContextUtilization calculates context utilization percentage and pressure level.
// It uses a three-step fallback chain for the effective context window:
//  1. context_management.context_window from transcript (most accurate)
//  2. CapacityManager per-model lookup
//  3. Default 200K
func (t *TranscriptTailer) computeContextUtilization() {
	if t.metrics.TotalTokens == 0 || t.metrics.ModelName == "" {
		t.metrics.ContextUtilization = 0.0
		t.metrics.PressureLevel = "unknown"
		return
	}

	// Fallback chain for effective context window
	var effectiveContextWindow int64

	// 1. Transcript-reported context window (most accurate)
	if t.contextWindowOverride > 0 {
		effectiveContextWindow = t.contextWindowOverride
	}

	// 2. CapacityManager per-model lookup
	if effectiveContextWindow <= 0 && t.capacityMgr != nil {
		cap := t.capacityMgr.GetModelCapacity(t.metrics.ModelName)
		if cap.ContextWindow > 0 {
			effectiveContextWindow = cap.ContextWindow
		}
	}

	// 3. Default fallback
	if effectiveContextWindow <= 0 {
		effectiveContextWindow = int64(200000)
	}

	utilizationPercentage := (float64(t.metrics.TotalTokens) / float64(effectiveContextWindow)) * 100

	pressureLevel := "safe"
	if utilizationPercentage >= 90 {
		pressureLevel = "critical"
	} else if utilizationPercentage >= 80 {
		pressureLevel = "warning"
	} else if utilizationPercentage >= 60 {
		pressureLevel = "caution"
	}

	t.metrics.ContextUtilization = utilizationPercentage
	t.metrics.PressureLevel = pressureLevel
}