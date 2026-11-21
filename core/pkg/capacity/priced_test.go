package capacity

import "testing"

// TestPricedCost_TellsUnpricedFromFree pins #2061's distinction: an unpriced
// model and a free one both cost 0, and only the ok flag tells them apart.
func TestPricedCost_TellsUnpricedFromFree(t *testing.T) {
	cm := NewForTest(map[string]ModelCapacity{
		"priced": {ContextWindow: 1000, Pricing: &ModelPricing{InputPerMTok: 2, OutputPerMTok: 10}},
		"free":   {ContextWindow: 1000, Pricing: &ModelPricing{}},
	})

	if cost, ok := cm.PricedCostUSD("priced", 1_000_000, 0, 0, 0); !ok || cost != 2 {
		t.Errorf("PricedCostUSD(priced) = (%v, %v), want (2, true)", cost, ok)
	}
	if cost, ok := cm.PricedCostUSD("free", 1_000_000, 0, 0, 0); !ok || cost != 0 {
		t.Errorf("PricedCostUSD(free) = (%v, %v), want (0, true): a free model is priced", cost, ok)
	}
	if cost, ok := cm.PricedCostUSD("absent", 1_000_000, 0, 0, 0); ok || cost != 0 {
		t.Errorf("PricedCostUSD(absent) = (%v, %v), want (0, false): an absent model is unpriced", cost, ok)
	}
	if _, ok := cm.PricedCostFromBreakdown("absent", 1, 1, 1, 1, 1); ok {
		t.Error("PricedCostFromBreakdown(absent) reported a price")
	}
	if cost, ok := cm.PricedCostFromBreakdown("priced", 0, 1_000_000, 0, 0, 0); !ok || cost != 10 {
		t.Errorf("PricedCostFromBreakdown(priced) = (%v, %v), want (10, true)", cost, ok)
	}

	// Lock: the float-only wrappers still answer exactly what they did.
	if got := cm.EstimateCostUSD("priced", 1_000_000, 0, 0, 0); got != 2 {
		t.Errorf("EstimateCostUSD(priced) = %v, want 2", got)
	}
	if got := cm.EstimateCostFromBreakdown("absent", 1, 1, 1, 1, 1); got != 0 {
		t.Errorf("EstimateCostFromBreakdown(absent) = %v, want 0", got)
	}
}

// TestPriceTableSource_NamesTheTableAndItsDate: the source carries the
// table's own LastUpdated stamp, which parseLiteLLMData sets at fetch time.
func TestPriceTableSource_NamesTheTableAndItsDate(t *testing.T) {
	if got := NewForTest(nil).PriceTableSource(); got != "litellm" {
		t.Errorf("undated table: PriceTableSource() = %q, want %q", got, "litellm")
	}
	cm := NewForTest(nil)
	cm.config.LastUpdated = "2026-11-20"
	if got := cm.PriceTableSource(); got != "litellm@2026-11-20" {
		t.Errorf("dated table: PriceTableSource() = %q, want %q", got, "litellm@2026-11-20")
	}
}
