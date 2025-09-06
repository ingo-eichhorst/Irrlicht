package tailer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	// "path/filepath"
	"strconv"
	"strings"
	"time"
	
	// "github.com/ingo-eichhorst/multi-cc-bar/tools/model-capacity/pkg/capacity"
)

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
}

// TranscriptTailer monitors transcript files and computes metrics
type TranscriptTailer struct {
	path        string
	lastOffset  int64
	metrics     *SessionMetrics
	windowSize  time.Duration // Default 60 seconds
	// capacityMgr *capacity.CapacityManager
}

// NewTranscriptTailer creates a new tailer for the given transcript path
func NewTranscriptTailer(path string) *TranscriptTailer {
	// Initialize capacity manager with default config path
	// capacityConfigPath := filepath.Join("..", "model-capacity.json")
	// if absPath, err := filepath.Abs(capacityConfigPath); err == nil {
	// 	capacityConfigPath = absPath
	// }
	
	// capacityMgr, err := capacity.NewCapacityManager(capacityConfigPath)
	// if err != nil {
	// 	// Fallback to nil if capacity manager fails to initialize
	// 	fmt.Printf("Warning: failed to initialize capacity manager: %v\n", err)
	// 	capacityMgr = nil
	// }
	
	return &TranscriptTailer{
		path:       path,
		lastOffset: 0,
		metrics: &SessionMetrics{
			MessageHistory: make([]MessageEvent, 0),
			SessionStartAt: time.Now(),
		},
		windowSize: 60 * time.Second,
		// capacityMgr: capacityMgr,
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
			continue
		}

		if event != nil {
			t.addMessageEvent(*event)
		}
	}

	t.lastOffset = currentOffset
	
	// Compute current metrics
	t.computeMetrics()
	
	// Compute context utilization if we have capacity manager and model info
	t.computeContextUtilization()
	
	return t.metrics, scanner.Err()
}

// parseTranscriptLine attempts to parse a JSONL line into a message event
func (t *TranscriptTailer) parseTranscriptLine(line string) (*MessageEvent, error) {
	// Parse as generic JSON first
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, err
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
	}
	return messageEvents[eventType]
}

// addMessageEvent adds a new message event and maintains sliding window
func (t *TranscriptTailer) addMessageEvent(event MessageEvent) {
	t.metrics.MessageHistory = append(t.metrics.MessageHistory, event)
	t.metrics.LastMessageAt = event.Timestamp

	// Update session start time if this is earlier
	if t.metrics.SessionStartAt.IsZero() || event.Timestamp.Before(t.metrics.SessionStartAt) {
		t.metrics.SessionStartAt = event.Timestamp
	}
}

// computeMetrics calculates messages per minute and elapsed time
func (t *TranscriptTailer) computeMetrics() {
	if len(t.metrics.MessageHistory) == 0 {
		t.metrics.MessagesPerMinute = 0
		t.metrics.ElapsedSeconds = 0
		return
	}
	
	// Use the latest message timestamp as our reference point
	latestTime := t.metrics.LastMessageAt
	if latestTime.IsZero() {
		latestTime = time.Now()
	}
	
	// Calculate elapsed time since session start
	if !t.metrics.SessionStartAt.IsZero() {
		t.metrics.ElapsedSeconds = int64(latestTime.Sub(t.metrics.SessionStartAt).Seconds())
	}
	
	// For messages per minute, use a sliding window from the latest timestamp
	windowStart := latestTime.Add(-t.windowSize)
	messageCount := 0
	
	// Filter messages to sliding window and count
	filteredHistory := make([]MessageEvent, 0, len(t.metrics.MessageHistory))
	for _, msg := range t.metrics.MessageHistory {
		if msg.Timestamp.After(windowStart) || msg.Timestamp.Equal(windowStart) {
			filteredHistory = append(filteredHistory, msg)
			messageCount++
		}
	}
	
	// Update history to only keep recent messages
	t.metrics.MessageHistory = filteredHistory
	
	// Convert to messages per minute
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
	
	if modelName != "" {
		t.metrics.ModelName = modelName
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
		// Also check for total_tokens directly
		if total, ok := usage["total_tokens"].(float64); ok {
			totalTokens = int64(total)
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
	
	// Update running total
	if totalTokens > 0 {
		t.metrics.TotalTokens = totalTokens
	}
}

// computeContextUtilization calculates context utilization percentage and pressure level
func (t *TranscriptTailer) computeContextUtilization() {
	// For now, provide basic context utilization computation without the capacity manager
	// This will be enhanced later when we integrate the capacity system properly
	
	if t.metrics.TotalTokens == 0 || t.metrics.ModelName == "" {
		// Set defaults when we can't compute utilization
		t.metrics.ContextUtilization = 0.0
		t.metrics.PressureLevel = "unknown"
		return
	}
	
	// Basic context utilization estimation
	// Assume 200K context window for Claude models (standard)
	contextWindow := int64(200000)
	utilizationPercentage := (float64(t.metrics.TotalTokens) / float64(contextWindow)) * 100
	
	// Determine pressure level
	pressureLevel := "safe"
	if utilizationPercentage >= 96 {
		pressureLevel = "critical"
	} else if utilizationPercentage >= 81 {
		pressureLevel = "warning"
	} else if utilizationPercentage >= 51 {
		pressureLevel = "caution"
	}
	
	t.metrics.ContextUtilization = utilizationPercentage
	t.metrics.PressureLevel = pressureLevel
}