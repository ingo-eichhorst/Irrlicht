package zaiaccountapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"irrlicht/core/domain/session"
)

// Product is the billed product a Z.ai snapshot reports: the coding plan,
// never the general API.
const Product = "GLM Coding Plan"

// limitTypeCredit is the only limit type the live probe returned. CodexBar's
// source also names TOKENS_LIMIT and TIME_LIMIT; no Irrlicht probe has seen
// either, so they are dropped rather than guessed at.
const limitTypeCredit = "CREDIT_LIMIT"

// measureCredits is the unit CREDIT_LIMIT counts in (probe comment §1: "The
// unit is credits"), for session.RateLimitWindow.Measure.
const measureCredits = "credits"

// unitMinutes maps the response's `unit` to minutes per `number`. Only the
// two units the probe observed are known: unit 3 with number 5 reset five
// hours after first use, and unit 6 with number 1 reset seven days after the
// subscription began. Any other unit is dropped, not guessed.
var unitMinutes = map[int]int{3: 60, 6: 7 * 24 * 60}

var (
	// ErrNotSuccessful: the body is JSON but does not say success:true with
	// code 200.
	ErrNotSuccessful = errors.New("zaiaccountapi: response does not report success")
	// ErrNoQuotaWindows: no limit entry this package can read.
	ErrNoQuotaWindows = errors.New("zaiaccountapi: no readable quota window in the response")
	// ErrNoCodingPlan: the response names no coding-plan tier (`level`), so
	// its limits are not presented as the GLM Coding Plan's allowance.
	ErrNoCodingPlan = errors.New("zaiaccountapi: response names no coding-plan tier")
)

// quotaResponse is the STRICT allowlist this package reads from a
// GET /api/monitor/usage/quota/limit body, and the redaction boundary:
// decoding into it drops every field not named here, and RedactQuotaResponse
// re-marshals the same type, so nothing else reaches a fixture. Pointers
// keep "absent" distinct from zero through that round trip — the first
// probe's five-hour window has no nextResetTime, and that absence is the
// evidence. `msg` ("Operation successful") is left out: nothing reads it.
type quotaResponse struct {
	Code    *int       `json:"code,omitempty"`
	Success *bool      `json:"success,omitempty"`
	Data    *quotaData `json:"data,omitempty"`
}

type quotaData struct {
	Limits []limitJSON `json:"limits,omitempty"`
	Level  *string     `json:"level,omitempty"`
}

type limitJSON struct {
	Type          string   `json:"type,omitempty"`
	Unit          *int     `json:"unit,omitempty"`
	Number        *int     `json:"number,omitempty"`
	Usage         *float64 `json:"usage,omitempty"`
	CurrentValue  *float64 `json:"currentValue,omitempty"`
	Remaining     *float64 `json:"remaining,omitempty"`
	Percentage    *float64 `json:"percentage,omitempty"`
	NextResetTime *int64   `json:"nextResetTime,omitempty"`
}

// Quota is a parsed response, every figure as the provider reported it.
type Quota struct {
	// Level is the plan tier (`level`, "lite" on the probe), "" when absent.
	Level   string
	Windows []Window
}

// Window is one CREDIT_LIMIT entry.
type Window struct {
	// ID is the quota's identity within the plan, built from type, number
	// and unit ("CREDIT_LIMIT/5/3"): two entries with one ID are ambiguous.
	ID      string
	Minutes int
	// Limit is `usage`. The probe measured it: usage stayed 2000 while
	// currentValue went 0 → 2 → 11, so `usage` is the allowance and
	// currentValue the amount used. Reading it the other way round inverts
	// the window.
	Limit float64
	// Used is `currentValue`.
	Used float64
	// Remaining is `remaining` as reported. It is not Limit-Used: the probe
	// saw 1997 at 2 used and 1988 at 11 used of 2000, the integers being
	// rounded from fractional credits.
	Remaining float64
	// Percentage is `percentage` as reported: an integer that rounds up (1
	// at both 2/2000 and 11/2000).
	Percentage float64
	// ResetsAt is `nextResetTime`; nil when the response carries none. The
	// probe's five-hour window had none until the first request, and that
	// absence is a real state: it is never filled with zero or a computed
	// time.
	ResetsAt *time.Time
}

// Parse decodes body through the allowlist.
func Parse(body []byte) (Quota, error) {
	var resp quotaResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Quota{}, fmt.Errorf("zaiaccountapi: response is not valid JSON: %w", err)
	}
	if resp.Success == nil || !*resp.Success || resp.Code == nil || *resp.Code != 200 {
		return Quota{}, ErrNotSuccessful
	}
	if resp.Data == nil {
		return Quota{}, ErrNoQuotaWindows
	}
	q := Quota{}
	if resp.Data.Level != nil {
		q.Level = *resp.Data.Level
	}
	seen := map[string]bool{}
	for _, l := range resp.Data.Limits {
		w, ok, err := parseWindow(l)
		if err != nil {
			return Quota{}, err
		}
		if !ok {
			continue
		}
		if seen[w.ID] {
			return Quota{}, fmt.Errorf("zaiaccountapi: two limits share the identity %s", w.ID)
		}
		seen[w.ID] = true
		q.Windows = append(q.Windows, w)
	}
	if len(q.Windows) == 0 {
		return Quota{}, ErrNoQuotaWindows
	}
	return q, nil
}

