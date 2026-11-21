package tailer

import (
	"sort"
	"strings"

	"irrlicht/core/domain/session"
)

// costProvenance collects, over one pricing pass, which token buckets were
// priced from the price table and which could not be (#2061).
type costProvenance struct {
	usedTable bool
	unpriced  map[string]bool
}

// bucket records one model's token bucket: priced from the table, or — when
// it holds tokens but no price was known (no model name, no price table, or
// a model the table lacks) — unpriced. An empty bucket is neither.
func (p *costProvenance) bucket(model string, tokens int64, priced bool) {
	if tokens == 0 {
		return
	}
	if priced {
		p.usedTable = true
		return
	}
	if p.unpriced == nil {
		p.unpriced = make(map[string]bool)
	}
	p.unpriced[model] = true
}

// publishCostProvenance writes the pass's provenance onto t.metrics:
// PricingSource names every source that contributed to EstimatedCostUSD,
// UnpricedModels the buckets that contributed nothing for want of a price
// (sorted, since cumByModel iterates in random order), and TokensUnobserved
// whether a provider-cost turn's tokens were dropped.
func (t *TranscriptTailer) publishCostProvenance(p costProvenance) {
	var sources []string
	if p.usedTable && t.capacityMgr != nil {
		sources = append(sources, t.capacityMgr.PriceTableSource())
	}
	if t.providerCostSeen {
		sources = append(sources, session.PricingSourceProviderReported)
	}
	t.metrics.PricingSource = strings.Join(sources, session.PricingSourceSeparator)
	t.metrics.UnpricedModels = nil
	for model := range p.unpriced {
		t.metrics.UnpricedModels = append(t.metrics.UnpricedModels, model)
	}
	sort.Strings(t.metrics.UnpricedModels)
	t.metrics.TokensUnobserved = t.providerCostSeen
}
