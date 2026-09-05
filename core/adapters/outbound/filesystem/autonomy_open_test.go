package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/ports/outbound"
)

// writeOpenJournal drops a hand-written open-run journal into dir. Written as
// raw bytes on purpose: this is the shape a PREVIOUS daemon left behind, so the
// test must not depend on the writer it is checking the reader against — and it
// is what let this test run red against a build that had no journal writer at
// all.
func writeOpenJournal(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, autonomyOpenFileName), []byte(body), 0600); err != nil {
		t.Fatalf("write open journal: %v", err)
	}
}

// TestSpansInWindow_ReturnsTheRunStillInProgress is red-first defect 3 (#1905
// recording), at the layer that serves the clients: a span reached the log only
// when it CLOSED, so a window read returned nothing for a run that was still
// going — the longest run on the machine was the one guaranteed to be missing.
//
// Seen red before the fix: `spans in window = 0`.
func TestSpansInWindow_ReturnsTheRunStillInProgress(t *testing.T) {
	dir := t.TempDir()
	writeOpenJournal(t, dir, `{"sess-1":{"start":1000,"last_seen":1400,"project":"irrlicht",`+
		`"session":"sess-1","adapter":"claude-code","kind":"top"}}`)

	tr := NewAutonomySpanTrackerWithDir(dir)
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: 1500})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 1 {
		t.Fatalf("spans in window = %d, want 1 — a run that has not ended yet is invisible, "+
			"and it is the long ones that are still going", len(res.Spans))
	}
	if got := res.Spans[0].Start; got != 1000 {
		t.Errorf("span.Start = %d, want 1000", got)
	}
	if got := res.Spans[0].Session; got != "sess-1" {
		t.Errorf("span.Session = %q, want %q", got, "sess-1")
	}
}
