package metrics

import (
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// Adapter implements ports/outbound.MetricsCollector using the transcript-tailer package.
type Adapter struct{}

// New returns a new metrics Adapter.
func New() *Adapter { return &Adapter{} }

// ComputeMetrics analyses the transcript at transcriptPath and returns session metrics.
// Returns (nil, nil) when the transcript doesn't exist yet or yields no data.
func (a *Adapter) ComputeMetrics(transcriptPath string) (*session.SessionMetrics, error) {
	if transcriptPath == "" {
		return nil, nil
	}
	t := tailer.NewTranscriptTailer(transcriptPath)
	m, err := t.TailAndProcess()
	if err != nil || m == nil {
		return nil, nil //nolint:nilerr — absent transcript is not an error
	}
	result := &session.SessionMetrics{
		ElapsedSeconds:     m.ElapsedSeconds,
		TotalTokens:        m.TotalTokens,
		ModelName:          m.ModelName,
		ContextUtilization: m.ContextUtilization,
		PressureLevel:      m.PressureLevel,
		HasOpenToolCall:    m.HasOpenToolCall,
		OpenToolCallCount:  m.OpenToolCallCount,
	}
	if result.ModelName == "" {
		result.ModelName = "unknown"
	}
	if result.PressureLevel == "" {
		result.PressureLevel = "unknown"
	}
	return result, nil
}
