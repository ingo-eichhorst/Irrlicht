package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// The back-fill's run-kind classification, end to end (#1905 subagents).

// writeSubagentFixtureDataDir builds a data dir whose event log describes one
// top-level session and one subagent of it, plus a cost-era session the log
// never saw start.
func writeSubagentFixtureDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	birth := func(ts, sid, projectDir string) string {
		return fmt.Sprintf(`{"timestamp":%q,"event_type":"session-detector","session_id":%q,"message":%q}`,
			ts, sid, fmt.Sprintf(services.NewSessionInfoFormat, projectDir, "claude-code"))
	}
	msg := func(ts, sid, message string) string {
		return fmt.Sprintf(`{"timestamp":%q,"event_type":"session-detector","session_id":%q,"message":%q}`,
			ts, sid, message)
	}
	writeLogFixture(t, dir, "logs/events.log", strings.Join([]string{
		`{"timestamp":"2026-08-18T10:00:00Z","event_type":"startup","message":"booting"}`,
		birth("2026-08-18T10:00:05Z", "boss", "-Users-ingo-projects-irrlicht"),
		birth("2026-08-18T10:00:06Z", "kid", services.SubagentDirName),
		msg("2026-08-18T10:00:10Z", "boss", "transcript activity (ready → working)"),
		msg("2026-08-18T10:00:11Z", "kid", "transcript activity (ready → working)"),
		// The child finishes through the parent's task-notification, which is
		// where the parent's id enters the log.
		msg("2026-08-18T10:02:00Z", "kid",
			fmt.Sprintf(services.SubagentCompletedInfoFormat, session.StateWorking, "boss")),
		msg("2026-08-18T10:05:00Z", "boss", "turn ended with question or cue → waiting"),
	}, "\n")+"\n")
	// Cost rows. Two jobs, and only the first is about cost spans:
	//
	//   - `ghost` gets a stretch in the cost era, so the reconstruction
	//     produces one run for a session the retained event log never saw.
	//   - every session needs a row here at all, because the event log stamps
	//     no project and sessionProjects is the only join that supplies one. A
	//     span with no project is dropped outright, so `kid` would never be
	//     reconstructed to classify. Its single row inside the event-log era
	//     produces no cost span of its own (a lone row has no duration, and the
	//     boundary rule would drop it regardless).
	writeLogFixture(t, dir, "cost/proj.jsonl", strings.Join([]string{
		`{"ts":1776000000,"project":"proj","session":"ghost"}`,
		`{"ts":1776000060,"project":"proj","session":"ghost"}`,
		`{"ts":1776000120,"project":"proj","session":"ghost"}`,
		`{"ts":1776100000,"project":"proj","session":"boss"}`,
		`{"ts":1776100060,"project":"proj","session":"boss"}`,
		`{"ts":1780776010,"project":"proj","session":"boss"}`,
		`{"ts":1787047211,"project":"proj","session":"kid"}`,
	}, "\n")+"\n")
	return dir
}

func runBackfill(t *testing.T, opts options) {
	t.Helper()
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()
	if err := run(devnull, opts); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func readBackfilled(t *testing.T, dir string) map[string]outbound.AutonomySpan {
	t.Helper()
	tracker := filesystem.NewAutonomySpanTrackerWithDir(filepath.Join(dir, autonomyDirName))
	res, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{
		Start: 0, End: math.MaxInt64,
	})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	out := map[string]outbound.AutonomySpan{}
	for _, s := range res.Spans {
		out[s.Session] = s
	}
	return out
}

// THE CLASSIFICATION, written onto the rows. The subagent's run is marked as
// one and carries the parent the log named; the parent's run is marked
// top-level; and the session the log never saw start is UNKNOWN — not
// top-level, which is the assumption that would silently fill the default view
// with runs nothing classified.
func TestBackfillClassifiesEachReconstructedRun(t *testing.T) {
	dir := writeSubagentFixtureDataDir(t)
	runBackfill(t, options{dataDir: dir, gapSeconds: costGapSeconds, apply: true})
	spans := readBackfilled(t, dir)

	kid, ok := spans["kid"]
	if !ok {
		t.Fatalf("the subagent's run was not reconstructed at all: %+v", spans)
	}
	if kid.Kind != session.AutonomyKindSubagent {
		t.Fatalf("kid Kind = %q, want %q", kid.Kind, session.AutonomyKindSubagent)
	}
	if kid.Parent != "boss" {
		t.Fatalf("kid Parent = %q, want %q — the log names it, so the back-fill must derive it",
			kid.Parent, "boss")
	}

	boss, ok := spans["boss"]
	if !ok {
		t.Fatalf("the top-level run was not reconstructed: %+v", spans)
	}
	if boss.Kind != session.AutonomyKindTopLevel {
		t.Fatalf("boss Kind = %q, want %q", boss.Kind, session.AutonomyKindTopLevel)
	}

	ghost, ok := spans["ghost"]
	if !ok {
		t.Fatalf("the cost-era run was not reconstructed: %+v", spans)
	}
	if ghost.Kind != session.AutonomyKindUnknown {
		t.Fatalf("ghost Kind = %q, want %q — the retained log never saw this session start, so "+
			"neither kind was established", ghost.Kind, session.AutonomyKindUnknown)
	}
}

