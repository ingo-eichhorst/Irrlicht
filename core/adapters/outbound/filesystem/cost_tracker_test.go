package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// costSeriesByProject runs CostSeries with the Phase-1 default axis (cost ×
// project), so the bucketing/baseline tests keep their original call shape.
func costSeriesByProject(tr *CostTracker, start, end, bucket int64) (*outbound.CostSeriesResult, error) {
	return tr.CostSeries(outbound.SeriesQuery{Start: start, End: end, BucketSeconds: bucket, Group: "project"})
}

func newTestTracker(t *testing.T) *CostTracker {
	t.Helper()
	dir := t.TempDir()
	return NewCostTrackerWithDir(filepath.Join(dir, "cost"))
}

func readRows(t *testing.T, path string) []snapshotRow {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var rows []snapshotRow
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r snapshotRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		rows = append(rows, r)
	}
	return rows
}

func writeRow(t *testing.T, tr *CostTracker, project string, r snapshotRow) {
	t.Helper()
	if err := os.MkdirAll(tr.Dir(), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, _ := json.Marshal(r)
	data = append(data, '\n')
	path := tr.filePath(project)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestRecordSnapshot_AppendsOnChange(t *testing.T) {
	tr := newTestTracker(t)
	state := &session.SessionState{
		SessionID:   "s1",
		ProjectName: "proj-a",
		Metrics: &session.SessionMetrics{
			EstimatedCostUSD: 0.10,
			CumInputTokens:   100,
		},
	}
	if err := tr.RecordSnapshot(state); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same values → no new row.
	if err := tr.RecordSnapshot(state); err != nil {
		t.Fatalf("second: %v", err)
	}
	rows := readRows(t, tr.filePath("proj-a"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Cost != 0.10 || rows[0].Session != "s1" {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
}

func TestRecordSnapshot_ThrottlesByInterval(t *testing.T) {
	tr := newTestTracker(t)
	state := &session.SessionState{
		SessionID:   "s1",
		ProjectName: "proj-a",
		Metrics:     &session.SessionMetrics{EstimatedCostUSD: 0.10},
	}
	if err := tr.RecordSnapshot(state); err != nil {
		t.Fatal(err)
	}
	state.Metrics.EstimatedCostUSD = 0.20
	// Changed but within 60s of the previous write → throttled.
	if err := tr.RecordSnapshot(state); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, tr.filePath("proj-a"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row (throttled), got %d", len(rows))
	}

	// Forge an older lastWrite so the throttle permits the next append.
	tr.mu.Lock()
	lw := tr.lastWrite["s1"]
	lw.TS -= int64(costWriteInterval/time.Second) + 1
	tr.lastWrite["s1"] = lw
	tr.mu.Unlock()

	state.Metrics.EstimatedCostUSD = 0.30
	if err := tr.RecordSnapshot(state); err != nil {
		t.Fatal(err)
	}
	rows = readRows(t, tr.filePath("proj-a"))
	if len(rows) != 2 {
		t.Fatalf("want 2 rows after throttle clears, got %d", len(rows))
	}
}

func TestProjectCostsInWindow_SingleSessionInsideWindow(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 300, Session: "s1", Cost: 0.10})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 120, Session: "s1", Cost: 0.25})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 60, Session: "s1", Cost: 0.40})

	m, err := tr.projectCostsInWindow(3600)
	if err != nil {
		t.Fatal(err)
	}
	// No pre-window baseline → max − min in-window = 0.40 − 0.10 = 0.30.
	got := m["proj-a"]
	if abs(got-0.30) > 1e-9 {
		t.Fatalf("want 0.30, got %v", got)
	}
}

func TestProjectCostsInWindow_StraddleUsesPreWindowBaseline(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	// Pre-window row → the baseline for the session inside the window.
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 7200, Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 1800, Session: "s1", Cost: 1.25})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 600, Session: "s1", Cost: 1.60})

	m, err := tr.projectCostsInWindow(3600) // 1h window
	if err != nil {
		t.Fatal(err)
	}
	// Baseline from pre-window (1.00) → delta = 1.60 − 1.00 = 0.60.
	got := m["proj-a"]
	if abs(got-0.60) > 1e-9 {
		t.Fatalf("want 0.60, got %v", got)
	}
}

