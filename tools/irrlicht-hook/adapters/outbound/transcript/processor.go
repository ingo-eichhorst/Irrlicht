package transcript

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	// MaxMessageHistory limits the number of messages kept in memory for sliding window analysis
	MaxMessageHistory = 100
)

// SessionMetrics holds computed performance metrics (mirroring transcript-tailer)
type SessionMetrics struct {
	MessagesPerMinute      float64        `json:"messages_per_minute"`
	ElapsedSeconds         int64          `json:"elapsed_seconds"`
	LastMessageAt          time.Time      `json:"last_message_at"`
	MessageHistory         []MessageEvent `json:"-"` // Sliding window, not serialized
	SessionStartAt         time.Time      `json:"session_start_at"`
	TotalTokens            int64          `json:"total_tokens,omitempty"`
	ModelName              string         `json:"model_name,omitempty"`
	ContextUtilization     float64        `json:"context_utilization_percentage,omitempty"`
	PressureLevel          string         `json:"pressure_level,omitempty"` // "low", "medium", "high", "critical", "unknown"
	TotalEventCount        int64          `json:"total_event_count,omitempty"`
	RecentEventCount       int64          `json:"recent_event_count,omitempty"`
	RecentEventWindowStart time.Time      `json:"recent_event_window_start,omitempty"`
	MaxContextSize         int64          `json:"max_context_size,omitempty"`   // Maximum context size seen from assistant messages
	
	// ccusage-compatible consumption tracking
	CumulativeInputTokens        int64          `json:"cumulative_input_tokens,omitempty"`
	CumulativeOutputTokens       int64          `json:"cumulative_output_tokens,omitempty"`
	CumulativeCacheCreationTokens int64         `json:"cumulative_cache_creation_tokens,omitempty"`
	CumulativeCacheReadTokens     int64         `json:"cumulative_cache_read_tokens,omitempty"`
	
	// Processing state for incremental updates
	NewOffset              int64          `json:"-"` // Not serialized, used for tracking
}

// TokenInfo holds structured token usage information (ccusage-compatible)
type TokenInfo struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	TotalTokens              int64 `json:"total_tokens"`
}

// MessageEvent represents a parsed transcript message
type MessageEvent struct {
	Timestamp time.Time  `json:"timestamp"`
	Type      string     `json:"type"`
	Role      string     `json:"role,omitempty"`
	Tokens    int64      `json:"tokens,omitempty"`
	TokenInfo *TokenInfo `json:"token_info,omitempty"`
}

// Processor handles transcript file processing
type Processor struct {
	windowSize time.Duration
}

// NewProcessor creates a new transcript processor
func NewProcessor() *Processor {
	return &Processor{
		windowSize: 60 * time.Second,
	}
}

// ProcessIncremental processes transcript from a specific offset with cumulative token tracking
func (p *Processor) ProcessIncremental(transcriptPath string, lastOffset int64, baseTokens int64) (*SessionMetrics, error) {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Get file size
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := stat.Size()

	// If lastOffset is beyond current file size, file was likely rotated/cleared
	if lastOffset > fileSize {
		lastOffset = 0
		baseTokens = 0
	}

	// Seek to the last processed position
	if _, err := file.Seek(lastOffset, io.SeekStart); err != nil {
		return nil, err
	}

	// Initialize metrics with base tokens
	metrics := &SessionMetrics{
		MessageHistory: make([]MessageEvent, 0),
		SessionStartAt: time.Time{},
		TotalTokens:    baseTokens,   // Start with cumulative tokens
		MaxContextSize: baseTokens,   // Initialize with existing context size from previous processing
		
		// Initialize consumption tracking at zero (will accumulate from current processing)
		CumulativeInputTokens:        0,
		CumulativeOutputTokens:       0,
		CumulativeCacheCreationTokens: 0,
		CumulativeCacheReadTokens:     0,
	}

	parser := NewParser()
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large JSON lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max token size
	currentOffset := lastOffset
	newTokens := int64(0)

	for scanner.Scan() {
		line := scanner.Text()
		lineSize := int64(len(line) + 1) // +1 for newline
		
		event, err := parser.ParseTranscriptLine(line)
		if err != nil {
			currentOffset += lineSize
			continue // Skip invalid lines
		}
		if event == nil {
			currentOffset += lineSize
			continue // Skip non-message lines
		}

		// Update metrics with this event (only count new tokens)
		p.updateMetricsIncremental(metrics, event, &newTokens)
		
		// Extract model information from the line
		if model := p.extractModelFromLine(line, parser); model != "" {
			metrics.ModelName = model
		}

		currentOffset += lineSize
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading transcript: %w", err)
	}

	// Update total tokens based on max context size if available, otherwise use accumulation
	if metrics.MaxContextSize > 0 {
		metrics.TotalTokens = metrics.MaxContextSize
	} else {
		metrics.TotalTokens = baseTokens + newTokens
	}

	// Store the new offset for next processing
	metrics.NewOffset = currentOffset

	// Calculate final metrics
	p.calculateFinalMetrics(metrics)

	return metrics, nil
}

