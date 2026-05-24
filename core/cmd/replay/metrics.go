package main

import (
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// transitionFromMetrics builds a transition populated with classifier snapshot
// fields from domainMetrics. Callers supply the event-specific fields.
func transitionFromMetrics(eventIdx int, virtTime time.Time, cause transitionCause, prevState, newState, reason string, m *session.SessionMetrics) transition {
	return transition{
		EventIndex:    eventIdx,
		VirtualTime:   virtTime,
		Cause:         cause,
		PrevState:     prevState,
		NewState:      newState,
		Reason:        reason,
		LastEventType: m.LastEventType,
		HasOpenTool:   m.HasOpenToolCall,
		OpenToolNames: copyStrings(m.LastOpenToolNames),
		IsAgentDone:   m.IsAgentDone(),
		NeedsAttn:     m.NeedsUserAttention(),
		WaitingQuery:  m.IsWaitingForUserInput(),
		LastTextHead:  head(m.LastAssistantText, 80),
	}
}

// finalizeSummary fills the report's computed summary fields (consumed events,
// transitions, flickers, cost) from the replay state. Both replay and
// replayWithSidecar call this at the end to avoid duplicating the logic.
func finalizeSummary(report *replayReport, consumed int, stateDurations map[string]time.Duration, lastMetrics *tailer.SessionMetrics) {
	report.Summary.ConsumedEvents = consumed
	report.Summary.TotalTransitions = len(report.Transitions)
	report.Summary.StateDurations = stateDurations

	flickerCat, flickerReason, flickerTotal := computeFlickers(
		report.Transitions, report.Settings.FlickerMaxDuration)
	report.Summary.FlickerCount = flickerTotal
	report.Summary.FlickersByCategory = flickerCat
	report.Summary.FlickersByReason = flickerReason

	if lastMetrics != nil {
		report.Summary.EstimatedCostUSD = lastMetrics.EstimatedCostUSD
		report.Summary.CumInputTokens = lastMetrics.CumInputTokens
		report.Summary.CumOutputTokens = lastMetrics.CumOutputTokens
		report.Summary.CumCacheReadTokens = lastMetrics.CumCacheReadTokens
		report.Summary.CumCacheCreationTokens = lastMetrics.CumCacheCreationTokens
		report.Summary.ModelName = lastMetrics.ModelName
	}
}

// computeFlickers counts short-lived X→Y→X sandwich patterns.
func computeFlickers(trs []transition, maxDur time.Duration) (map[string]int, map[string]int, int) {
	byCategory := map[string]int{}
	byReason := map[string]int{}
	if len(trs) < 3 || maxDur <= 0 {
		return byCategory, byReason, 0
	}
	total := 0
	for i := 1; i < len(trs)-1; i++ {
		prev, cur, next := trs[i-1], trs[i], trs[i+1]
		if prev.NewState == cur.NewState || cur.NewState == next.NewState {
			continue
		}
		if prev.NewState != next.NewState {
			continue
		}
		dur := next.VirtualTime.Sub(cur.VirtualTime)
		// Zero-duration sandwiches are same-batch replay artifacts — skip so
		// flicker counts reflect what the UI actually sees.
		if dur <= 0 || dur >= maxDur {
			continue
		}
		byCategory[cur.NewState+"_between_"+prev.NewState]++
		reason := cur.Reason
		if reason == "" {
			reason = "(no reason)"
		}
		byReason[reason]++
		total++
	}
	return byCategory, byReason, total
}

func copyStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
