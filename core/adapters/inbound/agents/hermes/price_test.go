package hermes

import (
	"reflect"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/capacity"
)

// TestPriceSession_RecordsCostProvenance (#2061): a priced model names the
// price table; an unpriced one is listed, so its 0 cost is not read as free;
// a session with no tokens yet records neither.
func TestPriceSession_RecordsCostProvenance(t *testing.T) {
	cm := capacity.NewForTest(map[string]capacity.ModelCapacity{
		"priced-model": {ContextWindow: 1000, Pricing: &capacity.ModelPricing{InputPerMTok: 2}},
	})

	priced := &session.SessionMetrics{ModelName: "priced-model", CumInputTokens: 1_000_000}
	priceSession(cm, "priced-model", priced)
	if priced.EstimatedCostUSD != 2 || priced.PricingSource != "litellm" || len(priced.UnpricedModels) != 0 {
		t.Errorf("priced: cost %v, source %q, unpriced %q; want 2, litellm, none",
			priced.EstimatedCostUSD, priced.PricingSource, priced.UnpricedModels)
	}

	unpriced := &session.SessionMetrics{ModelName: "gpt-5.6-luna", CumInputTokens: 500}
	priceSession(cm, "gpt-5.6-luna", unpriced)
	if unpriced.PricingSource != "" || !reflect.DeepEqual(unpriced.UnpricedModels, []string{"gpt-5.6-luna"}) {
		t.Errorf("unpriced: source %q, unpriced %q; want none and [gpt-5.6-luna]", unpriced.PricingSource, unpriced.UnpricedModels)
	}

	empty := &session.SessionMetrics{ModelName: "priced-model"}
	priceSession(cm, "priced-model", empty)
	if empty.PricingSource != "" || len(empty.UnpricedModels) != 0 {
		t.Errorf("no tokens: source %q, unpriced %q; want neither", empty.PricingSource, empty.UnpricedModels)
	}
}
