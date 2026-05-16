package capacity

import (
	"reflect"
	"testing"
)

// TestModelAliases_ResolveToCanonical asserts every alias entry resolves to
// the same ModelCapacity as its canonical key. The manager is seeded with one
// distinct ModelCapacity per canonical target, so a mis-routed alias would
// surface as a mismatched ContextWindow or Pricing.
func TestModelAliases_ResolveToCanonical(t *testing.T) {
	canonicals := make(map[string]ModelCapacity)
	i := int64(1)
	for _, canonical := range modelAliases {
		if _, seen := canonicals[canonical]; seen {
			continue
		}
		canonicals[canonical] = ModelCapacity{
			ContextWindow: 100000 + i,
			MaxOutput:     8000 + i,
			Family:        canonical,
			DisplayName:   canonical,
			Pricing: &ModelPricing{
				InputPerMTok:  float64(i),
				OutputPerMTok: float64(i) * 2,
			},
		}
		i++
	}

	cm := NewForTest(canonicals)

	for alias, canonical := range modelAliases {
		gotAlias := cm.GetModelCapacity(alias)
		gotCanonical := cm.GetModelCapacity(canonical)
		if !reflect.DeepEqual(gotAlias, gotCanonical) {
			t.Errorf("alias %q resolved to %+v, want %+v (canonical %q)", alias, gotAlias, gotCanonical, canonical)
		}
		if gotAlias.ContextWindow == 0 {
			t.Errorf("alias %q resolved to zero-value capacity (canonical %q missing from test seed?)", alias, canonical)
		}
	}
}

// TestModelAliases_UnknownReturnsUnchanged asserts alias resolution is
// exact-match only: an unknown string falls through to the canonical lookup
// unchanged, no prefix or fuzzy matching.
func TestModelAliases_UnknownReturnsUnchanged(t *testing.T) {
	cm := NewForTest(map[string]ModelCapacity{
		"claude-opus-4-6": {ContextWindow: 200000},
	})

	mc := cm.GetModelCapacity("made-up-model-12345")
	if mc.ContextWindow != 0 || mc.Pricing != nil {
		t.Errorf("unknown model returned non-zero capacity: %+v", mc)
	}

	// Empty string — degenerate input from upstream parsers that failed to
	// extract a model name. Must not panic, must not match any alias.
	mc = cm.GetModelCapacity("")
	if mc.ContextWindow != 0 || mc.Pricing != nil {
		t.Errorf("empty-string lookup returned non-zero capacity: %+v", mc)
	}

	// A string that looks like an alias prefix but isn't an exact key must
	// not be normalized. "claude-4.6-opus-fast-mode" IS aliased; appending
	// suffixes must not match.
	mc = cm.GetModelCapacity("claude-4.6-opus-fast-mode-xtra")
	if mc.ContextWindow != 0 || mc.Pricing != nil {
		t.Errorf("alias-prefix string was fuzzily matched: %+v", mc)
	}
}

// TestModelAliases_ShadowDirectLookup asserts that when an alias key is
// *also* present as a direct entry in the LiteLLM table, the alias mapping
// wins. This pins the "alias resolves first" semantics: if a future LiteLLM
// version ships "claude-opus-4.6" as a real key, our alias still routes it
// to "claude-opus-4-6" (codeburn's canonical), avoiding silent drift.
func TestModelAliases_ShadowDirectLookup(t *testing.T) {
	const alias = "claude-opus-4.6" // present in modelAliases → "claude-opus-4-6"
	if _, ok := modelAliases[alias]; !ok {
		t.Fatalf("test premise broken: %q is not in modelAliases", alias)
	}

	cm := NewForTest(map[string]ModelCapacity{
		alias:             {ContextWindow: 1, DisplayName: "direct"},
		"claude-opus-4-6": {ContextWindow: 200000, DisplayName: "canonical"},
	})

	got := cm.GetModelCapacity(alias)
	if got.DisplayName != "canonical" || got.ContextWindow != 200000 {
		t.Errorf("alias %q returned direct entry instead of canonical: %+v", alias, got)
	}
}