// TailAndProcess reads the last ~64KB of transcript and processes new entries
func (p *Processor) TailAndProcess(transcriptPath string) (*SessionMetrics, error) {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Get file size
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := stat.Size()

	// Read from the last 64KB or beginning of file
	const tailSize = 64 * 1024
	var startPos int64 = 0
	if fileSize > tailSize {
		startPos = fileSize - tailSize
	}

	// Seek to the start position
	if _, err := file.Seek(startPos, io.SeekStart); err != nil {
		return nil, err
	}

	// Initialize metrics
	metrics := &SessionMetrics{
		MessageHistory: make([]MessageEvent, 0),
		SessionStartAt: time.Time{},
	}

	parser := NewParser()
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large JSON lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024) // 1MB max token size
	
	// Skip the first potentially incomplete line if we're not at the beginning
	if startPos > 0 && scanner.Scan() {
		// Skip first line as it might be incomplete
	}

	for scanner.Scan() {
		line := scanner.Text()
		event, err := parser.ParseTranscriptLine(line)
		if err != nil {
			continue // Skip invalid lines
		}
		if event == nil {
			continue // Skip non-message lines
		}

		// Update metrics with this event
		p.updateMetrics(metrics, event)
		
		// Extract model information from the line
		if model := p.extractModelFromLine(line, parser); model != "" {
			metrics.ModelName = model
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading transcript: %w", err)
	}

	// Calculate final metrics
	p.calculateFinalMetrics(metrics)

	return metrics, nil
}

// updateMetrics updates session metrics with a new event
func (p *Processor) updateMetrics(metrics *SessionMetrics, event *MessageEvent) {
	// Add to message history with trimming to prevent unbounded growth
	metrics.MessageHistory = append(metrics.MessageHistory, *event)
	p.trimMessageHistory(metrics)
	
	// Update session start time if this is the first message or earlier
	if metrics.SessionStartAt.IsZero() || event.Timestamp.Before(metrics.SessionStartAt) {
		metrics.SessionStartAt = event.Timestamp
	}
	
	// Update last message time
	if event.Timestamp.After(metrics.LastMessageAt) {
		metrics.LastMessageAt = event.Timestamp
	}
	
	// Dual tracking: context size + consumption (ccusage-compatible)
	if event.TokenInfo != nil {
		// Update consumption metrics (ccusage-style) - accumulate all tokens
		metrics.CumulativeInputTokens += event.TokenInfo.InputTokens
		metrics.CumulativeOutputTokens += event.TokenInfo.OutputTokens
		metrics.CumulativeCacheCreationTokens += event.TokenInfo.CacheCreationInputTokens
		metrics.CumulativeCacheReadTokens += event.TokenInfo.CacheReadInputTokens
		
		// Update context tracking (Irrlicht-specific) - only for assistant messages
		if event.Role == "assistant" {
			// For assistant messages, full context includes all input token types
			fullContext := event.TokenInfo.InputTokens + event.TokenInfo.CacheCreationInputTokens + event.TokenInfo.CacheReadInputTokens
			if fullContext > metrics.MaxContextSize {
				metrics.MaxContextSize = fullContext
			}
			// Use context size for TotalTokens (context window monitoring)
			metrics.TotalTokens = metrics.MaxContextSize
		} else if metrics.MaxContextSize > 0 {
			// For other messages, maintain established context size
			metrics.TotalTokens = metrics.MaxContextSize
		}
	}
	// If no context established yet (MaxContextSize == 0), TotalTokens remains 0
	
	// Update model name if present
	if event.Type == "model_info" && metrics.ModelName == "" {
		// Model name would be extracted by the parser
	}
	
	// Update event counts
	metrics.TotalEventCount++
	
	// Update recent event count (last 5 minutes)
	now := time.Now()
	windowStart := now.Add(-5 * time.Minute)
	if event.Timestamp.After(windowStart) {
		metrics.RecentEventCount++
		if metrics.RecentEventWindowStart.IsZero() {
			metrics.RecentEventWindowStart = windowStart
		}
	}
}

