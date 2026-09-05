package filesystem

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Mutation fixtures for the open-run journal (#1905 recording). Each names the
// wrong behaviour it catches; none had a "before the fix" state to run red in.

// A run in progress is MARKED, so nothing downstream can mistake it for a
// finished one.
//
// MUTATION CAUGHT: serving the journal without Running would put "the daemon
// checked on it four seconds ago" into the percentiles as "the run finished
// four seconds ago" — a floor read as a measurement, and always in the
// direction that shortens the longest runs.
func TestOpenRun_IsMarkedRunningAndCarriesNoEndReason(t *testing.T) {
	dir := t.TempDir()
	writeOpenJournal(t, dir, `{"s":{"start":1000,"last_seen":1400,"project":"p","session":"s","kind":"top"}}`)

	res, err := NewAutonomySpanTrackerWithDir(dir).SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: 1500})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(res.Spans))
	}
	got := res.Spans[0]
	if !got.Running {
		t.Fatal("an in-progress run is not marked Running — every consumer would treat its " +
			"seconds-so-far as a finished duration")
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty — a run that has not ended has no end reason, and any "+
			"value here is a claim about how it finished", got.Reason)
	}
	if res.Measurement.Running != 1 {
		t.Errorf("Measurement.Running = %d, want 1 — an uncounted running run cannot be stated on screen",
			res.Measurement.Running)
	}
}

// The journal is served by OVERLAP, and its end is clamped to the window. A run
// that has not ended cannot be asked where it ended, so the closed log's
// "ended inside the window" rule cannot decide it.
//
// MUTATION CAUGHT: reusing the closed rule (`End >= Start && End < q.End`)
// silently drops every open run from a trailing window that ends at "now",
// because a live journal's last-seen instant is at or beyond it.
func TestOpenRun_IsSelectedByOverlapNotByItsEnd(t *testing.T) {
	dir := t.TempDir()
	writeOpenJournal(t, dir, `{`+
		`"live":{"start":900,"last_seen":2000,"project":"p","session":"live","kind":"top"},`+
		`"old":{"start":10,"last_seen":50,"project":"p","session":"old","kind":"top"},`+
		`"future":{"start":9000,"last_seen":9000,"project":"p","session":"future","kind":"top"}`+
		`}`)

	// A trailing window whose end is BEFORE the live run's last-seen instant —
	// exactly what a request served a moment after a heartbeat looks like.
	res, err := NewAutonomySpanTrackerWithDir(dir).SpansInWindow(outbound.AutonomySpanQuery{Start: 1000, End: 1900})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 1 {
		t.Fatalf("got %d spans, want only the overlapping one: %+v", len(res.Spans), res.Spans)
	}
	got := res.Spans[0]
	if got.Session != "live" {
		t.Fatalf("returned %q, want %q — `old` ended before the window and `future` had not begun",
			got.Session, "live")
	}
	if got.End != 1900 {
		t.Errorf("End = %d, want the window's end 1900 — an open run's end-so-far is clamped to the "+
			"window, never reported beyond it", got.End)
	}
	if got.Start != 900 {
		t.Errorf("Start = %d, want 900 — the run began before the window and keeps its real start", got.Start)
	}
}

// Closing a run REPLACES its journal entry rather than adding to it. Otherwise
// the same stretch of time is on record twice: once as a finished run and once
// as one still going — and the next daemon adopts the stale entry as a run.
//
// MUTATION CAUGHT: dropping the clear from RecordSpan leaves a permanent
// phantom whose duration grows without bound.
func TestRecordSpan_ClearsTheSessionsOpenRun(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)

	open := outbound.AutonomySpan{Start: 1000, End: 1400, Project: "p", Session: "s",
		Kind: session.AutonomyKindTopLevel, Running: true}
	if err := tr.RecordOpenSpan(open); err != nil {
		t.Fatalf("RecordOpenSpan: %v", err)
	}
	closed := open
	closed.End = 1500
	closed.Running = false
	closed.Reason = session.StateReady
	if err := tr.RecordSpan(closed); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}

	openNow, err := tr.OpenSpans()
	if err != nil {
		t.Fatalf("OpenSpans: %v", err)
	}
	if len(openNow) != 0 {
		t.Fatalf("journal still holds %d runs after the run was filed as closed: %+v", len(openNow), openNow)
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: 2000})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 1 {
		t.Fatalf("window holds %d rows for one run, want 1 — the same stretch is on record twice",
			len(res.Spans))
	}
	if res.Spans[0].Running {
		t.Error("the surviving row is the open one, not the closed one")
	}
}

