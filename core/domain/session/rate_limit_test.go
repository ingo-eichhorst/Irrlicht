package session

import (
	"testing"
	"time"
)

func TestImminentWindow_PicksHighestPercent(t *testing.T) {
	snap := &RateLimitSnapshot{
		Windows: []RateLimitWindow{
			{UsedPercent: 14, WindowMinutes: 10080, ResetsAt: 2000},
			{UsedPercent: 47, WindowMinutes: 300, ResetsAt: 1000},
		},
	}
	imm := snap.ImminentWindow()
	if imm == nil {
		t.Fatal("expected non-nil window")
	}
	if imm.WindowMinutes != 300 {
		t.Fatalf("expected 5h window (300), got %d", imm.WindowMinutes)
	}
}

func TestImminentWindow_AllZeroReturnsNil(t *testing.T) {
	snap := &RateLimitSnapshot{
		Windows: []RateLimitWindow{
			{UsedPercent: 0, WindowMinutes: 300, ResetsAt: 1000},
			{UsedPercent: 0, WindowMinutes: 10080, ResetsAt: 2000},
		},
	}
	if snap.ImminentWindow() != nil {
		t.Fatal("expected nil for all-zero windows")
	}
}

func TestForecastCap_LinearProjection(t *testing.T) {
	// Burn 10% over 600 seconds (10 minutes) → 1% per minute.
	// At 30%, the cap is 70% away → 4200 seconds → 70 min from latest sample.
	base := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	history := []RateLimitSnapshot{
		{
			SampledAt: base.Unix(),
			Windows:   []RateLimitWindow{{UsedPercent: 20, WindowMinutes: 300, ResetsAt: base.Add(2 * time.Hour).Unix()}},
		},
		{
			SampledAt: base.Add(10 * time.Minute).Unix(),
			Windows:   []RateLimitWindow{{UsedPercent: 30, WindowMinutes: 300, ResetsAt: base.Add(2 * time.Hour).Unix()}},
		},
	}
	eta := ForecastCap(history, base.Add(10*time.Minute))
	if eta == nil {
		t.Fatal("expected non-nil eta")
	}
	want := base.Add(10*time.Minute + 70*time.Minute)
	if !eta.Equal(want) {
		t.Fatalf("expected eta %v, got %v", want, *eta)
	}
}

func TestForecastCap_WontHitCapReturnsNil(t *testing.T) {
	// 1% per 10 minutes → cap is 99% away → 990 minutes. Window resets in
	// 60 minutes, so forecast must return nil.
	base := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	history := []RateLimitSnapshot{
		{
			SampledAt: base.Unix(),
			Windows:   []RateLimitWindow{{UsedPercent: 0, WindowMinutes: 300, ResetsAt: base.Add(1 * time.Hour).Unix()}},
		},
		{
			SampledAt: base.Add(10 * time.Minute).Unix(),
			Windows:   []RateLimitWindow{{UsedPercent: 1, WindowMinutes: 300, ResetsAt: base.Add(1 * time.Hour).Unix()}},
		},
	}
	if eta := ForecastCap(history, base.Add(10*time.Minute)); eta != nil {
		t.Fatalf("expected nil eta when burn is too slow, got %v", *eta)
	}
}

func TestForecastCap_FlatBurnReturnsNil(t *testing.T) {
	base := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	history := []RateLimitSnapshot{
		{SampledAt: base.Unix(), Windows: []RateLimitWindow{{UsedPercent: 30, WindowMinutes: 300, ResetsAt: base.Add(1 * time.Hour).Unix()}}},
		{SampledAt: base.Add(10 * time.Minute).Unix(), Windows: []RateLimitWindow{{UsedPercent: 30, WindowMinutes: 300, ResetsAt: base.Add(1 * time.Hour).Unix()}}},
	}
	if eta := ForecastCap(history, base.Add(10*time.Minute)); eta != nil {
		t.Fatalf("expected nil for flat burn, got %v", *eta)
	}
}

func TestForecastCap_SingleSampleReturnsNil(t *testing.T) {
	base := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	history := []RateLimitSnapshot{
		{SampledAt: base.Unix(), Windows: []RateLimitWindow{{UsedPercent: 30, WindowMinutes: 300, ResetsAt: base.Add(1 * time.Hour).Unix()}}},
	}
	if eta := ForecastCap(history, base); eta != nil {
		t.Fatalf("expected nil for single sample, got %v", *eta)
	}
}

func TestForecastCap_ToleratesOffByOneWindowMinutes(t *testing.T) {
	// Codex v1 quirk: earliest sample reports 299, latest reports 300.
	// ImminentWindow uses 300; matching must tolerate ±1.
	base := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	history := []RateLimitSnapshot{
		{SampledAt: base.Unix(), Windows: []RateLimitWindow{{UsedPercent: 20, WindowMinutes: 299, ResetsAt: base.Add(2 * time.Hour).Unix()}}},
		{SampledAt: base.Add(10 * time.Minute).Unix(), Windows: []RateLimitWindow{{UsedPercent: 30, WindowMinutes: 300, ResetsAt: base.Add(2 * time.Hour).Unix()}}},
	}
	if ForecastCap(history, base.Add(10*time.Minute)) == nil {
		t.Fatal("expected forecast despite 299/300 window-minute mismatch")
	}
}
