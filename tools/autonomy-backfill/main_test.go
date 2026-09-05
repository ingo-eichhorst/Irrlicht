package main

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// THE MARKING RULE, round-tripped through the daemon's own store. A
// reconstructed span reads back as reconstructed and a measured one does not —
// which is the single fact every provenance note on both clients is built on.
func TestReconstructedSpansAreMarkedAndLiveOnesAreNot(t *testing.T) {
	dir := t.TempDir()
	tracker := filesystem.NewAutonomySpanTrackerWithDir(dir)

	live := outbound.AutonomySpan{Start: 5000, End: 5100, Project: "proj", Session: "live", Reason: session.StateReady}
	fromLog := outbound.AutonomySpan{Start: 3000, End: 3100, Project: "proj", Session: "log",
		Reason: session.StateWaiting, Source: session.AutonomySourceLog}
	fromCost := outbound.AutonomySpan{Start: 1000, End: 1100, Project: "proj", Session: "cost",
		Reason: session.AutonomyReasonUnknown, Source: session.AutonomySourceCost}
	for _, s := range []outbound.AutonomySpan{live, fromLog, fromCost} {
		if err := tracker.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}

	res, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) != 3 {
		t.Fatalf("read back %d spans, want 3", len(res.Spans))
	}
	bySession := map[string]outbound.AutonomySpan{}
	for _, s := range res.Spans {
		bySession[s.Session] = s
	}
	if bySession["live"].Source != "" {
		t.Errorf("the live span came back with source %q; absence IS the live case", bySession["live"].Source)
	}
	if session.IsAutonomyReconstructed(bySession["live"].Source) {
		t.Error("the live span reads back as reconstructed")
	}
	if !session.IsAutonomyReconstructed(bySession["log"].Source) {
		t.Error("the event-log span does not read back as reconstructed")
	}
	if bySession["cost"].Reason != session.AutonomyReasonUnknown {
		t.Errorf("the cost span's reason came back as %q", bySession["cost"].Reason)
	}
	if res.Provenance.Reconstructed != 2 {
		t.Errorf("Provenance.Reconstructed = %d, want 2", res.Provenance.Reconstructed)
	}
	if res.Provenance.CostDerived != 1 {
		t.Errorf("Provenance.CostDerived = %d, want 1", res.Provenance.CostDerived)
	}
	if res.Provenance.LiveSince != 5000 {
		t.Errorf("Provenance.LiveSince = %d, want 5000 — the earliest MEASURED start", res.Provenance.LiveSince)
	}
}

// A log holding nothing but measured spans must report zero reconstruction, so
// both clients stay silent rather than printing a provenance note about
// nothing.
func TestAnAllLiveLogReportsNoReconstruction(t *testing.T) {
	dir := t.TempDir()
	tracker := filesystem.NewAutonomySpanTrackerWithDir(dir)
	if err := tracker.RecordSpan(outbound.AutonomySpan{
		Start: 5000, End: 5100, Project: "proj", Session: "live", Reason: session.StateReady,
	}); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	res, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if res.Provenance.Reconstructed != 0 || res.Provenance.CostDerived != 0 {
		t.Fatalf("Provenance = %+v, want an all-zero count on an all-live log", res.Provenance)
	}
}

// There is no undo. A second --apply into a log that already holds
// reconstructed rows would double every figure in the section, and afterwards
// nothing could tell that apart from a machine that genuinely ran twice as
// much.
func TestGuardRefusesASecondBackfill(t *testing.T) {
	dir := t.TempDir()
	tracker := filesystem.NewAutonomySpanTrackerWithDir(dir)
	if err := guardAlreadyBackfilled(tracker, false); err != nil {
		t.Fatalf("the guard refused an empty span log: %v", err)
	}
	if err := tracker.RecordSpan(outbound.AutonomySpan{
		Start: 100, End: 200, Project: "proj", Session: "s",
		Reason: session.AutonomyReasonUnknown, Source: session.AutonomySourceCost,
	}); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	if err := guardAlreadyBackfilled(tracker, false); err == nil {
		t.Fatal("the guard allowed a second back-fill over an already reconstructed log")
	}
	if err := guardAlreadyBackfilled(tracker, true); err != nil {
		t.Fatalf("--force did not override the guard: %v", err)
	}
}

