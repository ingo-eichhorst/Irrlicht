package opencode

import (
	"reflect"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// TestSessionFold_CostProvenance (#2061): the 1.x store fold prices nothing
// from Irrlicht's table, so its cost is only what opencode reported. A step
// with tokens and no reported cost added nothing to the cost; its model must
// be listed as unpriced rather than read as free. Tokens are always added on
// this path, so none are unobserved.
func TestSessionFold_CostProvenance(t *testing.T) {
	cost := 0.02
	f := newSessionFold("/tmp/x")
	f.applyContribution(&tailer.PerTurnContribution{Model: "claude-sonnet-4-5", Usage: tailer.UsageBreakdown{Input: 10, Output: 2}, ProviderCostUSD: &cost})
	f.applyContribution(&tailer.PerTurnContribution{Model: "local-model", Usage: tailer.UsageBreakdown{Input: 30, Output: 4}})
	f.applyContribution(&tailer.PerTurnContribution{Model: "empty-step"})
	f.publishCostProvenance()

	m := f.metrics
	if m.CumInputTokens != 40 || m.CumOutputTokens != 6 || m.EstimatedCostUSD != 0.02 {
		t.Fatalf("lock: totals in %d / out %d / cost %v, want 40 / 6 / 0.02", m.CumInputTokens, m.CumOutputTokens, m.EstimatedCostUSD)
	}
	if m.PricingSource != tailer.PricingSourceProviderReported {
		t.Errorf("PricingSource = %q, want %q", m.PricingSource, tailer.PricingSourceProviderReported)
	}
	if want := []string{"local-model"}; !reflect.DeepEqual(m.UnpricedModels, want) {
		t.Errorf("UnpricedModels = %q, want %q", m.UnpricedModels, want)
	}
	if m.TokensUnobserved {
		t.Error("TokensUnobserved = true; this path adds every contribution's tokens")
	}
}
