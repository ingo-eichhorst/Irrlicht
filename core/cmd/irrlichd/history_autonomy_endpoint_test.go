package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"irrlicht/core/ports/outbound"
)

// fakeAutonomyStore serves a fixed span list, ignoring the window — the
// handler's bucketing, not the store's filtering, is what these tests are
// about.
type fakeAutonomyStore struct {
	spans      []outbound.AutonomySpan
	earliest   int64
	total      int
	truncated  bool
	provenance outbound.AutonomySpanProvenance
	// kinds is the window's run-kind census (#1905 subagents). A field rather
	// than something derived from `spans`, because the handler's job is to
	// carry the store's census onto the wire, not to recompute it.
	kinds outbound.AutonomySpanKinds
	// measurement is the window's lower-bound census (#1905 recording): how
	// many of the runs are still going, and how many started before Irrlicht
	// was watching. A field for the same reason `kinds` is one — the handler's
	// job is to carry it onto the wire, not to recompute it.
	measurement outbound.AutonomySpanMeasurement
	err         error
	lastQuery   outbound.AutonomySpanQuery
}

func (f *fakeAutonomyStore) RecordSpan(outbound.AutonomySpan) error     { return nil }
func (f *fakeAutonomyStore) RecordOpenSpan(outbound.AutonomySpan) error { return nil }
func (f *fakeAutonomyStore) SyncOpenSpans([]outbound.AutonomySpan) error {
	return nil
}
func (f *fakeAutonomyStore) OpenSpans() ([]outbound.AutonomySpan, error) { return nil, nil }
func (f *fakeAutonomyStore) Prune(int) error                             { return nil }
func (f *fakeAutonomyStore) SpansInWindow(q outbound.AutonomySpanQuery) (*outbound.AutonomySpanResult, error) {
	f.lastQuery = q
	if f.err != nil {
		return nil, f.err
	}
	return &outbound.AutonomySpanResult{
		Spans:         f.spans,
		EarliestStart: f.earliest,
		TotalRecorded: f.total,
		Truncated:     f.truncated,
		Provenance:    f.provenance,
		Kinds:         f.kinds,
		Measurement:   f.measurement,
	}, nil
}

func getAutonomy(t *testing.T, store outbound.AutonomySpanStore, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/history?"+query, nil)
	handleGetHistory(nil, nil, nil, nil, store)(rec, req)
	return rec
}

// spanEndingAt builds a span of the given length that ended at `end`.
func spanEndingAt(end, length int64, project, reason string) outbound.AutonomySpan {
	return outbound.AutonomySpan{
		Start:   end - length,
		End:     end,
		Project: project,
		Session: fmt.Sprintf("s-%d", end),
		Reason:  reason,
	}
}

// --- The two window vocabularies must stay distinct -------------------------

// TestAutonomyWindows_AreNotHistoryGranularities is the tripwire for the trap
// the issue calls out by name: chart=state's ?granularity= keys and the
// autonomy strip's ?window= keys OVERLAP textually and mean different things.
// A granularity key is a BUCKET WIDTH times a count; a window key is the
// WINDOW ITSELF.
//
// The committed mutation is mergedResolver below — the refactor this test
// exists to stop, i.e. "these tables look the same, let's have one". The test
// asserts production disagrees with it, so the day someone merges them this
// goes red instead of `24h` silently becoming a 30-day strip.
func TestAutonomyWindows_AreNotHistoryGranularities(t *testing.T) {
	// The MUTATION: resolve a strip window by reusing chart=state's table.
	mergedResolver := func(key string) (seconds int64, ok bool) {
		spec, known := historyGranularitySpecs[key]
		if !known {
			return 0, false
		}
		return spec.bucketSeconds * spec.buckets, true
	}

	shared := []string{"8h", "24h", "7d"}
	for _, key := range shared {
		want, ok := autonomySpanWindowSeconds[key]
		if !ok {
			t.Fatalf("autonomySpanWindowSeconds is missing %q — the overlap this test guards is gone; "+
				"re-derive the shared key list rather than deleting the check", key)
		}
		merged, ok := mergedResolver(key)
		if !ok {
			t.Fatalf("historyGranularitySpecs no longer has %q, so the two tables can no longer collide "+
				"on it; re-check whether this tripwire still covers the trap", key)
		}
		if merged == want {
			t.Fatalf("window %q resolves to the same %d seconds under both tables — the strip's window "+
				"vocabulary has been merged into chart=state's granularity vocabulary, which silently "+
				"redefines what the user's %q means", key, want, key)
		}
	}

	// And spell the headline case out, so the failure message names the bug
	// rather than only the inequality: 24h is one day here, thirty there.
	if autonomySpanWindowSeconds["24h"] != 24*3600 {
		t.Errorf("strip window 24h = %ds, want 86400 (one day)", autonomySpanWindowSeconds["24h"])
	}
	if merged, _ := mergedResolver("24h"); merged != 30*86400 {
		t.Errorf("granularity 24h resolves to a %ds window, want 30 days — the trap has changed shape", merged)
	}
}

