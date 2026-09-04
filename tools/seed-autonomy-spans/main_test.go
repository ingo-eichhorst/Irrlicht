package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// TestGuardRefusesAForeignSpanDir is the mutation fixture for the guard: it
// mutates the state the guard protects (a span directory this tool did not
// create) and confirms the guard refuses. Without it, a QA run would mix
// synthetic runs into a real span log with no way to tell them apart
// afterwards.
func TestGuardRefusesAForeignSpanDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), autonomyDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A real daemon's log: a span file, and no marker of ours.
	if err := os.WriteFile(filepath.Join(dir, "irrlicht.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := guardSpanDir(dir, false); err == nil {
		t.Fatal("the guard accepted a span directory it did not create — synthetic runs would be " +
			"mixed into a real log with no way to separate them again")
	}
	if err := guardSpanDir(dir, true); err != nil {
		t.Fatalf("--overwrite must be honored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "irrlicht.jsonl")); !os.IsNotExist(err) {
		t.Error("--overwrite left the old span file in place")
	}
}

func TestGuardRefusesItsOwnPreviousSeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), autonomyDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, seedMarker), []byte("x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := guardSpanDir(dir, false); err != nil {
		t.Fatalf("an empty seeded directory is safe to reuse: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "irrlicht.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := guardSpanDir(dir, false); err == nil {
		t.Error("the guard accepted a directory that already holds spans without --overwrite")
	}
}

func TestGuardAllowsAMissingDir(t *testing.T) {
	if err := guardSpanDir(filepath.Join(t.TempDir(), "nothing-here"), false); err != nil {
		t.Errorf("a directory that does not exist yet is the normal case: %v", err)
	}
}

// TestGeneratedCorpusPopulatesEveryWindow is the tool's reason for existing: a
// seeded machine must render populated in EVERY range and span the section
// offers, not just the default pair.
func TestGeneratedCorpusPopulatesEveryWindow(t *testing.T) {
	now := time.Now()
	spans := generateSpans(rand.New(rand.NewSource(1905)), now, defaultTotalDays)
	if len(spans) < 300 {
		t.Fatalf("generated %d spans, want at least a few hundred", len(spans))
	}
	windows := map[string]time.Duration{
		"8h":   8 * time.Hour,
		"24h":  24 * time.Hour,
		"7d":   7 * 24 * time.Hour,
		"30d":  30 * 24 * time.Hour,
		"12mo": 365 * 24 * time.Hour,
	}
	for name, w := range windows {
		cutoff := now.Add(-w).Unix()
		count := 0
		for _, s := range spans {
			if s.End >= cutoff {
				count++
			}
		}
		if count == 0 {
			t.Errorf("no runs inside the %s window — that view would render its empty state", name)
		}
	}
}

// TestGeneratedCorpusStraddlesTheSampleFloor keeps the seeded picture useful
// for reviewing the honesty rules: with every daily bucket on one side of the
// floor, a screenshot shows only one of the two renderings.
func TestGeneratedCorpusStraddlesTheSampleFloor(t *testing.T) {
	const sampleFloor = 20 // mirrors autonomySampleFloor in the daemon's handler
	now := time.Now()
	spans := generateSpans(rand.New(rand.NewSource(1905)), now, defaultTotalDays)
	perDay := map[int64]int{}
	for _, s := range spans {
		perDay[(now.Unix()-s.End)/86400]++
	}
	thin, thick := 0, 0
	for day, n := range perDay {
		if day >= recentDays {
			continue
		}
		if n < sampleFloor {
			thin++
		} else {
			thick++
		}
	}
	if thin == 0 || thick == 0 {
		t.Errorf("the last %d days are %d thin / %d full buckets — a seeded screenshot must show BOTH "+
			"the under-floor rendering and the normal one", recentDays, thin, thick)
	}

	// The Year range buckets by WEEK, so it needs its own straddle: the older
	// days' density is chosen for exactly this (see the const block's comment),
	// and without the check that choice is an unverified claim.
	perWeek := map[int64]int{}
	for _, s := range spans {
		perWeek[(now.Unix()-s.End)/(7*86400)]++
	}
	weeklyThin, weeklyThick := 0, 0
	for week, n := range perWeek {
		if week >= 52 {
			continue
		}
		if n < sampleFloor {
			weeklyThin++
		} else {
			weeklyThick++
		}
	}
	if weeklyThin == 0 || weeklyThick == 0 {
		t.Errorf("the last 52 weeks are %d thin / %d full buckets — the Year range must show both too",
			weeklyThin, weeklyThick)
	}
}

func TestGeneratedCorpusCoversEveryEndReason(t *testing.T) {
	spans := generateSpans(rand.New(rand.NewSource(1905)), time.Now(), 60)
	seen := map[string]int{}
	for _, s := range spans {
		seen[s.Reason]++
	}
	for _, reason := range session.AutonomyEndReasons() {
		if seen[reason] == 0 {
			t.Errorf("no seeded run ends in %q — the strip's legend would have an unused color", reason)
		}
	}
	if len(seen) != len(session.AutonomyEndReasons()) {
		t.Errorf("seeded reasons %v do not match the domain vocabulary %v", seen, session.AutonomyEndReasons())
	}
}

func TestGeneratedDurationsHaveALongTail(t *testing.T) {
	spans := generateSpans(rand.New(rand.NewSource(1905)), time.Now(), defaultTotalDays)
	var short, long int
	for _, s := range spans {
		d := s.Duration()
		if d < 60 {
			short++
		}
		if d > 3600 {
			long++
		}
	}
	if short == 0 || long == 0 {
		t.Errorf("durations span %d sub-minute and %d over-an-hour runs — a flat distribution would make "+
			"p95/p50/p5 sit evenly apart and hide what the three lines exist to show", short, long)
	}
}

// TestSeededRowsReadBackThroughTheDaemonsOwnReader is the format-agreement
// check: the tool writes through the daemon's tracker, so a seeded log must
// come back out of the daemon's reader unchanged.
func TestSeededRowsReadBackThroughTheDaemonsOwnReader(t *testing.T) {
	dir := t.TempDir()
	tracker := filesystem.NewAutonomySpanTrackerWithDir(dir)
	now := time.Now()
	spans := generateSpans(rand.New(rand.NewSource(7)), now, 40)
	for _, s := range spans {
		if err := tracker.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}
	res, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: now.Unix() + 1})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if res.TotalRecorded != len(spans) {
		t.Errorf("wrote %d spans, read back %d", len(spans), res.TotalRecorded)
	}
	for _, s := range res.Spans {
		if s.Project == "" || s.Reason == "" || s.Duration() <= 0 {
			t.Fatalf("a seeded row came back incomplete: %+v", s)
		}
	}
}
