package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// fakeAutonomyStore serves a fixed span list, ignoring the window — the
// handler's bucketing, not the store's filtering, is what these tests are
// about.
type fakeAutonomyStore struct {
	spans      []outbound.AutonomySpan
	earliest   int64
	total      int
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

// decodeAutonomy issues one request and decodes the panels payload.
func decodeAutonomy(t *testing.T, store outbound.AutonomySpanStore, query string) historyAutonomyProjectsResponse {
	t.Helper()
	rec := getAutonomy(t, store, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s → %d, want 200", query, rec.Code)
	}
	var resp historyAutonomyProjectsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// panelFor returns the panel for one project, failing when it was not drawn.
func panelFor(t *testing.T, resp historyAutonomyProjectsResponse, project string) historyAutonomyPanel {
	t.Helper()
	for _, p := range resp.Panels {
		if p.Project == project {
			return p
		}
	}
	t.Fatalf("no panel for %q; drew %d panels", project, len(resp.Panels))
	return historyAutonomyPanel{}
}

// spanEndingAt builds a span of the given length that ended at `end`.
func spanEndingAt(end, length int64, project, reason string) outbound.AutonomySpan {
	return outbound.AutonomySpan{
		Start:   end - length,
		End:     end,
		Project: project,
		Session: fmt.Sprintf("s-%d", end),
		Reason:  reason,
		Kind:    session.AutonomyKindTopLevel,
	}
}

// --- The window vocabulary must stay distinct from chart=state's -------------

// TestAutonomyWindows_AreNotHistoryGranularities is the tripwire for the trap
// the issue calls out by name: chart=state's ?granularity= keys and the
// Autonomy section's ?window= keys OVERLAP textually and mean different things.
// A granularity key is a BUCKET WIDTH times a count; a window key is the WINDOW
// ITSELF.
//
// The committed mutation is mergedResolver below — the refactor this test
// exists to stop, i.e. "these tables look the same, let's have one". The test
// asserts production disagrees with it, so the day someone merges them this
// goes red instead of `30d` silently becoming a nine-hundred-day window.
func TestAutonomyWindows_AreNotHistoryGranularities(t *testing.T) {
	// The MUTATION: resolve an Autonomy window by reusing chart=state's table.
	mergedResolver := func(key string) (seconds int64, ok bool) {
		spec, known := historyGranularitySpecs[key]
		if !known {
			return 0, false
		}
		return spec.bucketSeconds * spec.buckets, true
	}

	// The keys the two tables share. Derived by intersecting them rather than
	// typed, so a table that grows a new collision is covered without anyone
	// remembering to add it here — and so an EMPTY intersection fails loudly
	// instead of passing an empty loop (AGENTS.md: a check that cannot run must
	// not look like a check that found nothing).
	shared := []string{}
	for key := range autonomyWindowSpecs {
		if _, collides := historyGranularitySpecs[key]; collides {
			shared = append(shared, key)
		}
	}
	if len(shared) == 0 {
		t.Fatalf("autonomyWindowSpecs %v and historyGranularitySpecs no longer share a key, so this "+
			"tripwire cannot observe the trap it guards; re-check whether the trap still exists rather "+
			"than deleting the check", autonomyWindowSpecs)
	}

	for _, key := range shared {
		spec := autonomyWindowSpecs[key]
		want := spec.bucketSeconds * spec.buckets
		merged, ok := mergedResolver(key)
		if !ok {
			t.Fatalf("historyGranularitySpecs no longer has %q, so the two tables can no longer collide "+
				"on it; re-check whether this tripwire still covers the trap", key)
		}
		if merged == want {
			t.Fatalf("window %q resolves to the same %d seconds under both tables — the Autonomy window "+
				"vocabulary has been merged into chart=state's granularity vocabulary, which silently "+
				"redefines what the user's %q means", key, want, key)
		}
	}

	// And spell the headline case out, so the failure message names the bug
	// rather than only the inequality: 30d is thirty days here, and a bucket
	// width times thirty there.
	if spec := autonomyWindowSpecs["30d"]; spec.bucketSeconds*spec.buckets != 30*86400 {
		t.Errorf("Autonomy window 30d = %ds, want %d (thirty days)",
			spec.bucketSeconds*spec.buckets, 30*86400)
	}
	if merged, _ := mergedResolver("30d"); merged == 30*86400 {
		t.Errorf("granularity 30d resolves to a %ds window too — the trap has changed shape", merged)
	}
}

func TestAutonomy_RejectsAnUnknownWindow(t *testing.T) {
	// The section offers 30d|1y — ONE vocabulary for BOTH elements, so the two
	// charts drawn one above the other can never show different periods. The run
	// strip's old 8h…12mo vocabulary went with the strip, so a client still
	// sending one is told rather than silently served some other window.
	for _, chart := range []string{chartAutonomyProjects, chartAutonomyDuration} {
		for _, window := range []string{"8h", "24h", "12mo", "week"} {
			rec := getAutonomy(t, &fakeAutonomyStore{}, "chart="+chart+"&window="+window)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("chart=%s window=%s → %d, want 400", chart, window, rec.Code)
			}
			// The body names the chart that was asked for. A caller that sent
			// the wrong window to the right chart otherwise cannot tell which
			// of the section's two requests failed.
			if body := rec.Body.String(); !strings.Contains(body, chart) {
				t.Errorf("chart=%s window=%s 400 body %q does not name the chart", chart, window, body)
			}
		}
	}
}

// TestAutonomy_BothElementsShareOneWindowVocabulary pins the property the
// section's single Range control depends on: whatever window the aggregate
// chart accepts, the project panels accept, and they resolve to the SAME
// period.
//
// The committed mutation is splitVocabulary below — the shape the run strip
// had, where each element owned its own window keys. Two windows under one
// control is how a reader ends up comparing a month of one chart against a year
// of the other with nothing on screen saying they disagree.
func TestAutonomy_BothElementsShareOneWindowVocabulary(t *testing.T) {
	// The MUTATION: a per-chart window table, the run strip's old shape.
	splitVocabulary := map[string]map[string]bool{
		chartAutonomyDuration: {"30d": true, "1y": true},
		chartAutonomyProjects: {"8h": true, "24h": true, "7d": true},
	}
	if splitVocabulary[chartAutonomyDuration]["24h"] == splitVocabulary[chartAutonomyProjects]["24h"] {
		t.Fatal("the split-vocabulary mutation no longer disagrees with itself, so this fixture cannot " +
			"show that production shares one table")
	}

	for window := range autonomyWindowSpecs {
		durationResp := decodeAutonomyDuration(t, &fakeAutonomyStore{}, "chart="+chartAutonomyDuration+"&window="+window)
		panelsResp := decodeAutonomy(t, &fakeAutonomyStore{}, "chart="+chartAutonomyProjects+"&window="+window)
		if durationResp.BucketSeconds != panelsResp.BucketSeconds {
			t.Errorf("window=%s: aggregate buckets %ds, panels %ds — one Range control moving two "+
				"different periods", window, durationResp.BucketSeconds, panelsResp.BucketSeconds)
		}
		if len(durationResp.BucketStarts) != len(panelsResp.BucketStarts) {
			t.Errorf("window=%s: aggregate has %d buckets, panels %d",
				window, len(durationResp.BucketStarts), len(panelsResp.BucketStarts))
		}
	}
}

func TestAutonomy_WindowShapes(t *testing.T) {
	for _, tc := range []struct {
		window        string
		bucketSeconds int64
		buckets       int
	}{
		{"30d", 86400, 30},
		{"1y", 7 * 86400, 52},
	} {
		store := &fakeAutonomyStore{}
		resp := decodeAutonomy(t, store, "chart=autonomy_projects&window="+tc.window)
		if resp.BucketSeconds != tc.bucketSeconds {
			t.Errorf("window=%s bucket_seconds = %d, want %d", tc.window, resp.BucketSeconds, tc.bucketSeconds)
		}
		if len(resp.BucketStarts) != tc.buckets {
			t.Errorf("window=%s has %d buckets, want %d", tc.window, len(resp.BucketStarts), tc.buckets)
		}
	}
}

func TestAutonomy_DefaultWindow(t *testing.T) {
	resp := decodeAutonomy(t, &fakeAutonomyStore{}, "chart=autonomy_projects")
	if resp.Window != "30d" {
		t.Errorf("default window = %q, want 30d", resp.Window)
	}
}

// --- An empty bucket is a gap, not a zero -----------------------------------

// TestAutonomy_EmptyBucketIsAGapNotAZero pins the honesty rule for BOTH marks a
// panel draws: a day with no runs must break the line AND draw no bar. A zero
// bar sitting on the axis looks measured, and a zero point pulls the line down
// to it.
//
// The committed mutation is denseBuckets below — the "just emit every bucket"
// build that would draw a zero-height bar and a floor point for every quiet day.
func TestAutonomy_EmptyBucketIsAGapNotAZero(t *testing.T) {
	now := time.Now().Unix()
	// Two spans, both ending today: 29 of the 30 daily buckets are empty.
	store := &fakeAutonomyStore{
		spans: []outbound.AutonomySpan{
			spanEndingAt(now-60, 600, "irrlicht", "ready"),
			spanEndingAt(now-30, 900, "irrlicht", "waiting"),
		},
		earliest: now - 900,
		total:    2,
	}
	resp := decodeAutonomy(t, store, "chart=autonomy_projects&window=30d")
	if len(resp.BucketStarts) != 30 {
		t.Fatalf("bucket_starts = %d, want 30 (the axis is still fully drawn)", len(resp.BucketStarts))
	}
	panel := panelFor(t, resp, "irrlicht")
	for _, b := range panel.Buckets {
		if b.Longest <= 0 && b.Peak <= 0 {
			t.Fatalf("bucket ts=%d was emitted with longest=0 AND peak=0 — an empty bucket must be "+
				"OMITTED (a gap), never sent as a pair of measured zeros", b.TS)
		}
	}
	if len(panel.Buckets) != 1 {
		t.Fatalf("got %d non-empty buckets, want 1 (both spans ended today)", len(panel.Buckets))
	}

	// The MUTATION: a builder that emits a point per bucket regardless. It has
	// to differ from production here, or this test proves nothing.
	denseBuckets := len(resp.BucketStarts)
	if denseBuckets == len(panel.Buckets) {
		t.Fatalf("the dense mutation (%d buckets) is indistinguishable from production (%d) on this "+
			"fixture — it cannot show that empty buckets are dropped", denseBuckets, len(panel.Buckets))
	}
}

// --- Concurrency comes from overlap -----------------------------------------

// TestAutonomyConcurrency_PeakFromOverlap checks the derivation against a
// hand-built fixture whose answer can be read off the timeline by eye.
//
// One bucket-day holding four runs, laid out so the peak is unambiguous:
//
//	A  ├────────────────────────┤            (t+0    … t+4000)
//	B      ├──────────┤                      (t+1000 … t+2000)
//	C          ├──────────────┤              (t+1500 … t+3000)
//	D                              ├────┤    (t+5000 … t+5500)
//
// Between t+1500 and t+2000 three runs overlap; nothing else in the day reaches
// three. So the peak is 3, and the AVERAGE over the day is far below 1.
//
// The committed mutation is meanConcurrency below: the "report the average
// instead" build the maintainer explicitly rejected, because an average over a
// day is dominated by the hours nothing ran.
func TestAutonomyConcurrency_PeakFromOverlap(t *testing.T) {
	base := time.Now().Unix() - 40000
	spans := []outbound.AutonomySpan{
		{Start: base, End: base + 4000, Project: "irrlicht", Session: "a", Kind: session.AutonomyKindTopLevel},
		{Start: base + 1000, End: base + 2000, Project: "irrlicht", Session: "b", Kind: session.AutonomyKindTopLevel},
		{Start: base + 1500, End: base + 3000, Project: "irrlicht", Session: "c", Kind: session.AutonomyKindSubagent},
		{Start: base + 5000, End: base + 5500, Project: "irrlicht", Session: "d", Kind: session.AutonomyKindTopLevel},
	}
	resp := decodeAutonomy(t, &fakeAutonomyStore{spans: spans, total: 4, earliest: base},
		"chart=autonomy_projects&window=30d")
	panel := panelFor(t, resp, "irrlicht")
	if panel.Peak != 3 {
		t.Fatalf("window peak = %d, want 3 (A, B and C overlap between +1500 and +2000)", panel.Peak)
	}
	if !panel.PeakSplit || panel.PeakTop != 2 || panel.PeakSub != 1 {
		t.Errorf("peak split = %d + %d sub (known=%v), want 2 + 1 sub known — every run alive at the "+
			"peak carried a kind, so the split is derivable", panel.PeakTop, panel.PeakSub, panel.PeakSplit)
	}
	var bucketPeak int
	for _, b := range panel.Buckets {
		if b.Peak > bucketPeak {
			bucketPeak = b.Peak
		}
	}
	if bucketPeak != panel.Peak {
		t.Errorf("highest bucket peak %d does not match the header's %d — the header must never report "+
			"a number the bars below it contradict", bucketPeak, panel.Peak)
	}

	// The MUTATION: mean concurrency over the day, which is what "average" would
	// produce. Total working-seconds / bucket width.
	var workingSeconds int64
	for _, s := range spans {
		workingSeconds += s.Duration()
	}
	meanConcurrency := float64(workingSeconds) / 86400
	if int(meanConcurrency+0.5) == panel.Peak {
		t.Fatalf("the mean mutation (%.3f) rounds to the same figure as the peak (%d) on this fixture — "+
			"it cannot show that the reported number is a PEAK", meanConcurrency, panel.Peak)
	}
}

// TestAutonomyConcurrency_HandoverIsNotAnOverlap pins the half-open rule. A run
// that ends exactly as the next begins is one session working through a chain of
// turns, not two agents at once.
//
// The committed mutation is closedIntervals: apply starts before ends at the
// same instant, which is the one-character ordering slip that would report every
// back-to-back turn as concurrency 2.
func TestAutonomyConcurrency_HandoverIsNotAnOverlap(t *testing.T) {
	base := time.Now().Unix() - 40000
	spans := []outbound.AutonomySpan{
		{Start: base, End: base + 100, Project: "p", Session: "a", Kind: session.AutonomyKindTopLevel},
		{Start: base + 100, End: base + 200, Project: "p", Session: "b", Kind: session.AutonomyKindTopLevel},
	}
	got := autonomyWindowPeak(spans, base, base+86400)
	if got.peak != 1 {
		t.Fatalf("peak = %d over two back-to-back runs, want 1 — a handover is not an overlap", got.peak)
	}

	// The MUTATION: the same sweep with starts applied before ends.
	closed := 0
	cur := 0
	type ev struct {
		ts    int64
		delta int
	}
	evs := []ev{}
	for _, s := range spans {
		evs = append(evs, ev{s.Start, 1}, ev{s.End, -1})
	}
	for i := 0; i < len(evs); i++ { // starts first at a tie
		for j := i + 1; j < len(evs); j++ {
			if evs[j].ts < evs[i].ts || (evs[j].ts == evs[i].ts && evs[j].delta > evs[i].delta) {
				evs[i], evs[j] = evs[j], evs[i]
			}
		}
	}
	for _, e := range evs {
		cur += e.delta
		if cur > closed {
			closed = cur
		}
	}
	if closed == got.peak {
		t.Fatalf("the closed-interval mutation also reports %d — this fixture cannot show that ends "+
			"are applied before starts", closed)
	}
}

// TestAutonomyConcurrency_SplitOnlyWhereKindIsKnown pins the rule that keeps the
// figure honest across the back-fill boundary: before 18 Aug 2026 most rows
// carry session.AutonomyKindUnknown, and there is no way to recover which they
// were. A split is shown only when EVERY run alive at the peak said which kind
// it was.
//
// The committed mutation is unknownAsTopLevel below — reading a blank kind as
// top-level, which is the exact failure session.AutonomyKindOrUnknown exists to
// prevent: it would report "5 (5 + 0 sub)" over data that never said.
func TestAutonomyConcurrency_SplitOnlyWhereKindIsKnown(t *testing.T) {
	base := time.Now().Unix() - 40000
	spans := []outbound.AutonomySpan{
		{Start: base, End: base + 1000, Project: "p", Session: "a", Kind: session.AutonomyKindTopLevel},
		{Start: base + 100, End: base + 900, Project: "p", Session: "b", Kind: session.AutonomyKindUnknown},
	}
	got := autonomyWindowPeak(spans, base, base+86400)
	if got.peak != 2 {
		t.Fatalf("peak = %d, want 2 — an unclassified run still counts towards how many were working", got.peak)
	}
	if got.splitKnown {
		t.Fatalf("split reported as known (%d + %d sub) over a window holding an unclassified run — "+
			"the total must be shown alone rather than a guess", got.topLevel, got.subagent)
	}

	// The MUTATION: resolve a blank/unknown kind to top-level.
	unknownAsTopLevel := func(kind string) string {
		if kind == session.AutonomyKindSubagent {
			return session.AutonomyKindSubagent
		}
		return session.AutonomyKindTopLevel
	}
	mutated := make([]outbound.AutonomySpan, len(spans))
	copy(mutated, spans)
	for i := range mutated {
		mutated[i].Kind = unknownAsTopLevel(mutated[i].Kind)
	}
	if m := autonomyWindowPeak(mutated, base, base+86400); !m.splitKnown {
		t.Fatalf("the unknown-as-top-level mutation still reports the split as unknown — this fixture " +
			"cannot show that an unclassified run suppresses the split")
	}

	// And the positive half: once both rows say what they were, the split is
	// shown. Absence of a finding and inability to look must not look alike.
	spans[1].Kind = session.AutonomyKindSubagent
	known := autonomyWindowPeak(spans, base, base+86400)
	if !known.splitKnown || known.topLevel != 1 || known.subagent != 1 {
		t.Errorf("split = %d + %d sub (known=%v), want 1 + 1 sub known", known.topLevel, known.subagent, known.splitKnown)
	}
}

// --- Every project, ranked by total autonomous time -------------------------

// TestAutonomy_SendsEveryProjectRankedNotJustTheTopFew pins what the dropdown
// depends on: the payload carries a panel for EVERY project with a run in the
// window, ranked, not a five-project slice of them.
//
// It went red against the shipped build (autonomyPanelCount = 5), which sent
// five panels and reported the other three as `more_projects` — a picker built
// on that payload could offer only five of the eight projects its own summary
// counted, and the three it dropped were unreachable.
func TestAutonomy_SendsEveryProjectRankedNotJustTheTopFew(t *testing.T) {
	now := time.Now().Unix()
	spans := []outbound.AutonomySpan{}
	// Eight projects, each with a distinct total so the ranking is unambiguous.
	for i := 1; i <= 8; i++ {
		spans = append(spans, spanEndingAt(now-int64(i)*10, int64(i)*100, fmt.Sprintf("p%d", i), "ready"))
	}
	resp := decodeAutonomy(t, &fakeAutonomyStore{spans: spans, total: len(spans), earliest: now - 10000},
		"chart=autonomy_projects&window=30d")
	if len(resp.Panels) != 8 {
		t.Fatalf("drew %d panels over 8 projects, want 8 — a project the payload omits is one the "+
			"dropdown cannot offer", len(resp.Panels))
	}
	if resp.MoreProjects != 0 {
		t.Errorf("more_projects = %d, want 0 — nothing was left out", resp.MoreProjects)
	}
	if resp.PanelLimit != autonomyPanelCount {
		t.Errorf("panel_limit = %d, want %d — the cap is on the wire so a client reports what it was "+
			"sent rather than re-deciding the number", resp.PanelLimit, autonomyPanelCount)
	}
	if resp.Summary.Projects != 8 {
		t.Errorf("summary projects = %d, want 8", resp.Summary.Projects)
	}
	// Ranked, most autonomous time first — the order the client's default
	// selection (rank 1) and its dropdown both read.
	want := []string{"p8", "p7", "p6", "p5", "p4", "p3", "p2", "p1"}
	for i, p := range resp.Panels {
		if p.Project != want[i] {
			t.Fatalf("panel %d = %q, want %q (order is most autonomous time first)", i, p.Project, want[i])
		}
	}
}

// TestAutonomy_PanelCapStillBitesAndSaysSo pins the other half: the cap is a
// SAFETY cap, not a design, and a machine that reaches it is told rather than
// silently truncated.
//
// It runs the builder directly rather than through the handler because the
// fixture needs autonomyPanelCount+5 projects, and building that many HTTP
// requests would say nothing extra about the rule under test.
//
// The committed mutation is unboundedPanels below — the "just send them all"
// build, which is what makes a pathological install's payload unbounded.
func TestAutonomy_PanelCapStillBitesAndSaysSo(t *testing.T) {
	now := time.Now().Unix()
	const extra = 5
	spans := []outbound.AutonomySpan{}
	// Distinct totals so the rank is total order, and the cap's tail is exactly
	// the `extra` smallest.
	for i := 1; i <= autonomyPanelCount+extra; i++ {
		spans = append(spans, spanEndingAt(now-int64(i), int64(i)*10, fmt.Sprintf("p%04d", i), "ready"))
	}
	res := &outbound.AutonomySpanResult{Spans: spans, TotalRecorded: len(spans), EarliestStart: now - 100000}
	resp := buildAutonomyProjectsResponse("30d", 86400, now-30*86400, now, res)

	if len(resp.Panels) != autonomyPanelCount {
		t.Fatalf("sent %d panels over %d projects, want the cap of %d",
			len(resp.Panels), autonomyPanelCount+extra, autonomyPanelCount)
	}
	if resp.MoreProjects != extra {
		t.Errorf("more_projects = %d, want %d — a truncation nothing mentions is indistinguishable "+
			"from a project that never ran", resp.MoreProjects, extra)
	}
	if resp.Summary.Projects != autonomyPanelCount+extra {
		t.Errorf("summary projects = %d, want %d (the summary counts every project, including the "+
			"ones past the cap)", resp.Summary.Projects, autonomyPanelCount+extra)
	}

	// The MUTATION: no cap at all.
	unboundedPanels := len(spans)
	if unboundedPanels == len(resp.Panels) {
		t.Fatal("the uncapped mutation sends the same number of panels production does — this fixture " +
			"cannot show that the cap bites")
	}
}

// TestAutonomy_RanksByTotalTimeNotLongestRun is the judgement call made
// executable: one lucky overnight run must not promote a project nobody has
// touched, over the project that worked every day.
//
// The committed mutation is byLongestRun below — the ranking this test exists to
// refuse. `steady` has fifty 1-hour runs (50h total, 1h longest); `lucky` has one
// 11-hour run (11h total, 11h longest). Under production `steady` outranks
// `lucky`; under the mutation the order flips.
func TestAutonomy_RanksByTotalTimeNotLongestRun(t *testing.T) {
	now := time.Now().Unix()
	spans := []outbound.AutonomySpan{
		spanEndingAt(now-3600, 11*3600, "lucky", "ready"),
	}
	for i := 0; i < 50; i++ {
		spans = append(spans, spanEndingAt(now-int64(i)*4000-100, 3600, "steady", "ready"))
	}
	resp := decodeAutonomy(t, &fakeAutonomyStore{spans: spans, total: len(spans), earliest: now - 300000},
		"chart=autonomy_projects&window=30d")
	if len(resp.Panels) != 2 {
		t.Fatalf("drew %d panels, want 2", len(resp.Panels))
	}
	if resp.Panels[0].Project != "steady" {
		t.Fatalf("panels ranked %q first; want steady (50h of autonomous time against lucky's 11h)",
			resp.Panels[0].Project)
	}

	// The MUTATION: rank by longest run instead of total time.
	byLongestRun := func(a, b historyAutonomyPanel) bool { return a.Longest > b.Longest }
	if !byLongestRun(panelFor(t, resp, "lucky"), panelFor(t, resp, "steady")) {
		t.Fatalf("the longest-run mutation ranks these two the same way production does — this fixture " +
			"cannot show that the ranking is by TOTAL autonomous time")
	}
	// …and the wire carries the rank key, so the order can be checked rather
	// than trusted.
	if resp.Panels[0].TotalSeconds <= resp.Panels[1].TotalSeconds {
		t.Errorf("total_seconds is not descending across the panels (%v then %v)",
			resp.Panels[0].TotalSeconds, resp.Panels[1].TotalSeconds)
	}
}

// --- A still-running run counts towards the longest, and is marked ----------

func TestAutonomy_RunningRunCountsTowardsLongestAndIsMarked(t *testing.T) {
	now := time.Now().Unix()
	spans := []outbound.AutonomySpan{
		spanEndingAt(now-100, 600, "irrlicht", "ready"),
		{Start: now - 3*3600, End: now, Project: "irrlicht", Session: "live",
			Kind: session.AutonomyKindTopLevel, Running: true},
	}
	resp := decodeAutonomy(t, &fakeAutonomyStore{spans: spans, total: 2, earliest: now - 3*3600},
		"chart=autonomy_projects&window=30d")
	panel := panelFor(t, resp, "irrlicht")
	if panel.Longest != float64(3*3600) {
		t.Fatalf("longest = %v, want %d — a run that has already lasted three hours IS the longest run, "+
			"unlike its old effect on a percentile", panel.Longest, 3*3600)
	}
	if !panel.LongestRunning {
		t.Error("the longest run has not ended and must be marked: its length is a floor, not a measurement")
	}
	if resp.Summary.Longest != float64(3*3600) || !resp.Summary.LongestRunning {
		t.Errorf("summary longest = %v (running=%v), want %d running",
			resp.Summary.Longest, resp.Summary.LongestRunning, 3*3600)
	}
	if resp.Summary.LongestProject != "irrlicht" {
		t.Errorf("summary longest_project = %q, want irrlicht", resp.Summary.LongestProject)
	}
	var marked bool
	for _, b := range panel.Buckets {
		if b.Running {
			marked = true
		}
	}
	if !marked {
		t.Error("no bucket carries the running mark, so the panel's line would draw a floor as final")
	}
}

// --- "No data" must never read as "you did nothing" -------------------------

func TestAutonomy_EmptyWindowStillReportsWhenCollectionStarted(t *testing.T) {
	now := time.Now().Unix()
	// Nothing in the window, but the log has been collecting since yesterday.
	store := &fakeAutonomyStore{earliest: now - 86400, total: 7}
	resp := decodeAutonomy(t, store, "chart=autonomy_projects")
	if resp.EarliestSpan != now-86400 {
		t.Errorf("earliest_span = %d, want %d — without it an empty view cannot distinguish "+
			"\"nothing ran\" from \"this feature had not shipped yet\"", resp.EarliestSpan, now-86400)
	}
	if resp.TotalRecorded != 7 {
		t.Errorf("total_recorded = %d, want 7", resp.TotalRecorded)
	}
	if len(resp.Panels) != 0 {
		t.Errorf("drew %d panels over an empty window, want 0", len(resp.Panels))
	}
}

func TestAutonomy_NilStoreServesEmptyButValid(t *testing.T) {
	rec := getAutonomy(t, nil, "chart="+chartAutonomyProjects)
	if rec.Code != http.StatusOK {
		t.Fatalf("no store → %d, want 200", rec.Code)
	}
}

func TestAutonomy_StoreErrorIs500(t *testing.T) {
	store := &fakeAutonomyStore{err: errAutonomyProbe}
	rec := getAutonomy(t, store, "chart=autonomy_projects&window=30d")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("store error → %d, want 500", rec.Code)
	}
}