func TestProjectCostsInWindow_SessionOutsideWindowIgnored(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 7200, Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 5000, Session: "s1", Cost: 2.00})

	m, err := tr.projectCostsInWindow(3600)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["proj-a"]; ok {
		t.Fatalf("want no entry, got %v", m)
	}
}

func TestProjectCostsInWindow_MultipleSessionsSum(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 300, Session: "s1", Cost: 0.10})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 100, Session: "s1", Cost: 0.40})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 400, Session: "s2", Cost: 5.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 50, Session: "s2", Cost: 5.25})

	m, err := tr.projectCostsInWindow(3600)
	if err != nil {
		t.Fatal(err)
	}
	// s1: 0.40 − 0.10 = 0.30; s2: 5.25 − 5.00 = 0.25; total 0.55.
	if abs(m["proj-a"]-0.55) > 1e-9 {
		t.Fatalf("want 0.55, got %v", m["proj-a"])
	}
}

func TestProjectCostsInWindow_NegativeDeltaClamped(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	// Simulates a session that rotated: cost jumped down. We must not
	// contribute negative dollars to the total.
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 7200, Session: "s1", Cost: 10.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 100, Session: "s1", Cost: 1.00})

	m, err := tr.projectCostsInWindow(3600)
	if err != nil {
		t.Fatal(err)
	}
	if got := m["proj-a"]; got != 0 {
		t.Fatalf("want 0 (clamped), got %v", got)
	}
}

func TestPrune_RemovesOldRowsAndDeletesEmptyFiles(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	// Older than 10 days.
	writeRow(t, tr, "proj-old", snapshotRow{TS: now - 30*24*3600, Session: "s1", Cost: 0.10})
	writeRow(t, tr, "proj-old", snapshotRow{TS: now - 20*24*3600, Session: "s1", Cost: 0.20})

	// Mix of old + new.
	writeRow(t, tr, "proj-mix", snapshotRow{TS: now - 30*24*3600, Session: "s1", Cost: 0.10})
	writeRow(t, tr, "proj-mix", snapshotRow{TS: now - 1*24*3600, Session: "s1", Cost: 0.30})

	if err := tr.Prune(10); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(tr.filePath("proj-old")); !os.IsNotExist(err) {
		t.Fatalf("want proj-old file deleted, err=%v", err)
	}

	rows := readRows(t, tr.filePath("proj-mix"))
	if len(rows) != 1 {
		t.Fatalf("want 1 row after prune, got %d", len(rows))
	}
	if rows[0].Cost != 0.30 {
		t.Fatalf("want newest row kept, got %+v", rows[0])
	}
}

