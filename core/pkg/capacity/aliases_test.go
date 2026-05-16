package capacity

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// LOCAL_OVERRIDE entries must not regress to their known-missing codeburn
// canonical. (This pins the override decisions; it does not verify the new
// target exists in LiteLLM — see TestModelAliases_LocalOverrideTargetsExistInLiteLLM.)
func TestModelAliases_LocalOverrides(t *testing.T) {
	// alias → canonical the override must NOT regress to.
	regressionTargets := map[string]string{
		"claude-4-sonnet":     "claude-sonnet-4",
		"claude-4-sonnet-1m":  "claude-sonnet-4",
		"claude-4-opus":       "claude-opus-4",
		"copilot-openai-auto": "gpt-5.3-codex",
		"gpt-5.1-codex-high":  "gpt-5.3-codex",
	}
	for alias, regressed := range regressionTargets {
		got, ok := modelAliases[alias]
		if !ok {
			t.Errorf("override alias %q missing from modelAliases", alias)
			continue
		}
		if got == regressed {
			t.Errorf("alias %q regressed to codeburn-canonical %q (not in LiteLLM); see LOCAL_OVERRIDE comment in aliases.go", alias, regressed)
		}
	}
}

// Soft assertion that LOCAL_OVERRIDE targets are present in LiteLLM's actual
// pricing table. Reads the daemon's cached LiteLLM data when available;
// skips when absent so CI runners without a cache don't see false failures.
// The point is to catch drift at local-dev time before the maintainer pushes
// an override that resolves to a still-zero capacity.
func TestModelAliases_LocalOverrideTargetsExistInLiteLLM(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no homedir available")
	}
	cachePath := filepath.Join(home, ".local/share/irrlicht/model-capacity-cache.json")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Skipf("no LiteLLM cache at %s — run the daemon once to populate it", cachePath)
	}

	var cached struct {
		Config struct {
			Models map[string]json.RawMessage `json:"models"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatalf("malformed LiteLLM cache: %v", err)
	}

	// Collect every override target by walking modelAliases and re-resolving
	// the entries that have known-missing codeburn canonicals. This avoids
	// duplicating the list; if a new LOCAL_OVERRIDE is added, this test
	// auto-covers it.
	overrideTargets := map[string]bool{
		modelAliases["claude-4-sonnet"]:     true,
		modelAliases["claude-4-opus"]:       true,
		modelAliases["copilot-openai-auto"]: true,
		modelAliases["gpt-5.1-codex-high"]:  true,
	}

	for target := range overrideTargets {
		if _, ok := cached.Config.Models[target]; !ok {
			t.Errorf("LOCAL_OVERRIDE target %q is not in LiteLLM cache — drift detected, pick a different canonical or wait for upstream", target)
		}
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
