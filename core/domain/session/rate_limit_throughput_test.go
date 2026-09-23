package session

import (
	"slices"
	"testing"
	"time"
)

// TestImminentWindow_SkipsThroughputWindows is guard 3 of issue #2013 §7: a
// throughput ceiling is not a subscription allowance, and #2013 §1.4 says
// rendering one as a plan allowance "would be worse than showing nothing".
// ImminentWindow is where that has to bite — it is what a quota chip and
// ForecastCap both read. The mutation that reddens this is committed in
// tools/lib/cloud-attribution-mutations_test.sh.
func TestImminentWindow_SkipsThroughputWindows(t *testing.T) {
	// A Bedrock-style requests-per-minute ceiling sitting at 92% next to a
	// real plan allowance at 12%. The ceiling must not win the comparison
	// just because its percentage is higher.
	snap := &RateLimitSnapshot{Windows: []RateLimitWindow{
		{UsedPercent: 92, WindowMinutes: 1, Measure: "requests", LimitKind: LimitKindThroughput},
		{UsedPercent: 12, WindowMinutes: 300, LimitKind: LimitKindAllowance},
	}}
	imm := snap.ImminentWindow()
	if imm == nil {
		t.Fatal("ImminentWindow = nil, want the 300-minute allowance window")
	}
	if imm.LimitKind == LimitKindThroughput {
		t.Fatalf("ImminentWindow returned a throughput ceiling (%.0f%% over %d min) as the allowance to display",
			imm.UsedPercent, imm.WindowMinutes)
	}
	if imm.WindowMinutes != 300 || imm.UsedPercent != 12 {
		t.Errorf("ImminentWindow = %.0f%%/%dmin, want 12%%/300min", imm.UsedPercent, imm.WindowMinutes)
	}
}

// TestImminentWindow_ThroughputOnlySnapshotShowsNothing: with no allowance
// window at all, the honest answer is nil — not the ceiling relabelled.
func TestImminentWindow_ThroughputOnlySnapshotShowsNothing(t *testing.T) {
	snap := &RateLimitSnapshot{Windows: []RateLimitWindow{
		{UsedPercent: 92, WindowMinutes: 1, Measure: "requests", LimitKind: LimitKindThroughput},
		{UsedPercent: 40, WindowMinutes: 1, Measure: "tokens", LimitKind: LimitKindThroughput},
	}}
	if imm := snap.ImminentWindow(); imm != nil {
		t.Fatalf("ImminentWindow = %+v, want nil — a throughput-only snapshot has no allowance to show", *imm)
	}
}

// TestForecastCap_IgnoresThroughputWindows checks the consequence that
// matters most: a burn-down projection over a service ceiling would read as
// "you will exhaust your plan at 14:20". Epic #1977 §7 forbids it directly.
func TestForecastCap_IgnoresThroughputWindows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	resets := now.Add(time.Hour).Unix()
	history := []RateLimitSnapshot{
		{SampledAt: now.Add(-10 * time.Minute).Unix(), Windows: []RateLimitWindow{
			{UsedPercent: 10, WindowMinutes: 1, ResetsAt: resets, LimitKind: LimitKindThroughput},
		}},
		{SampledAt: now.Unix(), Windows: []RateLimitWindow{
			{UsedPercent: 80, WindowMinutes: 1, ResetsAt: resets, LimitKind: LimitKindThroughput},
		}},
	}
	if got := ForecastCap(history, now); got != nil {
		t.Fatalf("ForecastCap = %v, want nil — a throughput ceiling has no plan cap to forecast", got)
	}
}

