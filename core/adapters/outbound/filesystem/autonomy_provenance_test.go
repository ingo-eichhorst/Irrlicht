package filesystem

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// Autonomy back-fill provenance (#1905) — the storage half.
//
// tools/autonomy-backfill appends spans it reconstructed from logs already on
// disk. Every such row carries a `source`; every row the daemon measured
// carries none, which is what lets the back-fill append into a live log
// without rewriting a single row already in it.

func sourcedSpan(start, end int64, project, reason, source string) outbound.AutonomySpan {
	s := span(start, end, project, reason)
	s.Source = source
	s.Session = source + "-sess"
	return s
}

// THE MARKING RULE. Absence of a source is the live case, and a source — any
// source — is the reconstructed one.
func TestAutonomySpanTracker_MarksReconstructedRowsAndLeavesLiveOnesBare(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	for _, s := range []outbound.AutonomySpan{
		span(1000, 1100, "proj", session.StateReady), // measured live
		sourcedSpan(2000, 2100, "proj", session.StateWaiting, session.AutonomySourceLog),
		sourcedSpan(3000, 3100, "proj", session.AutonomyReasonUnknown, session.AutonomySourceCost),
	} {
		if err := tr.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}

	// The on-disk shape matters as much as the round trip: `source` is
	// omitempty, so a measured row must not carry the key at all. A row that
	// wrote `"source":""` would still read back as live today and would stop
	// doing so the moment anyone tested the key's presence.
	raw, err := os.ReadFile(filepath.Join(dir, "proj.jsonl"))
	if err != nil {
		t.Fatalf("read the span file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("wrote %d lines, want 3", len(lines))
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode the measured row: %v", err)
	}
	if _, present := first["source"]; present {
		t.Errorf("the measured row carries a `source` key: %s", lines[0])
	}

	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 3 {
		t.Fatalf("read back %d spans, want 3", len(res.Spans))
	}
	bySource := map[string]int{}
	for _, s := range res.Spans {
		bySource[s.Source]++
	}
	if bySource[""] != 1 || bySource[session.AutonomySourceLog] != 1 || bySource[session.AutonomySourceCost] != 1 {
		t.Fatalf("sources = %v, want one of each", bySource)
	}
	if res.Provenance.Reconstructed != 2 {
		t.Errorf("Reconstructed = %d, want 2", res.Provenance.Reconstructed)
	}
	if res.Provenance.CostDerived != 1 {
		t.Errorf("CostDerived = %d, want 1", res.Provenance.CostDerived)
	}
}

// LiveSince is the date before which everything on record is reconstructed. It
// is computed over the WHOLE log, not the window — a window holding no
// measured span cannot answer a question about the whole log.
func TestAutonomySpanTracker_LiveSinceIsLogWideNotWindowScoped(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	for _, s := range []outbound.AutonomySpan{
		sourcedSpan(1000, 1100, "proj", session.AutonomyReasonUnknown, session.AutonomySourceCost),
		sourcedSpan(2000, 2100, "proj", session.StateWaiting, session.AutonomySourceLog),
		span(9000, 9100, "proj", session.StateReady), // the earliest MEASURED span
		span(9500, 9600, "proj", session.StateReady),
	} {
		if err := tr.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}
	// A window holding only reconstructed spans still learns when live
	// collection began.
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: 5000})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 2 {
		t.Fatalf("window returned %d spans, want the 2 reconstructed ones", len(res.Spans))
	}
	if res.Provenance.LiveSince() != 9000 {
		t.Fatalf("LiveSince = %d, want 9000 — the earliest measured start ACROSS THE LOG, not this window",
			res.Provenance.LiveSince())
	}
	if res.Provenance.Reconstructed != 2 {
		t.Fatalf("Reconstructed = %d, want 2 (window-scoped)", res.Provenance.Reconstructed)
	}
}

// A log with nothing measured in it reports LiveSince 0 — which is a different
// claim from "measured since the epoch", and is why both clients test for zero
// before printing a date.
func TestAutonomySpanTracker_LiveSinceIsZeroWithNothingMeasured(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	if err := tr.RecordSpan(sourcedSpan(1000, 1100, "proj",
		session.AutonomyReasonUnknown, session.AutonomySourceCost)); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if res.Provenance.LiveSince() != 0 {
		t.Fatalf("LiveSince = %d, want 0 on a log holding nothing measured", res.Provenance.LiveSince())
	}
}