func TestPrune_DropsStaleLastWriteEntries(t *testing.T) {
	tr := newTestTracker(t)
	// Populate lastWrite for a session whose only snapshot row is older
	// than the retention cutoff. After Prune the file is deleted and
	// lastWrite must no longer reference the session.
	_ = tr.RecordSnapshot(&session.SessionState{
		SessionID:   "s-old",
		ProjectName: "proj-old",
		Metrics:     &session.SessionMetrics{EstimatedCostUSD: 0.10},
	})
	// Forge an old timestamp on the persisted row (RecordSnapshot writes
	// with time.Now). Overwrite with a hand-placed row.
	os.Remove(tr.filePath("proj-old"))
	writeRow(t, tr, "proj-old", snapshotRow{TS: time.Now().Unix() - 30*24*3600, Session: "s-old", Cost: 0.10})

	// Seed a second, current session so lastWrite isn't empty.
	_ = tr.RecordSnapshot(&session.SessionState{
		SessionID:   "s-new",
		ProjectName: "proj-new",
		Metrics:     &session.SessionMetrics{EstimatedCostUSD: 0.50},
	})

	if err := tr.Prune(10); err != nil {
		t.Fatal(err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if _, ok := tr.lastWrite["s-old"]; ok {
		t.Fatalf("want s-old purged from lastWrite, got %+v", tr.lastWrite)
	}
	if _, ok := tr.lastWrite["s-new"]; !ok {
		t.Fatalf("want s-new retained in lastWrite, got %+v", tr.lastWrite)
	}
}

func TestProjectCostsInWindows_SinglePassMatchesSingleWindow(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 12*3600, Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 1*3600, Session: "s1", Cost: 1.50})
	// s2 has a row before the week cutoff → baselines diverge between day
	// (baseline at 72h pre-day, cost 4.00) and week (baseline at 200h
	// pre-week, cost 3.00), so week should exceed day.
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 200*3600, Session: "s2", Cost: 3.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 3*24*3600, Session: "s2", Cost: 4.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 2*3600, Session: "s2", Cost: 4.75})

	windows := map[string]int64{
		"day":  24 * 3600,
		"week": 7 * 24 * 3600,
	}
	multi, err := tr.ProjectCostsInWindows(windows)
	if err != nil {
		t.Fatal(err)
	}
	day, err := tr.projectCostsInWindow(24 * 3600)
	if err != nil {
		t.Fatal(err)
	}
	week, err := tr.projectCostsInWindow(7 * 24 * 3600)
	if err != nil {
		t.Fatal(err)
	}

	if abs(multi["day"]["proj-a"]-day["proj-a"]) > 1e-9 {
		t.Fatalf("day mismatch: multi=%v single=%v", multi["day"]["proj-a"], day["proj-a"])
	}
	if abs(multi["week"]["proj-a"]-week["proj-a"]) > 1e-9 {
		t.Fatalf("week mismatch: multi=%v single=%v", multi["week"]["proj-a"], week["proj-a"])
	}
	if multi["week"]["proj-a"] <= multi["day"]["proj-a"] {
		t.Fatalf("week should be >= day, got day=%v week=%v", multi["day"]["proj-a"], multi["week"]["proj-a"])
	}
}

