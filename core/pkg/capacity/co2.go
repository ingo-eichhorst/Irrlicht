package capacity

import "strings"

// CO2Tier describes how trustworthy the energy/CO2e coefficients behind an
// estimate are. No provider exposes real per-request telemetry, so every
// figure here is modeled, not measured — the tier tells the caller how far
// the model is from a primary source.
type CO2Tier string

const (
	// CO2TierProviderDisclosed means the coefficient is derived directly from
	// a provider or third-party lifecycle-assessment disclosure that already
	// reports gCO2e (their own grid mix and PUE are baked in).
	CO2TierProviderDisclosed CO2Tier = "provider_disclosed"
	// CO2TierFallback means no model-specific disclosure exists; the estimate
	// uses the cross-model energy-per-query fallback converted via a global
	// average grid intensity and datacenter PUE.
	CO2TierFallback CO2Tier = "fallback"
)

const (
	// defaultPUE is the datacenter power-usage-effectiveness overhead applied
	// to raw compute energy when converting Wh/token to CO2e — EcoLogits
	// (github.com/genai-impact/ecologits) cites a 1.1-1.2 range for
	// disclosed hyperscaler PUE; we take the midpoint.
	defaultPUE = 1.15

	// globalGridGCO2PerKWh is the 2025 global-average grid carbon intensity
	// in grams CO2 per kWh (Ember Global Electricity Review 2025,
	// https://ember-energy.org/latest-insights/global-electricity-review-2025/).
	// Used whenever the serving datacenter's actual region/grid mix is
	// unknown, which is always true for a third-party API call.
	globalGridGCO2PerKWh = 460.0

	// fallbackWhPerToken is the last-resort raw-energy coefficient for any
	// model without a dedicated entry below. Derived from Epoch AI's
	// Feb 2025 revised estimate of ~0.3 Wh per frontier-model query
	// (https://epoch.ai/gradient-updates/how-much-energy-does-chatgpt-use),
	// divided by an assumed ~1,000 total tokens per query. This is the tier
	// most sessions land in today: neither Anthropic nor OpenAI has
	// published per-token energy figures.
	fallbackWhPerToken = 0.0003
)

// co2Coefficients holds one model family's energy/CO2 coefficient plus the
// confidence tier it came from.
type co2Coefficients struct {
	// gCO2PerToken, when nonzero, is a fully-baked CO2e-per-token figure
	// sourced directly from a disclosure that already reports gCO2e — used
	// as-is, with no PUE or grid-intensity multiplier (that's already
	// folded into the source number).
	gCO2PerToken float64
	tier         CO2Tier
}

// co2CoefficientsByFamily maps a coarse model-family keyword (matched
// case-insensitively against the model name) to its coefficient. Sourced
// from the public disclosures cited in issue #829:
//
//   - Gemini: Google's Aug 2025 environmental report gives a median
//     comprehensive-accounting text prompt of 0.24 Wh / 0.03 gCO2e
//     (https://services.google.com/fh/files/misc/measuring_the_environmental_impact_of_delivering_ai_at_google_scale.pdf).
//     The report doesn't state the prompt's token count, so this is
//     normalized against an assumed ~1,000 total tokens/prompt:
//     0.03 gCO2e / 1,000 tokens = 0.00003 gCO2e/token.
//   - Mistral: the peer-reviewed Mistral/Carbone4/ADEME lifecycle
//     assessment (Jul 2025) gives Mistral Large 2 marginal inference as
//     1.14 gCO2e per 400-token prompt, full lifecycle including amortized
//     training (https://mistral.ai/news/our-contribution-to-a-global-environmental-standard-for-ai/):
//     1.14 / 400 = 0.00285 gCO2e/token.
//
// Every other family (Claude, GPT, Llama, etc.) falls back to
// fallbackWhPerToken: no per-token figures for them are public.
var co2CoefficientsByFamily = map[string]co2Coefficients{
	"gemini":  {gCO2PerToken: 0.00003, tier: CO2TierProviderDisclosed},
	"mistral": {gCO2PerToken: 0.00285, tier: CO2TierProviderDisclosed},
	"mixtral": {gCO2PerToken: 0.00285, tier: CO2TierProviderDisclosed},
}

// co2CoefficientsForModel classifies by the model name itself rather than
// ModelCapacity.Family: Family is derived from LiteLLM's litellm_provider
// field for pricing-tier purposes and is inconsistent across hosting paths
// for the same model family (e.g. Gemini shows up as "gemini",
// "vertex_ai-language-models", or via Vertex/Bedrock aliases depending on
// how it's served) — matching the model name directly is robust regardless
// of hosting provider.
func co2CoefficientsForModel(modelName string) co2Coefficients {
	lower := strings.ToLower(modelName)
	for keyword, coeff := range co2CoefficientsByFamily {
		if strings.Contains(lower, keyword) {
			return coeff
		}
	}
	return co2Coefficients{tier: CO2TierFallback}
}

// EstimateCO2Grams estimates the CO2e footprint in grams for totalTokens
// processed by modelName, using whatever tier of coefficient is available.
// Takes a single pre-summed token count rather than a per-bucket breakdown:
// unlike $ pricing, no public source distinguishes an energy cost for
// input/output/cache tokens, so callers sum their buckets before calling.
// This is always an estimate, never a measurement — no provider exposes
// per-request energy telemetry — so callers should surface the returned tier
// alongside the number rather than presenting it as precise.
func EstimateCO2Grams(modelName string, totalTokens int64) (grams float64, tier CO2Tier) {
	if totalTokens <= 0 {
		return 0, CO2TierFallback
	}

	coeff := co2CoefficientsForModel(modelName)
	if coeff.gCO2PerToken > 0 {
		return float64(totalTokens) * coeff.gCO2PerToken, coeff.tier
	}

	grams = float64(totalTokens) * fallbackWhPerToken * defaultPUE * globalGridGCO2PerKWh / 1000
	return grams, CO2TierFallback
}

// WeakerCO2Tier returns whichever of a and b reflects lower confidence, for
// combining coefficients across multiple models used within one session — the
// session-level tier should reflect the least-trustworthy contributor, not
// imply more confidence than the weakest model's estimate warrants.
func WeakerCO2Tier(a, b CO2Tier) CO2Tier {
	if a == CO2TierFallback || b == CO2TierFallback {
		return CO2TierFallback
	}
	if a == "" {
		return b
	}
	return a
}
