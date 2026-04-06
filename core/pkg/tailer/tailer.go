package tailer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/pkg/capacity"
)

// MessageEvent represents a single message event from transcript
type MessageEvent struct {
	Timestamp time.Time `json:"timestamp"`
	EventType string    `json:"event_type"`
	Content   string    `json:"content,omitempty"`
}

// SessionMetrics holds computed performance metrics
type SessionMetrics struct {
	MessagesPerMinute   float64        `json:"messages_per_minute"`
	ElapsedSeconds      int64          `json:"elapsed_seconds"`
	LastMessageAt       time.Time      `json:"last_message_at"`
	MessageHistory      []MessageEvent `json:"-"` // Sliding window, not serialized
	SessionStartAt      time.Time      `json:"session_start_at"`
	TotalTokens         int64          `json:"total_tokens,omitempty"`
	InputTokens         int64          `json:"input_tokens,omitempty"`
	OutputTokens        int64          `json:"output_tokens,omitempty"`
	CacheReadTokens     int64          `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int64          `json:"cache_creation_tokens,omitempty"`
	EstimatedCostUSD    float64        `json:"estimated_cost_usd,omitempty"`
	ModelName           string         `json:"model_name,omitempty"`
	ContextWindow       int64          `json:"context_window,omitempty"`
	ContextUtilization  float64        `json:"context_utilization_percentage,omitempty"`
	PressureLevel       string         `json:"pressure_level,omitempty"` // "safe", "caution", "warning", "critical"

	// Raw event data for real-time client-side calculations
	TotalEventCount        int64     `json:"total_event_count,omitempty"`
	RecentEventCount       int64     `json:"recent_event_count,omitempty"`
	RecentEventWindowStart time.Time `json:"recent_event_window_start,omitempty"`

	// Tool call tracking — count unmatched tool_use/tool_result pairs
	HasOpenToolCall   bool `json:"has_open_tool_call"`
	OpenToolCallCount int  `json:"open_tool_call_count,omitempty"`

	// LastEventType is the event type of the most recent message event in
	// the transcript (e.g. "assistant", "user", "tool_use", "tool_result").
	// Used for content-based working/waiting detection.
	LastEventType string `json:"last_event_type,omitempty"`

	// LastOpenToolNames holds the tool names from the most recent assistant
	// message that called tools. Cleared when a user message appears.
	// Used to detect user-blocking tools (AskUserQuestion, ExitPlanMode).
	LastOpenToolNames []string `json:"last_open_tool_names,omitempty"`

	// LastToolResultWasError is true when the most recently processed
	// tool_result content block had is_error=true (user rejection / ESC).
	LastToolResultWasError bool `json:"last_tool_result_was_error"`

	// LastCWD is the most recent working directory seen in the transcript.
	// Extracted during parsing so callers don't need a separate file read.
	LastCWD string `json:"last_cwd,omitempty"`

	// LastAssistantText is the text content of the most recent assistant
	// message, truncated to ~200 characters.
	LastAssistantText string `json:"last_assistant_text,omitempty"`

	// PermissionMode is the session's permission mode (e.g. "default",
	// "plan", "bypassPermissions"). Extracted from "permission-mode" events.
	PermissionMode string `json:"permission_mode,omitempty"`
}

// TranscriptTailer monitors transcript files and computes metrics.
// Format-specific parsing is delegated to a TranscriptParser.
type TranscriptTailer struct {
	path        string
	lastOffset  int64
	metrics     *SessionMetrics
	windowSize  time.Duration // Default 60 seconds
	capacityMgr *capacity.CapacityManager

	// parser handles format-specific line parsing (Claude Code, Codex, Pi).
	parser TranscriptParser

	// adapter name for model config fallback.
	adapter string

	// Context window override from transcript or extended context model suffix.
	contextWindowOverride int64

	// Tool call pairing counters — accumulated from ParsedEvent deltas.
	toolUseCount    int
	toolResultCount int

	// lastOpenToolNames holds tool names from open tool calls.
	lastOpenToolNames []string

	// contentChars accumulates character count from message content for
	// token estimation when explicit token counts aren't available.
	contentChars int64

	// Token breakdown accumulators (latest snapshot, not cumulative).
	inputTokens         int64
	outputTokens        int64
	cacheReadTokens     int64
	cacheCreationTokens int64

	// lastToolResultWasError tracks is_error on the most recent tool_result.
	lastToolResultWasError bool

	// lastCWD tracks the most recent working directory seen in transcript lines.
	lastCWD string

	// lastAssistantText holds the text content of the most recent assistant
	// message, truncated to ~200 characters.
	lastAssistantText string
}

// NewTranscriptTailer creates a new tailer for the given transcript path.
// The parser handles format-specific line parsing; adapter is used for model
// config fallback.
func NewTranscriptTailer(path string, parser TranscriptParser, adapter string) *TranscriptTailer {
	return &TranscriptTailer{
		path:        path,
		lastOffset:  0,
		capacityMgr: capacity.DefaultCapacityManager(),
		parser:      parser,
		adapter:     adapter,
		metrics: &SessionMetrics{
			MessageHistory: make([]MessageEvent, 0),
			SessionStartAt: time.Time{},
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

	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat transcript: %w", err)
	}
	fileSize := stat.Size()

	const maxTailSize = 64 * 1024
	startPos := int64(0)
	switch {
	case fileSize < t.lastOffset:
		// File rotated/truncated.
		startPos = 0
	case t.lastOffset > 0:
		// Normal incremental path: never skip ahead of the last processed byte.
		startPos = t.lastOffset
	case fileSize > maxTailSize:
		// Initial read for large files: only tail the latest window.
		startPos = fileSize - maxTailSize
	}

	_, err = file.Seek(startPos, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("failed to seek transcript: %w", err)
	}

	currentOffset := startPos
	var reader io.Reader = file

	// On the initial truncated read of a large file, we may start in the
	// middle of a JSON line. If so, discard the partial line to align scanner
	// to a full JSONL entry boundary.
	if t.lastOffset == 0 && startPos > 0 {
		prev := []byte{0}
		if _, err := file.ReadAt(prev, startPos-1); err == nil && prev[0] != '\n' {
			br := bufio.NewReader(file)
			if discarded, err := br.ReadString('\n'); err == nil {
				currentOffset += int64(len(discarded))
			} else {
				return nil, fmt.Errorf("failed to align transcript boundary: %w", err)
			}
			reader = br
		}
	}

	scanner := bufio.NewScanner(reader)
	// Large tool results (especially from Pi/Codex read/bash output) can exceed
	// bufio.Scanner's 64KB default token size.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		currentOffset += int64(len(scanner.Bytes()) + 1)

		if line == "" {
			continue
		}

		// Quick JSON check.
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			continue
		}

		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		// Delegate to format-specific parser.
		parsed := t.parser.ParseLine(raw)
		if parsed == nil || parsed.Skip {
			// Even for skipped events, apply metadata that the parser extracted
			// (e.g. model from model_change, CWD from session header).
			if parsed != nil {
				t.applyMetadata(parsed)
			}
			continue
		}

		// Apply tool tracking deltas from the parser.
		t.toolUseCount += len(parsed.ToolUseNames)
		t.lastOpenToolNames = append(t.lastOpenToolNames, parsed.ToolUseNames...)
		t.toolResultCount += parsed.ToolResultCount
		for i := 0; i < parsed.ToolResultCount; i++ {
			if len(t.lastOpenToolNames) > 0 {
				t.lastOpenToolNames = t.lastOpenToolNames[1:]
			}
		}
		if parsed.ClearToolNames && parsed.ToolResultCount == 0 {
			t.lastOpenToolNames = nil
		}
		if parsed.IsError {
			t.lastToolResultWasError = true
		} else if parsed.ToolResultCount > 0 {
			t.lastToolResultWasError = false
		}

		// Apply metadata.
		t.applyMetadata(parsed)

		// Track assistant text.
		if parsed.AssistantText != "" {
			t.lastAssistantText = parsed.AssistantText
		}
		if parsed.ClearToolNames {
			t.lastAssistantText = ""
		}

		// Accumulate content chars.
		t.contentChars += parsed.ContentChars

		t.addMessageEvent(MessageEvent{
			Timestamp: parsed.Timestamp,
			EventType: parsed.EventType,
		})
	}

	t.lastOffset = currentOffset

	// Compute current metrics.
	t.computeMetrics()

	// Model config fallback.
	if t.metrics.ModelName == "" {
		if defaultModel := getDefaultModelFromConfig(t.adapter); defaultModel != "" {
			t.metrics.ModelName = defaultModel
		}
	}

	// Estimate tokens from content chars when no explicit token data exists.
	if t.metrics.TotalTokens == 0 && t.contentChars > 0 && t.capacityMgr != nil && t.metrics.ModelName != "" {
		cap := t.capacityMgr.GetModelCapacity(t.metrics.ModelName)
		ratio := cap.CharToTokenRatio
		if ratio <= 0 {
			ratio = 4.0
		}
		t.metrics.TotalTokens = int64(float64(t.contentChars) / ratio)
		if t.inputTokens == 0 && t.outputTokens == 0 {
			t.inputTokens = t.metrics.TotalTokens * 9 / 10
			t.outputTokens = t.metrics.TotalTokens - t.inputTokens
		}
	}

	// Recompute cost if token estimation filled in the breakdown.
	if t.metrics.EstimatedCostUSD == 0 && t.inputTokens > 0 && t.capacityMgr != nil && t.metrics.ModelName != "" {
		t.metrics.InputTokens = t.inputTokens
		t.metrics.OutputTokens = t.outputTokens
		t.metrics.EstimatedCostUSD = t.capacityMgr.EstimateCostUSD(
			t.metrics.ModelName, t.inputTokens, t.outputTokens, t.cacheReadTokens, t.cacheCreationTokens)
	}

	t.computeContextUtilization()

	return t.metrics, scanner.Err()
}

// applyMetadata applies model/token/CWD/permission metadata from a parsed event.
func (t *TranscriptTailer) applyMetadata(parsed *ParsedEvent) {
	if parsed.ModelName != "" {
		if strings.Contains(parsed.ModelName, "[1m]") {
			t.contextWindowOverride = 1000000
		}
		t.metrics.ModelName = NormalizeModelName(parsed.ModelName)
	}
	if parsed.ContextWindow > 0 {
		t.contextWindowOverride = parsed.ContextWindow
	}
	if parsed.Tokens != nil {
		if parsed.Tokens.Total > 0 {
			t.metrics.TotalTokens = parsed.Tokens.Total
		}
		if parsed.Tokens.Input > 0 || parsed.Tokens.Output > 0 {
			t.inputTokens = parsed.Tokens.Input
			t.outputTokens = parsed.Tokens.Output
			t.cacheReadTokens = parsed.Tokens.CacheRead
			t.cacheCreationTokens = parsed.Tokens.CacheCreation
		}
	}
	if parsed.CWD != "" {
		t.lastCWD = parsed.CWD
	}
	if parsed.PermissionMode != "" {
		t.metrics.PermissionMode = parsed.PermissionMode
	}
}

// addMessageEvent adds a new message event and maintains sliding window.
// Tool call counting is NOT done here — it's handled from ParsedEvent deltas
// in TailAndProcess to avoid double-counting.
func (t *TranscriptTailer) addMessageEvent(event MessageEvent) {
	t.metrics.MessageHistory = append(t.metrics.MessageHistory, event)
	t.metrics.LastMessageAt = event.Timestamp
	t.metrics.LastEventType = event.EventType

	if t.metrics.SessionStartAt.IsZero() || event.Timestamp.Before(t.metrics.SessionStartAt) {
		t.metrics.SessionStartAt = event.Timestamp
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

	currentTime := time.Now()
	latestTime := t.metrics.LastMessageAt
	if latestTime.IsZero() {
		latestTime = currentTime
	}

	if !t.metrics.SessionStartAt.IsZero() {
		t.metrics.ElapsedSeconds = int64(latestTime.Sub(t.metrics.SessionStartAt).Seconds())
	}

	t.metrics.TotalEventCount = int64(len(t.metrics.MessageHistory))

	fiveMinutesAgo := currentTime.Add(-5 * time.Minute)
	windowStart := fiveMinutesAgo
	if t.metrics.SessionStartAt.After(fiveMinutesAgo) {
		windowStart = t.metrics.SessionStartAt
	}
	t.metrics.RecentEventWindowStart = windowStart

	recentEventCount := int64(0)
	for _, msg := range t.metrics.MessageHistory {
		if msg.Timestamp.After(windowStart) || msg.Timestamp.Equal(windowStart) {
			recentEventCount++
		}
	}
	t.metrics.RecentEventCount = recentEventCount

	// Compute open tool call count from pairing counters.
	openCalls := t.toolUseCount - t.toolResultCount
	if openCalls < 0 {
		openCalls = 0
	}
	t.metrics.OpenToolCallCount = openCalls
	t.metrics.HasOpenToolCall = openCalls > 0
	t.metrics.LastOpenToolNames = t.lastOpenToolNames
	t.metrics.LastToolResultWasError = t.lastToolResultWasError
	t.metrics.LastCWD = t.lastCWD
	t.metrics.LastAssistantText = t.lastAssistantText

	// Token breakdown + estimated cost.
	t.metrics.InputTokens = t.inputTokens
	t.metrics.OutputTokens = t.outputTokens
	t.metrics.CacheReadTokens = t.cacheReadTokens
	t.metrics.CacheCreationTokens = t.cacheCreationTokens
	if t.capacityMgr != nil && t.metrics.ModelName != "" {
		t.metrics.EstimatedCostUSD = t.capacityMgr.EstimateCostUSD(
			t.metrics.ModelName, t.inputTokens, t.outputTokens, t.cacheReadTokens, t.cacheCreationTokens)
	}

	// Sliding window for messages per minute.
	legacyWindowStart := latestTime.Add(-t.windowSize)
	messageCount := 0
	filteredHistory := make([]MessageEvent, 0, len(t.metrics.MessageHistory))
	for _, msg := range t.metrics.MessageHistory {
		if msg.Timestamp.After(legacyWindowStart) || msg.Timestamp.Equal(legacyWindowStart) {
			filteredHistory = append(filteredHistory, msg)
			messageCount++
		}
	}
	t.metrics.MessageHistory = filteredHistory

	if messageCount > 0 {
		if len(filteredHistory) > 1 {
			timeSpan := latestTime.Sub(filteredHistory[0].Timestamp)
			if timeSpan > 0 {
				t.metrics.MessagesPerMinute = float64(messageCount) / timeSpan.Minutes()
			} else {
				t.metrics.MessagesPerMinute = float64(messageCount)
			}
		} else {
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

// computeContextUtilization calculates context utilization percentage and pressure level.
func (t *TranscriptTailer) computeContextUtilization() {
	if t.metrics.TotalTokens == 0 || t.metrics.ModelName == "" {
		t.metrics.ContextUtilization = 0.0
		t.metrics.PressureLevel = "unknown"
		return
	}

	var effectiveContextWindow int64

	if t.contextWindowOverride > 0 {
		effectiveContextWindow = t.contextWindowOverride
	}

	if effectiveContextWindow <= 0 && t.capacityMgr != nil {
		cap := t.capacityMgr.GetModelCapacity(t.metrics.ModelName)
		if ctx1m, ok := cap.BetaFeatures["context_1m"]; ok && ctx1m > 0 {
			effectiveContextWindow = ctx1m
		} else if cap.ContextWindow > 0 {
			effectiveContextWindow = cap.ContextWindow
		}
	}

	// Unknown model: no context window data available — report raw tokens only.
	if effectiveContextWindow <= 0 {
		t.metrics.ContextWindow = 0
		t.metrics.ContextUtilization = 0
		t.metrics.PressureLevel = "unknown"
		return
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

	t.metrics.ContextWindow = effectiveContextWindow
	t.metrics.ContextUtilization = utilizationPercentage
	t.metrics.PressureLevel = pressureLevel
}

// --- Model config fallback ---

// getDefaultModelFromConfig reads the default model from the appropriate config
// based on adapter name.
func getDefaultModelFromConfig(adapter string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch adapter {
	case "pi":
		return getPiModel(homeDir)
	case "codex":
		return getCodexModel(homeDir)
	default:
		return getClaudeModel(homeDir)
	}
}

func getClaudeModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	if model, ok := settings["model"].(string); ok {
		return NormalizeModelName(model)
	}
	return ""
}

func getCodexModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".codex", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "model") && strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if strings.TrimSpace(parts[0]) == "model" {
				model := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
				if model != "" {
					return model
				}
			}
		}
	}
	return ""
}

func getPiModel(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, ".pi", "agent", "settings.json"))
	if err != nil {
		return ""
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return ""
	}
	if model, ok := settings["defaultModel"].(string); ok {
		return NormalizeModelName(model)
	}
	return ""
}