// RecordSpan clears the open entry even when the closed span is not worth
// FILING (no project, or no positive duration). The run is over either way, and
// an entry left behind would be adopted as a live run by the next daemon.
//
// MUTATION CAUGHT: putting the clear behind RecordSpan's "nothing useful to
// store" early return — the natural place to put it — leaks exactly the runs
// whose session had no project name resolved, which is the population a session
// discovered and torn down quickly falls into.
func TestRecordSpan_ClearsTheOpenRunEvenWhenItFilesNothing(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	if err := tr.RecordOpenSpan(outbound.AutonomySpan{Start: 1000, End: 1400, Session: "s"}); err != nil {
		t.Fatalf("RecordOpenSpan: %v", err)
	}
	// No project: RecordSpan files nothing.
	if err := tr.RecordSpan(outbound.AutonomySpan{Start: 1000, End: 1500, Session: "s"}); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	open, err := tr.OpenSpans()
	if err != nil {
		t.Fatalf("OpenSpans: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("journal still holds %d runs: %+v", len(open), open)
	}
}

// SyncOpenSpans is a REPLACE, and that is what keeps the journal from leaking.
//
// MUTATION CAUGHT: implementing it as a merge means a session that disappeared
// between ticks keeps an entry nothing will ever close.
func TestSyncOpenSpans_ReplacesRatherThanMerges(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	for _, id := range []string{"a", "b"} {
		if err := tr.RecordOpenSpan(outbound.AutonomySpan{Start: 100, End: 200, Session: id, Project: "p"}); err != nil {
			t.Fatalf("RecordOpenSpan: %v", err)
		}
	}
	if err := tr.SyncOpenSpans([]outbound.AutonomySpan{{Start: 100, End: 300, Session: "a", Project: "p"}}); err != nil {
		t.Fatalf("SyncOpenSpans: %v", err)
	}
	open, err := tr.OpenSpans()
	if err != nil {
		t.Fatalf("OpenSpans: %v", err)
	}
	if len(open) != 1 || open[0].Session != "a" {
		t.Fatalf("journal = %+v, want only `a` — `b`'s session is gone and its entry has to go with it", open)
	}
	if open[0].End != 300 {
		t.Errorf("last seen = %d, want 300 — the surviving run's heartbeat did not move forward", open[0].End)
	}
}

// The journal must not be mistaken for a project's closed log. Both live in the
// same directory, and both the window read and Prune walk it by extension.
//
// MUTATION CAUGHT: naming the journal `open.jsonl` makes every open run parse
// as a closed span too, so each is counted twice — once with its real end and
// once with none.
func TestOpenJournal_IsNotReadAsAProjectLog(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	if err := tr.RecordOpenSpan(outbound.AutonomySpan{Start: 1000, End: 1400, Session: "s", Project: "p"}); err != nil {
		t.Fatalf("RecordOpenSpan: %v", err)
	}
	if filepath.Ext(autonomyOpenFileName) == autonomyFileExt {
		t.Fatalf("the journal is named %q, which the directory walk reads as a project's closed log",
			autonomyOpenFileName)
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: 2000})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 1 {
		t.Fatalf("got %d rows for one open run, want 1: %+v", len(res.Spans), res.Spans)
	}
	if res.TotalRecorded != 0 {
		t.Errorf("TotalRecorded = %d, want 0 — an open run is not a row in the closed log, and "+
			"counting it there would make `N runs recorded` include runs that have not finished",
			res.TotalRecorded)
	}
}

// A corrupt journal reads as EMPTY rather than failing the whole request. The
// closed log is the answer; the journal is the extra half, and refusing to
// serve a window because of it would replace one missing row with a broken
// section.
//
// MUTATION CAUGHT: propagating the parse error turns a truncated write — the
// exact thing a crash mid-write produces, and a crash is when this file matters
// most — into an unusable Autonomy tab.
func TestOpenJournal_CorruptFileDoesNotBreakTheWindowRead(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	if err := tr.RecordSpan(span(1000, 1100, "p", session.StateReady)); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	writeOpenJournal(t, dir, `{"s":{"start":1000,`)

	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: 2000})
	if err != nil {
		t.Fatalf("SpansInWindow returned an error for a corrupt journal: %v", err)
	}
	if len(res.Spans) != 1 {
		t.Fatalf("got %d spans, want the one closed row: %+v", len(res.Spans), res.Spans)
	}
}

// The lower-bound mark survives the round trip to disk, and an OLD row without
// the key reads back as measured — the only thing it can mean, since a build
// that could not open a span for such a run never wrote one.
func TestSpanRow_CarriesTheLowerBoundMarkToDiskAndBack(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	bounded := span(1000, 1100, "p", session.StateReady)
	bounded.Session = "bounded"
	bounded.StartLowerBound = true
	if err := tr.RecordSpan(bounded); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	writeRawSpanRow(t, dir, "p",
		`{"start":2000,"end":2100,"project":"p","session":"legacy","reason":"ready"}`)

	raw, err := os.ReadFile(filepath.Join(dir, "p"+autonomyFileExt))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(splitFirstLine(string(raw))), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first["start_lower_bound"] != true {
		t.Fatalf("the marked row lost its mark on disk: %v", first)
	}

	res := allSpans(t, tr)
	if len(res.Spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(res.Spans))
	}
	if !res.Spans[0].StartLowerBound {
		t.Error("the marked row read back as measured")
	}
	if res.Spans[1].StartLowerBound {
		t.Error("a row written before the mark existed read back as a lower bound — a build that " +
			"could not open a span for such a run never wrote one, so absence means measured")
	}
	if res.Measurement.LowerBoundStart != 1 {
		t.Errorf("Measurement.LowerBoundStart = %d, want 1", res.Measurement.LowerBoundStart)
	}
}

// splitFirstLine returns everything before the first newline.
func splitFirstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
