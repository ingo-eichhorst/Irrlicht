package capacity

import (
	"reflect"
	"testing"
)

// Each canonical gets a distinct ModelCapacity so a mis-routed alias surfaces
// as a value mismatch rather than passing on equal zero values.
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

// Alias must resolve before the LiteLLM lookup, so an alias key that also
// appears as a direct LiteLLM entry routes through the canonical — otherwise
// a future LiteLLM addition could silently undo a deliberate codeburn mapping.
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
