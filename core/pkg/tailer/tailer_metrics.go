package tailer

import (
	"strings"
	"time"
)

func (t *TranscriptTailer) applyMetadata(parsed *ParsedEvent) {
	t.applyModelMetadata(parsed)
	t.applyTokenSnapshot(parsed.Tokens)
	t.accumulateTokens(parsed)
	if parsed.CWD != "" {
		t.lastCWD = parsed.CWD
	}
	if parsed.PermissionMode != "" {
		t.metrics.PermissionMode = parsed.PermissionMode
	}
}

// applyModelMetadata records the model name (with the [1m] extended-context
// suffix triggering a 1M context-window override) and an adapter-supplied
// context-window override.
func (t *TranscriptTailer) applyModelMetadata(parsed *ParsedEvent) {
	if parsed.ModelName != "" {
		if strings.Contains(parsed.ModelName, "[1m]") {
			t.contextWindowOverride = 1000000
		}
		t.metrics.ModelName = NormalizeModelName(parsed.ModelName)
	}
	if parsed.ContextWindow > 0 {
		t.contextWindowOverride = parsed.ContextWindow
	}
}

// applyTokenSnapshot updates the latest-turn snapshot fields used for
// context utilization. Cumulative accumulation is handled separately.
func (t *TranscriptTailer) applyTokenSnapshot(tokens *TokenSnapshot) {
	if tokens == nil {
		return
	}
	if tokens.Total > 0 {
		t.metrics.TotalTokens = tokens.Total
	}
	if tokens.Input > 0 || tokens.Output > 0 {
		t.inputTokens = tokens.Input
		t.outputTokens = tokens.Output
		t.cacheReadTokens = tokens.CacheRead
		t.cacheCreationTokens = tokens.CacheCreation
	}
}

// accumulateTokens dispatches to the appropriate cumulative-token path.
// In priority order: new per-turn contribution > Codex cumulative-total >
// Claude Code requestID dedup > Pi-style direct add.
func (t *TranscriptTailer) accumulateTokens(parsed *ParsedEvent) {
	switch {
	case parsed.Contribution != nil:
		t.applyContribution(parsed.Contribution)
	case parsed.CumulativeTokens != nil:
		t.advanceCumulativeTotal(parsed.CumulativeTokens)
	case parsed.Tokens != nil && parsed.RequestID != "":
		t.rollForwardRequestID(parsed.RequestID, parsed.Tokens)
	case parsed.Tokens != nil && (parsed.Tokens.Input > 0 || parsed.Tokens.Output > 0):
		t.addTokenDelta(parsed.Tokens)
	}
}

// applyContribution handles the new Phase-2+ path where the adapter already
// deduped and handed us a finalized per-turn contribution. Provider-reported
// USD cost wins; otherwise we accumulate tokens into the per-model breakdown.
func (t *TranscriptTailer) applyContribution(c *PerTurnContribution) {
	if c.ProviderCostUSD != nil {
		t.cumProviderCostUSD += *c.ProviderCostUSD
		return
	}
	if c.Model == "" && c.Usage.Input == 0 && c.Usage.Output == 0 {
		return
	}
	bd := t.cumByModel[c.Model]
	if bd == nil {
		bd = &UsageBreakdown{}
		t.cumByModel[c.Model] = bd
	}
	bd.Input += c.Usage.Input
	bd.Output += c.Usage.Output
	bd.CacheRead += c.Usage.CacheRead
	bd.CacheCreation5m += c.Usage.CacheCreation5m
	bd.CacheCreation1h += c.Usage.CacheCreation1h
}

