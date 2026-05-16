package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionMetricsJSON_RateLimitFields confirms the new rate-limit fields
// appear in serialized SessionMetrics with the expected JSON keys, and that
// nil values are omitted (so API-key sessions don't carry empty keys).
func TestSessionMetricsJSON_RateLimitFields(t *testing.T) {
	eta := int64(1778761800)
	m := &SessionMetrics{
		RateLimit: &RateLimitSnapshot{
			SampledAt: 1700000000,
			PlanType:  "max",
			Windows: []RateLimitWindow{
				{UsedPercent: 47.5, WindowMinutes: 300, ResetsAt: 1778761800},
				{UsedPercent: 14.0, WindowMinutes: 10080, ResetsAt: 1779188400},
			},
		},
		RateLimitForecastEta: &eta,
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)

	for _, want := range []string{
		`"rate_limit":`,
		`"plan_type":"max"`,
		`"windows":[`,
		`"used_percent":47.5`,
		`"window_minutes":300`,
		`"resets_at":1778761800`,
		`"rate_limit_forecast_eta":1778761800`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("expected JSON to contain %q, got %s", want, s)
		}
	}
}

func TestSessionMetricsJSON_NilRateLimitOmitted(t *testing.T) {
	m := &SessionMetrics{
		// No RateLimit, no RateLimitForecastEta.
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "rate_limit") {
		t.Errorf("expected nil rate_limit fields to be omitted, got %s", s)
	}
}
