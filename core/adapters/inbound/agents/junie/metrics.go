package junie

import (
	"irrlicht/core/pkg/tailer"
)

// Metrics mapping for Junie's two metadata event kinds, both nested under
// SessionA2uxEvent.event.agentEvent:
//
//   - LlmResponseMetadataEvent — one per LLM call, carrying modelUsage[]
//     entries with model, provider-reported cost (USD), and token counts.
//     Unlike Junie's block events, metadata events are NEVER replayed at
//     task finalization (verified across 45 live sessions: every
//     COMPLETED-state metadata line is a distinct new call, not a re-emit of
//     an earlier one), so one event maps to exactly one PerTurnContribution
//     with no dedup needed.
//   - ContextWindowReportEvent — the running context footprint (used / size);
//     the redundant "percentage" field is ignored, the tailer derives its own.
//
// Both are Skip=true: pure bookkeeping that must not become the session's
// LastEventType or disturb the state machine. The tailer folds metadata from
// skipped events through applySkippedEvent → applyMetadata (#1798's routing).

// parseLlmMetadata maps one LlmResponseMetadataEvent onto a per-call cost
// contribution (the pi pattern: provider-reported cost is authoritative, so
// ProviderCostUSD is set whenever cost > 0 and token pricing is skipped) plus
// the latest-call token snapshot for display.
//
// Every captured event carries exactly one modelUsage entry; the loop sums a
// hypothetical multi-entry event (cost is additive; token attribution then
// follows the dominant entry) rather than dropping data the parser can't
// fully attribute. An all-zero entry with no cost produces no contribution.
func (p *Parser) parseLlmMetadata(agentEvent map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	entries, _ := agentEvent["modelUsage"].([]any)
	var usage tailer.UsageBreakdown
	var cost float64
	var model string
	var modelTokens int64
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		input := i64(entry, "inputTokens")
		cacheRead := i64(entry, "cacheInputTokens")
		cacheCreate := i64(entry, "cacheCreateTokens")
		usage.Input += input
		usage.Output += i64(entry, "outputTokens")
		usage.CacheRead += cacheRead
		// Junie doesn't distinguish cache-write TTLs; 5m is the default tier
		// (the pi cacheWrite precedent).
		usage.CacheCreation5m += cacheCreate
		cost += f64(entry, "cost")
		// The entry with the largest prompt-side footprint (every token the
		// model was shown: fresh, cache-read, cache-written) names the call's
		// model.
		if m := str(entry, "model"); m != "" && (model == "" || input+cacheRead+cacheCreate > modelTokens) {
			model = m
			modelTokens = input + cacheRead + cacheCreate
		}
	}
	if model == "" && cost == 0 && usage == (tailer.UsageBreakdown{}) {
		return
	}
	p.electModel(model, modelTokens, ev)
	ev.Contribution = &tailer.PerTurnContribution{Model: tailer.NormalizeModelName(model), Usage: usage}
	if cost > 0 {
		c := cost
		ev.Contribution.ProviderCostUSD = &c
	}
	if usage.Input > 0 || usage.Output > 0 {
		ev.Tokens = &tailer.TokenSnapshot{
			Input:         usage.Input,
			Output:        usage.Output,
			CacheRead:     usage.CacheRead,
			CacheCreation: usage.CacheCreation5m,
		}
	}
}

// electModel keeps the session's displayed ModelName on the MAIN model.
// Junie interleaves helper calls (guardrails, summarizers — gpt-4.1-mini,
// haiku, nano in live captures) with the main model's calls under the same
// MainAgent envelope, so latest-wins would flicker between them several
// times per turn. Instead the model whose single call carried the largest
// prompt-side footprint so far this turn wins — the main model's calls carry
// the whole conversation context (18k+ tokens from its very first call in
// live captures), helpers see only snippets. Strictly-greater so an
// equal-footprint call can't churn the election, and the high-water mark
// resets on each user prompt so a mid-session /model switch takes effect on
// the next turn instead of losing to the old model's larger history.
func (p *Parser) electModel(model string, tokens int64, ev *tailer.ParsedEvent) {
	if model == "" || tokens <= p.modelElectionTokens {
		return
	}
	p.modelElectionTokens = tokens
	ev.ModelName = tailer.NormalizeModelName(model)
}

// parseContextWindowReport maps the running context footprint onto the
// context-utilization fields: "used" becomes the latest total-token snapshot
// and "size" the adapter-supplied context-window override, from which the
// tailer derives utilization and pressure itself.
func parseContextWindowReport(agentEvent map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	if used := i64(agentEvent, "used"); used > 0 {
		ev.Tokens = &tailer.TokenSnapshot{Total: used}
	}
	if size := i64(agentEvent, "size"); size > 0 {
		ev.ContextWindow = size
	}
}

// launchModelFromPrompt reads the ModelForLaunchAttachment a UserPromptEvent
// MAY carry (it names the model the task was launched with; most prompts omit
// it) — an authoritative model signal that bridges the gap until the first
// main-model LLM call wins the token election.
func launchModelFromPrompt(raw map[string]any) string {
	attachments, _ := raw["customAttachments"].([]any)
	for _, a := range attachments {
		attachment, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if str(attachment, "kind") == "ModelForLaunchAttachment" {
			if id := str(attachment, "modelId"); id != "" {
				return tailer.NormalizeModelName(id)
			}
		}
	}
	return ""
}

// i64 reads a numeric field from a decoded JSON object as int64 (encoding/json
// decodes all JSON numbers to float64), returning 0 when absent or non-numeric.
func i64(m map[string]any, key string) int64 {
	return int64(f64(m, key))
}

// f64 reads a numeric field from a decoded JSON object, returning 0 when the
// map is nil, the key is absent, or the value is not a number.
func f64(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	v, _ := m[key].(float64)
	return v
}
