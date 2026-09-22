package museaccountapi

import (
	"errors"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// powerFixture mirrors the pinned herdr-agent-quota fixture
// (tests/fixtures/muse/subscription-power.json, verified by fetching it at
// commit 530c94b721b823a480072000012984ac8660cb2e) — used here as a
// known-shape input, independent of #2007's own live-probe fixture.
const powerFixture = `{
  "base_url": "https://api.meta.ai/v1",
  "has_payment_method": false,
  "is_subs_active": true,
  "subs_tier_id": "1000000000000001",
  "subs_tier_name": "Muse Code Power Usage",
  "subs_usage": {
    "window": {"used_percent": 4, "window_duration_mins": 300, "resets_at": 1789068250},
    "weekly": {"used_percent": 28, "resets_at": 1789344000},
    "tier": "1000000000000001"
  }
}`

func TestBuildSnapshot_PowerFixtureMapsRollingAndWeeklyWindows(t *testing.T) {
	now := time.Unix(1000, 0)
	snap, err := BuildSnapshot([]byte(powerFixture), now)
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("len(Windows) = %d, want 2", len(snap.Windows))
	}
	rolling, weekly := snap.Windows[0], snap.Windows[1]
	if rolling.UsedPercent != 4 || rolling.WindowMinutes != 300 || rolling.ResetsAt != 1789068250 {
		t.Errorf("rolling window = %+v, want {4 300 1789068250 ...}", rolling)
	}
	if weekly.UsedPercent != 28 || weekly.WindowMinutes != weeklyMinutes || weekly.ResetsAt != 1789344000 {
		t.Errorf("weekly window = %+v, want {28 %d 1789344000 ...}", weekly, weeklyMinutes)
	}
	if snap.Provider != session.ProviderMeta {
		t.Errorf("Provider = %q, want %q", snap.Provider, session.ProviderMeta)
	}
	if snap.Product != "Muse Code Power Usage" {
		t.Errorf("Product = %q, want %q", snap.Product, "Muse Code Power Usage")
	}
	if snap.ObservationSource != "account_api" {
		t.Errorf("ObservationSource = %q, want account_api", snap.ObservationSource)
	}
	if snap.AttributionQuality != session.AttributionQualityConfirmed {
		t.Errorf("AttributionQuality = %q, want %q", snap.AttributionQuality, session.AttributionQualityConfirmed)
	}
	if snap.SampledAt != 1000 || snap.LastSuccessAt != 1000 || snap.LastAttemptAt != 1000 {
		t.Errorf("timestamps = sampled=%d success=%d attempt=%d, want all 1000", snap.SampledAt, snap.LastSuccessAt, snap.LastAttemptAt)
	}
}

// TestBuildSnapshot_NeverDerivesAnAccountRef is a LOCK (passes by
// construction, per accountRef's own implementation) proving
// ConfirmedAccountRef stays empty across every fixture this test exercises
// — including ones that carry identity-shaped fields elsewhere in the raw
// response, which BuildSnapshot never even decodes into (subscriptionResponse's
// allowlist). Mutation fixture #1
// (tools/lib/museaccountapi-token-hash-account-id-mutations_test.sh) mutates
// accountRef() to derive a value from the response body and confirms this
// test goes red.
func TestBuildSnapshot_NeverDerivesAnAccountRef(t *testing.T) {
	inputs := []string{
		powerFixture,
		`{"is_subs_active":true,"subs_usage":{"window":{"used_percent":1,"resets_at":5}},"api_key":"sk-should-be-ignored","user_email":"person@example.com"}`,
	}
	for _, in := range inputs {
		snap, err := BuildSnapshot([]byte(in), time.Unix(1, 0))
		if err != nil {
			t.Fatalf("BuildSnapshot(%s): %v", in, err)
		}
		if snap.ConfirmedAccountRef != "" {
			t.Fatalf("ConfirmedAccountRef = %q, want empty — issue #2007 §1.3 forbids deriving an account id from response/credential data", snap.ConfirmedAccountRef)
		}
	}
}

func TestBuildSnapshot_InactiveSubscriptionIsNotAnError(t *testing.T) {
	_, err := BuildSnapshot([]byte(`{"is_subs_active":false,"subs_usage":null}`), time.Unix(1, 0))
	if !errors.Is(err, ErrNoActiveSubscription) {
		t.Fatalf("err = %v, want ErrNoActiveSubscription", err)
	}
}

func TestBuildSnapshot_MissingSubsUsage(t *testing.T) {
	_, err := BuildSnapshot([]byte(`{"is_subs_active":true}`), time.Unix(1, 0))
	if !errors.Is(err, ErrNoQuotaWindows) {
		t.Fatalf("err = %v, want ErrNoQuotaWindows", err)
	}
}

func TestBuildSnapshot_EmptyWindowsReportsNoQuotaWindows(t *testing.T) {
	_, err := BuildSnapshot([]byte(`{"subs_usage":{"window":{},"weekly":{}}}`), time.Unix(1, 0))
	if !errors.Is(err, ErrNoQuotaWindows) {
		t.Fatalf("err = %v, want ErrNoQuotaWindows", err)
	}
}

func TestBuildSnapshot_OutOfRangePercentageRejectedNotClamped(t *testing.T) {
	_, err := BuildSnapshot([]byte(`{"subs_usage":{"weekly":{"used_percent":140,"resets_at":5}}}`), time.Unix(1, 0))
	if err == nil {
		t.Fatal("expected an error for used_percent=140 (out of [0,100]), got none — a bad provider value must never be silently clamped")
	}
}

func TestBuildSnapshot_NegativePercentageRejected(t *testing.T) {
	_, err := BuildSnapshot([]byte(`{"subs_usage":{"weekly":{"used_percent":-1,"resets_at":5}}}`), time.Unix(1, 0))
	if err == nil {
		t.Fatal("expected an error for used_percent=-1")
	}
}

func TestBuildSnapshot_DifferentRollingLengthKeepsItsOwnDuration(t *testing.T) {
	snap, err := BuildSnapshot([]byte(`{"subs_usage":{"window":{"used_percent":10,"window_duration_mins":480,"resets_at":5}}}`), time.Unix(1, 0))
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].WindowMinutes != 480 {
		t.Fatalf("Windows = %+v, want one window with WindowMinutes=480", snap.Windows)
	}
}

func TestBuildSnapshot_InvalidJSON(t *testing.T) {
	if _, err := BuildSnapshot([]byte(`not json`), time.Unix(1, 0)); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}
