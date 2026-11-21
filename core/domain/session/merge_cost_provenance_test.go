package session

import (
	"reflect"
	"testing"
)

// TestMergeMetrics_CarriesCostProvenance is the #1256 allowlist trap aimed at
// #2061's four fields: newMergedMetrics zeroes any field its literal omits.
func TestMergeMetrics_CarriesCostProvenance(t *testing.T) {
	cov := &UsageCoverage{TokenTypes: []TokenType{TokenInput, TokenOutput}}
	newM := &SessionMetrics{
		EstimatedCostUSD: 1.5,
		CumInputTokens:   10,
		PricingSource:    "litellm@2026-11-20+provider-reported",
		UnpricedModels:   []string{"local"},
		TokensUnobserved: true,
		UsageCoverage:    cov,
	}
	merged := MergeMetrics(newM, &SessionMetrics{EstimatedCostUSD: 1, CumInputTokens: 5})

	if merged.PricingSource != newM.PricingSource {
		t.Errorf("PricingSource = %q, want %q — missing from newMergedMetrics' allowlist", merged.PricingSource, newM.PricingSource)
	}
	if !reflect.DeepEqual(merged.UnpricedModels, newM.UnpricedModels) {
		t.Errorf("UnpricedModels = %q, want %q — missing from newMergedMetrics' allowlist", merged.UnpricedModels, newM.UnpricedModels)
	}
	if !merged.TokensUnobserved {
		t.Error("TokensUnobserved dropped — missing from newMergedMetrics' allowlist")
	}
	if merged.UsageCoverage != cov {
		t.Errorf("UsageCoverage = %v, want the fresh declaration — missing from newMergedMetrics' allowlist", merged.UsageCoverage)
	}
}

// TestMergeMetrics_CarriedCostKeepsItsProvenance: when a pass computes no cost
// and the old one is carried forward, the old cost's provenance comes with it.
// An old cost labelled with the fresh pass's empty source would read as
// priced from nothing; an old cost with an unpriced model would lose the
// caveat that it understates.
func TestMergeMetrics_CarriedCostKeepsItsProvenance(t *testing.T) {
	oldM := &SessionMetrics{
		EstimatedCostUSD: 2,
		PricingSource:    "litellm@2026-11-20",
		UnpricedModels:   []string{"local"},
	}
	merged := MergeMetrics(&SessionMetrics{}, oldM)

	if merged.EstimatedCostUSD != 2 {
		t.Fatalf("lock: a zero cost still carries the old one forward, got %v", merged.EstimatedCostUSD)
	}
	if merged.PricingSource != oldM.PricingSource {
		t.Errorf("PricingSource = %q, want the carried cost's %q", merged.PricingSource, oldM.PricingSource)
	}
	if !reflect.DeepEqual(merged.UnpricedModels, oldM.UnpricedModels) {
		t.Errorf("UnpricedModels = %q, want the carried cost's %q", merged.UnpricedModels, oldM.UnpricedModels)
	}
}

// TestMergeMetrics_CarriedTokensKeepTheirCaveat: carried token counts keep the
// "some tokens uncounted" flag they were published with; fresh counts publish
// their own.
func TestMergeMetrics_CarriedTokensKeepTheirCaveat(t *testing.T) {
	oldM := &SessionMetrics{CumInputTokens: 50, TokensUnobserved: true}

	if merged := MergeMetrics(&SessionMetrics{}, oldM); !merged.TokensUnobserved {
		t.Error("carried token counts lost their TokensUnobserved caveat")
	}
	if merged := MergeMetrics(&SessionMetrics{CumInputTokens: 60}, oldM); merged.TokensUnobserved {
		t.Error("fresh token counts inherited a caveat they were not published with")
	}
}

// TestMergeMetrics_CoverageSurvivesAnUnstampedPass: the coverage is a static
// adapter declaration; a pass that did not stamp it has not withdrawn it.
func TestMergeMetrics_CoverageSurvivesAnUnstampedPass(t *testing.T) {
	cov := &UsageCoverage{TokenTypes: TokenTypes()}
	merged := MergeMetrics(&SessionMetrics{}, &SessionMetrics{UsageCoverage: cov})
	if merged.UsageCoverage != cov {
		t.Errorf("UsageCoverage = %v, want the old declaration", merged.UsageCoverage)
	}
}

// TestUsageCoverage_Observes covers the nil and partial declarations a
// consumer reads.
func TestUsageCoverage_Observes(t *testing.T) {
	var none *UsageCoverage
	if none.Observes(TokenInput) || none.ObservesAll() {
		t.Error("a nil coverage must observe nothing")
	}
	partial := &UsageCoverage{TokenTypes: []TokenType{TokenInput, TokenOutput}}
	if !partial.Observes(TokenOutput) || partial.Observes(TokenCacheWrite) || partial.ObservesAll() {
		t.Errorf("partial coverage answered wrongly: %+v", partial)
	}
	if !(&UsageCoverage{TokenTypes: TokenTypes()}).ObservesAll() {
		t.Error("a full declaration must observe all")
	}
	for _, tt := range TokenTypes() {
		if !IsTokenType(tt) {
			t.Errorf("IsTokenType(%q) = false for a member of TokenTypes", tt)
		}
	}
	if IsTokenType("reasoning") {
		t.Error(`IsTokenType("reasoning") = true; the vocabulary is closed`)
	}
}
