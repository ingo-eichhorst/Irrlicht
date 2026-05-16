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

	// A string that looks like an alias prefix but isn't an exact key must
	// not be normalized. "claude-4.6-opus-fast-mode" IS aliased; appending
	// suffixes must not match.
	mc = cm.GetModelCapacity("claude-4.6-opus-fast-mode-xtra")
	if mc.ContextWindow != 0 || mc.Pricing != nil {
		t.Errorf("alias-prefix string was fuzzily matched: %+v", mc)
	}
}
