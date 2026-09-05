package filesystem

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func span(start, end int64, project, reason string) outbound.AutonomySpan {
	return outbound.AutonomySpan{
		Start: start, End: end, Project: project,
		Session: "sess", Adapter: "claude-code", Model: "claude-opus-4", Reason: reason,
	}
}

func TestAutonomySpanTracker_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	now := time.Now().Unix()

	spans := []outbound.AutonomySpan{
		span(now-3600, now-3000, "irrlicht", session.StateReady),
		span(now-2000, now-1900, "articles", session.StateWaiting),
		span(now-500, now-100, "irrlicht", session.StateError),
	}
	for _, s := range spans {
		if err := tr.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}

	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: now - 7200, End: now})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 3 {
		t.Fatalf("got %d spans, want 3", len(res.Spans))
	}
	if res.EarliestStart != now-3600 {
		t.Errorf("earliest = %d, want %d", res.EarliestStart, now-3600)
	}
	if res.TotalRecorded != 3 {
		t.Errorf("total = %d, want 3", res.TotalRecorded)
	}
	// Ordered by start ascending.
	for i := 1; i < len(res.Spans); i++ {
		if res.Spans[i-1].Start > res.Spans[i].Start {
			t.Fatalf("spans are not ordered by start: %v", res.Spans)
		}
	}
	// Every field survives the round trip.
	got := res.Spans[0]
	if got.Project != "irrlicht" || got.Adapter != "claude-code" ||
		got.Model != "claude-opus-4" || got.Reason != session.StateReady {
		t.Errorf("round trip lost provenance: %+v", got)
	}
	// One file per project, like the cost log.
	for _, p := range []string{"irrlicht", "articles"} {
		if _, err := os.Stat(filepath.Join(dir, p+autonomyFileExt)); err != nil {
			t.Errorf("no span file for project %q: %v", p, err)
		}
	}
}

// TestAutonomySpanTracker_WindowFiltersOnEnd pins which timestamp decides
// membership. A span is only recorded once it has ended, so its END is the
// only timestamp the window can filter on without dropping long runs that
// started before the window opened.
func TestAutonomySpanTracker_WindowFiltersOnEnd(t *testing.T) {
	tr := NewAutonomySpanTrackerWithDir(t.TempDir())
	now := time.Now().Unix()
	// A four-hour run that STARTED six hours ago and ended two hours ago.
	if err := tr.RecordSpan(span(now-6*3600, now-2*3600, "irrlicht", session.StateReady)); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: now - 3*3600, End: now})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 1 {
		t.Fatalf("got %d spans, want 1 — a run that ended inside the window belongs to it even "+
			"though it started before", len(res.Spans))
	}
	// And one that ended before the window is out.
	res, err = tr.SpansInWindow(outbound.AutonomySpanQuery{Start: now - 3600, End: now})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 0 {
		t.Fatalf("got %d spans, want 0", len(res.Spans))
	}
	// …but it still counts towards the log-wide facts, which is what makes
	// "collecting since <date>" honest.
	if res.EarliestStart != now-6*3600 || res.TotalRecorded != 1 {
		t.Errorf("earliest/total = %d/%d, want %d/1 — log-wide facts are not window-scoped",
			res.EarliestStart, res.TotalRecorded, now-6*3600)
	}
}

func TestAutonomySpanTracker_LimitReportsTruncation(t *testing.T) {
	tr := NewAutonomySpanTrackerWithDir(t.TempDir())
	now := time.Now().Unix()
	for i := 0; i < 10; i++ {
		if err := tr.RecordSpan(span(now-int64(i)*100-50, now-int64(i)*100, "irrlicht", session.StateReady)); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: now - 10000, End: now + 1, Limit: 4})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 4 || !res.Truncated {
		t.Errorf("got %d spans truncated=%v, want 4 and truncated=true", len(res.Spans), res.Truncated)
	}
	res, err = tr.SpansInWindow(outbound.AutonomySpanQuery{Start: now - 10000, End: now + 1, Limit: 100})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if res.Truncated {
		t.Error("truncated=true with a limit nothing hit")
	}
}

