package junie

import (
	"math"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// Characterization locks of the captured LlmResponseMetadataEvent /
// ContextWindowReportEvent shapes (see docs/testing-philosophy.md): each
// verbatim line pins the exact ParsedEvent the parser must produce for it.
// The synthetic multi-entry / zero-token variants are labelled as such —
// no live capture has ever shown them (checked across 45 sessions), they
// pin the forward-compatible handling the plan requires.

func TestParser_LlmMetadata_Contribution(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"gpt-5.6-sol","cost":0.017589,"inputTokens":3,"cacheInputTokens":18688,"cacheCreateTokens":424,"outputTokens":186,"time":0}]}},"taskId":"task-260824-174337-1avu","timestampMs":1787586306156}`))
	if ev == nil || !ev.Skip {
		t.Fatal("metadata is bookkeeping: expected Skip=true (the tailer folds it via the skipped-event path)")
	}
	c := ev.Contribution
	if c == nil {
		t.Fatal("expected a Contribution")
	}
	if c.Model != "gpt-5.6-sol" {
		t.Errorf("Model = %q, want gpt-5.6-sol", c.Model)
	}
	want := tailer.UsageBreakdown{Input: 3, Output: 186, CacheRead: 18688, CacheCreation5m: 424}
	if c.Usage != want {
		t.Errorf("Usage = %+v, want %+v", c.Usage, want)
	}
	if c.ProviderCostUSD == nil || *c.ProviderCostUSD != 0.017589 {
		t.Errorf("ProviderCostUSD = %v, want 0.017589 (Junie's cost is authoritative — the pi ProviderCostWins pattern)", c.ProviderCostUSD)
	}
	if ev.Tokens == nil || ev.Tokens.Input != 3 || ev.Tokens.Output != 186 || ev.Tokens.CacheRead != 18688 || ev.Tokens.CacheCreation != 424 {
		t.Errorf("Tokens snapshot = %+v, want the call's own breakdown", ev.Tokens)
	}
	if ev.ModelName != "gpt-5.6-sol" {
		t.Errorf("ModelName = %q, want gpt-5.6-sol (largest footprint so far elects the model)", ev.ModelName)
	}
}

func TestParser_LlmMetadata_ScientificNotationCost(t *testing.T) {
	// Junie serializes small costs in E notation ("9.36E-5"); encoding/json
	// reads it as an ordinary float64 and the sum must not drop it.
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"gemini-3.5-flash-lite","cost":9.5475E-4,"inputTokens":3789,"cacheInputTokens":0,"cacheCreateTokens":0,"outputTokens":5,"time":0}]}},"taskId":"task-260825-141156-1aft","timestampMs":1787659917709}`))
	if ev.Contribution == nil || ev.Contribution.ProviderCostUSD == nil {
		t.Fatal("expected a Contribution with ProviderCostUSD")
	}
	if *ev.Contribution.ProviderCostUSD != 9.5475e-4 {
		t.Errorf("ProviderCostUSD = %v, want 9.5475e-4", *ev.Contribution.ProviderCostUSD)
	}
	if ev.Contribution.Usage.Input != 3789 || ev.Contribution.Usage.Output != 5 {
		t.Errorf("Usage = %+v", ev.Contribution.Usage)
	}
}

func TestParser_LlmMetadata_MultiModelEntries(t *testing.T) {
	// SYNTHETIC variant of a captured line: no live event has ever carried
	// more than one modelUsage entry. Costs and tokens sum; the dominant
	// (largest prompt-side footprint) entry names the model.
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"claude-fable-5","cost":0.092276,"inputTokens":2,"cacheInputTokens":18631,"cacheCreateTokens":4826,"outputTokens":266,"time":0},{"model":"gpt-4.1-mini-2025-04-14","cost":9.36E-5,"inputTokens":186,"cacheInputTokens":0,"cacheCreateTokens":0,"outputTokens":12,"time":0}]}},"taskId":"t","timestampMs":1787663438843}`))
	c := ev.Contribution
	if c == nil {
		t.Fatal("expected a Contribution")
	}
	if c.Model != "claude-fable-5" {
		t.Errorf("Model = %q, want claude-fable-5 (dominant entry)", c.Model)
	}
	want := tailer.UsageBreakdown{Input: 188, Output: 278, CacheRead: 18631, CacheCreation5m: 4826}
	if c.Usage != want {
		t.Errorf("Usage = %+v, want summed %+v", c.Usage, want)
	}
	if c.ProviderCostUSD == nil || math.Abs(*c.ProviderCostUSD-0.0923696) > 1e-12 {
		t.Errorf("ProviderCostUSD = %v, want summed 0.0923696", c.ProviderCostUSD)
	}
}

func TestParser_LlmMetadata_AllZeroEntry_NoContribution(t *testing.T) {
	// SYNTHETIC: an all-zero entry with no cost carries nothing to account
	// — no contribution, no snapshot, still a clean Skip.
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"","cost":0,"inputTokens":0,"cacheInputTokens":0,"cacheCreateTokens":0,"outputTokens":0,"time":0}]}},"taskId":"t","timestampMs":1787663438843}`))
	if !ev.Skip {
		t.Error("expected Skip=true")
	}
	if ev.Contribution != nil {
		t.Errorf("Contribution = %+v, want nil for an all-zero entry", ev.Contribution)
	}
	if ev.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil", ev.Tokens)
	}
}