// A log holding only MEASURED rows is not a back-filled log: the guard must
// not refuse the first run just because the daemon has been collecting.
func TestGuardAllowsBackfillOverAPurelyLiveLog(t *testing.T) {
	dir := t.TempDir()
	tracker := filesystem.NewAutonomySpanTrackerWithDir(dir)
	if err := tracker.RecordSpan(outbound.AutonomySpan{
		Start: 100, End: 200, Project: "proj", Session: "s", Reason: session.StateReady,
	}); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	if err := guardAlreadyBackfilled(tracker, false); err != nil {
		t.Fatalf("the guard refused a first back-fill over a purely live log: %v", err)
	}
}

// A source this tool has stopped being able to read produces FEWER spans, and
// fewer spans is indistinguishable from a quieter month — so it refuses rather
// than writing a fraction of the history.
func TestGuardRefusesAnUnreadableSource(t *testing.T) {
	res := result{
		EventLog: &eventLog{Stats: parseStats{Lines: 1000, Malformed: 500}},
		CostLog:  &costLog{Stats: parseStats{Lines: 1000, Malformed: 0}},
	}
	if err := guardMalformed(res, options{apply: true}); err == nil {
		t.Fatal("guardMalformed allowed a 50% unreadable event log")
	}
	if err := guardMalformed(res, options{apply: true, allowMalformed: true}); err != nil {
		t.Fatalf("--allow-malformed did not override the guard: %v", err)
	}
	clean := result{
		EventLog: &eventLog{Stats: parseStats{Lines: 1000, Malformed: 1}},
		CostLog:  &costLog{Stats: parseStats{Lines: 1000, Malformed: 0}},
	}
	if err := guardMalformed(clean, options{apply: true}); err != nil {
		t.Fatalf("guardMalformed refused a 0.1%% unreadable log: %v", err)
	}
}

// End to end over a fixture data dir: both sources, both marks, nothing
// written without --apply.
func TestRunDryRunWritesNothing(t *testing.T) {
	dir := writeFixtureDataDir(t)
	opts := options{dataDir: dir, gapSeconds: costGapSeconds}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()
	if err := run(devnull, opts); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, autonomyDirName)); !os.IsNotExist(err) {
		t.Fatalf("a dry run created %s — it must write nothing at all", filepath.Join(dir, autonomyDirName))
	}
}

func TestRunApplyWritesMarkedSpansFromBothSources(t *testing.T) {
	dir := writeFixtureDataDir(t)
	opts := options{dataDir: dir, gapSeconds: costGapSeconds, apply: true}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()
	if err := run(devnull, opts); err != nil {
		t.Fatalf("run: %v", err)
	}

	tracker := filesystem.NewAutonomySpanTrackerWithDir(filepath.Join(dir, autonomyDirName))
	res, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	if len(res.Spans) == 0 {
		t.Fatal("--apply wrote no spans at all")
	}
	if res.Provenance.Reconstructed != len(res.Spans) {
		t.Fatalf("Reconstructed = %d but %d spans were written — every back-filled row must be marked",
			res.Provenance.Reconstructed, len(res.Spans))
	}
	sources := map[string]int{}
	for _, s := range res.Spans {
		sources[s.Source]++
		if s.Source == session.AutonomySourceCost && s.Reason != session.AutonomyReasonUnknown {
			t.Errorf("a cost-derived span claims reason %q", s.Reason)
		}
	}
	if sources[session.AutonomySourceLog] == 0 || sources[session.AutonomySourceCost] == 0 {
		t.Fatalf("sources = %v, want spans from both", sources)
	}
	// And it must refuse to run again over its own output.
	if err := run(devnull, opts); err == nil {
		t.Fatal("a second --apply was allowed over an already back-filled log")
	}
}