func TestRecordSnapshot_SkipsWithoutProjectOrMetrics(t *testing.T) {
	tr := newTestTracker(t)
	if err := tr.RecordSnapshot(&session.SessionState{SessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	if err := tr.RecordSnapshot(&session.SessionState{
		SessionID: "s1",
		Metrics:   &session.SessionMetrics{EstimatedCostUSD: 1.0},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tr.Dir()); !os.IsNotExist(err) {
		t.Fatalf("expected no cost dir, err=%v", err)
	}
}

func TestRecordBaseline_WritesOncePerSession(t *testing.T) {
	tr := newTestTracker(t)
	state := &session.SessionState{
		SessionID:   "s1",
		ProjectName: "proj-a",
		FirstSeen:   1_700_000_000,
		Metrics:     &session.SessionMetrics{EstimatedCostUSD: 0.50},
	}
	if err := tr.RecordBaseline(state); err != nil {
		t.Fatal(err)
	}
	// Second call must no-op because lastWrite is already populated.
	state.Metrics.EstimatedCostUSD = 0.80
	if err := tr.RecordBaseline(state); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, tr.filePath("proj-a"))
	if len(rows) != 1 {
		t.Fatalf("want 1 baseline row, got %d", len(rows))
	}
	if rows[0].TS != 1_700_000_000 {
		t.Fatalf("want baseline ts=FirstSeen, got %d", rows[0].TS)
	}
}

func TestProjectKey_SanitisesUnsafeChars(t *testing.T) {
	if k := projectKey("foo/bar"); k != "foo_bar" {
		t.Fatalf("want foo_bar, got %s", k)
	}
	if k := projectKey(""); k != "" {
		t.Fatalf("want empty, got %s", k)
	}
	if k := projectKey("ok.name-1_2"); k != "ok.name-1_2" {
		t.Fatalf("want ok.name-1_2, got %s", k)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func sumF(s []float64) float64 {
	var sum float64
	for _, v := range s {
		sum += v
	}
	return sum
}

// TestCostSeries_BucketsDeltas covers the core bucketing rules across two
// projects in separate files: deltas land in the bucket of the later row;
// a pre-range row seeds the baseline so the first in-range delta measures
// spend since the last snapshot; and a session whose first row is in-range
// contributes nothing for that row (no pre-range baseline).
func TestCostSeries_BucketsDeltas(t *testing.T) {
	tr := newTestTracker(t)
	const start, bucket int64 = 1000, 100
	const end = start + 4*bucket // 4 buckets: [1000,1100,1200,1300]

	// proj-a / s1: baseline before start, then deltas into buckets 0, 1, 3.
	writeRow(t, tr, "proj-a", snapshotRow{TS: 950, Project: "proj-a", Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Session: "s1", Cost: 1.30})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Session: "s1", Cost: 1.50})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1350, Project: "proj-a", Session: "s1", Cost: 2.00})
	// proj-a / s2: first row is in-range → seeds baseline (no delta), one delta into bucket 2.
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Session: "s2", Cost: 0.50})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1250, Project: "proj-a", Session: "s2", Cost: 0.90})
	// proj-b / s3: separate file, single delta into bucket 0.
	writeRow(t, tr, "proj-b", snapshotRow{TS: 980, Project: "proj-b", Session: "s3", Cost: 5.00})
	writeRow(t, tr, "proj-b", snapshotRow{TS: 1010, Project: "proj-b", Session: "s3", Cost: 5.20})

	res, err := costSeriesByProject(tr, start, end, bucket)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.BucketStarts) != 4 || res.BucketStarts[0] != 1000 || res.BucketStarts[3] != 1300 {
		t.Fatalf("bucket starts: %v", res.BucketStarts)
	}
	wantA := []float64{0.30, 0.20, 0.40, 0.50} // s1 → 0,1,3 ; s2 → 2
	for i, want := range wantA {
		if abs(res.ByKey["proj-a"][i]-want) > 1e-9 {
			t.Errorf("proj-a bucket %d: want %v, got %v", i, want, res.ByKey["proj-a"][i])
		}
	}
	if abs(res.Totals["proj-a"]-1.40) > 1e-9 {
		t.Errorf("proj-a total: want 1.40, got %v", res.Totals["proj-a"])
	}
	wantB := []float64{0.20, 0, 0, 0}
	for i, want := range wantB {
		if abs(res.ByKey["proj-b"][i]-want) > 1e-9 {
			t.Errorf("proj-b bucket %d: want %v, got %v", i, want, res.ByKey["proj-b"][i])
		}
	}
	if abs(res.Totals["proj-b"]-0.20) > 1e-9 {
		t.Errorf("proj-b total: want 0.20, got %v", res.Totals["proj-b"])
	}
}

// TestCostSeries_SumMatchesWindowTotal verifies a series' per-project sum over
// a trailing window equals the total ProjectCostsInWindows computes for the
// same window — they share the baseline/delta model.
func TestCostSeries_SumMatchesWindowTotal(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 12*3600, Project: "proj-a", Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 1*3600, Project: "proj-a", Session: "s1", Cost: 1.50})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 200*3600, Project: "proj-a", Session: "s2", Cost: 3.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 3*24*3600, Project: "proj-a", Session: "s2", Cost: 4.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 2*3600, Project: "proj-a", Session: "s2", Cost: 4.75})

	const day int64 = 24 * 3600
	window, err := tr.ProjectCostsInWindows(map[string]int64{"day": day})
	if err != nil {
		t.Fatal(err)
	}
	res, err := costSeriesByProject(tr, now-day, now, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if abs(sumF(res.ByKey["proj-a"])-window["day"]["proj-a"]) > 1e-9 {
		t.Fatalf("series sum %v != window total %v", sumF(res.ByKey["proj-a"]), window["day"]["proj-a"])
	}
	if abs(res.Totals["proj-a"]-window["day"]["proj-a"]) > 1e-9 {
		t.Fatalf("totals %v != window total %v", res.Totals["proj-a"], window["day"]["proj-a"])
	}
}

