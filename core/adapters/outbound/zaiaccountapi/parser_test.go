package zaiaccountapi

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// liveProbe is testdata/live_probe_2026-11-21.json: the three redacted
// responses of #2010's live probe.
type liveProbe struct {
	Captures []struct {
		At              string          `json:"at"`
		StatusCode      int             `json:"status_code"`
		RawTopLevelKeys []string        `json:"raw_top_level_keys"`
		RedactedBody    json.RawMessage `json:"redacted_body"`
	} `json:"captures"`
}

func loadLiveProbe(t *testing.T) liveProbe {
	t.Helper()
	data, err := os.ReadFile("testdata/live_probe_2026-11-21.json")
	if err != nil {
		t.Fatalf("reading the live-probe fixture: %v", err)
	}
	var p liveProbe
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("parsing the live-probe fixture: %v", err)
	}
	if len(p.Captures) != 3 {
		t.Fatalf("vacuity: the fixture holds %d captures, want the probe's 3", len(p.Captures))
	}
	return p
}

// captureAt returns the capture taken at the given probe time.
func captureAt(t *testing.T, at string) []byte {
	t.Helper()
	for _, c := range loadLiveProbe(t).Captures {
		if c.At == at {
			return c.RedactedBody
		}
	}
	t.Fatalf("no capture at %s in the live-probe fixture", at)
	return nil
}

const (
	probeNothingUsed = "2026-11-21T13:12:58Z"
	probeAfterRun    = "2026-11-21T13:16:20Z"
	probeAfterRoutes = "2026-11-21T13:20:20Z"
)

