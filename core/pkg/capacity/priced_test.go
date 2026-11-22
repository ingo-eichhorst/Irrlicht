package capacity

import "testing"

// TestPricedCost_TellsUnpricedFromFree pins #2061's distinction: an unpriced
// model and a free one both cost 0, and only the ok flag tells them apart.
func TestPricedCost_TellsUnpricedFromFree(t *testing.T) {
	cm := NewForTest(map[string]ModelCapacity{
		"priced": {ContextWindow: 1000, Pricing: &ModelPricing{InputPerMTok: 2, OutputPerMTok: 10}},
		"free":   {ContextWindow: 1000, Pricing: &ModelPricing{}},
	})

	checkPricedCostUSD(t, cm)
	checkPricedCostFromBreakdown(t, cm)

	// Lock: the float-only wrappers still answer exactly what they did.
	if got := cm.EstimateCostUSD("priced", 1_000_000, 0, 0, 0); got != 2 {
		t.Errorf("EstimateCostUSD(priced) = %v, want 2", got)
	}
	if got := cm.EstimateCostFromBreakdown("absent", 1, 1, 1, 1, 1); got != 0 {
		t.Errorf("EstimateCostFromBreakdown(absent) = %v, want 0", got)
	}
}

// checkPricedCostUSD: a priced, a free and an absent model through
// PricedCostUSD.
func checkPricedCostUSD(t *testing.T, cm *CapacityManager) {
	if cost, ok := cm.PricedCostUSD(Tokens{Model: "priced", Input: 1_000_000}); !ok || cost != 2 {
		t.Errorf("PricedCostUSD(priced) = (%v, %v), want (2, true)", cost, ok)
	}
	if cost, ok := cm.PricedCostUSD(Tokens{Model: "free", Input: 1_000_000}); !ok || cost != 0 {
		t.Errorf("PricedCostUSD(free) = (%v, %v), want (0, true): a free model is priced", cost, ok)
	}
	if cost, ok := cm.PricedCostUSD(Tokens{Model: "absent", Input: 1_000_000}); ok || cost != 0 {
		t.Errorf("PricedCostUSD(absent) = (%v, %v), want (0, false): an absent model is unpriced", cost, ok)
	}
}

// checkPricedCostFromBreakdown: an absent and a priced model through
// PricedCostFromBreakdown.
func checkPricedCostFromBreakdown(t *testing.T, cm *CapacityManager) {
	absent := TokenBreakdown{Model: "absent", Input: 1, Output: 1, CacheRead: 1, CacheCreate5m: 1, CacheCreate1h: 1}
	if _, ok := cm.PricedCostFromBreakdown(absent); ok {
		t.Error("PricedCostFromBreakdown(absent) reported a price")
	}
	if cost, ok := cm.PricedCostFromBreakdown(TokenBreakdown{Model: "priced", Output: 1_000_000}); !ok || cost != 10 {
		t.Errorf("PricedCostFromBreakdown(priced) = (%v, %v), want (10, true)", cost, ok)
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
