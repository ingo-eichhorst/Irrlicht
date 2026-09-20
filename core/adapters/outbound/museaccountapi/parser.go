package museaccountapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"irrlicht/core/domain/session"
)

// ErrNoActiveSubscription is returned by BuildSnapshot when the response
// reports is_subs_active:false — a valid, non-error account state (an
// API-key-only login, or a lapsed subscription), not a fetch failure. The
// pinned herdr-agent-quota source treats this identically
// (ProviderError::Unavailable, never a parse error): a caller must not
// retry-as-failure or manufacture a zero-usage snapshot for this state — it
// simply publishes no snapshot.
var ErrNoActiveSubscription = errors.New("museaccountapi: no active Muse Code subscription")

// ErrNoQuotaWindows is returned when the response carries neither a
// readable rolling window nor a weekly reading — an unrecognized or empty
// subs_usage shape, not a network failure.
var ErrNoQuotaWindows = errors.New("museaccountapi: no readable quota windows in Muse account-quota response")

// subscriptionResponse is the STRICT allowlist this package ever reads from
// a POST https://api.meta.ai/muse-code/key response. Issue #2007 §1.3: "The
// response can carry more than the quota block ... Retain only approved,
// redacted fields." The pinned herdr-agent-quota source's own doc comment
// confirms the response also carries the account's API key, name, and
// email. json.Unmarshal into this narrow struct IS the enforcement: any
// field not named here is silently dropped by decode and never reachable
// from anywhere else in this package — BuildSnapshot and
// RedactSubscriptionResponse (redact.go) are this package's only two
// consumers of a raw response body, and both decode through this one type.
type subscriptionResponse struct {
	IsSubsActive *bool  `json:"is_subs_active"`
	SubsTierName string `json:"subs_tier_name"`
	SubsUsage    *struct {
		Window *quotaWindowJSON `json:"window"`
		Weekly *quotaWindowJSON `json:"weekly"`
	} `json:"subs_usage"`
}

type quotaWindowJSON struct {
	UsedPercent        *float64 `json:"used_percent"`
	WindowDurationMins *int     `json:"window_duration_mins"`
	ResetsAt           *int64   `json:"resets_at"`
}

// fiveHourMinutes/weeklyMinutes are the nominal window lengths when the
// response gives used_percent but no explicit duration — mirroring
// core/domain/session.RateLimitSnapshot's own doc comment convention
// ("Claude Code's flat five_hour / seven_day fields map to 300 and 10080
// respectively") and the pinned herdr source's parse_rolling_window
// (FIVE_HOUR_MINUTES = 300).
const (
	fiveHourMinutes = 300
	weeklyMinutes   = 10080
)

// BuildSnapshot is this package's redaction boundary AND parser for a raw
// account-quota response body — see subscriptionResponse's doc comment for
// why decoding through that type is what "retain only approved fields"
// means structurally, not just descriptively.
func BuildSnapshot(body []byte, now time.Time) (session.RateLimitSnapshot, error) {
	var resp subscriptionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return session.RateLimitSnapshot{}, fmt.Errorf("museaccountapi: response is not valid JSON: %w", err)
	}
	if resp.IsSubsActive != nil && !*resp.IsSubsActive {
		return session.RateLimitSnapshot{}, ErrNoActiveSubscription
	}
	if resp.SubsUsage == nil {
		return session.RateLimitSnapshot{}, ErrNoQuotaWindows
	}

	var windows []session.RateLimitWindow
	w, err := quotaWindow(resp.SubsUsage.Window, fiveHourMinutes)
	if err != nil {
		return session.RateLimitSnapshot{}, err
	}
	if w != nil {
		windows = append(windows, *w)
	}
	w, err = quotaWindow(resp.SubsUsage.Weekly, weeklyMinutes)
	if err != nil {
		return session.RateLimitSnapshot{}, err
	}
	if w != nil {
		windows = append(windows, *w)
	}
	if len(windows) == 0 {
		return session.RateLimitSnapshot{}, ErrNoQuotaWindows
	}

	return session.RateLimitSnapshot{
		Windows:   windows,
		SampledAt: now.Unix(),

		Provider:            session.ProviderMeta,
		Product:             resp.SubsTierName,
		ConfirmedAccountRef: accountRef(),
		AttributionEvidence: "muse_account_api",
		ObservationSource:   "account_api",
		AttributionQuality:  session.AttributionQualityConfirmed,
		LastSuccessAt:       now.Unix(),
		LastAttemptAt:       now.Unix(),
	}, nil
}

// quotaWindow maps one subs_usage.{window,weekly} object to a
// session.RateLimitWindow, or returns (nil, nil) when the object carries no
// used_percent (absent, not zero — herdr's an_inactive_or_missing test
// treats an empty {} the same way). used_percent outside [0,100] is
// REJECTED, never clamped (mirrors the pinned source's own
// an_out_of_range_percentage_is_rejected_not_clamped test) — a provider
// value irrlicht cannot trust is a parse failure, not a silently-corrected
// display value.
func quotaWindow(w *quotaWindowJSON, defaultMinutes int) (*session.RateLimitWindow, error) {
	if w == nil || w.UsedPercent == nil {
		return nil, nil
	}
	used := *w.UsedPercent
	if used < 0 || used > 100 {
		return nil, fmt.Errorf("museaccountapi: used_percent %v is out of the [0,100] range", used)
	}
	minutes := defaultMinutes
	if w.WindowDurationMins != nil && *w.WindowDurationMins > 0 {
		minutes = *w.WindowDurationMins
	}
	var resetsAt int64
	if w.ResetsAt != nil {
		resetsAt = *w.ResetsAt
	}
	return &session.RateLimitWindow{
		UsedPercent:   used,
		WindowMinutes: minutes,
		ResetsAt:      resetsAt,
	}, nil
}

// accountRef is the ONLY place ConfirmedAccountRef is decided for a Muse
// snapshot, and it always returns "". Issue #2007's live probe (dated
// 2026-09-20, recorded in the PR body) found no stable provider-issued
// account identifier: Muse's `/status` shows only a display name and an
// email address, neither of which is provider-issued, and both the epic
// (#1977 §3.2/§5.1) and this ticket's own §1.3 forbid deriving one from the
// access token ("a token hash is credential identity, not account
// identity... token rotation changes it").
//
// This does NOT currently gate anything in
// core/application/services/quotainherit.go: donorKey and recipientKey each
// switch on s.Adapter with no "muse" case (checked directly, issue #2007
// review), so a Muse session can never be a cross-session donor or
// recipient there today regardless of what ConfirmedAccountRef holds — that
// protection is "muse isn't a case in the switch", not this field. Leaving
// ConfirmedAccountRef empty is still the right call on its own terms (a
// non-provider-issued value must never be published as if it were one), and
// is what a FUTURE "muse" case in quotainherit.go would need to keep
// respecting if one is ever added.
// TestBuildSnapshot_NeverDerivesAnAccountRef (mutation fixture #1) mutates
// this to return a hash derived from the response body and confirms the
// lock test goes red.
func accountRef() string {
	return ""
}