func TestAutonomyWindows_RejectTheOtherElementsVocabulary(t *testing.T) {
	// The duration chart offers 30d|1y; the strip offers 8h…12mo. Neither
	// accepts the other's keys, so a client that sends the wrong one is told,
	// rather than silently served some other window.
	rec := getAutonomy(t, &fakeAutonomyStore{}, "chart=autonomy_duration&window=8h")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("chart=autonomy_duration&window=8h → %d, want 400", rec.Code)
	}
	rec = getAutonomy(t, &fakeAutonomyStore{}, "chart=autonomy_spans&window=1y")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("chart=autonomy_spans&window=1y → %d, want 400", rec.Code)
	}
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
		store := &fakeAutonomyStore{}
		rec := getAutonomy(t, store, "chart=autonomy_duration&window="+tc.window)
		if rec.Code != http.StatusOK {
			t.Fatalf("window=%s → %d, want 200", tc.window, rec.Code)
		}
		var resp historyAutonomyDurationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.BucketSeconds != tc.bucketSeconds {
			t.Errorf("window=%s bucket_seconds = %d, want %d", tc.window, resp.BucketSeconds, tc.bucketSeconds)
		}
		if len(resp.BucketStarts) != tc.buckets {
			t.Errorf("window=%s has %d buckets, want %d", tc.window, len(resp.BucketStarts), tc.buckets)
		}
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
	rec := getAutonomy(t, store, "chart=autonomy_duration&window=30d")
	var resp historyAutonomyDurationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
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
		rec := getAutonomy(t, build(tc.n), "chart=autonomy_duration&window=30d")
		var resp historyAutonomyDurationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
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
	rec := getAutonomy(t, &fakeAutonomyStore{spans: spans, total: 3, earliest: now - 300},
		"chart=autonomy_duration&window=30d")
	var resp historyAutonomyDurationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
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
	rec := getAutonomy(t, &fakeAutonomyStore{spans: spans, total: 12, earliest: now - 1200},
		"chart=autonomy_duration&window=30d")
	var resp historyAutonomyDurationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
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

// --- "No data" must never read as "you did nothing" -------------------------

func TestAutonomy_EmptyWindowStillReportsWhenCollectionStarted(t *testing.T) {
	now := time.Now().Unix()
	// Nothing in the window, but the log has been collecting since yesterday.
	store := &fakeAutonomyStore{earliest: now - 86400, total: 7}
	for _, chart := range []string{chartAutonomyDuration, chartAutonomySpans} {
		rec := getAutonomy(t, store, "chart="+chart)
		if rec.Code != http.StatusOK {
			t.Fatalf("chart=%s → %d, want 200", chart, rec.Code)
		}
		var probe struct {
			Earliest int64 `json:"earliest_span"`
			Total    int   `json:"total_recorded"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &probe); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if probe.Earliest != now-86400 {
			t.Errorf("chart=%s earliest_span = %d, want %d — without it an empty view cannot "+
				"distinguish \"nothing ran\" from \"this feature had not shipped yet\"",
				chart, probe.Earliest, now-86400)
		}
		if probe.Total != 7 {
			t.Errorf("chart=%s total_recorded = %d, want 7", chart, probe.Total)
		}
	}
}

func TestAutonomy_NilStoreServesEmptyButValid(t *testing.T) {
	for _, chart := range []string{chartAutonomyDuration, chartAutonomySpans} {
		rec := getAutonomy(t, nil, "chart="+chart)
		if rec.Code != http.StatusOK {
			t.Fatalf("chart=%s with no store → %d, want 200", chart, rec.Code)
		}
	}
}

// --- The strip payload ------------------------------------------------------

func TestAutonomySpans_RowOrderAndFields(t *testing.T) {
	now := time.Now().Unix()
	store := &fakeAutonomyStore{
		spans: []outbound.AutonomySpan{
			spanEndingAt(now-100, 60, "articles", "ready"),
			spanEndingAt(now-200, 3600, "irrlicht", "waiting"),
			spanEndingAt(now-300, 120, "articles", "error"),
		},
		earliest: now - 4000,
		total:    3,
	}
	rec := getAutonomy(t, store, "chart=autonomy_spans&window=24h")
	var resp historyAutonomySpansResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Spans) != 3 {
		t.Fatalf("spans = %d, want 3", len(resp.Spans))
	}
	// irrlicht has 3600 autonomous seconds, articles 180 — busiest row first.
	if len(resp.Projects) != 2 || resp.Projects[0] != "irrlicht" {
		t.Errorf("projects = %v, want irrlicht first (most autonomous seconds)", resp.Projects)
	}
	if resp.Spans[0].Reason == "" {
		t.Error("span rows must carry their end reason — it is the signal the strip draws")
	}
	if store.lastQuery.Limit != autonomySpanLimit {
		t.Errorf("strip query Limit = %d, want %d", store.lastQuery.Limit, autonomySpanLimit)
	}
	if store.lastQuery.End-store.lastQuery.Start != 24*3600 {
		t.Errorf("window=24h asked the store for %ds, want 86400",
			store.lastQuery.End-store.lastQuery.Start)
	}
}

func TestAutonomySpans_TruncationIsReported(t *testing.T) {
	store := &fakeAutonomyStore{truncated: true, total: 99999}
	rec := getAutonomy(t, store, "chart=autonomy_spans&window=12mo")
	var resp historyAutonomySpansResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Truncated {
		t.Error("a clipped strip must say so — a partial window drawn as if whole is exactly the " +
			"silent-wrong-number failure this feature exists to avoid")
	}
}

func TestAutonomy_DefaultWindows(t *testing.T) {
	store := &fakeAutonomyStore{}
	rec := getAutonomy(t, store, "chart=autonomy_spans")
	var spans historyAutonomySpansResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &spans); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if spans.Window != "24h" {
		t.Errorf("default strip window = %q, want 24h", spans.Window)
	}
	rec = getAutonomy(t, store, "chart=autonomy_duration")
	var dur historyAutonomyDurationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &dur); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dur.Window != "30d" {
		t.Errorf("default duration window = %q, want 30d", dur.Window)
	}
}

func TestAutonomy_StoreErrorIs500(t *testing.T) {
	store := &fakeAutonomyStore{err: fmt.Errorf("disk on fire")}
	rec := getAutonomy(t, store, "chart=autonomy_duration&window=30d")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("store error → %d, want 500", rec.Code)
	}
}