func TestParser_LlmMetadata_ZeroTokensWithCost(t *testing.T) {
	// SYNTHETIC: a zero-token entry that still reports a cost keeps the
	// cost (billing happened) while emitting no token snapshot.
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"gpt-5.4-nano","cost":1.8199E-4,"inputTokens":0,"cacheInputTokens":0,"cacheCreateTokens":0,"outputTokens":0,"time":0}]}},"taskId":"t","timestampMs":1787663445998}`))
	if ev.Contribution == nil || ev.Contribution.ProviderCostUSD == nil || *ev.Contribution.ProviderCostUSD != 1.8199e-4 {
		t.Fatalf("Contribution = %+v, want cost-only contribution", ev.Contribution)
	}
	if ev.Tokens != nil {
		t.Errorf("Tokens = %+v, want nil when the call moved no tokens", ev.Tokens)
	}
}

func TestParser_ContextWindowReport(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"ContextWindowReportEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"percentage":1.1220932006835938,"used":11766,"size":1048576}},"taskId":"task-260825-141156-1aft","timestampMs":1787659943556}`))
	if ev == nil || !ev.Skip {
		t.Fatal("context-window report is bookkeeping: expected Skip=true")
	}
	if ev.Tokens == nil || ev.Tokens.Total != 11766 {
		t.Errorf("Tokens = %+v, want Total=11766 (the 'used' field)", ev.Tokens)
	}
	if ev.ContextWindow != 1048576 {
		t.Errorf("ContextWindow = %d, want 1048576 (the 'size' field)", ev.ContextWindow)
	}
}

// TestParser_ModelElection_HelperDoesNotOustMainModel pins the election rule:
// Junie interleaves small helper-model calls (summarizers, guardrails) with
// the main model's full-context calls, so latest-wins would flicker the
// displayed model several times per turn. Only a call with a LARGER
// prompt-side footprint than any seen this turn re-elects.
func TestParser_ModelElection_HelperDoesNotOustMainModel(t *testing.T) {
	p := &Parser{}
	// Main model, 18631 cache-create tokens (real captured line).
	first := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"claude-fable-5","cost":0.2388975,"inputTokens":1,"cacheInputTokens":0,"cacheCreateTokens":18631,"outputTokens":120,"time":0}]}},"taskId":"t","timestampMs":1787663426600}`))
	if first.ModelName != "claude-fable-5" {
		t.Fatalf("first ModelName = %q, want claude-fable-5", first.ModelName)
	}
	// Helper model, 186-token footprint (real captured line) — must not
	// re-elect: an empty ModelName leaves the tailer's sticky value alone.
	second := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"gpt-4.1-mini-2025-04-14","cost":9.360000000000001E-5,"inputTokens":186,"cacheInputTokens":0,"cacheCreateTokens":0,"outputTokens":12,"time":0}]}},"taskId":"t","timestampMs":1787663419595}`))
	if second.ModelName != "" {
		t.Errorf("helper call elected ModelName = %q, want \"\" (footprint 186 < 18632)", second.ModelName)
	}
	// The helper's cost still counts.
	if second.Contribution == nil || second.Contribution.ProviderCostUSD == nil {
		t.Error("helper call must still contribute its cost")
	}
}