// updateMetricsIncremental updates session metrics with a new event for incremental processing
func (p *Processor) updateMetricsIncremental(metrics *SessionMetrics, event *MessageEvent, newTokens *int64) {
	// Add to message history with trimming to prevent unbounded growth
	metrics.MessageHistory = append(metrics.MessageHistory, *event)
	p.trimMessageHistory(metrics)
	
	// Update session start time if this is the first message or earlier
	if metrics.SessionStartAt.IsZero() || event.Timestamp.Before(metrics.SessionStartAt) {
		metrics.SessionStartAt = event.Timestamp
	}
	
	// Update last message time
	if event.Timestamp.After(metrics.LastMessageAt) {
		metrics.LastMessageAt = event.Timestamp
	}
	
	// Dual tracking for incremental processing
	if event.TokenInfo != nil {
		// Update consumption metrics (ccusage-style) - track new tokens
		*newTokens += event.TokenInfo.TotalTokens
		
		// Update consumption accumulators
		metrics.CumulativeInputTokens += event.TokenInfo.InputTokens
		metrics.CumulativeOutputTokens += event.TokenInfo.OutputTokens
		metrics.CumulativeCacheCreationTokens += event.TokenInfo.CacheCreationInputTokens
		metrics.CumulativeCacheReadTokens += event.TokenInfo.CacheReadInputTokens
		
		// Update context tracking (Irrlicht-specific) - only for assistant messages
		if event.Role == "assistant" {
			oldMaxContext := metrics.MaxContextSize
			// For assistant messages, full context includes all input token types
			fullContext := event.TokenInfo.InputTokens + event.TokenInfo.CacheCreationInputTokens + event.TokenInfo.CacheReadInputTokens
			if fullContext > metrics.MaxContextSize {
				metrics.MaxContextSize = fullContext
				// Context growth affects the incremental count
				contextGrowth := fullContext - oldMaxContext
				*newTokens = contextGrowth // Override with context growth instead of total
			}
		}
	}
	
	// Update model name if present
	if event.Type == "model_info" && metrics.ModelName == "" {
		// Model name would be extracted by the parser
	}
	
	// Update event counts
	metrics.TotalEventCount++
	
	// Update recent event count (last 5 minutes)
	now := time.Now()
	windowStart := now.Add(-5 * time.Minute)
	if event.Timestamp.After(windowStart) {
		metrics.RecentEventCount++
		if metrics.RecentEventWindowStart.IsZero() {
			metrics.RecentEventWindowStart = windowStart
		}
	}
}

// calculateFinalMetrics computes derived metrics after processing all events
func (p *Processor) calculateFinalMetrics(metrics *SessionMetrics) {
	if metrics.SessionStartAt.IsZero() {
		return
	}
	
	// Calculate elapsed time
	endTime := metrics.LastMessageAt
	if endTime.IsZero() {
		endTime = time.Now()
	}
	
	elapsed := endTime.Sub(metrics.SessionStartAt)
	metrics.ElapsedSeconds = int64(elapsed.Seconds())
	
	// Calculate messages per minute
	if metrics.ElapsedSeconds > 0 {
		minutes := float64(metrics.ElapsedSeconds) / 60.0
		metrics.MessagesPerMinute = float64(len(metrics.MessageHistory)) / minutes
	}
	
	// Calculate context utilization
	p.calculateContextUtilization(metrics)
}

// CalculateTranscriptChecksum calculates a checksum of the first 1KB of the transcript
// This helps detect when the transcript file has been rotated or cleared
func (p *Processor) CalculateTranscriptChecksum(transcriptPath string) (string, error) {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Read first 1KB for checksum
	buffer := make([]byte, 1024)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", err
	}

	// Calculate SHA256 hash
	hash := sha256.Sum256(buffer[:n])
	return hex.EncodeToString(hash[:]), nil
}

// FileExists checks if a file exists
func (p *Processor) FileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// GetFileSize returns the size of a file
func (p *Processor) GetFileSize(path string) (int64, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return stat.Size(), nil
}

// GetLastModified returns the last modification time of a file
func (p *Processor) GetLastModified(path string) (time.Time, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return stat.ModTime(), nil
}

// extractModelFromLine extracts model information from a transcript line
func (p *Processor) extractModelFromLine(line string, parser *Parser) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	// Try to parse as JSON for model information
	if strings.HasPrefix(line, "{") {
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err == nil {
			return parser.ExtractModelFromJSON(data)
		}
	}

	return ""
}

// trimMessageHistory trims the message history to prevent unbounded memory growth
func (p *Processor) trimMessageHistory(metrics *SessionMetrics) {
	if len(metrics.MessageHistory) > MaxMessageHistory {
		// Keep only the most recent MaxMessageHistory messages
		keep := len(metrics.MessageHistory) - MaxMessageHistory
		metrics.MessageHistory = metrics.MessageHistory[keep:]
	}
}

// calculateContextUtilization calculates context utilization percentage and pressure level
func (p *Processor) calculateContextUtilization(metrics *SessionMetrics) {
	// Use MaxContextSize for utilization calculation, fallback to TotalTokens if not available
	contextTokens := metrics.MaxContextSize
	if contextTokens == 0 {
		contextTokens = metrics.TotalTokens
	}
	
	if contextTokens == 0 || metrics.ModelName == "" {
		// Set defaults when we can't compute utilization
		metrics.ContextUtilization = 0.0
		metrics.PressureLevel = "unknown"
		return
	}
	
	// Context utilization calculation adjusted for autocompaction
	// Claude Code autocompacts at ~155K tokens
	effectiveContextWindow := int64(155000)
	utilizationPercentage := (float64(contextTokens) / float64(effectiveContextWindow)) * 100
	
	// Determine pressure level based on proximity to autocompaction
	pressureLevel := "low"
	if utilizationPercentage >= 90 {
		pressureLevel = "critical"
	} else if utilizationPercentage >= 80 {
		pressureLevel = "high"
	} else if utilizationPercentage >= 60 {
		pressureLevel = "medium"
	}
	
	metrics.ContextUtilization = utilizationPercentage
	metrics.PressureLevel = pressureLevel
}