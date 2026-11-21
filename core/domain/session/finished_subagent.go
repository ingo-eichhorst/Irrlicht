package session

import "slices"

// FinishedSubagent is the final usage of a child session the daemon has
// already removed (#2061). The daemon deletes a subagent's session once its
// parent's turn ends or the liveness sweep finds it finished, and the parent's
// own counts exclude it, so without this record the subagent's tokens and
// cost would vanish from every usage answer that lists the parent's
// subagents. It carries usage fields only: no prompt or response text.
type FinishedSubagent struct {
	SessionID       string `json:"session_id"`
	ParentSessionID string `json:"parent_session_id"`
	Adapter         string `json:"adapter,omitempty"`
	// FirstSeen is the child session's own FirstSeen, so a consumer can list
	// finished and live subagents in one order.
	FirstSeen int64 `json:"first_seen,omitempty"`
	// FinishedAt is when the daemon removed the session (Unix seconds); a
	// store's retention window counts from it.
	FinishedAt int64 `json:"finished_at"`
	// Usage is UsageSnapshot of the session's final metrics; nil when the
	// session had none.
	Usage *SessionMetrics `json:"usage,omitempty"`
}

// UsageSnapshot copies only the usage fields of m — model, elapsed time,
// cumulative tokens, cost and their provenance — for a FinishedSubagent.
// Everything else, the transcript-derived text above all, is left out.
// Returns nil for nil.
func UsageSnapshot(m *SessionMetrics) *SessionMetrics {
	if m == nil {
		return nil
	}
	out := &SessionMetrics{
		ModelName:              m.ModelName,
		ElapsedSeconds:         m.ElapsedSeconds,
		CumInputTokens:         m.CumInputTokens,
		CumOutputTokens:        m.CumOutputTokens,
		CumCacheReadTokens:     m.CumCacheReadTokens,
		CumCacheCreationTokens: m.CumCacheCreationTokens,
		EstimatedCostUSD:       m.EstimatedCostUSD,
		PricingSource:          m.PricingSource,
		UnpricedModels:         slices.Clone(m.UnpricedModels),
		TokensUnobserved:       m.TokensUnobserved,
	}
	if m.UsageCoverage != nil {
		cov := *m.UsageCoverage
		cov.TokenTypes = slices.Clone(m.UsageCoverage.TokenTypes)
		out.UsageCoverage = &cov
	}
	return out
}