func windowByID(t *testing.T, q Quota, id string) Window {
	t.Helper()
	for _, w := range q.Windows {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("no window %s in %+v", id, q.Windows)
	return Window{}
}

const (
	fiveHourID = "CREDIT_LIMIT/5/3"
	weeklyID   = "CREDIT_LIMIT/1/6"
)

// The live probe after one 2-turn run: 2 used of 2000, remaining 1997. Pins
// the field meanings the probe measured — `usage` is the LIMIT,
// currentValue the amount used — and that remaining is taken as reported,
// not derived (2000-2 would be 1998).
func TestParse_LiveProbeFieldMeanings(t *testing.T) {
	q, err := Parse(captureAt(t, probeAfterRun))
	if err != nil {
		t.Fatal(err)
	}
	w := windowByID(t, q, fiveHourID)
	if w.Limit != 2000 || w.Used != 2 {
		t.Errorf("five-hour Limit/Used = %v/%v, want 2000/2: `usage` is the limit and currentValue the used amount", w.Limit, w.Used)
	}
	if w.Remaining != 1997 {
		t.Errorf("five-hour Remaining = %v, want 1997 as reported (never Limit-Used)", w.Remaining)
	}
	if w.Percentage != 1 || w.Minutes != 300 {
		t.Errorf("five-hour Percentage/Minutes = %v/%v, want 1/300", w.Percentage, w.Minutes)
	}
	if w.ResetsAt == nil || w.ResetsAt.UnixMilli() != 1795284966038 {
		t.Errorf("five-hour ResetsAt = %v, want 1795284966038 ms", w.ResetsAt)
	}
	weekly := windowByID(t, q, weeklyID)
	if weekly.Limit != 10000 || weekly.Used != 2 || weekly.Remaining != 9997 || weekly.Minutes != 10080 {
		t.Errorf("weekly = %+v, want limit 10000, used 2, remaining 9997, 10080 minutes", weekly)
	}
	if q.Level != "lite" {
		t.Errorf("Level = %q, want lite", q.Level)
	}
}

// 11 used of 2000 left 1988 remaining, not 1989: the second sample of the
// same non-derivable relationship.
func TestParse_RemainingIsNotLimitMinusUsed(t *testing.T) {
	q, err := Parse(captureAt(t, probeAfterRoutes))
	if err != nil {
		t.Fatal(err)
	}
	if w := windowByID(t, q, fiveHourID); w.Remaining != 1988 {
		t.Fatalf("five-hour Remaining = %v, want the reported 1988 (2000-11 would be 1989)", w.Remaining)
	}
}

// Before anything was used the five-hour window had no nextResetTime. That
// absence is a real state and stays absent: never zero, never "now".
func TestParse_MissingResetStaysMissing(t *testing.T) {
	q, err := Parse(captureAt(t, probeNothingUsed))
	if err != nil {
		t.Fatal(err)
	}
	if w := windowByID(t, q, fiveHourID); w.ResetsAt != nil {
		t.Fatalf("five-hour ResetsAt = %v, want nil: the response carried no nextResetTime", w.ResetsAt)
	}
	if w := windowByID(t, q, weeklyID); w.ResetsAt == nil || w.ResetsAt.UnixMilli() != 1795871277998 {
		t.Fatalf("weekly ResetsAt = %v, want 1795871277998 ms", w.ResetsAt)
	}
}

// The domain cannot hold "no reset" (0 reads as stale to both clients), so
// BuildSnapshot leaves the unstarted window out instead of writing 0.
func TestBuildSnapshot_UnstartedWindowIsNotPublished(t *testing.T) {
	snap, err := BuildSnapshot(captureAt(t, probeNothingUsed), time.Unix(1795270378, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Windows) != 1 || snap.Windows[0].WindowMinutes != 10080 {
		t.Fatalf("windows = %+v, want only the weekly window", snap.Windows)
	}
	for _, w := range snap.Windows {
		if w.ResetsAt <= 0 {
			t.Errorf("window %+v carries ResetsAt %d; a missing reset must never be published as zero", w, w.ResetsAt)
		}
	}
}

func TestBuildSnapshot_LiveProbeShape(t *testing.T) {
	fetched := time.Unix(1795270580, 0)
	snap, err := BuildSnapshot(captureAt(t, probeAfterRun), fetched)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Provider != session.ProviderZai || snap.Product != Product || snap.PlanType != "lite" {
		t.Errorf("Provider/Product/PlanType = %q/%q/%q, want zai/%s/lite", snap.Provider, snap.Product, snap.PlanType, Product)
	}
	if snap.ConfirmedAccountRef != "" {
		t.Errorf("ConfirmedAccountRef = %q, want empty: the response carries no account id", snap.ConfirmedAccountRef)
	}
	if snap.AttributionQuality != session.AttributionQualityConfirmed || snap.ObservationSource != "account_api" {
		t.Errorf("attribution = %q/%q, want confirmed/account_api", snap.AttributionQuality, snap.ObservationSource)
	}
	if snap.SampledAt != fetched.Unix() || snap.LastSuccessAt != fetched.Unix() {
		t.Errorf("SampledAt/LastSuccessAt = %d/%d, want the fetch time %d", snap.SampledAt, snap.LastSuccessAt, fetched.Unix())
	}
	if len(snap.Windows) != 2 {
		t.Fatalf("windows = %+v, want the five-hour and weekly windows", snap.Windows)
	}
	for _, w := range snap.Windows {
		if w.Measure != "credits" || w.LimitKind != session.LimitKindAllowance || w.UsedPercent != 1 {
			t.Errorf("window %+v, want credits, allowance, the reported 1 percent", w)
		}
	}
	if snap.Windows[0].ResetsAt != 1795284966 {
		t.Errorf("five-hour ResetsAt = %d, want 1795284966 (seconds)", snap.Windows[0].ResetsAt)
	}
}

// #2010 §7.2: a published allowance is never hardcoded. An entry with no
// reported limit (`usage`) is not presented — the figure would have no
// observation behind it.
func TestParse_NeverInventsALimit(t *testing.T) {
	body := []byte(`{"code":200,"success":true,"data":{"level":"lite","limits":[
		{"type":"CREDIT_LIMIT","unit":3,"number":5,"currentValue":2,"remaining":1997,"percentage":1,"nextResetTime":1795284966038}]}}`)
	q, err := Parse(body)
	if !errors.Is(err, ErrNoQuotaWindows) {
		t.Fatalf("Parse = %+v, %v; want ErrNoQuotaWindows: a window with no reported limit has no observation behind any figure for it", q, err)
	}
}

// #2010 §7.3: usage from another product is never presented as the coding
// plan's allowance. A response naming no plan tier is not presented as the
// plan, and a limit type no probe has seen is dropped rather than guessed.
func TestBuildSnapshot_OnlyTheCodingPlanIsPresentedAsThePlan(t *testing.T) {
	noTier := []byte(`{"code":200,"success":true,"data":{"limits":[
		{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":2,"remaining":1997,"percentage":1,"nextResetTime":1795284966038}]}}`)
	if snap, err := BuildSnapshot(noTier, time.Unix(1, 0)); !errors.Is(err, ErrNoCodingPlan) {
		t.Errorf("BuildSnapshot(no level) = %+v, %v; want ErrNoCodingPlan", snap, err)
	}

	otherType := []byte(`{"code":200,"success":true,"data":{"level":"lite","limits":[
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":2,"remaining":1997,"percentage":1,"nextResetTime":1795284966038}]}}`)
	if snap, err := BuildSnapshot(otherType, time.Unix(1, 0)); !errors.Is(err, ErrNoQuotaWindows) {
		t.Errorf("BuildSnapshot(TOKENS_LIMIT only) = %+v, %v; want ErrNoQuotaWindows (an unverified limit type is dropped)", snap, err)
	}
}

func TestParse_RejectsUnsuccessfulAndImpossible(t *testing.T) {
	for name, body := range map[string]string{
		"success false":   `{"code":200,"success":false,"data":{"level":"lite","limits":[]}}`,
		"code not 200":    `{"code":401,"success":true,"data":{"level":"lite","limits":[]}}`,
		"no success":      `{"code":200,"data":{"level":"lite","limits":[]}}`,
		"not json":        `<html>`,
		"percent over":    `{"code":200,"success":true,"data":{"level":"lite","limits":[{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":2,"remaining":1997,"percentage":101}]}}`,
		"remaining > lim": `{"code":200,"success":true,"data":{"level":"lite","limits":[{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":0,"remaining":2001,"percentage":0}]}}`,
		"duplicate id":    `{"code":200,"success":true,"data":{"level":"lite","limits":[{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":0,"remaining":2000,"percentage":0},{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":0,"remaining":2000,"percentage":0}]}}`,
		"zero reset":      `{"code":200,"success":true,"data":{"level":"lite","limits":[{"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":2000,"currentValue":0,"remaining":2000,"percentage":0,"nextResetTime":0}]}}`,
	} {
		if q, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: Parse = %+v, want an error", name, q)
		}
	}
}

// An unknown unit is dropped, not mapped to a guessed duration.
func TestParse_UnknownUnitIsDropped(t *testing.T) {
	body := []byte(`{"code":200,"success":true,"data":{"level":"lite","limits":[
		{"type":"CREDIT_LIMIT","unit":4,"number":1,"usage":5,"currentValue":0,"remaining":5,"percentage":0,"nextResetTime":1795284966038}]}}`)
	if _, err := Parse(body); !errors.Is(err, ErrNoQuotaWindows) {
		t.Fatalf("err = %v, want ErrNoQuotaWindows for an unknown unit", err)
	}
}
