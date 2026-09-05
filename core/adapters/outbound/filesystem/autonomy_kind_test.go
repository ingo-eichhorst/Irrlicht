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

// Autonomy run KIND (#1905 subagents) — the storage half.
//
// A subagent's span is a NESTED INTERVAL inside its parent's: the daemon holds
// a parent `working` while its children run. So the store has to know which
// kind a row is, exclude subagent rows by default, and — this is the part with
// teeth — never let a row that never said read as one of the two answers.

func kindedSpan(start, end int64, sessionID, kind, parent string) outbound.AutonomySpan {
	s := span(start, end, "proj", session.StateReady)
	s.Session = sessionID
	s.Kind = kind
	s.Parent = parent
	return s
}

// writeRawSpanRow appends one line of JSON straight into a project's log,
// bypassing RecordSpan entirely — the only way to produce a row shaped like one
// a build that predates the `kind` field wrote.
func writeRawSpanRow(t *testing.T, dir, project, line string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, project+autonomyFileExt)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func allSpans(t *testing.T, tr *AutonomySpanTracker, includeSubagents bool) *outbound.AutonomySpanResult {
	t.Helper()
	res, err := tr.SpansInWindow(outbound.AutonomySpanQuery{
		Start: 0, End: math.MaxInt64, IncludeSubagents: includeSubagents,
	})
	if err != nil {
		t.Fatalf("SpansInWindow: %v", err)
	}
	return res
}

// EVERY NEW ROW CARRIES AN EXPLICIT KIND — including one whose caller left the
// field blank, which lands on disk as the literal word "unknown" rather than as
// an absent key to be re-guessed later.
//
// That is what makes a blank `kind` on disk mean exactly one thing: a row
// written before this field existed.
func TestAutonomySpanTracker_WritesAnExplicitKindOnEveryRow(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	for _, s := range []outbound.AutonomySpan{
		kindedSpan(1000, 1100, "top-sess", session.AutonomyKindTopLevel, ""),
		kindedSpan(2000, 2100, "sub-sess", session.AutonomyKindSubagent, "top-sess"),
		kindedSpan(3000, 3100, "blank-sess", "", ""), // caller said nothing
	} {
		if err := tr.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "proj.jsonl"))
	if err != nil {
		t.Fatalf("read the span file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("wrote %d lines, want 3", len(lines))
	}
	want := []string{session.AutonomyKindTopLevel, session.AutonomyKindSubagent, session.AutonomyKindUnknown}
	for i, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("decode row %d: %v", i, err)
		}
		got, present := row["kind"]
		if !present {
			t.Fatalf("row %d has no `kind` key — every row this build writes must state one, "+
				"so that a blank means only `written before the field existed`: %s", i, line)
		}
		if got != want[i] {
			t.Fatalf("row %d kind = %v, want %q", i, got, want[i])
		}
	}
	// `parent` stays omitempty: a top-level run has no parent, and writing
	// `"parent":""` would make the key say nothing at a cost per row in a log
	// whose whole budget is 400 days of them.
	var top map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatalf("decode the top-level row: %v", err)
	}
	if _, present := top["parent"]; present {
		t.Fatalf("a top-level row carries a `parent` key: %s", lines[0])
	}
}

// THE LEGACY ROW. A row with no `kind` key at all — the shape every row on the
// maintainer's disk had before this change — must read back as UNKNOWN, be
// COUNTED as unknown, and be RETURNED under the default query.
//
// Each third of that is a separate way to get it wrong: resolving it to
// top-level is the silent claim; counting it as top-level hides it from the
// panel's sentence; dropping it deletes most of a back-filled history.
func TestAutonomySpanTracker_LegacyRowIsNeverSilentlyClassified(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	writeRawSpanRow(t, dir, "proj",
		`{"start":1000,"end":1100,"project":"proj","session":"legacy","reason":"ready"}`)

	res := allSpans(t, tr, false)
	if len(res.Spans) != 1 {
		t.Fatalf("returned %d spans, want 1 — a legacy row must not be dropped by the default view", len(res.Spans))
	}
	if got := res.Spans[0].Kind; got != session.AutonomyKindUnknown {
		t.Fatalf("legacy row Kind = %q, want %q — absence must never resolve to a claim",
			got, session.AutonomyKindUnknown)
	}
	if res.Kinds.Unknown != 1 {
		t.Fatalf("Kinds.Unknown = %d, want 1 — an uncounted unknown cannot be stated on screen", res.Kinds.Unknown)
	}
	if res.Kinds.TopLevel != 0 {
		t.Fatalf("Kinds.TopLevel = %d, want 0 — the legacy row was counted as a top-level run", res.Kinds.TopLevel)
	}
}