// advanceCumulativeTotal is the Codex-style path: each bucket only moves
// forward, ensuring we never regress on a late out-of-order event.
func (t *TranscriptTailer) advanceCumulativeTotal(ct *TokenSnapshot) {
	if ct.Input > t.cumInputTokens {
		t.cumInputTokens = ct.Input
	}
	if ct.Output > t.cumOutputTokens {
		t.cumOutputTokens = ct.Output
	}
	if ct.CacheRead > t.cumCacheReadTokens {
		t.cumCacheReadTokens = ct.CacheRead
	}
	if ct.CacheCreation > t.cumCacheCreationTokens {
		t.cumCacheCreationTokens = ct.CacheCreation
	}
	t.pendingSnapshot = nil
}

// rollForwardRequestID is the Claude Code legacy path: when the requestID
// changes, flush the previous turn's pending snapshot into cum totals before
// replacing it with the fresh one.
func (t *TranscriptTailer) rollForwardRequestID(reqID string, tokens *TokenSnapshot) {
	if reqID != t.lastRequestID && t.lastRequestID != "" && t.pendingSnapshot != nil {
		t.addTokenDelta(t.pendingSnapshot)
	}
	t.pendingSnapshot = tokens
	t.lastRequestID = reqID
}

// addTokenDelta adds one snapshot into the cum-by-bucket counters. Shared
// by the Pi direct-add and Claude Code pending-flush paths.
func (t *TranscriptTailer) addTokenDelta(u *TokenSnapshot) {
	t.cumInputTokens += u.Input
	t.cumOutputTokens += u.Output
	t.cumCacheReadTokens += u.CacheRead
	t.cumCacheCreationTokens += u.CacheCreation
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

// computeCumulativeTokens aggregates per-model token counts and estimated cost.
// It must run on every TailAndProcess pass — even when no new events were
// processed — so that ledger-rehydrated state is reflected immediately.
func (t *TranscriptTailer) computeCumulativeTokens() {
	if len(t.cumByModel) > 0 || t.cumProviderCostUSD > 0 {
		// New path: price per-model, sum provider-reported costs.
		var totalInput, totalOutput, totalCacheRead, totalCacheCreate int64
		var pricedCost float64
		for modelName, bd := range t.cumByModel {
			totalInput += bd.Input
			totalOutput += bd.Output
			totalCacheRead += bd.CacheRead
			totalCacheCreate += bd.CacheCreation5m + bd.CacheCreation1h
			if t.capacityMgr != nil {
				pricedCost += t.capacityMgr.EstimateCostFromBreakdown(
					modelName, bd.Input, bd.Output, bd.CacheRead, bd.CacheCreation5m, bd.CacheCreation1h)
			}
		}
		// Include the pending contribution from stateful parsers (Claude Code).
		if pc, ok := t.parser.(pendingContributor); ok {
			if pending := pc.PendingContribution(); pending != nil {
				totalInput += pending.Usage.Input
				totalOutput += pending.Usage.Output
				totalCacheRead += pending.Usage.CacheRead
				totalCacheCreate += pending.Usage.CacheCreation5m + pending.Usage.CacheCreation1h
				if t.capacityMgr != nil && pending.Model != "" {
					pricedCost += t.capacityMgr.EstimateCostFromBreakdown(
						pending.Model,
						pending.Usage.Input, pending.Usage.Output, pending.Usage.CacheRead,
						pending.Usage.CacheCreation5m, pending.Usage.CacheCreation1h)
				}
			}
		}
		t.metrics.CumInputTokens = totalInput
		t.metrics.CumOutputTokens = totalOutput
		t.metrics.CumCacheReadTokens = totalCacheRead
		t.metrics.CumCacheCreationTokens = totalCacheCreate
		t.metrics.EstimatedCostUSD = pricedCost + t.cumProviderCostUSD
	} else {
		// Legacy path: scalar accumulators (testParser and pre-Contribution adapters).
		effectiveCumInput := t.cumInputTokens
		effectiveCumOutput := t.cumOutputTokens
		effectiveCumCacheRead := t.cumCacheReadTokens
		effectiveCumCacheCreate := t.cumCacheCreationTokens
		if t.pendingSnapshot != nil {
			effectiveCumInput += t.pendingSnapshot.Input
			effectiveCumOutput += t.pendingSnapshot.Output
			effectiveCumCacheRead += t.pendingSnapshot.CacheRead
			effectiveCumCacheCreate += t.pendingSnapshot.CacheCreation
		}
		t.metrics.CumInputTokens = effectiveCumInput
		t.metrics.CumOutputTokens = effectiveCumOutput
		t.metrics.CumCacheReadTokens = effectiveCumCacheRead
		t.metrics.CumCacheCreationTokens = effectiveCumCacheCreate

		if t.capacityMgr != nil && t.metrics.ModelName != "" {
			t.metrics.EstimatedCostUSD = t.capacityMgr.EstimateCostUSD(
				t.metrics.ModelName, effectiveCumInput, effectiveCumOutput,
				effectiveCumCacheRead, effectiveCumCacheCreate)
		}
	}
}

// computeMetrics calculates messages per minute and elapsed time
func (t *TranscriptTailer) computeMetrics() {
	// Cumulative cost/token aggregation must run regardless of whether any new
	// events were processed this pass — the tailer may have been rehydrated from
	// a ledger with a non-zero cumByModel and then polled with no new transcript
	// content (e.g., immediately after daemon restart before the agent produces
	// more output). Skipping this would return CumInputTokens=0 in that window.
	t.computeCumulativeTokens()

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

	// Open tool calls are derived directly from the id-keyed map — the only
	// source of truth. See the openToolCalls field comment for history (#102,
	// #114, #117).
	openCalls := len(t.openToolCalls)
	t.metrics.OpenToolCallCount = openCalls
	t.metrics.HasOpenToolCall = openCalls > 0
	names := make([]string, 0, openCalls)
	for _, name := range t.openToolCalls {
		names = append(names, name)
	}
	t.metrics.LastOpenToolNames = names
	t.metrics.LastWasUserInterrupt = t.lastWasUserInterrupt
	t.metrics.LastWasToolDenial = t.lastWasToolDenial
	t.metrics.LastCWD = t.lastCWD
	t.metrics.LastAssistantText = t.lastAssistantText
	if len(t.tasks) > 0 {
		t.metrics.Tasks = append([]Task(nil), t.tasks...)
	} else {
		t.metrics.Tasks = nil
	}

	// Token snapshot (latest turn — for context utilization display).
	t.metrics.InputTokens = t.inputTokens
	t.metrics.OutputTokens = t.outputTokens
	t.metrics.CacheReadTokens = t.cacheReadTokens
	t.metrics.CacheCreationTokens = t.cacheCreationTokens

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

// computeContextUtilization calculates context utilization percentage and pressure level.
//
// Note on ContextWindowUnknown: we deliberately do NOT clear the flag in the
// early-return path. A pre-tokens pass would otherwise transiently set it to
// false on every TailAndProcess call, producing flicker between
// `unknown=true` (after computation) and `unknown=false` (before). The flag
// is only set or cleared in branches that actually computed a window, so the
// last "real" answer is sticky. MergeMetrics also prefers the older true
// value over a fresh false (defense in depth).
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
		effectiveContextWindow = t.capacityMgr.GetModelCapacity(t.metrics.ModelName).ContextWindow
	}

	// No pricing for this model (capacity manager doesn't know it). We
	// intentionally do NOT invent a synthetic context window — guessing
	// wrong (e.g. 100k tokens against an assumed 32k) shows >100%
	// utilization which is more confusing than honest "unknown". Instead,
	// the macOS client uses the ContextWindowUnknown flag to render a
	// tokens-only label without a percentage, so the row still has signal
	// instead of silently hiding the column.
	if effectiveContextWindow <= 0 {
		t.metrics.ContextWindow = 0
		t.metrics.ContextUtilization = 0
		t.metrics.PressureLevel = "unknown"
		t.metrics.ContextWindowUnknown = true
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
	t.metrics.ContextWindowUnknown = false
}
