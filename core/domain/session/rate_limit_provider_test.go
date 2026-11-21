package session

import (
	"testing"
	"time"
)

// TestForecastCap_NeverPairsTwoProvidersFiveHourWindows is #2010 §7.1: an
// Anthropic five-hour window and a Z.ai five-hour window share a duration
// and, here, a reset instant — exactly what ForecastCap's window match keys
// on — and are still two quotas. A history spanning both must produce no
// forecast. The same pair from ONE provider does forecast, so the nil below
// is the provider check and not something else in the fit.
func TestForecastCap_NeverPairsTwoProvidersFiveHourWindows(t *testing.T) {
	const reset = 1795284966
	now := time.Unix(1795270000, 0)
	sample := func(provider string, at int64, used float64) RateLimitSnapshot {
		return RateLimitSnapshot{
			Provider:  provider,
			SampledAt: at,
			Windows:   []RateLimitWindow{{UsedPercent: used, WindowMinutes: 300, ResetsAt: reset}},
		}
	}

	same := []RateLimitSnapshot{sample(ProviderZai, 1795270000, 10), sample(ProviderZai, 1795270600, 20)}
	if ForecastCap(same, now) == nil {
		t.Fatal("vacuity: the same provider's two samples must forecast, or the cross-provider nil below proves nothing")
	}

	mixed := []RateLimitSnapshot{sample(ProviderAnthropic, 1795270000, 10), sample(ProviderZai, 1795270600, 20)}
	if eta := ForecastCap(mixed, now); eta != nil {
		t.Fatalf("ForecastCap paired an anthropic 5h window with a zai 5h window (eta %v); equal duration is not the same quota", eta)
	}
}