// The counts describe the spans the caller actually RECEIVED. Counted during
// the read instead, a window clipped by Limit would report reconstructions the
// caller never got — and the clients print these numbers as "N of the runs in
// view".
func TestAutonomySpanTracker_ProvenanceIsCountedAfterTruncation(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	// Five spans, oldest first; only the oldest two survive a Limit of 2, and
	// exactly one of those two is reconstructed.
	spans := []outbound.AutonomySpan{
		span(1000, 1100, "proj", session.StateReady),
		sourcedSpan(2000, 2100, "proj", session.AutonomyReasonUnknown, session.AutonomySourceCost),
		sourcedSpan(3000, 3100, "proj", session.StateWaiting, session.AutonomySourceLog),
		sourcedSpan(4000, 4100, "proj", session.StateWaiting, session.AutonomySourceLog),
		sourcedSpan(5000, 5100, "proj", session.StateWaiting, session.AutonomySourceLog),
	}
	for i, s := range spans {
		s.Session = s.Session + "-" + string(rune('a'+i))
		if err := tr.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64, Limit: 2})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if !res.Truncated || len(res.Spans) != 2 {
		t.Fatalf("expected a truncated 2-span result, got %d spans (truncated=%v)", len(res.Spans), res.Truncated)
	}
	if res.Provenance.Reconstructed != 1 || res.Provenance.CostDerived != 1 {
		t.Fatalf("provenance = %+v, want the counts of the RETURNED spans (1 reconstructed, 1 cost-derived)",
			res.Provenance)
	}
	// LiveSince stays log-wide even under a limit.
	if res.Provenance.LiveSince() != 1000 {
		t.Fatalf("LiveSince = %d, want 1000", res.Provenance.LiveSince())
	}
}

// EraStarts is keyed by the row's OWN source field, so a source this build has
// never heard of gets its own era instead of being folded into someone else's —
// which is what lets the daemon derive a handover for it without a case.
func TestAutonomySpanTracker_EraStartsAreKeyedByTheRawSource(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	for _, s := range []outbound.AutonomySpan{
		sourcedSpan(1000, 1100, "proj", session.AutonomyReasonUnknown, session.AutonomySourceCost),
		sourcedSpan(1200, 1300, "proj", session.AutonomyReasonUnknown, session.AutonomySourceCost), // later, ignored
		sourcedSpan(2000, 2100, "proj", session.StateWaiting, session.AutonomySourceLog),
		span(3000, 3100, "proj", session.StateReady),
	} {
		if err := tr.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	want := map[string]int64{
		session.AutonomySourceCost: 1000,
		session.AutonomySourceLog:  2000,
		"":                         3000,
	}
	for source, start := range want {
		if got := res.Provenance.EraStarts[source]; got != start {
			t.Errorf("EraStarts[%q] = %d, want %d (the EARLIEST start of that provenance)", source, got, start)
		}
	}
	if len(res.Provenance.EraStarts) != len(want) {
		t.Errorf("EraStarts = %v, want exactly %v", res.Provenance.EraStarts, want)
	}
}

// A window read over a missing log still hands back a usable (non-nil) era map:
// "the log does not exist yet" is the most common path here, and a caller that
// writes into a nil map panics.
func TestAutonomySpanTracker_EraStartsAreNeverNil(t *testing.T) {
	res, err := NewAutonomySpanTrackerWithDir(filepath.Join(t.TempDir(), "absent")).
		SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if res.Provenance.EraStarts == nil {
		t.Fatal("EraStarts is nil on a missing log")
	}
	if res.Provenance.LiveSince() != 0 {
		t.Fatalf("LiveSince() = %d on an empty log, want 0", res.Provenance.LiveSince())
	}
}

// A source this build does not recognize still reads as reconstructed. A row
// written by a newer back-fill must never read back as MEASURED just because
// its source is unfamiliar — that is the one direction of this field that
// silently turns a reconstruction into a claim.
func TestAutonomySpanTracker_UnknownSourceStillCountsAsReconstructed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "proj.jsonl"),
		[]byte(`{"start":1000,"end":1100,"project":"proj","session":"s","source":"some-future-source"}`+"\n"),
		0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tr := NewAutonomySpanTrackerWithDir(dir)
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if res.Provenance.Reconstructed != 1 {
		t.Fatalf("Reconstructed = %d, want 1 — any non-empty source is reconstructed", res.Provenance.Reconstructed)
	}
	if res.Provenance.CostDerived != 0 {
		t.Fatalf("CostDerived = %d, want 0 — an unrecognized source is not the cost log", res.Provenance.CostDerived)
	}
	if res.Provenance.LiveSince() != 0 {
		t.Fatalf("LiveSince = %d, want 0 — an unrecognized source must not be counted as measured",
			res.Provenance.LiveSince())
	}
}
