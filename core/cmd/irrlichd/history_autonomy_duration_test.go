package main

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Tests for the Autonomy section's AGGREGATE element (chart=autonomy_duration).
//
// They came back with the chart in #1905's restore, unchanged in substance from
// the versions that shipped before #1919 deleted it — the same fixtures, the
// same committed mutations, the same R-7 arithmetic spelled out. What changed is
// the file they live in and the summary type's name
// (historyAutonomyDurationSummary), since the project panels now own the plain
// one.
//
// Everything here except TestAutonomyDuration_RunningRunIsNotAPercentileSample
// is a LOCK: it pins behaviour that existed before, so it passes by
// construction against the restored code and cannot have been seen red first.
// The one thing it can and does show is that the restore is faithful.

// decodeAutonomyDuration issues one request and decodes the aggregate payload.
func decodeAutonomyDuration(t *testing.T, store outbound.AutonomySpanStore, query string) historyAutonomyDurationResponse {
	t.Helper()
	rec := getAutonomy(t, store, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s → %d, want 200", query, rec.Code)
	}
	var resp historyAutonomyDurationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestAutonomyDuration_WindowShapes(t *testing.T) {
	for _, tc := range []struct {
		window        string
		bucketSeconds int64
		buckets       int
	}{
		{"30d", 86400, 30},
		{"1y", 7 * 86400, 52},
	} {
		resp := decodeAutonomyDuration(t, &fakeAutonomyStore{}, "chart=autonomy_duration&window="+tc.window)
		if resp.Chart != chartAutonomyDuration {
			t.Errorf("window=%s chart = %q, want %q", tc.window, resp.Chart, chartAutonomyDuration)
		}
		if resp.BucketSeconds != tc.bucketSeconds {
			t.Errorf("window=%s bucket_seconds = %d, want %d", tc.window, resp.BucketSeconds, tc.bucketSeconds)
		}
		if len(resp.BucketStarts) != tc.buckets {
			t.Errorf("window=%s has %d buckets, want %d", tc.window, len(resp.BucketStarts), tc.buckets)
		}
	}
}

func TestAutonomyDuration_DefaultWindow(t *testing.T) {
	resp := decodeAutonomyDuration(t, &fakeAutonomyStore{}, "chart=autonomy_duration")
	if resp.Window != "30d" {
		t.Errorf("default window = %q, want 30d", resp.Window)
	}
}

// --- An empty bucket is a gap, not a zero -----------------------------------

// TestAutonomyDuration_EmptyBucketIsAGapNotAZero pins the honesty rule: a day
// with no runs must not pull the line to the axis.
//
// The committed mutation is denseBuckets below — the "just emit every bucket"
// build that would draw a zero for every quiet day.
func TestAutonomyDuration_EmptyBucketIsAGapNotAZero(t *testing.T) {
	now := time.Now().Unix()
	// Two spans, both ending today: 29 of the 30 daily buckets are empty.
	store := &fakeAutonomyStore{
		spans: []outbound.AutonomySpan{
			spanEndingAt(now-60, 600, "irrlicht", "ready"),
			spanEndingAt(now-30, 900, "irrlicht", "waiting"),
		},
		earliest: now - 600,
		total:    2,
	}
	resp := decodeAutonomyDuration(t, store, "chart=autonomy_duration&window=30d")
	if len(resp.BucketStarts) != 30 {
		t.Fatalf("bucket_starts = %d, want 30 (the axis is still fully drawn)", len(resp.BucketStarts))
	}
	for _, b := range resp.Buckets {
		if b.Count == 0 {
			t.Fatalf("bucket ts=%d was emitted with count 0 — an empty bucket must be OMITTED "+
				"(a gap), never sent as a zero that pulls the line to the axis", b.TS)
		}
	}
	if len(resp.Buckets) != 1 {
		t.Fatalf("got %d non-empty buckets, want 1 (both spans ended today)", len(resp.Buckets))
	}

	// The MUTATION: a builder that emits a point per bucket regardless. It has
	// to differ from production here, or this test proves nothing.
	denseBuckets := len(resp.BucketStarts)
	if denseBuckets == len(resp.Buckets) {
		t.Fatalf("the dense mutation (%d points) is indistinguishable from production (%d) on this "+
			"fixture — it cannot show that empty buckets are dropped", denseBuckets, len(resp.Buckets))
	}
}

// --- The sample floor -------------------------------------------------------

// TestAutonomyDuration_SampleFloorMarksThinBuckets pins the floor and the fact
// that a thin bucket is MARKED rather than hidden or smoothed.
//
// Both boundary cases are derived from autonomySampleFloor rather than typed,
// so moving the constant moves the test with it — and the third assertion is
// the committed mutation: a build that dropped the floor entirely would report
// thin=false on the under-floor bucket, which is what the second case catches.
func TestAutonomyDuration_SampleFloorMarksThinBuckets(t *testing.T) {
	now := time.Now().Unix()
	// Put everything in "today" so both cases land in one bucket.
	build := func(n int) *fakeAutonomyStore {
		spans := make([]outbound.AutonomySpan, 0, n)
		for i := 0; i < n; i++ {
			spans = append(spans, spanEndingAt(now-int64(i)-1, int64(60+i*10), "irrlicht", "ready"))
		}
		return &fakeAutonomyStore{spans: spans, earliest: now - 86400, total: n}
	}
	for _, tc := range []struct {
		n        int
		wantThin bool
	}{
		{autonomySampleFloor - 1, true},
		{autonomySampleFloor, false},
	} {
		resp := decodeAutonomyDuration(t, build(tc.n), "chart=autonomy_duration&window=30d")
		if resp.SampleFloor != autonomySampleFloor {
			t.Errorf("sample_floor = %d, want %d — clients render the floor, so it travels on the wire",
				resp.SampleFloor, autonomySampleFloor)
		}
		if len(resp.Buckets) == 0 {
			t.Fatalf("n=%d produced no buckets at all — a thin bucket must be MARKED, never hidden", tc.n)
		}
		var thinFound bool
		var total int
		for _, b := range resp.Buckets {
			total += b.Count
			if b.Thin {
				thinFound = true
			}
		}
		if total != tc.n {
			t.Errorf("n=%d: buckets hold %d spans in total, want %d — no sample may be dropped", tc.n, total, tc.n)
		}
		if thinFound != tc.wantThin {
			t.Errorf("n=%d: thin=%v, want %v (floor is %d)", tc.n, thinFound, tc.wantThin, autonomySampleFloor)
		}
	}
}

// TestAutonomyDuration_ThinBucketsOuterLinesAreItsExtremes states the reason
// the floor exists, as an executable claim rather than a comment: under the
// floor the p95 IS the max and the p5 IS the min for a small enough sample, so
// the outer lines stop being percentiles.
func TestAutonomyDuration_ThinBucketsOuterLinesAreItsExtremes(t *testing.T) {
	now := time.Now().Unix()
	spans := []outbound.AutonomySpan{
		spanEndingAt(now-10, 100, "irrlicht", "ready"),
		spanEndingAt(now-20, 200, "irrlicht", "ready"),
		spanEndingAt(now-30, 300, "irrlicht", "waiting"),
	}
	resp := decodeAutonomyDuration(t, &fakeAutonomyStore{spans: spans, total: 3, earliest: now - 300},
		"chart=autonomy_duration&window=30d")
	if len(resp.Buckets) != 1 {
		t.Fatalf("want exactly 1 non-empty bucket, got %d", len(resp.Buckets))
	}
	b := resp.Buckets[0]
	if !b.Thin {
		t.Fatalf("a 3-sample bucket must be thin (floor %d)", autonomySampleFloor)
	}
	// R-7 over {100,200,300}: p95 = 200 + 0.9*100 = 290; p5 = 100 + 0.1*100 = 110.
	if math.Abs(b.P95-290) > 1e-9 || math.Abs(b.P5-110) > 1e-9 {
		t.Errorf("p95/p5 = %v/%v, want 290/110 (R-7 over 3 samples)", b.P95, b.P5)
	}
	if b.Max != 300 || b.Min != 100 {
		t.Errorf("max/min = %v/%v, want 300/100", b.Max, b.Min)
	}
	if b.P95 <= b.Max*0.9 {
		t.Errorf("p95 %v is not pinned near the max %v — the reason the floor exists has changed", b.P95, b.Max)
	}
}

// --- Percentiles are computed daemon-side -----------------------------------

func TestAutonomyDuration_SummaryPercentilesAreServerComputed(t *testing.T) {
	now := time.Now().Unix()
	// The same 12-sample fixture stats' own test uses, so a convention change
	// fails in both places rather than only the one nobody reads.
	spans := make([]outbound.AutonomySpan, 0, 12)
	for i := 1; i <= 12; i++ {
		spans = append(spans, spanEndingAt(now-int64(i), int64(i*100), "irrlicht", "ready"))
	}
	resp := decodeAutonomyDuration(t, &fakeAutonomyStore{spans: spans, total: 12, earliest: now - 1200},
		"chart=autonomy_duration&window=30d")
	if resp.Summary.Count != 12 {
		t.Fatalf("summary count = %d, want 12", resp.Summary.Count)
	}
	if math.Abs(resp.Summary.P95-1145) > 1e-9 {
		t.Errorf("summary p95 = %v, want 1145 (R-7 — nearest rank would give 1200)", resp.Summary.P95)
	}
	if resp.Summary.Min != 100 || resp.Summary.Max != 1200 {
		t.Errorf("summary min/max = %v/%v, want 100/1200 — the true extremes stay figures, not lines",
			resp.Summary.Min, resp.Summary.Max)
	}
}

// --- A running run is not a percentile sample -------------------------------

// TestAutonomyDuration_RunningRunIsNotAPercentileSample pins the one place the
// section's two elements disagree ON PURPOSE, and it is the only test in this
// file that is not a lock: the panels' figure is a MAXIMUM, where "the longest
// run already lasted 3h" is true, so a running run counts there; the aggregate
// chart's figures are PERCENTILES, where folding a floor in always shortens and
// shortens the longest runs hardest.
//
// It went red against the restored chart before the `if s.Running { continue }`
// guard was carried over with it — with the guard removed, the 3h floor becomes
// the window's max and the summary count rises to 2.
func TestAutonomyDuration_RunningRunIsNotAPercentileSample(t *testing.T) {
	now := time.Now().Unix()
	spans := []outbound.AutonomySpan{
		spanEndingAt(now-100, 600, "irrlicht", "ready"),
		{Start: now - 3*3600, End: now, Project: "irrlicht", Session: "live",
			Kind: session.AutonomyKindTopLevel, Running: true},
	}
	store := &fakeAutonomyStore{
		spans: spans, total: 2, earliest: now - 3*3600,
		measurement: outbound.AutonomySpanMeasurement{Running: 1},
	}
	resp := decodeAutonomyDuration(t, store, "chart=autonomy_duration&window=30d")

	if resp.Summary.Count != 1 {
		t.Errorf("summary count = %d, want 1 — a run still in progress is a FLOOR on its own length, "+
			"and a percentile computed from floors always shortens", resp.Summary.Count)
	}
	if resp.Summary.Max != 600 {
		t.Errorf("summary max = %v, want 600 — the 3h floor must not pass as a measured extreme",
			resp.Summary.Max)
	}
	// …and it is COUNTED and NAMED rather than quietly dropped: the reader is
	// told the section is showing fewer samples than runs.
	if resp.Measurement.Running != 1 {
		t.Errorf("measurement.running = %d, want 1 — dropping a run silently is what left 5 of a "+
			"day's 35 runs on the record", resp.Measurement.Running)
	}
	// The same run IS the panels' longest, which is the asymmetry this test
	// exists to state. Asserted here so the two claims cannot drift apart.
	panels := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")
	if got := panelFor(t, panels, "irrlicht").Longest; got != float64(3*3600) {
		t.Errorf("panel longest = %v, want %d — the panels report a maximum, where a floor is a true "+
			"statement about the longest run", got, 3*3600)
	}
}

// --- Provenance, kinds and measurement ride on BOTH payloads ----------------

// TestAutonomyDuration_CarriesTheSameCensusesAsThePanels pins that the section's
// two elements cannot disagree about what they counted: one converter each,
// shared, so a reader comparing the two charts is reading one census.
func TestAutonomyDuration_CarriesTheSameCensusesAsThePanels(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeAutonomyStore{
		spans:    []outbound.AutonomySpan{spanEndingAt(now-60, 600, "irrlicht", "ready")},
		earliest: now - 86400,
		total:    9,
		provenance: outbound.AutonomySpanProvenance{
			Reconstructed: 4,
			CostDerived:   2,
			EraStarts:     map[string]int64{"cost": now - 80000, "": now - 40000},
		},
		kinds:       outbound.AutonomySpanKinds{TopLevel: 5, Subagent: 3, Unknown: 1},
		measurement: outbound.AutonomySpanMeasurement{Running: 1, LowerBoundStart: 2},
	}
	duration := decodeAutonomyDuration(t, store, "chart=autonomy_duration&window=30d")
	panels := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")

	if duration.Provenance.Reconstructed != panels.Provenance.Reconstructed ||
		duration.Provenance.CostDerived != panels.Provenance.CostDerived ||
		duration.Provenance.LiveSince != panels.Provenance.LiveSince ||
		len(duration.Provenance.Boundaries) != len(panels.Provenance.Boundaries) {
		t.Errorf("provenance differs between the elements: %+v vs %+v",
			duration.Provenance, panels.Provenance)
	}
	if duration.Kinds != panels.Kinds {
		t.Errorf("kinds differ between the elements: %+v vs %+v", duration.Kinds, panels.Kinds)
	}
	if duration.Measurement != panels.Measurement {
		t.Errorf("measurement differs between the elements: %+v vs %+v",
			duration.Measurement, panels.Measurement)
	}
	if duration.EarliestSpan != panels.EarliestSpan || duration.TotalRecorded != panels.TotalRecorded {
		t.Errorf("the collecting-since figures differ: %d/%d vs %d/%d",
			duration.EarliestSpan, duration.TotalRecorded, panels.EarliestSpan, panels.TotalRecorded)
	}
	if len(duration.Provenance.Boundaries) == 0 {
		t.Fatal("the fixture produced no source boundary, so this check cannot observe that both " +
			"elements carry the same one")
	}
}

func TestAutonomyDuration_NilStoreServesEmptyButValid(t *testing.T) {
	rec := getAutonomy(t, nil, "chart="+chartAutonomyDuration)
	if rec.Code != http.StatusOK {
		t.Fatalf("no store → %d, want 200", rec.Code)
	}
}

func TestAutonomyDuration_StoreErrorIs500(t *testing.T) {
	store := &fakeAutonomyStore{err: errAutonomyProbe}
	rec := getAutonomy(t, store, "chart=autonomy_duration&window=30d")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("store error → %d, want 500", rec.Code)
	}
}

// errAutonomyProbe is the store failure both elements' 500 tests use, named once
// so neither can drift into asserting a different error's status.
var errAutonomyProbe = errors.New("disk on fire")