// THE SERVED VIEW, over the rows the back-fill actually wrote. Retargeted at
// the FIELD (#1905 recording): reading the log back the way the daemon serves
// it returns the reconstructed subagent's run like any other, CARRYING its
// classification and its parent, and the census names it.
//
// What the back-fill has to get right is unchanged — it is the only producer
// that can genuinely fail to classify a run, because the cost log carries no
// parentage at all. What changed is that its verdict is no longer a reason to
// drop a row.
func TestBackfilledSubagentRunIsServedWithItsClassification(t *testing.T) {
	dir := writeSubagentFixtureDataDir(t)
	runBackfill(t, options{dataDir: dir, gapSeconds: costGapSeconds, apply: true})

	tracker := filesystem.NewAutonomySpanTrackerWithDir(filepath.Join(dir, autonomyDirName))
	res, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	var kid *outbound.AutonomySpan
	for i := range res.Spans {
		if res.Spans[i].Session == "kid" {
			kid = &res.Spans[i]
		}
	}
	if kid == nil {
		t.Fatalf("the subagent's reconstructed run is missing: %+v", res.Spans)
	}
	if kid.Kind != session.AutonomyKindSubagent || kid.Parent != "boss" {
		t.Fatalf("the subagent's run lost its classification on the way back: kind=%q parent=%q",
			kid.Kind, kid.Parent)
	}
	if res.Kinds.Subagent != 1 {
		t.Fatalf("Kinds.Subagent = %d, want 1 — a kind nothing counts cannot be stated on screen",
			res.Kinds.Subagent)
	}
	if res.Kinds.Unknown < 1 {
		t.Fatalf("Kinds.Unknown = %d, want at least 1 (the cost-era run)", res.Kinds.Unknown)
	}
}

// RE-RUNNABLE. Running the tool again with --replace reclassifies history
// instead of appending a second copy of it, and the measured rows — the only
// rows in the log nothing can rebuild — survive untouched.
func TestBackfillReplaceReclassifiesInsteadOfDoubling(t *testing.T) {
	dir := writeSubagentFixtureDataDir(t)
	tracker := filesystem.NewAutonomySpanTrackerWithDir(filepath.Join(dir, autonomyDirName))
	// A measured row far in the future of the fixture, so the live floor does
	// not swallow the reconstruction.
	measured := outbound.AutonomySpan{
		Start: 1_900_000_000, End: 1_900_000_100, Project: "proj", Session: "measured",
		Reason: session.StateReady, Kind: session.AutonomyKindTopLevel,
	}
	if err := tracker.RecordSpan(measured); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}

	runBackfill(t, options{dataDir: dir, gapSeconds: costGapSeconds, apply: true})
	first, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}

	// A second --apply WITHOUT --replace is refused, which is what makes
	// --replace the answer rather than a convenience.
	if err := run(os.Stdout, options{dataDir: dir, gapSeconds: costGapSeconds, apply: true}); err == nil {
		t.Fatal("a second --apply was accepted; appending a reconstruction doubles every figure")
	}

	runBackfill(t, options{dataDir: dir, gapSeconds: costGapSeconds, apply: true, replace: true})
	second, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}

	if len(second.Spans) != len(first.Spans) {
		t.Fatalf("after --replace the log holds %d spans, want the same %d — a re-run must reclassify, "+
			"not double", len(second.Spans), len(first.Spans))
	}
	if second.Provenance.Reconstructed != first.Provenance.Reconstructed {
		t.Fatalf("reconstructed count went %d → %d across a --replace re-run",
			first.Provenance.Reconstructed, second.Provenance.Reconstructed)
	}
	found := false
	for _, s := range second.Spans {
		if s.Session == "measured" {
			found = true
			if session.IsAutonomyReconstructed(s.Source) {
				t.Fatalf("the measured row came back reconstructed: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("--replace removed the MEASURED row; only the reconstruction may be replaced")
	}
}