// parseWindow maps one limit entry. ok is false for an entry this package
// does not present: an unknown type or unit, or one missing a figure. A
// missing figure is never filled in — not with a published allowance, not
// with a value derived from the others. err is set for a figure that is
// present but cannot be true, which makes the whole reading untrusted.
func parseWindow(l limitJSON) (Window, bool, error) {
	if l.Type != limitTypeCredit {
		return Window{}, false, nil
	}
	if l.Unit == nil || l.Number == nil || *l.Number <= 0 {
		return Window{}, false, nil
	}
	perUnit, known := unitMinutes[*l.Unit]
	if !known {
		return Window{}, false, nil
	}
	if l.Usage == nil || *l.Usage <= 0 {
		return Window{}, false, nil
	}
	if l.CurrentValue == nil || l.Remaining == nil || l.Percentage == nil {
		return Window{}, false, nil
	}
	w := Window{
		ID:         l.Type + "/" + strconv.Itoa(*l.Number) + "/" + strconv.Itoa(*l.Unit),
		Minutes:    perUnit * *l.Number,
		Limit:      *l.Usage,
		Used:       *l.CurrentValue,
		Remaining:  *l.Remaining,
		Percentage: *l.Percentage,
	}
	if w.Percentage < 0 || w.Percentage > 100 || w.Used < 0 || w.Remaining < 0 || w.Remaining > w.Limit {
		return Window{}, false, fmt.Errorf("zaiaccountapi: limit %s reports an impossible reading", w.ID)
	}
	if l.NextResetTime != nil {
		if *l.NextResetTime <= 0 {
			return Window{}, false, fmt.Errorf("zaiaccountapi: limit %s reports a non-positive reset time", w.ID)
		}
		t := time.UnixMilli(*l.NextResetTime)
		w.ResetsAt = &t
	}
	return w, true, nil
}

// BuildSnapshot parses body into the session snapshot a Z.ai-routed session
// carries. fetchedAt is when the body was fetched.
//
// The snapshot is the coding plan's: it is built only when the response
// names a plan tier (`level`, the one product identifier the live response
// carries), so a response with limits but no tier — which no probe has
// seen — is not presented as the plan's allowance.
//
// A window with no reported reset is not published. The domain cannot hold
// "no reset": session.RateLimitWindow.ResetsAt is a plain int64, and both
// clients read 0 as stale — platforms/web/quotaChips.js bucketChips marks a
// snapshot stale when any window has resets_at*1000 <= now, "to match
// Swift's mergeIntoBuckets stale rule exactly". Publishing the first
// probe's unstarted five-hour window as 0 would mark the whole reading
// stale, and any computed time would be invented. Leaving it out says the
// truth the probe measured: that window has not started. Parse keeps it,
// with ResetsAt nil. A body whose every window lacks a reset publishes
// nothing (ErrNoQuotaWindows).
func BuildSnapshot(body []byte, fetchedAt time.Time) (session.RateLimitSnapshot, error) {
	q, err := Parse(body)
	if err != nil {
		return session.RateLimitSnapshot{}, err
	}
	if q.Level == "" {
		return session.RateLimitSnapshot{}, ErrNoCodingPlan
	}
	windows := make([]session.RateLimitWindow, 0, len(q.Windows))
	for _, w := range q.Windows {
		resetsAt, ok := publishedReset(w)
		if !ok {
			continue
		}
		windows = append(windows, session.RateLimitWindow{
			UsedPercent:   w.Percentage,
			WindowMinutes: w.Minutes,
			ResetsAt:      resetsAt,
			Measure:       measureCredits,
			LimitKind:     session.LimitKindAllowance,
		})
	}
	if len(windows) == 0 {
		return session.RateLimitSnapshot{}, ErrNoQuotaWindows
	}
	return session.RateLimitSnapshot{
		Windows:             windows,
		PlanType:            q.Level,
		SampledAt:           fetchedAt.Unix(),
		Provider:            session.ProviderZai,
		Product:             Product,
		ConfirmedAccountRef: accountRef(),
		AttributionEvidence: "zai_account_api",
		ObservationSource:   "account_api",
		AttributionQuality:  session.AttributionQualityConfirmed,
		LastSuccessAt:       fetchedAt.Unix(),
		LastAttemptAt:       fetchedAt.Unix(),
	}, nil
}

// publishedReset returns the window's reset in Unix seconds, or ok false
// for a window with no reported reset, which BuildSnapshot leaves out.
func publishedReset(w Window) (int64, bool) {
	if w.ResetsAt == nil {
		return 0, false
	}
	return w.ResetsAt.Unix(), true
}

// accountRef is the only place ConfirmedAccountRef is decided for a Z.ai
// snapshot, and it is always "": the probe found no account identifier
// anywhere in the response (probe comment §1). A fingerprint of the key is
// credential identity, not account identity (#1977 §3.2), and is used only
// to key the poll (services.ZaiAccountQuotaKey), never published here.
func accountRef() string {
	return ""
}