func TestAutonomySpanTracker_SkipsUnusableSpans(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	now := time.Now().Unix()
	cases := map[string]outbound.AutonomySpan{
		"no project":    span(now-100, now, "", session.StateReady),
		"zero duration": span(now, now, "irrlicht", session.StateReady),
		"end before start": {
			Start: now, End: now - 100, Project: "irrlicht", Reason: session.StateReady,
		},
	}
	for name, s := range cases {
		if err := tr.RecordSpan(s); err != nil {
			t.Errorf("%s: RecordSpan returned an error; it should no-op quietly: %v", name, err)
		}
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: now + 1000})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 0 {
		t.Errorf("got %d spans, want 0 — none of these is a run: %v", len(res.Spans), res.Spans)
	}
}

func TestAutonomySpanTracker_PruneDropsOldRowsAndKeepsRecent(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	now := time.Now().Unix()
	old := now - 500*86400
	if err := tr.RecordSpan(span(old-600, old, "irrlicht", session.StateReady)); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	if err := tr.RecordSpan(span(now-600, now-60, "irrlicht", session.StateWaiting)); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	if err := tr.Prune(400); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: now + 1})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if res.TotalRecorded != 1 {
		t.Fatalf("total after prune = %d, want 1", res.TotalRecorded)
	}
	if res.Spans[0].End != now-60 {
		t.Errorf("the surviving span ends at %d, want %d — prune kept the wrong row",
			res.Spans[0].End, now-60)
	}
}

// TestAutonomySpanTracker_PruneRemovesAnEmptiedFile pins pruneJSONLFile's
// contract, shared with the cost log: a file nothing survives in is removed,
// not left behind as an empty one for every later scan to open.
func TestAutonomySpanTracker_PruneRemovesAnEmptiedFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	old := time.Now().Unix() - 500*86400
	if err := tr.RecordSpan(span(old-600, old, "irrlicht", session.StateReady)); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "irrlicht"+autonomyFileExt)); err != nil {
		t.Fatalf("the span file was never written, so this test would pass vacuously: %v", err)
	}
	if err := tr.Prune(400); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "irrlicht"+autonomyFileExt)); !os.IsNotExist(err) {
		t.Errorf("an emptied span file was left on disk (stat err = %v)", err)
	}
}

func TestAutonomySpanTracker_PruneNoOpsOnZeroOrMissingDir(t *testing.T) {
	tr := NewAutonomySpanTrackerWithDir(filepath.Join(t.TempDir(), "never-created"))
	if err := tr.Prune(400); err != nil {
		t.Errorf("Prune on a missing dir: %v", err)
	}
	if err := tr.Prune(0); err != nil {
		t.Errorf("Prune(0): %v", err)
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: 1})
	if err != nil {
		t.Fatalf("SpansInWindow on a missing dir: %v", err)
	}
	if len(res.Spans) != 0 {
		t.Errorf("got %d spans from a missing dir", len(res.Spans))
	}
}

// TestAutonomySpanTracker_MalformedLinesAreSkipped mirrors the cost log's
// policy: one truncated tail line (a crash mid-append) must not blind the
// reader to everything before it.
func TestAutonomySpanTracker_MalformedLinesAreSkipped(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	now := time.Now().Unix()
	if err := tr.RecordSpan(span(now-600, now-60, "irrlicht", session.StateReady)); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "irrlicht"+autonomyFileExt), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("{\"start\":123,\"end\":\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: now + 1})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 1 {
		t.Errorf("got %d spans, want 1 — the good row before the torn one must still be read",
			len(res.Spans))
	}
}

// TestAutonomySpanTracker_ProjectFallsBackToFilename mirrors the cost log:
// a row written without a project name is attributed to the file it lives in.
func TestAutonomySpanTracker_ProjectFallsBackToFilename(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	now := time.Now().Unix()
	line := `{"start":` + strconv.FormatInt(now-600, 10) + `,"end":` + strconv.FormatInt(now-60, 10) + `,"reason":"ready"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "legacy"+autonomyFileExt), []byte(line), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tr := NewAutonomySpanTrackerWithDir(dir)
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: now + 1})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 1 || res.Spans[0].Project != "legacy" {
		t.Errorf("got %+v, want one span attributed to the filename \"legacy\"", res.Spans)
	}
}
