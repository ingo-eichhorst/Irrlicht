package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
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
}

// MessageEvent represents a parsed transcript message
type MessageEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Role      string    `json:"role,omitempty"`
	Tokens    int64     `json:"tokens,omitempty"`
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
	// Add to message history
	metrics.MessageHistory = append(metrics.MessageHistory, *event)
	
	// Update session start time if this is the first message or earlier
	if metrics.SessionStartAt.IsZero() || event.Timestamp.Before(metrics.SessionStartAt) {
		metrics.SessionStartAt = event.Timestamp
	}
	
	// Update last message time
	if event.Timestamp.After(metrics.LastMessageAt) {
		metrics.LastMessageAt = event.Timestamp
	}
	
	// Update token count
	metrics.TotalTokens += event.Tokens
	
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

// calculateContextUtilization calculates context utilization percentage and pressure level
func (p *Processor) calculateContextUtilization(metrics *SessionMetrics) {
	if metrics.TotalTokens == 0 || metrics.ModelName == "" {
		// Set defaults when we can't compute utilization
		metrics.ContextUtilization = 0.0
		metrics.PressureLevel = "unknown"
		return
	}
	
	// Context utilization calculation adjusted for autocompaction
	// Claude Code autocompacts at ~155K tokens
	effectiveContextWindow := int64(155000)
	utilizationPercentage := (float64(metrics.TotalTokens) / float64(effectiveContextWindow)) * 100
	
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