// TestIsSubscriptionAllowance_FailsClosedOnAnUnrecognisedKind is the guard
// for the review finding this file's first version shipped: with
// `LimitKind != LimitKindThroughput`, every value except the exact string
// "throughput" read as an allowance, so a one-character typo in a future
// adapter would put a service ceiling on the plan-allowance display —
// precisely what #2013 §1.4 calls worse than showing nothing. "" stays an
// allowance because it is the legacy zero value, not an unrecognised one.
func TestIsSubscriptionAllowance_FailsClosedOnAnUnrecognisedKind(t *testing.T) {
	// The measured typo, plus other shapes a careless caller produces.
	for _, kind := range []string{"throughtput", "Throughput", "THROUGHPUT", "rate", "rpm", " throughput", "throughput "} {
		if slices.Contains(LimitKinds, kind) {
			t.Fatalf("test setup: %q is in LimitKinds, so it is not an unrecognised value", kind)
		}
		w := &RateLimitWindow{UsedPercent: 92, WindowMinutes: 1, LimitKind: kind}
		if w.IsSubscriptionAllowance() {
			t.Errorf("LimitKind %q read as a subscription allowance; an unrecognised kind must fail closed", kind)
		}
		snap := &RateLimitSnapshot{Windows: []RateLimitWindow{*w}}
		if imm := snap.ImminentWindow(); imm != nil {
			t.Errorf("LimitKind %q: ImminentWindow surfaced it at %.0f%% as the window to display",
				kind, imm.UsedPercent)
		}
	}

	// Every member of the closed set is handled explicitly, so the set is
	// not dead data: exactly one of the two reads as an allowance.
	allowances := 0
	for _, kind := range LimitKinds {
		if (&RateLimitWindow{LimitKind: kind}).IsSubscriptionAllowance() {
			allowances++
		}
	}
	if len(LimitKinds) != 2 || allowances != 1 {
		t.Errorf("LimitKinds = %v with %d reading as allowances, want 2 kinds and exactly 1", LimitKinds, allowances)
	}
}

// TestForecastCap_WillNotPairAnAllowanceWithAThroughputWindow closes the
// narrower hole in the same distinction: ImminentWindow guarantees the LATEST
// sample is an allowance, but ForecastCap matches the earlier one on duration
// and reset time alone, so a throughput ceiling sharing a reset instant would
// otherwise contribute its slope to a plan-cap forecast.
func TestForecastCap_WillNotPairAnAllowanceWithAThroughputWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	resets := now.Add(time.Hour).Unix()
	history := []RateLimitSnapshot{
		{SampledAt: now.Add(-10 * time.Minute).Unix(), Windows: []RateLimitWindow{
			// Same duration and same reset instant as the allowance below,
			// but a throughput ceiling — and the only candidate.
			{UsedPercent: 5, WindowMinutes: 300, ResetsAt: resets, LimitKind: LimitKindThroughput},
		}},
		{SampledAt: now.Unix(), Windows: []RateLimitWindow{
			{UsedPercent: 80, WindowMinutes: 300, ResetsAt: resets, LimitKind: LimitKindAllowance},
		}},
	}
	if got := ForecastCap(history, now); got != nil {
		t.Fatalf("ForecastCap = %v, want nil — the earlier sample was a throughput ceiling, not the same quota", got)
	}
}

// TestIsSubscriptionAllowance_LegacyWindowsAreUnaffected is a LOCK, not
// red-first evidence: it passes by construction and exists to pin that every
// window written before LimitKind existed still reads as an allowance. The
// zero value is what every stored row and every live reading carries today.
func TestIsSubscriptionAllowance_LegacyWindowsAreUnaffected(t *testing.T) {
	legacy := &RateLimitWindow{UsedPercent: 55, WindowMinutes: 300}
	if legacy.LimitKind != "" {
		t.Fatalf("test setup: LimitKind = %q, want the zero value", legacy.LimitKind)
	}
	if !legacy.IsSubscriptionAllowance() {
		t.Error("a legacy window stopped reading as an allowance — every stored row would vanish from the UI")
	}
	if (&RateLimitWindow{LimitKind: LimitKindAllowance}).IsSubscriptionAllowance() != true {
		t.Error("an explicit allowance did not read as one")
	}
	if (&RateLimitWindow{LimitKind: LimitKindThroughput}).IsSubscriptionAllowance() != false {
		t.Error("an explicit throughput ceiling read as an allowance")
	}
	if (*RateLimitWindow)(nil).IsSubscriptionAllowance() {
		t.Error("a nil window read as an allowance")
	}

	// And the pre-#2013 ImminentWindow behaviour over unlabelled windows is
	// unchanged: highest percentage wins.
	snap := &RateLimitSnapshot{Windows: []RateLimitWindow{
		{UsedPercent: 20, WindowMinutes: 300},
		{UsedPercent: 70, WindowMinutes: 10080},
	}}
	imm := snap.ImminentWindow()
	if imm == nil || imm.WindowMinutes != 10080 {
		t.Errorf("ImminentWindow over legacy windows = %+v, want the 10080-minute one", imm)
	}
}
