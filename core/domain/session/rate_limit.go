package session

import (
	"time"
)

// Canonical billing-provider keys for RateLimitSnapshot.Provider. Centralised
// here (the domain layer both adapters and application/services already
// depend on) so a typo at any stamping or reading site fails the build
// instead of silently creating a phantom provider bucket — the same failure
// mode core/application/services/quotainherit.go's own provider constants
// guard against for the account-key map.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// AttributionQualityConfirmed is the only AttributionQuality value that
// ProviderForSession (core/application/services/quotainherit.go) treats as
// resolvable evidence. AttributionQualityEstimated exists for a future
// heuristic source; nothing sets it yet, and until something does, the only
// two states a snapshot's AttributionQuality is ever seen in are "confirmed"
// (stamped by an adapter that observed native evidence) and "" (unstamped —
// unpopulated fields, or a legacy row).
const (
	AttributionQualityConfirmed = "confirmed"
	AttributionQualityEstimated = "estimated"
)

// RateLimitSnapshot is one provider-emitted reading of subscription quota.
// Codex's schema is the superset; Claude Code's statusline JSON is a strict
// subset that maps to a five-hour and/or seven-day snapshot with no credits
// and no reached-type.
//
// Snapshots are per-account: a Claude Pro/Max user running multiple sessions
// on the same OAuth account sees identical Windows in every snapshot — the
// bucket is account-scoped, not per-session.
type RateLimitSnapshot struct {
	// Windows holds the provider-emitted rate-limit windows. Providers may
	// report one, both, or neither of the commonly observed 5-hour and 7-day
	// windows; an absent window was not reported and must not be synthesized.
	// Order is provider-defined; callers pick the most-imminent for display.
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

	// --- Additive billing-identity fields (issue #1994 / epic #1977 §7). ---
	//
	// Provider, ObservationSource, AttributionEvidence, and AttributionQuality
	// are stamped at creation by the two adapters that observe native quota
	// evidence today: claudecode/statusline.go (Claude Code's statusline
	// hook) and codex/parser.go (Codex's transcript rate_limits). Product,
	// ConfirmedAccountRef, QuotaScope, QuotaID, LastSuccessAt, LastAttemptAt,
	// and RetrievalFailure are NOT populated by any adapter yet — no
	// observation source exists for them until the route ticket and the
	// account-API ticket land (epic #1977 §10's later work packages). Every
	// field here is additive and omitempty, so a legacy row (or one written
	// before the corresponding stamp existed) decodes with the unpopulated
	// ones at their zero value — which means "not recorded", never
	// "confirmed empty" or "confirmed unknown". No stored row is rewritten
	// and no migration runs.
	//
	// Deliberately kept as separate fields rather than one mutually
	// exclusive enum: ObservationSource and AttributionQuality answer
	// different questions (a polled value is still provider-reported; a
	// reported value can still go stale) and conflating them would lose
	// that distinction — see docs/testing-philosophy.md.

	// Provider is the confirmed billing provider this snapshot is evidence
	// for ("anthropic"/"openai" — the Provider* constants above — or ""
	// when unstamped). This is what makes the snapshot self-describing:
	// core/application/services/quotainherit.go's ProviderForSession reads
	// this field directly instead of switching on the session's adapter
	// name, which is what let a claude-code session running against
	// Bedrock/Vertex (a different billing relationship entirely, and one
	// that never populates this field) get misattributed to Anthropic
	// before #1994.
	Provider string `json:"provider,omitempty"`

	// Product identifies the specific commercial product this quota belongs
	// to (e.g. "claude_pro_max", "chatgpt_plus"), distinct from the coarser
	// Provider field above — one provider can offer several products with
	// independently-scoped quotas.
	Product string `json:"product,omitempty"`

	// ConfirmedAccountRef is the provider account identifier this snapshot
	// was actually observed against (e.g. an OAuth account id). Empty means
	// no account identity could be confirmed for this snapshot specifically.
	// Cross-session sharing must never bridge two snapshots whose
	// ConfirmedAccountRef differ, and must never bridge when either is
	// empty — see AccountKey and applyDonors in
	// core/application/services/quotainherit.go, which enforce this for the
	// inheritance path.
	ConfirmedAccountRef string `json:"confirmed_account_ref,omitempty"`

	// QuotaScope names what the quota is scoped to, for a provider that
	// exposes more than one shape (e.g. "individual", "workspace"). Empty
	// means the provider does not distinguish, or the scope was not
	// observed.
	QuotaScope string `json:"quota_scope,omitempty"`

	// AttributionEvidence names what established Provider (and
	// ConfirmedAccountRef, once something populates it) for this snapshot —
	// today "claude_code_statusline" or "codex_transcript_rate_limits". An
	// adapter name alone is never valid evidence — see ProviderForSession's
	// doc comment in core/application/services/quotainherit.go.
	AttributionEvidence string `json:"attribution_evidence,omitempty"`

	// QuotaID is a stable provider-issued identifier for the specific quota
	// bucket this snapshot reports on, when the provider exposes one. When a
	// provider exposes no stable id, callers document their own composite
	// key rather than leaving this populated with a guess.
	QuotaID string `json:"quota_id,omitempty"`

	// ObservationSource names the mechanism that produced this snapshot:
	// "transcript" (parsed from the agent's own transcript/output), "hook"
	// (a first-party hook payload), "store" (an on-disk store the agent
	// itself writes), "account_api", "browser_backed_api", or "calculation"
	// (derived rather than observed).
	ObservationSource string `json:"observation_source,omitempty"`

	// AttributionQuality is the confidence in the attribution above: one of
	// the AttributionQuality* constants — AttributionQualityConfirmed
	// (session-specific evidence established the provider) or
	// AttributionQualityEstimated (a heuristic, not evidence; nothing sets
	// this yet). ProviderForSession treats anything other than
	// AttributionQualityConfirmed — including "", the zero value — as no
	// resolvable evidence.
	AttributionQuality string `json:"attribution_quality,omitempty"`

	// LastSuccessAt and LastAttemptAt are Unix seconds for the most recent
	// successful and most recent attempted retrieval, for an observation
	// source that polls rather than receiving a push. Equal to SampledAt
	// for a transcript/hook source that only ever "succeeds" (there is no
	// failed attempt to distinguish).
	LastSuccessAt int64 `json:"last_success_at,omitempty"`
	LastAttemptAt int64 `json:"last_attempt_at,omitempty"`

	// RetrievalFailure classifies the most recent failed attempt, when
	// LastAttemptAt is after LastSuccessAt (e.g. "auth_expired",
	// "rate_limited", "network"). Empty means the last attempt succeeded, or
	// no attempt has failed yet.
	RetrievalFailure string `json:"retrieval_failure,omitempty"`
}