// TestParser_ModelElection_ResetsOnUserPrompt pins the per-turn scope: after
// a new prompt the high-water mark is zero again, so a mid-session /model
// switch's first (small) call can win instead of losing to the old model's
// larger history forever.
func TestParser_ModelElection_ResetsOnUserPrompt(t *testing.T) {
	p := &Parser{}
	p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"claude-fable-5","cost":0.2388975,"inputTokens":1,"cacheInputTokens":0,"cacheCreateTokens":18631,"outputTokens":120,"time":0}]}},"taskId":"t","timestampMs":1787663426600}`))
	p.ParseLine(line(t, `{"kind":"UserPromptEvent","requestId":"prompt-260824-174155-sm5c","prompt":"test","presentablePrompt":"test","requiresConfirmation":true,"timestampMs":1787586115220}`))
	ev := p.ParseLine(line(t, `{"kind":"SessionA2uxEvent","event":{"state":"IN_PROGRESS","agentEvent":{"kind":"LlmResponseMetadataEvent","agent":{"kind":"MainAgent","id":"main","name":"main","type":"LINEAR"},"modelUsage":[{"model":"gemini-3.5-flash-lite","cost":9.5475E-4,"inputTokens":3789,"cacheInputTokens":0,"cacheCreateTokens":0,"outputTokens":5,"time":0}]}},"taskId":"t","timestampMs":1787659917709}`))
	if ev.ModelName != "gemini-3.5-flash-lite" {
		t.Errorf("ModelName = %q, want gemini-3.5-flash-lite (election high-water resets on a new prompt)", ev.ModelName)
	}
}

func TestParser_UserPrompt_LaunchModelAttachment(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(line(t, `{"kind":"UserPromptEvent","requestId":"prompt-260825-141156-1x4b","prompt":"can sprign webflux use rxjava","presentablePrompt":"can sprign webflux use rxjava","customAttachments":[{"kind":"ModelForLaunchAttachment","modelId":"gemini-3.7-flash"},{"kind":"EffortForLaunchAttachment","effort":"medium"}],"extraAttachments":[{"kind":"TaskRequestParametersAttachment","brave":false},{"kind":"TaskRequestCustomAttachment","key":"planAgent","value":true}],"requiresConfirmation":true,"timestampMs":1787659916203}`))
	if ev.EventType != "user_message" {
		t.Errorf("EventType = %q, want user_message", ev.EventType)
	}
	if ev.ModelName != "gemini-3.7-flash" {
		t.Errorf("ModelName = %q, want gemini-3.7-flash (the launch attachment names the task's model)", ev.ModelName)
	}
}

// TestMetrics_EndToEnd replays the real-metrics-session fixture (verbatim
// captured lines from session-260825-150418-1vll, sequenced) through the
// PRODUCTION tailer — the vibe metrics_test pattern — and asserts the
// dashboard-visible metrics: summed provider cost, elected main model, and
// the context-window fields from the latest ContextWindowReportEvent.
//
// The fixture deliberately ends the way real sessions do: with a post-
// TaskState helper call (gpt-4.1-mini, the task-naming pass) — the ModelName
// assertion below is what proves latest-wins would be wrong.
func TestMetrics_EndToEnd(t *testing.T) {
	m := tailFixture(t, fixtureLines(t, "real-metrics-session.jsonl"))

	// Sum of the five calls' provider-reported costs. Junie's cost is
	// authoritative (ProviderCostUSD), so no token×price estimation is mixed
	// in.
	const wantCost = 0.00317 + 0.2388975 + 9.360000000000001e-5 + 0.092276 + 3.2720000000000004e-4
	if math.Abs(m.EstimatedCostUSD-wantCost) > 1e-9 {
		t.Errorf("EstimatedCostUSD = %v, want %v", m.EstimatedCostUSD, wantCost)
	}
	if m.ModelName != "claude-fable-5" {
		t.Errorf("ModelName = %q, want claude-fable-5 (the main model — not the trailing task-naming helper a latest-wins mapping would show)", m.ModelName)
	}
	// Latest ContextWindowReportEvent: used 23459 of size 1000000.
	if m.TotalTokens != 23459 {
		t.Errorf("TotalTokens = %d, want 23459", m.TotalTokens)
	}
	if m.ContextWindow != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000 (Junie's own reported size, not a capacity-map lookup)", m.ContextWindow)
	}
	if math.Abs(m.ContextUtilization-2.3459) > 0.001 {
		t.Errorf("ContextUtilization = %v, want ~2.3459", m.ContextUtilization)
	}
	if m.PressureLevel != "safe" {
		t.Errorf("PressureLevel = %q, want safe", m.PressureLevel)
	}
	// Metrics must not have disturbed the state mapping: the fixture ends
	// with TaskState → turn_done.
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done", m.LastEventType)
	}
}
