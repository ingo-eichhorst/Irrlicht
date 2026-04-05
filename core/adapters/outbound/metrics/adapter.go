package metrics

import (
	"sync"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// lockedTailer wraps a TranscriptTailer with its own mutex so that
// concurrent ComputeMetrics calls for different sessions don't block
// each other — only calls for the same transcript path serialize.
type lockedTailer struct {
	mu sync.Mutex
	t  *tailer.TranscriptTailer
}

// Adapter implements ports/outbound.MetricsCollector using the transcript-tailer package.
// It caches TranscriptTailer instances per path so that lastOffset-based
// incremental reads work across calls, avoiding re-parsing the full 64KB tail.
type Adapter struct {
	mu      sync.Mutex // protects the map only
	tailers map[string]*lockedTailer
}

// New returns a new metrics Adapter.
func New() *Adapter {
	return &Adapter{tailers: make(map[string]*lockedTailer)}
}

// RemoveTailer removes the cached tailer for a transcript path.
// Call when a session is removed to free resources.
func (a *Adapter) RemoveTailer(path string) {
	a.mu.Lock()
	delete(a.tailers, path)
	a.mu.Unlock()
}

// ComputeMetrics analyses the transcript at transcriptPath and returns session metrics.
// Returns (nil, nil) when the transcript doesn't exist yet or yields no data.
func (a *Adapter) ComputeMetrics(transcriptPath string) (*session.SessionMetrics, error) {
	if transcriptPath == "" {
		return nil, nil
	}
	// Get or create the per-path tailer (map lock held briefly).
	a.mu.Lock()
	lt, ok := a.tailers[transcriptPath]
	if !ok {
		lt = &lockedTailer{t: tailer.NewTranscriptTailer(transcriptPath)}
		a.tailers[transcriptPath] = lt
	}
	a.mu.Unlock()

	// Per-tailer lock: serializes calls for the same path but allows
	// different sessions to process concurrently.
	lt.mu.Lock()
	m, err := lt.t.TailAndProcess()
	lt.mu.Unlock()
	if err != nil || m == nil {
		return nil, nil //nolint:nilerr — absent transcript is not an error
	}
	result := &session.SessionMetrics{
		ElapsedSeconds:     m.ElapsedSeconds,
		TotalTokens:        m.TotalTokens,
		ModelName:          m.ModelName,
		ContextWindow:      m.ContextWindow,
		ContextUtilization: m.ContextUtilization,
		PressureLevel:      m.PressureLevel,
		HasOpenToolCall:    m.HasOpenToolCall,
		OpenToolCallCount:  m.OpenToolCallCount,
		LastEventType:          m.LastEventType,
		LastOpenToolNames:      m.LastOpenToolNames,
		LastToolResultWasError: m.LastToolResultWasError,
		EstimatedCostUSD:       m.EstimatedCostUSD,
		LastCWD:                m.LastCWD,
	}
	if result.ModelName == "" {
		result.ModelName = "unknown"
	}
	if result.PressureLevel == "" {
		result.PressureLevel = "unknown"
	}
	return result, nil
}