// RateLimitWindow is a single time-windowed bucket reading.
type RateLimitWindow struct {
	// UsedPercent is the provider-reported utilization for this window
	// expressed as a percentage in [0, 100]. Floats are preserved as-is —
	// providers occasionally return values with floating-point noise (e.g.
	// 14.000000000000002) which the UI should round, not the parser.
	UsedPercent float64 `json:"used_percent"`

	// WindowMinutes is the nominal window length. Codex emits the duration
	// explicitly; Claude Code's flat five_hour / seven_day fields map to 300
	// and 10080 respectively. Some Codex v1 samples emit 299 / 10079 due to
	// a server-side rounding quirk — the parser must tolerate both.
	WindowMinutes int `json:"window_minutes"`

	// ResetsAt is the wall-clock time (Unix seconds) at which the window
	// rolls over and UsedPercent returns to zero.
	ResetsAt int64 `json:"resets_at"`

	// Measure names the explicit unit this window's usage is counted in
	// when a provider exposes one (e.g. "requests", "tokens", "credits").
	// Additive (issue #1994 / epic #1977 §7); empty for every window
	// observed today, since UsedPercent is reported pre-normalized to a
	// percentage regardless of the underlying counter.
	Measure string `json:"measure,omitempty"`
}

// CreditsSnapshot describes a prepaid balance, populated only on the
// API-key / usage path. Subscription users see Credits=nil.
type CreditsSnapshot struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited,omitempty"`
	Balance    float64 `json:"balance,omitempty"`

	// --- Additive fields (issue #1994 / epic #1977 §7). ---
	//
	// Balance's own `omitempty` drops a real zero exactly like an absent
	// value, so a genuinely-observed zero balance is indistinguishable from
	// "never observed" on the wire. BalanceObserved is the only place that
	// distinction survives a round trip; it is false (its zero value) for
	// every row written before this field existed, which reads as "not
	// observed" for those rows too — matching this ticket's evidence rule
	// that absence must never be reported as a guessed zero.
	BalanceObserved bool `json:"balance_observed,omitempty"`

	// Currency is the explicit currency or credit-unit label for Balance
	// (e.g. "USD", "CNY", or a provider's own credit-unit name). Empty means
	// unlabeled — a legacy row, or a provider that reports a unitless
	// credit count. Verified against DeepSeek's balance API, whose response
	// carries `currency` ("CNY" or "USD") alongside total/granted/topped-up
	// components that the previous unitless Balance field could not hold:
	// https://api-docs.deepseek.com/api/get-user-balance/
	Currency string `json:"currency,omitempty"`

	// Total, Granted, and ToppedUp are Balance's components when a provider
	// reports them separately (DeepSeek's total_balance / granted_balance /
	// topped_up_balance, cited above). nil means the provider doesn't break
	// the balance down this way.
	Total    *float64 `json:"total,omitempty"`
	Granted  *float64 `json:"granted,omitempty"`
	ToppedUp *float64 `json:"topped_up,omitempty"`
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
	// minute (Codex v1 quirk: 299/10079). WindowMinutes alone is not enough:
	// two distinct quotas can share a nominal duration (e.g. two unrelated
	// 5-hour windows), so also require ResetsAt to agree — the same window
	// instance rolls over at the same wall-clock time across nearby
	// samples, while two different quotas' reset times are unrelated (issue
	// #1994).
	var prev *RateLimitWindow
	for i := range earliest.Windows {
		w := &earliest.Windows[i]
		if abs(w.WindowMinutes-imminent.WindowMinutes) <= 1 && w.ResetsAt == imminent.ResetsAt {
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
	// Convert the float seconds-until-cap to a Duration without first
	// truncating the float: `Duration(secondsToCap) * Second` would
	// cast 12.7 → 12 before multiplying, losing sub-second precision.
	// Multiplying then casting preserves it (rounded by Duration's
	// integer nanosecond representation).
	eta := time.Unix(latest.SampledAt, 0).Add(time.Duration(secondsToCap * float64(time.Second)))
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