// THE DEFAULT VIEW. Subagent rows are left out, and the count of what was left
// out survives the filter — because "N subagent runs excluded" is exactly the
// number a client cannot compute from a list those runs are missing from.
func TestAutonomySpanTracker_DefaultExcludesSubagentsButStillCountsThem(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	for _, s := range []outbound.AutonomySpan{
		kindedSpan(1000, 1100, "top-a", session.AutonomyKindTopLevel, ""),
		kindedSpan(1010, 1050, "sub-a", session.AutonomyKindSubagent, "top-a"),
		kindedSpan(1020, 1040, "sub-b", session.AutonomyKindSubagent, "top-a"),
		kindedSpan(2000, 2100, "unk-a", session.AutonomyKindUnknown, ""),
	} {
		if err := tr.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}

	def := allSpans(t, tr, false)
	if len(def.Spans) != 2 {
		t.Fatalf("default view returned %d spans, want 2 (the top-level one and the unknown one): %+v",
			len(def.Spans), def.Spans)
	}
	for _, s := range def.Spans {
		if s.Kind == session.AutonomyKindSubagent {
			t.Fatalf("the default view returned a subagent run: %+v", s)
		}
	}
	if def.Kinds != (outbound.AutonomySpanKinds{TopLevel: 1, Subagent: 2, Unknown: 1}) {
		t.Fatalf("default Kinds = %+v, want {TopLevel:1 Subagent:2 Unknown:1} — the census counts the "+
			"WINDOW, not the rows that survived the filter", def.Kinds)
	}

	all := allSpans(t, tr, true)
	if len(all.Spans) != 4 {
		t.Fatalf("include-subagents view returned %d spans, want 4", len(all.Spans))
	}
	if all.Kinds != def.Kinds {
		t.Fatalf("the census changed with the mode: %+v vs %+v — the same window holds the same runs "+
			"whichever ones were asked for", all.Kinds, def.Kinds)
	}
	// The parent link survives the round trip: it is what lets a nested run be
	// attributed to the run that contains it, not merely excluded from it.
	var sub *outbound.AutonomySpan
	for i := range all.Spans {
		if all.Spans[i].Session == "sub-a" {
			sub = &all.Spans[i]
		}
	}
	if sub == nil {
		t.Fatal("the subagent run is missing from the include-subagents view")
	}
	if sub.Parent != "top-a" {
		t.Fatalf("subagent run Parent = %q, want %q", sub.Parent, "top-a")
	}
}

// DropReconstructed removes the reconstruction and nothing else. It is what
// makes re-running the back-fill RECLASSIFY history rather than append a second
// copy of it — and the measured rows are the ones that must survive, since they
// are the only rows in the log nothing can rebuild.
func TestAutonomySpanTracker_DropReconstructedKeepsMeasuredRows(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	for _, s := range []outbound.AutonomySpan{
		span(1000, 1100, "proj", session.StateReady), // measured
		sourcedSpan(2000, 2100, "proj", session.StateWaiting, session.AutonomySourceLog),
		sourcedSpan(3000, 3100, "proj", session.AutonomyReasonUnknown, session.AutonomySourceCost),
	} {
		if err := tr.RecordSpan(s); err != nil {
			t.Fatalf("RecordSpan: %v", err)
		}
	}
	// A row written by a build whose source this one does not recognize is
	// reconstructed too (session.IsAutonomyReconstructed: ANY source counts),
	// so it goes as well — leaving it behind would double it on the next run.
	writeRawSpanRow(t, dir, "proj",
		`{"start":4000,"end":4100,"project":"proj","session":"future","source":"telemetry"}`)

	dropped, err := tr.DropReconstructed()
	if err != nil {
		t.Fatalf("DropReconstructed: %v", err)
	}
	if dropped != 3 {
		t.Fatalf("dropped %d rows, want 3", dropped)
	}
	res := allSpans(t, tr, true)
	if len(res.Spans) != 1 {
		t.Fatalf("kept %d spans, want 1: %+v", len(res.Spans), res.Spans)
	}
	if res.Spans[0].Start != 1000 || session.IsAutonomyReconstructed(res.Spans[0].Source) {
		t.Fatalf("the surviving span is not the measured one: %+v", res.Spans[0])
	}
	if res.Provenance.Reconstructed != 0 {
		t.Fatalf("Reconstructed = %d after the drop, want 0 — a second back-fill would refuse or double",
			res.Provenance.Reconstructed)
	}
}

// A file holding ONLY reconstructed rows is removed rather than left as an
// empty shell, and a file holding only measured rows is untouched. Both are
// pruneJSONLFile's contract; this pins that DropReconstructed inherits it
// rather than reimplementing the rewrite.
func TestAutonomySpanTracker_DropReconstructedOnAWhollyRebuiltProject(t *testing.T) {
	dir := t.TempDir()
	tr := NewAutonomySpanTrackerWithDir(dir)
	rebuilt := sourcedSpan(2000, 2100, "rebuilt", session.StateReady, session.AutonomySourceLog)
	if err := tr.RecordSpan(rebuilt); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	if err := tr.RecordSpan(span(1000, 1100, "measured", session.StateReady)); err != nil {
		t.Fatalf("RecordSpan: %v", err)
	}
	if _, err := tr.DropReconstructed(); err != nil {
		t.Fatalf("DropReconstructed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rebuilt.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("the wholly-reconstructed project's file survived (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "measured.jsonl")); err != nil {
		t.Fatalf("the measured project's file was removed: %v", err)
	}
}