// TestCostSeries_CapsBucketCount guards against a wide span + tiny bucket
// forcing an unbounded allocation: the bucket is coarsened so the count stays
// within maxSeriesBuckets while still covering [start, end).
func TestCostSeries_CapsBucketCount(t *testing.T) {
	tr := newTestTracker(t)
	res, err := costSeriesByProject(tr, 0, 2_000_000_000, 1) // naive n would be ~2e9
	if err != nil {
		t.Fatal(err)
	}
	if len(res.BucketStarts) > maxSeriesBuckets {
		t.Fatalf("bucket count %d exceeds cap %d", len(res.BucketStarts), maxSeriesBuckets)
	}
	if res.BucketSeconds <= 1 {
		t.Fatalf("bucket should have been coarsened above 1s, got %d", res.BucketSeconds)
	}
	// Buckets must still span the whole range.
	last := res.BucketStarts[len(res.BucketStarts)-1]
	if last+res.BucketSeconds < 2_000_000_000 {
		t.Fatalf("buckets do not cover the range: last=%d bucket=%d", last, res.BucketSeconds)
	}
}

func TestCostSeries_EmptyAndInvalid(t *testing.T) {
	tr := newTestTracker(t) // dir does not exist yet
	res, err := costSeriesByProject(tr, 1000, 2000, 100)
	if err != nil {
		t.Fatalf("empty dir should not error: %v", err)
	}
	if len(res.ByKey) != 0 || len(res.Totals) != 0 {
		t.Fatalf("want empty maps, got %+v", res)
	}
	if len(res.BucketStarts) != 10 {
		t.Fatalf("want 10 buckets, got %d", len(res.BucketStarts))
	}
	// Degenerate args return an empty result, not an error.
	for _, bad := range [][3]int64{{1000, 2000, 0}, {2000, 1000, 100}} {
		r, err := costSeriesByProject(tr, bad[0], bad[1], bad[2])
		if err != nil {
			t.Fatalf("args %v: unexpected error %v", bad, err)
		}
		if len(r.BucketStarts) != 0 {
			t.Fatalf("args %v: want no buckets, got %d", bad, len(r.BucketStarts))
		}
	}
}

func TestRecordSnapshot_StampsProvider(t *testing.T) {
	tr := newTestTracker(t)
	state := &session.SessionState{
		SessionID:   "s1",
		ProjectName: "proj-a",
		Adapter:     "codex",
		Metrics:     &session.SessionMetrics{EstimatedCostUSD: 0.10},
	}
	if err := tr.RecordSnapshot(state); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, tr.filePath("proj-a"))
	if len(rows) != 1 || rows[0].Provider != "openai" {
		t.Fatalf("want one row with provider openai, got %+v", rows)
	}
}

func TestRecordSnapshot_UsesInjectedProviderResolver(t *testing.T) {
	tr := newTestTracker(t)
	// pi resolves to "" under the default resolver; the injected one
	// attributes it (mirrors the daemon wiring for wrapper agents).
	tr.SetProviderResolver(func(s *session.SessionState) string { return "anthropic" })
	state := &session.SessionState{
		SessionID:   "s1",
		ProjectName: "proj-a",
		Adapter:     "pi",
		Metrics:     &session.SessionMetrics{EstimatedCostUSD: 0.10},
	}
	if err := tr.RecordSnapshot(state); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, tr.filePath("proj-a"))
	if len(rows) != 1 || rows[0].Provider != "anthropic" {
		t.Fatalf("want one row with injected provider anthropic, got %+v", rows)
	}
}

