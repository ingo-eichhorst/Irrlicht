package museaccountapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedactSubscriptionResponse_DropsNonApprovedFields is mutation fixture
// #2: input carries approved fields PLUS several the pinned herdr source
// documents the real response also carries (api_key, name/user_full_name,
// email/user_email) plus arbitrary unknown fields, and asserts the output's
// key set is EXACTLY the approved allowlist — not merely that a few named
// forbidden keys are absent (AGENTS.md: "a validator that can't parse its
// input checks MORE, never less"). tools/lib/museaccountapi-redact-nonapproved-field-mutations_test.sh
// mutates RedactSubscriptionResponse to also copy one forbidden field
// through and confirms this test goes red.
func TestRedactSubscriptionResponse_DropsNonApprovedFields(t *testing.T) {
	input := `{
		"is_subs_active": true,
		"subs_tier_name": "Muse Code Everyday Usage",
		"subs_usage": {"window": {"used_percent": 4, "resets_at": 5}, "weekly": {"used_percent": 28, "resets_at": 6}},
		"api_key": "sk-should-never-appear",
		"user_email": "person@example.com",
		"user_full_name": "Real Name",
		"base_url": "https://api.meta.ai/v1",
		"action_url": "https://api.meta.ai/upgrade?t=secret-token"
	}`
	out, err := RedactSubscriptionResponse([]byte(input))
	if err != nil {
		t.Fatalf("RedactSubscriptionResponse: %v", err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	wantKeys := map[string]bool{"is_subs_active": true, "subs_tier_name": true, "subs_usage": true}
	for k := range got {
		if !wantKeys[k] {
			t.Errorf("redacted output retained non-approved key %q (value hidden from this failure message)", k)
		}
	}
	for k := range wantKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("redacted output is missing approved key %q", k)
		}
	}

	if s := string(out); containsAny(s, "sk-should-never-appear", "person@example.com", "Real Name", "secret-token") {
		t.Fatalf("redacted output contains forbidden data verbatim: %s", s)
	}
}

func TestRedactSubscriptionResponse_NoSubsUsage(t *testing.T) {
	out, err := RedactSubscriptionResponse([]byte(`{"is_subs_active":false}`))
	if err != nil {
		t.Fatalf("RedactSubscriptionResponse: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if _, ok := got["subs_usage"]; ok {
		t.Errorf("subs_usage present when input had none: %s", out)
	}
}

func TestRedactSubscriptionResponse_InvalidJSON(t *testing.T) {
	if _, err := RedactSubscriptionResponse([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