// Flags must work AFTER the data dir, because that is the order the usage line
// documents and the order anyone would type. Go's flag package stops at the
// first non-flag argument, so the un-reordered parse reads `<dir> --apply` as
// two positionals and silently drops --apply — a dry run where the caller
// asked for a write.
func TestFlagsWorkOnEitherSideOfTheDataDir(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"flags after the path", []string{"/data", "--apply", "--gap-seconds", "300"}},
		{"flags before the path", []string{"--apply", "--gap-seconds", "300", "/data"}},
		{"flags on both sides", []string{"--apply", "/data", "--gap-seconds=300"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseFlags(tc.argv)
			if err != nil {
				t.Fatalf("parseFlags(%v): %v", tc.argv, err)
			}
			if opts.dataDir != "/data" {
				t.Errorf("dataDir = %q, want /data", opts.dataDir)
			}
			if !opts.apply {
				t.Error("--apply was silently dropped")
			}
			if opts.gapSeconds != 300 {
				t.Errorf("gapSeconds = %d, want 300", opts.gapSeconds)
			}
		})
	}
}

// The dry run is the DEFAULT, and a caller who forgets --apply must get a
// report rather than a write.
func TestApplyDefaultsOff(t *testing.T) {
	opts, err := parseFlags([]string{"/data"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if opts.apply {
		t.Fatal("--apply defaults ON; the tool must write nothing unless asked")
	}
	if opts.gapSeconds != costGapSeconds {
		t.Fatalf("gapSeconds = %d, want the documented default %d", opts.gapSeconds, costGapSeconds)
	}
}

func TestParseFlagsRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"no data dir", nil},
		{"two data dirs", []string{"/a", "/b"}},
		{"a gap of zero", []string{"/data", "--gap-seconds", "0"}},
		{"a negative gap", []string{"/data", "--gap-seconds", "-1"}},
		{"an unparseable --since", []string{"/data", "--since", "last tuesday"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseFlags(tc.argv); err == nil {
				t.Fatalf("parseFlags(%v) accepted bad input", tc.argv)
			}
		})
	}
}

// THE DOUBLE-COUNT RULE, end to end, and this is the realistic case: by the
// time anyone runs this tool the daemon has already been measuring for a
// while, and the event log it reconstructs from covers that same period. A run
// written twice — once measured, once rebuilt — inflates every figure in the
// section while looking exactly like a busier week.
func TestApplyNeverWritesIntoTheMeasuredEra(t *testing.T) {
	dir := writeFixtureDataDir(t)
	spanDir := filepath.Join(dir, autonomyDirName)
	tracker := filesystem.NewAutonomySpanTrackerWithDir(spanDir)

	// The daemon measured a run an hour before the fixture's logged
	// transitions, so its whole event-log era is already covered.
	liveStart, err := time.Parse(time.RFC3339, "2026-08-18T09:00:00Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	floor := liveStart.Unix()
	if err := tracker.RecordSpan(outbound.AutonomySpan{
		Start: floor, End: floor + 60, Project: "proj", Session: "live", Reason: session.StateReady,
	}); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}

	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()
	if err := run(devnull, options{dataDir: dir, gapSeconds: costGapSeconds, apply: true}); err != nil {
		t.Fatalf("run: %v", err)
	}

	res, err := tracker.SpansInWindow(outbound.AutonomySpanQuery{Start: 0, End: math.MaxInt64})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	for _, s := range res.Spans {
		if session.IsAutonomyReconstructed(s.Source) && s.End >= floor {
			t.Fatalf("a reconstructed span reaches into the measured era: %+v (live floor %d)", s, floor)
		}
	}
	// …and the era the daemon never measured was still back-filled, or the
	// bound has simply swallowed everything.
	if res.Provenance.Reconstructed == 0 {
		t.Fatal("nothing was reconstructed at all; the live floor dropped the whole cost era too")
	}
	if res.Provenance.CostDerived == 0 {
		t.Fatal("no cost-derived span survived, though the cost log predates the live floor by months")
	}
}