// TestProviderCostsInWindows_BucketsByProvider verifies that a single project
// file mixing providers attributes each session to its own provider (the
// reason the rollup can't be derived from the per-project map), and that
// sessions with no known provider are excluded.
func TestProviderCostsInWindows_BucketsByProvider(t *testing.T) {
	tr := newTestTracker(t)
	now := time.Now().Unix()
	// Each session: a pre-window baseline + an in-window max ⇒ contribution
	// is max−baseline. All three live in one project file.
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 10*3600, Provider: "anthropic", Session: "a1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 1*3600, Provider: "anthropic", Session: "a1", Cost: 1.25})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 10*3600, Provider: "openai", Session: "o1", Cost: 2.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 1*3600, Provider: "openai", Session: "o1", Cost: 2.50})
	// Unknown-provider session (pre-schema row / wrapper agent) — excluded.
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 10*3600, Session: "u1", Cost: 5.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: now - 1*3600, Session: "u1", Cost: 9.00})

	got, err := tr.ProviderCostsInWindows(map[string]int64{"day": 24 * 3600, "week": 7 * 24 * 3600})
	if err != nil {
		t.Fatal(err)
	}
	for _, tf := range []string{"day", "week"} {
		if v := got[tf]["anthropic"]; abs(v-0.25) > 0.01 {
			t.Errorf("%s anthropic: want ≈0.25, got %v", tf, v)
		}
		if v := got[tf]["openai"]; abs(v-0.50) > 0.01 {
			t.Errorf("%s openai: want ≈0.50, got %v", tf, v)
		}
		if v, ok := got[tf][""]; ok {
			t.Errorf("%s: empty-provider bucket must be excluded, got %v", tf, v)
		}
	}
}

// --- Phase 2 (#750): branch/model schema + generalized series ---

// TestRecordSnapshot_StampsBranchAndModel verifies the new v2 columns are
// populated from SessionState at write time.
func TestRecordSnapshot_StampsBranchAndModel(t *testing.T) {
	tr := newTestTracker(t)
	state := &session.SessionState{
		SessionID:   "s1",
		ProjectName: "proj-a",
		GitBranch:   "feat/x",
		Model:       "claude-opus",
		Metrics:     &session.SessionMetrics{EstimatedCostUSD: 0.10},
	}
	if err := tr.RecordSnapshot(state); err != nil {
		t.Fatal(err)
	}
	rows := readRows(t, tr.filePath("proj-a"))
	if len(rows) != 1 || rows[0].Branch != "feat/x" || rows[0].Model != "claude-opus" {
		t.Fatalf("want branch+model stamped, got %+v", rows)
	}
}

// TestSnapshotSchema_PreV2RowsReadUnknown asserts the schema bump is backward
// compatible: a row written without the branch/model columns decodes with both
// empty and buckets under the unknown ("") key on those axes — no migration.
func TestSnapshotSchema_PreV2RowsReadUnknown(t *testing.T) {
	if costSchemaVersion < 2 {
		t.Fatalf("schema version should be >=2 after #750, got %d", costSchemaVersion)
	}
	tr := newTestTracker(t)
	// snapshotRow with no Branch/Model marshals (omitempty) exactly like a
	// pre-v2 row on disk.
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Session: "s1", Cost: 1.30})
	rows := readRows(t, tr.filePath("proj-a"))
	if rows[0].Branch != "" || rows[0].Model != "" {
		t.Fatalf("pre-v2 row should decode with empty branch/model, got %+v", rows[0])
	}
	res, err := tr.CostSeries(outbound.SeriesQuery{Start: 1000, End: 1400, BucketSeconds: 100, Group: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	if abs(res.Totals[""]-0.30) > 1e-9 {
		t.Fatalf("missing-branch delta should bucket under \"\" (unknown), got %+v", res.Totals)
	}
}

// TestCostSeries_IntervalEndAttribution is the core mid-session-change AC: a
// session that switches branch mid-window credits each delta to the branch
// active at the interval's end row, never lumping the whole session onto one.
func TestCostSeries_IntervalEndAttribution(t *testing.T) {
	tr := newTestTracker(t)
	const start, bucket, end int64 = 1000, 100, 1400
	// s1 starts on main, then switches to feat at TS=1250.
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Branch: "main", Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Branch: "main", Session: "s1", Cost: 1.30})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1250, Project: "proj-a", Branch: "feat", Session: "s1", Cost: 1.70})

	res, err := tr.CostSeries(outbound.SeriesQuery{Start: start, End: end, BucketSeconds: bucket, Group: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	if abs(res.Totals["main"]-0.30) > 1e-9 {
		t.Errorf("main: want 0.30 (only the pre-switch delta), got %v", res.Totals["main"])
	}
	if abs(res.Totals["feat"]-0.40) > 1e-9 {
		t.Errorf("feat: want 0.40 (the post-switch delta), got %v", res.Totals["feat"])
	}
	// The post-switch delta lands in feat's bucket 2 (TS 1250), not main's.
	if abs(res.ByKey["feat"][2]-0.40) > 1e-9 {
		t.Errorf("feat bucket 2: want 0.40, got %v", res.ByKey["feat"])
	}
}

// TestCostSeries_TokensMetricSplit verifies the tokens metric buckets total
// tokens and accumulates the aggregate in/out/cache split.
func TestCostSeries_TokensMetricSplit(t *testing.T) {
	tr := newTestTracker(t)
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Session: "s1", CumIn: 100, CumOut: 10})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Session: "s1", CumIn: 200, CumOut: 30, CumRead: 50})

	res, err := tr.CostSeries(outbound.SeriesQuery{Start: 1000, End: 1400, BucketSeconds: 100, Group: "project", Metric: "tokens"})
	if err != nil {
		t.Fatal(err)
	}
	// total tokens delta = (200+30+50) − (100+10) = 170, into bucket 1.
	if abs(res.Totals["proj-a"]-170) > 1e-9 || abs(res.ByKey["proj-a"][1]-170) > 1e-9 {
		t.Errorf("tokens total: want 170 in bucket 1, got %+v", res.ByKey["proj-a"])
	}
	if res.TokenSplit == nil {
		t.Fatal("tokens metric must populate TokenSplit")
	}
	if abs(res.TokenSplit.Input-100) > 1e-9 || abs(res.TokenSplit.Output-20) > 1e-9 || abs(res.TokenSplit.Cache-50) > 1e-9 {
		t.Errorf("split: want in=100 out=20 cache=50, got %+v", res.TokenSplit)
	}
	// Cost metric never populates the split.
	costRes, _ := tr.CostSeries(outbound.SeriesQuery{Start: 1000, End: 1400, BucketSeconds: 100, Group: "project", Metric: "cost"})
	if costRes.TokenSplit != nil {
		t.Errorf("cost metric should leave TokenSplit nil, got %+v", costRes.TokenSplit)
	}
}

