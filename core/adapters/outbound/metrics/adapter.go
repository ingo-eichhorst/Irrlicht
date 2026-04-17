package metrics

import (
	"sync"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/pi"
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

// parserFor returns the format-specific TranscriptParser for the given adapter name.
func parserFor(adapter string) tailer.TranscriptParser {
	switch adapter {
	case codex.AdapterName:
		return &codex.Parser{}
	case pi.AdapterName:
		return &pi.Parser{}
	default:
		return &claudecode.Parser{}
	}
}

// countOpenSubagents returns the adapter-specific count of in-process child
// agents currently running. Each adapter decides how to recognize subagents
// in its own transcript format; codex and pi currently report subagents as
// separate transcript sessions (tracked via ParentSessionID), so they return
// zero here and the domain-level ComputeSubagentSummary picks them up by
// walking file-based children.
func countOpenSubagents(adapter string, m *tailer.SessionMetrics) int {
	switch adapter {
	case claudecode.AdapterName:
		return claudecode.CountOpenSubagents(m)
	}
	return 0
}

// ComputeMetrics analyses the transcript at transcriptPath and returns session metrics.
// The adapter parameter selects the correct transcript parser.
// Returns (nil, nil) when the transcript doesn't exist yet or yields no data.
func (a *Adapter) ComputeMetrics(transcriptPath, adapter string) (*session.SessionMetrics, error) {
	if transcriptPath == "" {
		return nil, nil
	}
	a.mu.Lock()
	lt, ok := a.tailers[transcriptPath]
	if !ok {
		lt = &lockedTailer{t: tailer.NewTranscriptTailer(transcriptPath, parserFor(adapter), adapter)}
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
		ElapsedSeconds:         m.ElapsedSeconds,
		TotalTokens:            m.TotalTokens,
		ModelName:              m.ModelName,
		ContextWindow:          m.ContextWindow,
		ContextUtilization:     m.ContextUtilization,
		PressureLevel:          m.PressureLevel,
		HasOpenToolCall:        m.HasOpenToolCall,
		OpenToolCallCount:      m.OpenToolCallCount,
		OpenSubagents:          countOpenSubagents(adapter, m),
		LastEventType:          m.LastEventType,
		LastOpenToolNames:      m.LastOpenToolNames,
		LastWasUserInterrupt:   m.LastWasUserInterrupt,
		LastWasToolDenial:      m.LastWasToolDenial,
		EstimatedCostUSD:       m.EstimatedCostUSD,
		CumInputTokens:        m.CumInputTokens,
		CumOutputTokens:       m.CumOutputTokens,
		CumCacheReadTokens:    m.CumCacheReadTokens,
		CumCacheCreationTokens: m.CumCacheCreationTokens,
		LastCWD:                m.LastCWD,
		LastAssistantText:      m.LastAssistantText,
		PermissionMode:         m.PermissionMode,
	}
	if len(m.SubagentCompletions) > 0 {
		result.SubagentCompletions = make([]session.SubagentCompletion, len(m.SubagentCompletions))
		for i, c := range m.SubagentCompletions {
			result.SubagentCompletions[i] = session.SubagentCompletion{
				AgentID:   c.AgentID,
				ToolUseID: c.ToolUseID,
				Status:    c.Status,
			}
		}
	}
	if result.ModelName == "" {
		result.ModelName = "unknown"
	}
	if result.PressureLevel == "" {
		result.PressureLevel = "unknown"
	}
	return result, nil
}
