package capacity

import "testing"

func TestEstimateCO2Grams_ZeroTokens(t *testing.T) {
	grams, tier := EstimateCO2Grams("claude-sonnet-5", 0, 0, 0, 0)
	if grams != 0 {
		t.Errorf("grams = %v, want 0", grams)
	}
	if tier != CO2TierFallback {
		t.Errorf("tier = %v, want %v", tier, CO2TierFallback)
	}
}

func TestEstimateCO2Grams_FallbackTier(t *testing.T) {
	// Claude/GPT have no public per-token disclosure, so they land in the
	// fallback tier: tokens * fallbackWhPerToken * PUE * grid / 1000.
	for _, model := range []string{"claude-sonnet-5", "gpt-5", "unknown-model-xyz"} {
		grams, tier := EstimateCO2Grams(model, 1_000_000, 0, 0, 0)
		if tier != CO2TierFallback {
			t.Errorf("%s: tier = %v, want %v", model, tier, CO2TierFallback)
		}
		want := 1_000_000.0 * fallbackWhPerToken * defaultPUE * globalGridGCO2PerKWh / 1000
		if grams != want {
			t.Errorf("%s: grams = %v, want %v", model, grams, want)
		}
	}
}

func TestEstimateCO2Grams_ProviderDisclosedTier(t *testing.T) {
	tests := []struct {
		model string
		want  float64 // gCO2e/token
	}{
		{"gemini-2.5-pro", 0.00003},
		{"gemini-3.1-flash-lite-preview", 0.00003},
		{"mistral-large-2411", 0.00285},
		{"open-mixtral-8x22b", 0.00285},
	}
	for _, tt := range tests {
		grams, tier := EstimateCO2Grams(tt.model, 1000, 0, 0, 0)
		if tier != CO2TierProviderDisclosed {
			t.Errorf("%s: tier = %v, want %v", tt.model, tier, CO2TierProviderDisclosed)
		}
		want := 1000.0 * tt.want
		if grams != want {
			t.Errorf("%s: grams = %v, want %v", tt.model, grams, want)
		}
	}
}

func TestEstimateCO2Grams_SumsAllTokenBuckets(t *testing.T) {
	grams, _ := EstimateCO2Grams("gemini-2.5-flash", 100, 200, 300, 400)
	const want = 0.03
	if diff := grams - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("grams = %v, want %v", grams, want)
	}
}

func TestWeakerCO2Tier(t *testing.T) {
	tests := []struct {
		a, b CO2Tier
		want CO2Tier
	}{
		{CO2TierProviderDisclosed, CO2TierProviderDisclosed, CO2TierProviderDisclosed},
		{CO2TierProviderDisclosed, CO2TierFallback, CO2TierFallback},
		{CO2TierFallback, CO2TierProviderDisclosed, CO2TierFallback},
		{"", CO2TierProviderDisclosed, CO2TierProviderDisclosed},
	}
	for _, tt := range tests {
		if got := WeakerCO2Tier(tt.a, tt.b); got != tt.want {
			t.Errorf("WeakerCO2Tier(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}
