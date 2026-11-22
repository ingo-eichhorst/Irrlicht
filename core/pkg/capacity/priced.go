package capacity

// Tokens is one model's token counts, as PricedCostUSD prices them.
type Tokens struct {
	Model         string
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
}

// TokenBreakdown is one model's token counts with the cache writes split into
// Anthropic's 5-minute and 1-hour buckets, as PricedCostFromBreakdown prices
// them.
type TokenBreakdown struct {
	Model         string
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreate5m int64
	CacheCreate1h int64
}

// PricedCostUSD is EstimateCostUSD plus whether the price table had an entry
// for the model at all: (0, false) is an unpriced model, (0, true) a free one.
// The two read identically as a float, which is how an unpriced model came to
// show as costing nothing (#2061).
func (cm *CapacityManager) PricedCostUSD(t Tokens) (float64, bool) {
	cap := cm.GetModelCapacity(t.Model)
	if cap.Pricing == nil {
		cm.logPricingMiss(t.Model)
		return 0, false
	}
	p := cap.Pricing
	cost := float64(t.Input)*p.InputPerMTok +
		float64(t.Output)*p.OutputPerMTok +
		float64(t.CacheRead)*p.CacheReadPerMTok +
		float64(t.CacheCreation)*p.CacheCreationPerMTok
	return cost / 1_000_000, true
}

// PricedCostFromBreakdown is EstimateCostFromBreakdown plus whether the price
// table had an entry for the model — see PricedCostUSD.
func (cm *CapacityManager) PricedCostFromBreakdown(b TokenBreakdown) (float64, bool) {
	cap := cm.GetModelCapacity(b.Model)
	if cap.Pricing == nil {
		cm.logPricingMiss(b.Model)
		return 0, false
	}
	p := cap.Pricing

	// Choose cache-creation rate: prefer sub-rates when populated.
	var cacheCreateCost float64
	if p.CacheCreation5mPerMTok > 0 || p.CacheCreation1hPerMTok > 0 {
		cacheCreateCost = float64(b.CacheCreate5m)*p.CacheCreation5mPerMTok +
			float64(b.CacheCreate1h)*p.CacheCreation1hPerMTok
	} else {
		cacheCreateCost = float64(b.CacheCreate5m+b.CacheCreate1h) * p.CacheCreationPerMTok
	}

	cost := float64(b.Input)*p.InputPerMTok +
		float64(b.Output)*p.OutputPerMTok +
		float64(b.CacheRead)*p.CacheReadPerMTok +
		cacheCreateCost
	return cost / 1_000_000, true
}

// PriceTableSource names the price table this manager prices from, for a
// cost's provenance (#2061): "litellm@<date>", the date being the table's
// own LastUpdated stamp (parseLiteLLMData sets it to the day the table was
// fetched), or plain "litellm" for a table that carries no date — the
// in-memory NewForTest one does not.
func (cm *CapacityManager) PriceTableSource() string {
	cm.maybeReload()
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil || cm.config.LastUpdated == "" {
		return "litellm"
	}
	return "litellm@" + cm.config.LastUpdated
}
