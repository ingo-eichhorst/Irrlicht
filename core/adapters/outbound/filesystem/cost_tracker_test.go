package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

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

	m, err := tr.ProjectCostsInWindow(3600)
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

	m, err := tr.ProjectCostsInWindow(3600) // 1h window
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

	m, err := tr.ProjectCostsInWindow(3600)
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

	m, err := tr.ProjectCostsInWindow(3600)
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

	m, err := tr.ProjectCostsInWindow(3600)
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
	day, err := tr.ProjectCostsInWindow(24 * 3600)
	if err != nil {
		t.Fatal(err)
	}
	week, err := tr.ProjectCostsInWindow(7 * 24 * 3600)
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
