package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/session"
)

// The message shapes below are REAL: they come from a census of every distinct
// arrow-bearing `session-detector` message in this machine's retained event
// log (nineteen shapes across 6,677 transitions), produced by
//
//	cat "$HOME/Library/Application Support/Irrlicht"/logs/events.log* \
//	  | grep '"session-detector"' | <group by message shape>
//
// They are the whole reason destinationState takes the LAST arrow match rather
// than the first: a third of them name a state on both sides of the arrow.
func TestDestinationState(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    string
		wantOK  bool
	}{
		{"plain ready", "agent finished turn → ready", session.StateReady, true},
		{"plain waiting", "turn ended with question or cue → waiting", session.StateWaiting, true},
		{"plain error", "session error → error", session.StateError, true},
		{"no space before the arrow", "force ready→working on first activity", session.StateWorking, true},
		{"state on both sides", "transcript activity (waiting → working)", session.StateWorking, true},
		{"state on both sides, error origin", "transcript activity (error → working)", session.StateWorking, true},
		{"trailing prose after the destination",
			"finished orphaned subagent (working → ready) — parent abc turn done", session.StateReady, true},
		{"parenthesised with a parent id",
			"subagent completed via parent task-notification (working → ready, parent abc)", session.StateReady, true},
		{"two clauses, last one wins",
			"children done, parent re-evaluated: session error → error", session.StateError, true},
		// The negatives matter as much: an arrow pointing at something that is
		// not a state must not be read as a transition, or the reconstruction
		// invents runs out of log prose.
		{"arrow to a non-state", "turn already complete at first discovery → synthetic catch-up", "", false},
		{"no arrow at all", "holding parent working — active children still running", "", false},
		{"empty message", "", "", false},
		{"state named without an arrow", "new session detected, currently working", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := destinationState(tc.message)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("destinationState(%q) = (%q, %v), want (%q, %v)", tc.message, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// The arrow pattern is DERIVED from session.CanonicalStates(), so a fifth
// state is picked up without anyone remembering to. This asserts the
// derivation rather than the current four names.
func TestArrowPatternCoversEveryCanonicalState(t *testing.T) {
	states := session.CanonicalStates()
	if len(states) == 0 {
		t.Fatal("session.CanonicalStates() is empty — cannot verify anything")
	}
	for _, s := range states {
		got, ok := destinationState("something happened → " + s)
		if !ok || got != s {
			t.Errorf("canonical state %q is not matched by arrowPattern (got %q, ok=%v)", s, got, ok)
		}
	}
}

func TestParseLogTimestamp(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   int64
		wantOK bool
	}{
		{"offset with nanoseconds", "2026-09-03T18:18:44.795422+02:00", 1788452324, true},
		{"utc Z", "2026-09-03T16:18:44Z", 1788452324, true},
		{"empty", "", 0, false},
		{"not a time", "yesterday", 0, false},
		// A timestamp this parser cannot read must NOT fall back to the zero
		// time: 1970 would place the line at the very start of the window and
		// manufacture a fifty-year span out of one bad record.
		{"unix seconds as a number", "1780762724", 0, false},
		{"date only", "2026-09-03", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLogTimestamp(tc.in)
			if ok != tc.wantOK || (ok && got != tc.want) {
				t.Fatalf("parseLogTimestamp(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestClusterRestarts(t *testing.T) {
	cases := []struct {
		name string
		in   []int64
		want []int64
	}{
		{"empty", nil, nil},
		{"one entry", []int64{100}, []int64{100}},
		{"a boot's burst collapses to its first line", []int64{100, 100, 101, 103}, []int64{100}},
		{"two boots stay two", []int64{100, 101, 1000, 1001}, []int64{100, 1000}},
		{"unsorted input is sorted first", []int64{1000, 100, 1001, 101}, []int64{100, 1000}},
		{"exactly at the window is still one boot", []int64{100, 130}, []int64{100}},
		{"one second past the window is a second boot", []int64{100, 131}, []int64{100, 131}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clusterRestarts(append([]int64(nil), tc.in...), restartClusterSeconds)
			if len(got) != len(tc.want) {
				t.Fatalf("clusterRestarts(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("clusterRestarts(%v) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

// readEventLog must survive a corrupt log without either crashing or going
// quiet about it. Both halves are asserted: the good lines around the damage
// still parse, AND the damage is counted — because a source that silently
// yields fewer transitions is indistinguishable from a quiet week.
func TestReadEventLogCountsWhatItCannotParse(t *testing.T) {
	dir := t.TempDir()
	writeLogFixture(t, dir, "logs/events.log", strings.Join([]string{
		`{"timestamp":"2026-08-18T10:00:00Z","event_type":"startup","message":"booting"}`,
		`{"timestamp":"2026-08-18T10:00:10Z","event_type":"session-detector","session_id":"s1","message":"transcript activity (ready → working)"}`,
		`{"timestamp":"2026-08-18T10:05:00Z","event_type":"session-detector","session_id":"s1","message":"agent finished turn → ready"}`,
		``, // a blank line is not damage and is not counted
		`{"timestamp":"2026-08-18T10:06:00Z","event_type":"session-detector",`, // truncated mid-append
		`not json at all`,
		`{"timestamp":"nonsense","event_type":"session-detector","session_id":"s1","message":"agent finished turn → ready"}`,
		`{"timestamp":"2026-08-18T10:07:00Z","event_type":"session-detector","session_id":"s2","message":"turn ended with question or cue → waiting"}`,
	}, "\n")+"\n")

	log, err := readEventLog(dir)
	if err != nil {
		t.Fatalf("readEventLog: %v", err)
	}
	if got, want := log.Stats.Malformed, 3; got != want {
		t.Errorf("malformed = %d, want %d (a truncated line, a non-JSON line, an unreadable timestamp)", got, want)
	}
	if got, want := len(log.Transitions), 3; got != want {
		t.Errorf("transitions = %d, want %d — the good lines around the damage must still parse", got, want)
	}
	if got, want := len(log.Restarts), 1; got != want {
		t.Errorf("restarts = %d, want %d", got, want)
	}
	if log.Stats.MalformedShare() == 0 {
		t.Error("MalformedShare() is 0 with three malformed lines — the guard in main would never fire")
	}
}

// Rotated files are named newest-first (events.log, then events.log.1), so the
// merge cannot assume filename order is time order.
func TestReadEventLogMergesRotatedFilesInTimeOrder(t *testing.T) {
	dir := t.TempDir()
	writeLogFixture(t, dir, "logs/events.log",
		`{"timestamp":"2026-08-20T10:00:00Z","event_type":"session-detector","session_id":"s1","message":"agent finished turn → ready"}`+"\n")
	writeLogFixture(t, dir, "logs/events.log.1",
		`{"timestamp":"2026-08-19T10:00:00Z","event_type":"session-detector","session_id":"s1","message":"transcript activity (ready → working)"}`+"\n")

	log, err := readEventLog(dir)
	if err != nil {
		t.Fatalf("readEventLog: %v", err)
	}
	if len(log.Transitions) != 2 {
		t.Fatalf("transitions = %d, want 2", len(log.Transitions))
	}
	if log.Transitions[0].State != session.StateWorking {
		t.Fatalf("first transition is %q; the older rotated file must sort first, or every span "+
			"reconstructed across a rotation is nonsense", log.Transitions[0].State)
	}
}

// A missing source is an error, not an empty result: "there is nothing here to
// read" and "there was nothing to reconstruct" are different claims.
func TestReadEventLogRefusesAMissingLog(t *testing.T) {
	if _, err := readEventLog(t.TempDir()); err == nil {
		t.Fatal("readEventLog on a directory with no log returned no error")
	}
}

func writeLogFixture(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}
