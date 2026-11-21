package tailer

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Cost and token provenance (#2061). EstimatedCostUSD and the Cum*Tokens
// totals are plain numbers; these tests pin the fields that say how each was
// formed, so a consumer never reads "unpriced" or "uncounted" as zero.

func costPtr(v float64) *float64 { return &v }

// TestCostProvenance_UnpricedModelIsListedNotFree: a model the price table
// lacks contributes 0 to the cost. It must be listed as unpriced, and the
// table must not be named as the source of a cost it did not price.
func TestCostProvenance_UnpricedModelIsListedNotFree(t *testing.T) {
	p := &extraParser{contribs: []*PerTurnContribution{
		{Model: "local-model-without-a-price", Usage: UsageBreakdown{Input: 500, Output: 40}},
	}}
	m, err := newExtraTailer(t, p, 1).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.EstimatedCostUSD != 0 {
		t.Fatalf("lock: an unpriced model still costs 0 in EstimatedCostUSD, got %v", m.EstimatedCostUSD)
	}
	if want := []string{"local-model-without-a-price"}; !reflect.DeepEqual(m.UnpricedModels, want) {
		t.Errorf("UnpricedModels = %q, want %q — an unpriced model's zero cost must not read as free", m.UnpricedModels, want)
	}
	if m.PricingSource != "" {
		t.Errorf("PricingSource = %q, want empty: nothing was priced", m.PricingSource)
	}
	if m.TokensUnobserved {
		t.Error("TokensUnobserved = true, want false: every token was counted")
	}
}

// TestCostProvenance_PricedModelNamesThePriceTable: a priced bucket names the
// table, and lists nothing as unpriced. A bucket with no model name cannot be
// priced and is listed under "".
func TestCostProvenance_PricedModelNamesThePriceTable(t *testing.T) {
	p := &extraParser{contribs: []*PerTurnContribution{
		{Model: "claude-sonnet-4-5", Usage: UsageBreakdown{Input: 100, Output: 10}},
	}}
	m, err := newExtraTailer(t, p, 1).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if m.PricingSource != "litellm" {
		t.Errorf("PricingSource = %q, want %q (NewForTest's table carries no date)", m.PricingSource, "litellm")
	}
	if len(m.UnpricedModels) != 0 {
		t.Errorf("UnpricedModels = %q, want none", m.UnpricedModels)
	}

	// A bucket with no model name (an adapter whose turn named no model,
	// before any model was seen) cannot be priced.
	tl := newExtraTailer(t, &extraParser{}, 1)
	tl.cumByModel[""] = &UsageBreakdown{Input: 7}
	tl.computeCumulativeTokens()
	if want := []string{""}; !reflect.DeepEqual(tl.metrics.UnpricedModels, want) {
		t.Errorf("UnpricedModels = %q, want %q for tokens with no model name", tl.metrics.UnpricedModels, want)
	}
}

// TestCostProvenance_ProviderCostTurnLeavesTokensUnobserved: a turn that
// reports its own cost is priced from that cost and its tokens are dropped
// (applyContribution's early return). The cost is "provider-reported" and the
// token totals are flagged as incomplete — including for a reported $0.00,
// which aider writes for a free model and which cumProviderCostUSD alone
// cannot distinguish from "no provider cost at all".
func TestCostProvenance_ProviderCostTurnLeavesTokensUnobserved(t *testing.T) {
	for _, cost := range []float64{0.25, 0} {
		p := &extraParser{contribs: []*PerTurnContribution{
			{Model: "claude-sonnet-4-5", Usage: UsageBreakdown{Input: 900, Output: 90}, ProviderCostUSD: costPtr(cost)},
		}}
		m, err := newExtraTailer(t, p, 1).TailAndProcess()
		if err != nil {
			t.Fatal(err)
		}
		if m.CumInputTokens != 0 || m.CumOutputTokens != 0 {
			t.Fatalf("lock: a provider-cost turn's tokens are still dropped, got in/out %d/%d", m.CumInputTokens, m.CumOutputTokens)
		}
		if !m.TokensUnobserved {
			t.Errorf("cost %v: TokensUnobserved = false, want true — the turn's 990 tokens were never counted", cost)
		}
		if m.PricingSource != PricingSourceProviderReported {
			t.Errorf("cost %v: PricingSource = %q, want %q", cost, m.PricingSource, PricingSourceProviderReported)
		}
	}
}

// TestCostProvenance_MixedSourcesNameBoth: a session priced partly from the
// table and partly by the provider names both, table first.
func TestCostProvenance_MixedSourcesNameBoth(t *testing.T) {
	p := &extraParser{contribs: []*PerTurnContribution{
		{Model: "claude-sonnet-4-5", Usage: UsageBreakdown{Input: 100, Output: 10}},
		{Model: "claude-sonnet-4-5", Usage: UsageBreakdown{Input: 5}, ProviderCostUSD: costPtr(0.01)},
	}}
	m, err := newExtraTailer(t, p, 2).TailAndProcess()
	if err != nil {
		t.Fatal(err)
	}
	if want := "litellm+" + PricingSourceProviderReported; m.PricingSource != want {
		t.Errorf("PricingSource = %q, want %q", m.PricingSource, want)
	}
	if !m.TokensUnobserved {
		t.Error("TokensUnobserved = false, want true")
	}
}

// TestCostProvenance_ProviderCostSeenSurvivesRestart: the flag is persisted in
// the ledger, and a ledger written before the field existed (no
// provider_cost_seen key) still reads a positive provider cost as "seen".
func TestCostProvenance_ProviderCostSeenSurvivesRestart(t *testing.T) {
	before := newLedgerTestTailer()
	before.providerCostSeen = true
	if after := restartThroughLedger(t, before); !after.providerCostSeen {
		t.Error("providerCostSeen did not survive the ledger round-trip")
	}

	var legacy LedgerState
	if err := json.Unmarshal([]byte(`{"schema_version":8,"last_offset":10,"cum_provider_cost_usd":0.5}`), &legacy); err != nil {
		t.Fatal(err)
	}
	tl := newLedgerTestTailer()
	tl.SetLedgerState(legacy)
	if !tl.providerCostSeen {
		t.Error("a pre-#2061 ledger with a positive provider cost must read as provider cost seen")
	}
}

// TestCostProvenance_RotationClearsProviderCostSeen: a rewritten transcript is
// re-read from byte 0, so the flag describes the new content only.
func TestCostProvenance_RotationClearsProviderCostSeen(t *testing.T) {
	tl := newLedgerTestTailer()
	tl.providerCostSeen = true
	tl.resetAccumulatorsForRotation()
	if tl.providerCostSeen {
		t.Error("providerCostSeen survived a rotation reset")
	}
}
