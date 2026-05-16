package session

import (
	"time"
)

// RateLimitSnapshot is one provider-emitted reading of subscription quota.
// Codex's schema is the superset; Claude Code's statusline JSON is a strict
// subset that maps to a two-window snapshot (five_hour, seven_day) with no
// credits and no reached-type.
//
// Snapshots are per-account: a Claude Pro/Max user running multiple sessions
// on the same OAuth account sees identical Windows in every snapshot — the
// bucket is account-scoped, not per-session.
type RateLimitSnapshot struct {
	// Windows holds one entry per rate-limit window (typically two: a 5-hour
	// primary and a 7-day secondary). Order is provider-defined; callers
	// pick the most-imminent for display.
	Windows []RateLimitWindow `json:"windows"`

	// PlanType identifies the subscription tier when the provider supplies
	// one ("plus", "pro", "max", "team", "enterprise"). Empty for the
	// API-key / usage path where Credits is populated instead.
	PlanType string `json:"plan_type,omitempty"`

	// Credits, when non-nil, indicates the user is on a prepaid / API-key
	// path and the bucket is balance-based rather than time-window-based.
	// Claude Code never populates this; Codex does on API-key auth.
	Credits *CreditsSnapshot `json:"credits,omitempty"`

	// ReachedType, when non-empty, signals that one of the windows has
	// hit its cap. UI uses this to switch the display into a warning state.
	ReachedType string `json:"reached_type,omitempty"`

	// SampledAt is the wall-clock time at which the snapshot was observed,
	// stored as Unix seconds. Used as the x-axis when computing burn rate.
	SampledAt int64 `json:"sampled_at"`
}

// RateLimitWindow is a single time-windowed bucket reading.
type RateLimitWindow struct {
	// UsedPercent is the provider-reported utilization for this window
	// expressed as a percentage in [0, 100]. Floats are preserved as-is —
	// providers occasionally return values with floating-point noise (e.g.
	// 14.000000000000002) which the UI should round, not the parser.
	UsedPercent float64 `json:"used_percent"`

	// WindowMinutes is the nominal window length. Codex emits this
	// explicitly (300, 10080); Claude Code's flat five_hour / seven_day
	// fields map to 300 and 10080 respectively. Some Codex v1 samples
	// emit 299 / 10079 due to a server-side rounding quirk — the parser
	// must tolerate both.
	WindowMinutes int `json:"window_minutes"`

	// ResetsAt is the wall-clock time (Unix seconds) at which the window
	// rolls over and UsedPercent returns to zero.
	ResetsAt int64 `json:"resets_at"`
}

// CreditsSnapshot describes a prepaid balance, populated only on the
// API-key / usage path. Subscription users see Credits=nil.
type CreditsSnapshot struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited,omitempty"`
	Balance    float64 `json:"balance,omitempty"`
}

// ImminentWindow returns the window with the soonest projected cap given the
// current snapshot — defined as the one with the highest UsedPercent. Returns
// nil when the snapshot has no windows or every window is at zero (rendering
// has no signal to display).
func (s *RateLimitSnapshot) ImminentWindow() *RateLimitWindow {
	if s == nil || len(s.Windows) == 0 {
		return nil
	}
	var best *RateLimitWindow
	for i := range s.Windows {
		w := &s.Windows[i]
		if best == nil || w.UsedPercent > best.UsedPercent {
			best = w
		}
	}
	if best == nil || best.UsedPercent <= 0 {
		return nil
	}
	return best
}

// ForecastCap projects when UsedPercent for the most-imminent window will hit
// 100% given a history of snapshots. The projection is a simple linear fit
// over the recent samples: rate = (latest.UsedPercent - earliest.UsedPercent)
// / (latest.SampledAt - earliest.SampledAt). The history is expected to be
// "sample on change" — callers should drop duplicate-percent samples before
// passing them in so zero-delta noise from statusline ticks doesn't flatten
// the slope (see issue #309 cadence-gotcha findings).
//
// Returns nil when:
//   - history is shorter than 2 samples (no slope possible),
//   - the slope is non-positive (usage is flat or decreasing),
//   - the projected ETA is after the window's ResetsAt (the user won't hit
//     the cap in the current window).
//
// The returned time is rounded to the nearest second.
func ForecastCap(history []RateLimitSnapshot, now time.Time) *time.Time {
	if len(history) < 2 {
		return nil
	}
	latest := history[len(history)-1]
	earliest := history[0]
	imminent := latest.ImminentWindow()
	if imminent == nil {
		return nil
	}
	// Find the same window in the earliest snapshot — match on WindowMinutes
	// rather than slice index, since providers may reorder. Tolerate ±1
	// minute (Codex v1 quirk: 299/10079).
	var prev *RateLimitWindow
	for i := range earliest.Windows {
		w := &earliest.Windows[i]
		if abs(w.WindowMinutes-imminent.WindowMinutes) <= 1 {
			prev = w
			break
		}
	}
	if prev == nil {
		return nil
	}
	dtSeconds := latest.SampledAt - earliest.SampledAt
	if dtSeconds <= 0 {
		return nil
	}
	dPct := imminent.UsedPercent - prev.UsedPercent
	if dPct <= 0 {
		return nil
	}
	ratePerSecond := dPct / float64(dtSeconds)
	remaining := 100.0 - imminent.UsedPercent
	if remaining <= 0 {
		// Already at or past the cap — surface the current time.
		t := now.Round(time.Second)
		return &t
	}
	secondsToCap := remaining / ratePerSecond
	eta := time.Unix(latest.SampledAt, 0).Add(time.Duration(secondsToCap) * time.Second)
	if imminent.ResetsAt > 0 && eta.After(time.Unix(imminent.ResetsAt, 0)) {
		// Won't hit cap before the window rolls over.
		return nil
	}
	eta = eta.Round(time.Second)
	return &eta
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