// TestCostSeries_ScopeFilter verifies the drilldown primitive: only rows
// matching the scope field/value contribute.
func TestCostSeries_ScopeFilter(t *testing.T) {
	tr := newTestTracker(t)
	// Two sessions in one project on different branches.
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Branch: "main", Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Branch: "main", Session: "s1", Cost: 1.30})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Branch: "feat", Session: "s2", Cost: 2.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Branch: "feat", Session: "s2", Cost: 2.50})

	res, err := tr.CostSeries(outbound.SeriesQuery{
		Start: 1000, End: 1400, BucketSeconds: 100,
		Group: "session", ScopeField: "branch", ScopeValue: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if abs(res.Totals["s1"]-0.30) > 1e-9 {
		t.Errorf("scoped to branch=main: want s1=0.30, got %v", res.Totals["s1"])
	}
	if _, ok := res.Totals["s2"]; ok {
		t.Errorf("scoped to branch=main must exclude s2 (branch=feat), got %+v", res.Totals)
	}
}

// TestCostSeries_GroupByTokenType verifies the tokens metric grouped by
// token_type splits each row's per-kind deltas into the four bands.
func TestCostSeries_GroupByTokenType(t *testing.T) {
	tr := newTestTracker(t)
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Session: "s1", CumIn: 100, CumOut: 10})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Session: "s1", CumIn: 200, CumOut: 30, CumRead: 50, CumCreate: 5})

	res, err := tr.CostSeries(outbound.SeriesQuery{Start: 1000, End: 1400, BucketSeconds: 100, Group: "token_type", Metric: "tokens"})
	if err != nil {
		t.Fatal(err)
	}
	// Deltas land in bucket 1 (TS 1150): in=100 out=20 read=50 create=5.
	want := map[string]float64{"input": 100, "output": 20, "cache_read": 50, "cache_creation": 5}
	for k, v := range want {
		if abs(res.Totals[k]-v) > 1e-9 {
			t.Errorf("band %s total: want %v, got %v", k, v, res.Totals[k])
		}
		if abs(res.ByKey[k][1]-v) > 1e-9 {
			t.Errorf("band %s bucket 1: want %v, got %v", k, v, res.ByKey[k])
		}
	}
	if len(res.Totals) != 4 {
		t.Errorf("token_type grouping should yield exactly 4 bands, got %+v", res.Totals)
	}
}

