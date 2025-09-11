package metrics

import (
	"sync"
	"time"
)

// SessionMetrics holds computed performance metrics from transcript analysis  
type SessionMetrics struct {
	ElapsedSeconds       int64   `json:"elapsed_seconds"`
	TotalTokens          int64   `json:"total_tokens"`           // Context size (for monitoring)
	ModelName            string  `json:"model_name"`
	ContextUtilization   float64 `json:"context_utilization_percentage"`
	PressureLevel        string  `json:"pressure_level"`
	
	// ccusage-compatible consumption metrics
	CumulativeInputTokens        int64   `json:"cumulative_input_tokens,omitempty"`
	CumulativeOutputTokens       int64   `json:"cumulative_output_tokens,omitempty"`
	CumulativeCacheCreationTokens int64  `json:"cumulative_cache_creation_tokens,omitempty"`
	CumulativeCacheReadTokens     int64  `json:"cumulative_cache_read_tokens,omitempty"`
	TotalConsumptionTokens       int64   `json:"total_consumption_tokens,omitempty"` // Sum of all consumption
}

// NewSessionMetrics creates a new SessionMetrics instance
func NewSessionMetrics() *SessionMetrics {
	return &SessionMetrics{
		PressureLevel: "unknown",
	}
}

// PressureLevel represents the context window pressure level
type PressureLevel string

const (
	PressureLow      PressureLevel = "low"
	PressureMedium   PressureLevel = "medium"
	PressureHigh     PressureLevel = "high"
	PressureCritical PressureLevel = "critical"
	PressureUnknown  PressureLevel = "unknown"
)

// DeterminePressureLevel calculates the pressure level based on context utilization
func DeterminePressureLevel(contextUtilization float64) PressureLevel {
	switch {
	case contextUtilization >= 90.0:
		return PressureCritical
	case contextUtilization >= 75.0:
		return PressureHigh
	case contextUtilization >= 50.0:
		return PressureMedium
	case contextUtilization >= 0.0:
		return PressureLow
	default:
		return PressureUnknown
	}
}

// SetPressureLevel updates the pressure level based on context utilization
func (sm *SessionMetrics) SetPressureLevel() {
	sm.PressureLevel = string(DeterminePressureLevel(sm.ContextUtilization))
}

// Update updates the metrics with new values
func (sm *SessionMetrics) Update(elapsedSeconds, totalTokens int64, modelName string, contextUtilization float64) {
	sm.ElapsedSeconds = elapsedSeconds
	sm.TotalTokens = totalTokens
	sm.ModelName = modelName
	sm.ContextUtilization = contextUtilization
	sm.SetPressureLevel()
	
	// Calculate total consumption tokens (ccusage-compatible)
	sm.TotalConsumptionTokens = sm.CumulativeInputTokens + sm.CumulativeOutputTokens + 
		sm.CumulativeCacheCreationTokens + sm.CumulativeCacheReadTokens
}

// IsValid checks if the metrics have valid values
func (sm *SessionMetrics) IsValid() bool {
	return sm.ElapsedSeconds >= 0 && sm.TotalTokens >= 0 && sm.ContextUtilization >= 0
}

// MetricsCalculator defines the interface for calculating session metrics
type MetricsCalculator interface {
	CalculateMetrics(transcriptPath string, existingMetrics *SessionMetrics) (*SessionMetrics, error)
}

// TranscriptAnalyzer defines the interface for transcript analysis
type TranscriptAnalyzer interface {
	AnalyzeTranscript(path string) (*AnalysisResult, error)
}

// AnalysisResult holds the results of transcript analysis
type AnalysisResult struct {
	ElapsedSeconds     int64
	TotalTokens        int64
	ModelName          string
	ContextUtilization float64
	SessionStartTime   *time.Time
	LastActivityTime   *time.Time
}

// ToSessionMetrics converts the analysis result to session metrics
func (ar *AnalysisResult) ToSessionMetrics() *SessionMetrics {
	metrics := &SessionMetrics{
		ElapsedSeconds:     ar.ElapsedSeconds,
		TotalTokens:        ar.TotalTokens,
		ModelName:          ar.ModelName,
		ContextUtilization: ar.ContextUtilization,
	}
	metrics.SetPressureLevel()
	return metrics
}

// SystemMetrics tracks overall system performance
type SystemMetrics struct {
	EventsProcessed int64
	TotalLatencyMs  int64
	LastEventTime   time.Time
	mu              *sync.Mutex
}

// NewSystemMetrics creates a new SystemMetrics instance
func NewSystemMetrics() *SystemMetrics {
	return &SystemMetrics{
		LastEventTime: time.Now(),
		mu:            &sync.Mutex{},
	}
}

// RecordEvent records metrics for a processed event
func (sm *SystemMetrics) RecordEvent(processingTime time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	sm.EventsProcessed++
	sm.TotalLatencyMs += processingTime.Milliseconds()
	sm.LastEventTime = time.Now()
}

// GetAverageLatencyMs returns the average processing latency
func (sm *SystemMetrics) GetAverageLatencyMs() float64 {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if sm.EventsProcessed == 0 {
		return 0
	}
	return float64(sm.TotalLatencyMs) / float64(sm.EventsProcessed)
}

// GetStats returns current system statistics
func (sm *SystemMetrics) GetStats() Stats {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	return Stats{
		EventsProcessed:  sm.EventsProcessed,
		AverageLatencyMs: sm.GetAverageLatencyMs(),
		LastEventTime:    sm.LastEventTime,
	}
}

// Stats holds statistical information
type Stats struct {
	EventsProcessed  int64
	AverageLatencyMs float64
	LastEventTime    time.Time
}

// MetricsCollector defines the interface for collecting metrics
type MetricsCollector interface {
	RecordEventProcessing(eventType string, duration time.Duration)
	RecordError(eventType string, errorType string)
	GetCurrentStats() Stats
}