// TestAutonomy_ReadIsUnlimited pins the read the concurrency figure depends on.
// The run strip capped its read because it drew one column per run; a peak
// computed from a clipped span list is simply wrong, silently, and always
// downwards.
func TestAutonomy_ReadIsUnlimited(t *testing.T) {
	store := &fakeAutonomyStore{}
	_ = decodeAutonomy(t, store, "chart=autonomy_projects&window=1y")
	if store.lastQuery.Limit != 0 {
		t.Errorf("store query Limit = %d, want 0 (unlimited) — a clipped read would understate every "+
			"concurrency peak with nothing on screen saying so", store.lastQuery.Limit)
	}
	if store.lastQuery.End-store.lastQuery.Start != 52*7*86400 {
		t.Errorf("window=1y asked the store for %ds, want %d",
			store.lastQuery.End-store.lastQuery.Start, 52*7*86400)
	}
}

// --- A stated figure must be reachable on the axis that states it -----------

// TestAutonomy_PanelLongestIsAlwaysOnItsOwnAxis pins the property whose absence
// was the aggregate chart's Y-domain defect (#1905): a figure the header states
// must be a value the plot beside it can actually reach.
//
// The panel's line domain is built from its BUCKET longests, and its header
// states the WINDOW longest. Those are two different reductions of the same
// spans, and they agree only because every returned span is bucketed: the store
// selects on `End < q.Start || End >= q.End` (foldSpanRow), so every span it
// returns has an end inside the window and therefore an index inside [0, n).
// A span counted towards the header but dropped from every bucket would put the
// header's figure above the top of its own plot.
//
// A LOCK: it passes by construction against the code as written, and it is
// here so a later change to either reduction — a filter on one side, a clamp on
// the other — cannot silently break the pair. The mutation below is what such a
// change looks like.
func TestAutonomy_PanelLongestIsAlwaysOnItsOwnAxis(t *testing.T) {
	now := time.Now().Unix()
	spans := []outbound.AutonomySpan{
		spanEndingAt(now-100, 600, "irrlicht", "ready"),
		spanEndingAt(now-20*86400, 41_940, "irrlicht", "ready"), // the long one, 20 days back
		spanEndingAt(now-5*86400, 900, "irrlicht", "ready"),
	}
	resp := decodeAutonomy(t, &fakeAutonomyStore{spans: spans, total: len(spans), earliest: now - 30*86400},
		"chart=autonomy_projects&window=30d")
	panel := panelFor(t, resp, "irrlicht")

	var acrossBuckets float64
	for _, b := range panel.Buckets {
		if b.Longest > acrossBuckets {
			acrossBuckets = b.Longest
		}
	}
	if acrossBuckets <= 0 {
		t.Fatal("no bucket carries a longest run, so this check cannot observe the pair it guards")
	}
	if panel.Longest != acrossBuckets {
		t.Errorf("header states longest %v but the highest bucket is %v — the header's figure is not "+
			"reachable on the plot beside it, which is the aggregate chart's Y-domain defect in the "+
			"other direction", panel.Longest, acrossBuckets)
	}

	// The MUTATION: a bucketing pass that drops what falls outside the window
	// instead of clamping it, while the header keeps counting it. It has to
	// differ from production here, or this fixture proves nothing.
	dropped := 0.0
	for _, s := range spans {
		if s.End >= now-10*86400 && float64(s.Duration()) > dropped {
			dropped = float64(s.Duration())
		}
	}
	if dropped == panel.Longest {
		t.Fatal("the dropping mutation agrees with production on this fixture — it cannot show that " +
			"every counted run is also bucketed")
	}
}