// THE LOCAL-ONLY RULE, mechanically enforced. This tool is a one-off the
// maintainer runs by hand on one machine; every other user's Autonomy view
// correctly starts empty on the day they upgrade, and the shipped empty state
// already says so. Wiring it into daemon startup, a migration or first-run
// logic would silently reconstruct history on machines nobody asked.
//
// Two ways the daemon could make it RUN, and both are refused:
//
//	an import of its module path — it is `package main`, so this would not
//	even build, but the rule is stated rather than left to the compiler;
//	an exec of its binary name.
//
// Naming it in a doc comment is deliberately fine and in fact desirable: the
// span tracker's own header explains where a `source` comes from. That is why
// this looks for the two executable shapes rather than for the string.
func TestBackfillIsNotWiredIntoTheDaemon(t *testing.T) {
	const modulePath = "irrlicht/tools/autonomy-backfill"
	const binaryName = "autonomy-backfill"

	coreDir := filepath.Join("..", "..", "core")
	if _, err := os.Stat(coreDir); err != nil {
		t.Fatalf("cannot reach %s: %v — this check must fail loudly rather than pass vacuously", coreDir, err)
	}
	visited := 0
	var offenders []string
	err := filepath.WalkDir(coreDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		visited++
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, modulePath) {
				offenders = append(offenders, path+": imports it")
				break
			}
			if strings.Contains(line, binaryName) && strings.Contains(line, "exec.Command") {
				offenders = append(offenders, path+": execs it")
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", coreDir, err)
	}
	// Absence of a finding and inability to look must never look the same.
	if visited == 0 {
		t.Fatalf("walked %s and read no Go files — the check could not run", coreDir)
	}
	// ...and neither must "the pattern went stale". The tool's own package
	// must match the shapes being searched for, or a rename would silently
	// turn this into a check of nothing.
	if !strings.Contains(readSelf(t, "main.go"), binaryName) {
		t.Fatalf("this tool's own main.go no longer contains %q — the search pattern has gone stale", binaryName)
	}
	if len(offenders) > 0 {
		t.Fatalf("the daemon can run the back-fill tool: %v — it must never run automatically", offenders)
	}
}

func readSelf(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// writeFixtureDataDir builds a minimal data dir holding both sources: an event
// log whose transitions close cleanly, and a cost log whose activity predates
// the event log's first transition.
func writeFixtureDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeLogFixture(t, dir, "logs/events.log", strings.Join([]string{
		`{"timestamp":"2026-08-18T10:00:00Z","event_type":"startup","message":"booting"}`,
		`{"timestamp":"2026-08-18T10:00:10Z","event_type":"session-detector","session_id":"s1","message":"transcript activity (ready → working)"}`,
		`{"timestamp":"2026-08-18T10:05:00Z","event_type":"session-detector","session_id":"s1","message":"turn ended with question or cue → waiting"}`,
	}, "\n")+"\n")
	// Cost rows for the same session, before the event log's era, plus rows
	// inside it (which the boundary rule drops).
	writeLogFixture(t, dir, "cost/proj.jsonl", strings.Join([]string{
		`{"ts":1776000000,"project":"proj","session":"s1"}`,
		`{"ts":1776000060,"project":"proj","session":"s1"}`,
		`{"ts":1776000120,"project":"proj","session":"s1"}`,
		`{"ts":1780776010,"project":"proj","session":"s1"}`,
	}, "\n")+"\n")
	return dir
}
