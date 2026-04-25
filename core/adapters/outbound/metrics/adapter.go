package metrics

import (
	"os"
	"sync"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// lockedTailer wraps a TranscriptTailer with its own mutex so that
// concurrent ComputeMetrics calls for different sessions don't block
// each other — only calls for the same transcript path serialize.
type lockedTailer struct {
	mu sync.Mutex
	t  *tailer.TranscriptTailer
	lp string // path to the session ledger file; empty when ledger dir is unavailable
}

// Adapter implements ports/outbound.MetricsCollector using the transcript-tailer package.
// It caches TranscriptTailer instances per path so that lastOffset-based
// incremental reads work across calls, avoiding re-parsing the full 64KB tail.
type Adapter struct {
	mu          sync.Mutex // protects the tailers map only
	tailers     map[string]*lockedTailer
	parsers     map[string]agents.ParserFactory
	subagents   map[string]agents.SubagentCounter
	fallback    agents.ParserFactory // used for unknown adapter names
}

// New returns a new metrics Adapter configured from the given agent
// registrations. The adapter uses cfgs[0].NewParser as the fallback parser for
// unknown adapter names to preserve the historical "default to Claude Code"
// behavior; callers should pass the Claude Code config first.
func New(cfgs []agents.Config) *Adapter {
	parsers := make(map[string]agents.ParserFactory, len(cfgs))
	subs := make(map[string]agents.SubagentCounter, len(cfgs))
	var fallback agents.ParserFactory
	for i, c := range cfgs {
		parsers[c.Name] = c.NewParser
		if c.CountOpenSubagents != nil {
			subs[c.Name] = c.CountOpenSubagents
		}
		if i == 0 {
			fallback = c.NewParser
		}
	}
	return &Adapter{
		tailers:   make(map[string]*lockedTailer),
		parsers:   parsers,
		subagents: subs,
		fallback:  fallback,
	}
}

// parserFor returns a fresh TranscriptParser for the given adapter name,
// falling back to the first registered adapter's parser for unknown names.
func (a *Adapter) parserFor(name string) tailer.TranscriptParser {
	if f, ok := a.parsers[name]; ok {
		return f()
	}
	if a.fallback != nil {
		return a.fallback()
	}
	return nil
}

// countOpenSubagents returns the adapter-specific count of in-process child
// agents currently running, or zero when the adapter did not register a counter.
func (a *Adapter) countOpenSubagents(name string, m *tailer.SessionMetrics) int {
	if f, ok := a.subagents[name]; ok {
		return f(m)
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
		t := tailer.NewTranscriptTailer(transcriptPath, a.parserFor(adapter), adapter)
		lp := ledgerPath(transcriptPath)
		if s := loadLedger(lp); s != nil {
			t.SetLedgerState(*s)
		}
		lt = &lockedTailer{t: t, lp: lp}
		a.tailers[transcriptPath] = lt
	}
	a.mu.Unlock()

	// Per-tailer lock: serializes calls for the same path but allows
	// different sessions to process concurrently.
	lt.mu.Lock()
	m, err := lt.t.TailAndProcess()
	if err == nil && m != nil {
		saveLedger(lt.lp, lt.t.GetLedgerState())
	}
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
		ContextWindowUnknown:   m.ContextWindowUnknown,
		HasOpenToolCall:        m.HasOpenToolCall,
		OpenToolCallCount:      m.OpenToolCallCount,
		OpenSubagents:          a.countOpenSubagents(adapter, m),
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
		SawUserBlockingToolClosedThisPass: m.SawUserBlockingToolClosedThisPass,
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
	result.Tasks = tailerTasksToDomain(m.Tasks)
	if result.ModelName == "" {
		result.ModelName = "unknown"
	}
	if result.PressureLevel == "" {
		result.PressureLevel = "unknown"
	}
	return result, nil
}

// PruneEntry releases per-session state when a session ends: drops the
// in-memory tailer cache entry and removes the on-disk ledger file. Idempotent
// on a missing transcript path or already-removed ledger file. Silent on I/O
// errors — the ledger is best-effort cache, not authoritative state.
//
// We don't take the per-tailer lock — caller invariant is that PruneEntry runs
// in response to EventRemoved (transcript file gone), so any concurrent
// TailAndProcess returns nil metrics without saving. If a save did race in,
// the daemon-startup orphan sweep would clean it up on next restart.
func (a *Adapter) PruneEntry(transcriptPath string) {
	if transcriptPath == "" {
		return
	}
	a.mu.Lock()
	delete(a.tailers, transcriptPath)
	a.mu.Unlock()
	if lp := ledgerPath(transcriptPath); lp != "" {
		_ = os.Remove(lp)
	}
}

// tailerTasksToDomain converts a tailer task slice to the domain mirror type.
func tailerTasksToDomain(src []tailer.Task) []session.Task {
	if len(src) == 0 {
		return nil
	}
	dst := make([]session.Task, len(src))
	for i, t := range src {
		dst[i] = session.Task{
			ID:          t.ID,
			Subject:     t.Subject,
			Description: t.Description,
			ActiveForm:  t.ActiveForm,
			Status:      t.Status,
		}
	}
	return dst
}