// TestCostSeries_ProjectFilter verifies the project cross-filter keeps only the
// selected projects' sessions.
func TestCostSeries_ProjectFilter(t *testing.T) {
	tr := newTestTracker(t)
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Session: "s1", Cost: 1.40})
	writeRow(t, tr, "proj-b", snapshotRow{TS: 1050, Project: "proj-b", Session: "s2", Cost: 5.00})
	writeRow(t, tr, "proj-b", snapshotRow{TS: 1150, Project: "proj-b", Session: "s2", Cost: 6.00})

	res, err := tr.CostSeries(outbound.SeriesQuery{
		Start: 1000, End: 1400, BucketSeconds: 100, Group: "session", Projects: []string{"proj-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if abs(res.Totals["s1"]-0.40) > 1e-9 {
		t.Errorf("project=proj-a: want s1=0.40, got %v", res.Totals["s1"])
	}
	if _, ok := res.Totals["s2"]; ok {
		t.Errorf("project=proj-a must exclude proj-b's s2, got %+v", res.Totals)
	}
}

// TestCostSeries_ProviderFilter verifies the provider cross-filter, including
// selecting the empty-provider rows via the "unknown" key.
func TestCostSeries_ProviderFilter(t *testing.T) {
	tr := newTestTracker(t)
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Provider: "anthropic", Session: "s1", Cost: 1.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Provider: "anthropic", Session: "s1", Cost: 1.40})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Provider: "openai", Session: "s2", Cost: 2.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Provider: "openai", Session: "s2", Cost: 2.70})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Session: "s3", Cost: 9.00})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Session: "s3", Cost: 9.50})

	res, err := tr.CostSeries(outbound.SeriesQuery{
		Start: 1000, End: 1400, BucketSeconds: 100, Group: "session", Providers: []string{"anthropic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if abs(res.Totals["s1"]-0.40) > 1e-9 || len(res.Totals) != 1 {
		t.Errorf("provider=anthropic: want only s1=0.40, got %+v", res.Totals)
	}
	// "unknown" selects the empty-provider session.
	unk, err := tr.CostSeries(outbound.SeriesQuery{
		Start: 1000, End: 1400, BucketSeconds: 100, Group: "session", Providers: []string{"unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if abs(unk.Totals["s3"]-0.50) > 1e-9 || len(unk.Totals) != 1 {
		t.Errorf("provider=unknown: want only s3=0.50, got %+v", unk.Totals)
	}
}

// TestCostSeries_TokenTypeFilter verifies the token_type cross-filter restricts
// which counters the tokens metric (and its split) sums.
func TestCostSeries_TokenTypeFilter(t *testing.T) {
	tr := newTestTracker(t)
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1050, Project: "proj-a", Session: "s1", CumIn: 100, CumOut: 10})
	writeRow(t, tr, "proj-a", snapshotRow{TS: 1150, Project: "proj-a", Session: "s1", CumIn: 200, CumOut: 30, CumRead: 50})

	res, err := tr.CostSeries(outbound.SeriesQuery{
		Start: 1000, End: 1400, BucketSeconds: 100, Group: "project", Metric: "tokens",
		TokenTypes: []string{"input"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only input deltas count: 200−100 = 100.
	if abs(res.Totals["proj-a"]-100) > 1e-9 {
		t.Errorf("token_type=input total: want 100, got %v", res.Totals["proj-a"])
	}
	if res.TokenSplit == nil || abs(res.TokenSplit.Input-100) > 1e-9 || res.TokenSplit.Output != 0 || res.TokenSplit.Cache != 0 {
		t.Errorf("token_type=input split: want in=100 out=0 cache=0, got %+v", res.TokenSplit)
	}
}
