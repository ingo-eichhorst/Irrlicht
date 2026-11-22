package mistralvibe

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"irrlicht/core/domain/session"
)

// Snapshot stamps. Product names the Vibe plan, the one Mistral product this
// route reads. AttributionEvidence says what links the reading to a vibe
// session: the user's assertion, made by granting the permission, that the
// Chrome profile's Mistral account is the one the vibe CLI uses. It is never
// provider-issued evidence, and ConfirmedAccountRef stays empty (accountRef).
const (
	Product             = "mistral-vibe"
	AttributionEvidence = "user-asserted"
	ObservationSource   = "browser_backed_api"
)

// nominalMonthMinutes is the window length used only when a response carries
// no reset_at to measure the month from: 30 days. Muse's parser does the
// same for a window with no explicit duration (museaccountapi's
// fiveHourMinutes). Every response the 2026-11-21 probe saw had a reset_at.
const nominalMonthMinutes = 30 * 24 * 60

var (
	// ErrUnexpectedShape: the body is not a one-element tRPC batch whose
	// element carries result.data.json. A tRPC error element (which has an
	// "error" key and no "result") lands here, never as a zero reading.
	ErrUnexpectedShape = errors.New("mistralvibe: response is not the expected tRPC result")
	// ErrNoUsage: the result has no usage_percentage. Absent is not zero.
	ErrNoUsage = errors.New("mistralvibe: response carries no usage_percentage")
	// ErrOutOfRange: usage_percentage is outside [0, 100]. Refused, never
	// clamped or rescaled.
	ErrOutOfRange = errors.New("mistralvibe: usage_percentage is outside [0, 100]")
)

// usageJSON is the STRICT allowlist this package reads from a response:
// exactly the three fields issue #2012's live probe measured
// (testdata/live_probe_2026-11-21T133745Z_usage.json). Decoding into it is
// the redaction boundary: any other field is dropped by the decoder and
// reachable from nowhere in this package. Every field is a pointer, so
// "absent" and "zero" stay different. payg_enabled is kept as provider data
// (RedactUsage carries it into fixtures) and interpreted nowhere.
type usageJSON struct {
	UsagePercentage *float64 `json:"usage_percentage"`
	PAYGEnabled     *bool    `json:"payg_enabled"`
	ResetAt         *string  `json:"reset_at"`
}

type batchElement struct {
	Result *struct {
		Data *struct {
			JSON *usageJSON `json:"json"`
		} `json:"data"`
	} `json:"result"`
}

// Usage is one decoded reading.
type Usage struct {
	// UsedPercent is the provider-reported percentage, 0 to 100, exactly as
	// sent: 0.081567 means 0.08 %, not 8 %. Never multiplied or divided.
	UsedPercent float64
	// ResetAt is when the window resets; zero when the response has none.
	ResetAt time.Time
}

// ParseUsage decodes a response body through the allowlist.
//
// usage_percentage is a percentage from 0 to 100. That scale is
// source-backed, not measured: CodexBar's parser rejects values outside
// 0-100 (MistralUsageFetcher.swift:141) and its test fixture is 2.81, and no
// Mistral page showing the figure has been seen to confirm it. A value
// outside the range is refused. tools/lib/mistralvibe-mutations_test.sh (B2)
// reads it as a fraction instead and requires the 0.081567 live reading to
// go red.
func ParseUsage(body []byte) (Usage, error) {
	u, err := decodeAllowlisted(body)
	if err != nil {
		return Usage{}, err
	}
	if u.UsagePercentage == nil {
		return Usage{}, ErrNoUsage
	}
	pct := *u.UsagePercentage
	if !isPercentage(pct) {
		return Usage{}, fmt.Errorf("%w: %v", ErrOutOfRange, pct)
	}
	out := Usage{UsedPercent: pct}
	if u.ResetAt != nil {
		t, err := time.Parse(time.RFC3339, *u.ResetAt)
		if err != nil {
			return Usage{}, fmt.Errorf("%w: reset_at %q is not RFC 3339", ErrUnexpectedShape, *u.ResetAt)
		}
		out.ResetAt = t
	}
	return out, nil
}

// isPercentage reports whether p is a percentage from 0 to 100. NaN is not.
func isPercentage(p float64) bool {
	return !math.IsNaN(p) && p >= 0 && p <= 100
}

// decodeAllowlisted unwraps the one batch element down to its allowlisted
// json object.
func decodeAllowlisted(body []byte) (*usageJSON, error) {
	var batch []batchElement
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnexpectedShape, err)
	}
	if len(batch) != 1 {
		return nil, ErrUnexpectedShape
	}
	if u := batch[0].usage(); u != nil {
		return u, nil
	}
	return nil, ErrUnexpectedShape
}

// usage is the element's result.data.json object, or nil when any level of
// it is absent.
func (e batchElement) usage() *usageJSON {
	if e.Result == nil || e.Result.Data == nil {
		return nil
	}
	return e.Result.Data.JSON
}

// BuildSnapshot parses body into the snapshot written onto vibe sessions:
// one monthly allowance window with the provider's percentage and reset. It
// claims no used or limit count, because the response has none.
func BuildSnapshot(body []byte, fetchedAt time.Time) (session.RateLimitSnapshot, error) {
	u, err := ParseUsage(body)
	if err != nil {
		return session.RateLimitSnapshot{}, err
	}
	w := session.RateLimitWindow{
		UsedPercent:   u.UsedPercent,
		WindowMinutes: monthMinutes(u.ResetAt),
		LimitKind:     session.LimitKindAllowance,
	}
	if !u.ResetAt.IsZero() {
		w.ResetsAt = u.ResetAt.Unix()
	}
	return session.RateLimitSnapshot{
		Windows:   []session.RateLimitWindow{w},
		SampledAt: fetchedAt.Unix(),

		Provider:            session.ProviderMistral,
		Product:             Product,
		ConfirmedAccountRef: accountRef(),
		AttributionEvidence: AttributionEvidence,
		ObservationSource:   ObservationSource,
		// Confirmed: granting the permission is the confirmation (issue
		// #2012's binding decision; session.AttributionQualityConfirmed's
		// doc), and an unstamped quality would be dropped on every disk
		// decode (filesystem.dropLegacyRateLimit). AttributionEvidence says
		// it was user-asserted.
		AttributionQuality: session.AttributionQualityConfirmed,
		LastSuccessAt:      fetchedAt.Unix(),
		LastAttemptAt:      fetchedAt.Unix(),
	}, nil
}

// monthMinutes is the length of the calendar month that ends at reset: the
// probe's reset was 2026-12-01T00:00:00Z, the start of a UTC month. With no
// reset it is nominalMonthMinutes.
func monthMinutes(reset time.Time) int {
	if reset.IsZero() {
		return nominalMonthMinutes
	}
	return int(reset.Sub(reset.AddDate(0, -1, 0)).Minutes())
}

// accountRef is the only place ConfirmedAccountRef is decided for a Mistral
// Vibe snapshot, and it is always empty. The response carries no account,
// organization or plan identifier (issue #2012's live probe), and a cookie
// value is credential identity, not account identity. The link to the vibe
// CLI's account is the user's assertion, recorded in AttributionEvidence.
func accountRef() string {
	return ""
}
