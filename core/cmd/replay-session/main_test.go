package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
)

// fixturePath returns an absolute path to a fixture under the repo-root
// testdata/replay/<adapter>/ tree. The test binary runs from the package
// directory (core/cmd/replay-session), so we walk up three parents.
func fixturePath(t *testing.T, rel string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "replay", rel))
	if err != nil {
		t.Fatalf("abs fixture path: %v", err)
	}
	return abs
}

// TestReplayWithSidecar_GoldenFixture locks in the regression oracle: the
// committed 10-full-lifecycle-839f0678 fixture must replay to the exact
// set of state transitions the daemon recorded in the sidecar, with no
// ordered-diff mismatches, AND the replay's own transition sequence must
// match the expected lifecycle (pre-session → working turns → tool-
// waiting → question-waiting → final ready → post-session cleanup).
//
// Any drift here indicates a real change in classifier, tailer, or
// debounce behavior and should be investigated before merging.
func TestReplayWithSidecar_GoldenFixture(t *testing.T) {
	transcript := fixturePath(t, "claudecode/10-full-lifecycle-839f0678.jsonl")
	sidecar := fixturePath(t, "claudecode/10-full-lifecycle-839f0678.events.jsonl")

	report, err := ReplayWithSidecar(transcript, sidecar, ReportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ReplayWithSidecar: %v", err)
	}

	check, err := runExtendedCheck(sidecar, report.Transitions)
	if err != nil {
		t.Fatalf("runExtendedCheck: %v", err)
	}

	// Lock in the expected shape vs the sidecar oracle.
	const (
		wantRecorded = 10
		wantMatches  = 10
	)
	if check.RecordedCount != wantRecorded {
		t.Errorf("recorded transitions: got %d, want %d", check.RecordedCount, wantRecorded)
	}
	if check.OrderedMatches != wantMatches {
		t.Errorf("ordered matches: got %d, want %d", check.OrderedMatches, wantMatches)
	}
	if len(check.OrderedMismatches) != 0 {
		t.Errorf("ordered mismatches: got %d, want 0 — %+v", len(check.OrderedMismatches), check.OrderedMismatches)
	}
	if len(check.MissingKinds) != 0 {
		t.Errorf("missing kinds: got %v, want none", check.MissingKinds)
	}
	if len(check.ExtraKinds) != 0 {
		t.Errorf("extra kinds: got %v, want none", check.ExtraKinds)
	}

	// Lock in the exact transition sequence. If someone changes the
	// classifier or debounce machinery so that a different set of
	// transitions is emitted — even if the counts happen to match —
	// this assertion catches it.
	wantSequence := []string{
		"→ready",          // init seed
		"ready→working",   // first user turn starts
		"working→ready",   // first turn done
		"ready→working",   // second turn starts (ls)
		"working→waiting", // user-blocking tool open
		"waiting→working", // tool resolved, work resumed
		"working→waiting", // assistant asked a question
		"waiting→working", // user answered
		"working→ready",   // second turn done
		"ready→working",   // post-session cleanup activity
		"working→ready",   // final ready
	}
	if got, want := len(report.Transitions), len(wantSequence); got != want {
		t.Fatalf("replay transitions: got %d, want %d", got, want)
	}
	for i, tr := range report.Transitions {
		got := tr.PrevState + "→" + tr.NewState
		if got != wantSequence[i] {
			t.Errorf("transition %d: got %q, want %q", i, got, wantSequence[i])
		}
	}
}

// TestReplayWithSidecar_RejectsMultiSessionSidecar ensures the fail-fast
// guard against sidecars containing more than one session_id works —
// curated fixtures are required to be single-session.
func TestReplayWithSidecar_RejectsMultiSessionSidecar(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "fake.jsonl")
	sidecar := filepath.Join(dir, "fake.events.jsonl")

	// Minimal transcript with a single line so ReadFile succeeds.
	if err := os.WriteFile(transcript, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	// Sidecar with two different session_ids.
	sidecarBody := `{"seq":1,"ts":"2026-04-11T10:00:00Z","kind":"transcript_activity","session_id":"sess-A","file_size":10}
{"seq":2,"ts":"2026-04-11T10:00:01Z","kind":"transcript_activity","session_id":"sess-B","file_size":20}
`
	if err := os.WriteFile(sidecar, []byte(sidecarBody), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	_, err := ReplayWithSidecar(transcript, sidecar, ReportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error for multi-session sidecar, got nil")
	}
}

// TestRunExtendedCheck_DetectsDrift proves the check actually reports
// mismatches when the replay diverges from the sidecar. Without this
// test, a bug in compareTransitions that always reports 0 mismatches
// would silently pass TestReplayWithSidecar_GoldenFixture forever (the
// check would claim "all 10 match" even when it didn't look at them).
//
// We construct a synthetic sidecar with a known state-transition sequence,
// then feed the check a replay that diverges at two positions and ends
// short. The check must flag exactly those differences.
func TestRunExtendedCheck_DetectsDrift(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "test.events.jsonl")

	// Recorded: 4 transitions, all for the same session.
	body := `{"seq":1,"ts":"2026-04-11T10:00:00Z","kind":"state_transition","session_id":"s","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-04-11T10:00:01Z","kind":"state_transition","session_id":"s","prev_state":"working","new_state":"waiting"}
{"seq":3,"ts":"2026-04-11T10:00:02Z","kind":"state_transition","session_id":"s","prev_state":"waiting","new_state":"working"}
{"seq":4,"ts":"2026-04-11T10:00:03Z","kind":"state_transition","session_id":"s","prev_state":"working","new_state":"ready"}
`
	if err := os.WriteFile(sidecar, []byte(body), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	// Replayed: diverges at index 1 (working→ready instead of working→waiting),
	// matches index 0, mismatches indices 2+3, and omits the final one.
	replayed := []Transition{
		{PrevState: "", NewState: "ready", Cause: CauseInit},       // init row, skipped by the check
		{PrevState: "ready", NewState: "working"},                  // match
		{PrevState: "working", NewState: "ready"},                  // mismatch: want working→waiting
		{PrevState: "waiting", NewState: "working"},                // note: replay skipped through ready, landing in waiting→working
	}

	check, err := runExtendedCheck(sidecar, replayed)
	if err != nil {
		t.Fatalf("runExtendedCheck: %v", err)
	}
	if check.RecordedCount != 4 {
		t.Errorf("recorded count: got %d, want 4", check.RecordedCount)
	}
	if check.ReplayedCount != 3 {
		t.Errorf("replayed count: got %d, want 3 (init row dropped)", check.ReplayedCount)
	}
	if len(check.OrderedMismatches) == 0 {
		t.Fatal("drift went undetected: OrderedMismatches is empty")
	}
	// We expect at least: one state_differs and one missing_in_replay.
	var sawStateDiffers, sawMissing bool
	for _, m := range check.OrderedMismatches {
		switch m.Kind {
		case "state_differs":
			sawStateDiffers = true
		case "missing_in_replay":
			sawMissing = true
		}
	}
	if !sawStateDiffers {
		t.Errorf("expected a state_differs mismatch, got %+v", check.OrderedMismatches)
	}
	if !sawMissing {
		t.Errorf("expected a missing_in_replay mismatch (replay is shorter), got %+v", check.OrderedMismatches)
	}
}

// TestReplay_GoldenFixture locks in the non-sidecar line-timestamp
// batching path — the existing 11 committed fixtures all go through
// this codepath and have no other regression oracle. If Replay()'s
// debounce, tailer, or classifier behavior drifts, we want a go test
// failure, not a silent pass that only the shell fixture script would
// catch.
//
// Uses fixture 07 because it's small (26 events) and exercises the
// tool-denial/ESC path — more interesting than a boilerplate session.
func TestReplay_GoldenFixture(t *testing.T) {
	src := fixturePath(t, "claudecode/07-tool-denial-and-esc-db57d2ab.jsonl")
	report, err := Replay(src, ReportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// Baseline captured from replay-session against the committed fixture.
	// If this drifts, something changed in Replay()'s debounce or the
	// detector logic it exercises — investigate before updating the numbers.
	const (
		wantTransitions = 6
		wantFlickers    = 1
	)
	if report.Summary.TotalTransitions != wantTransitions {
		t.Errorf("total transitions: got %d, want %d", report.Summary.TotalTransitions, wantTransitions)
	}
	if report.Summary.FlickerCount != wantFlickers {
		t.Errorf("flicker count: got %d, want %d", report.Summary.FlickerCount, wantFlickers)
	}

	wantSequence := []string{
		"→ready",
		"ready→working",
		"working→ready",
		"ready→working",
		"working→ready",
		"ready→working",
	}
	if got, want := len(report.Transitions), len(wantSequence); got != want {
		t.Fatalf("replay transitions: got %d, want %d", got, want)
	}
	for i, tr := range report.Transitions {
		got := tr.PrevState + "→" + tr.NewState
		if got != wantSequence[i] {
			t.Errorf("transition %d: got %q, want %q", i, got, wantSequence[i])
		}
	}

	// No sidecar path, so extended_check should stay nil.
	if report.ExtendedCheck != nil {
		t.Errorf("non-sidecar replay produced ExtendedCheck: %+v", report.ExtendedCheck)
	}
}

// TestDetectAdapter exercises the path-based adapter inference over
// the canonical session-storage roots and the repo-local fixture layout.
func TestDetectAdapter(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"claude code session root", "/Users/u/.claude/projects/-Users-u-proj/abc.jsonl", "claude-code"},
		{"claude code testdata", "testdata/replay/claudecode/07.jsonl", "claude-code"},
		{"codex session root", "/Users/u/.codex/sessions/2026/04/01/sess.jsonl", "codex"},
		{"codex testdata", "testdata/replay/codex/01.jsonl", "codex"},
		{"pi agent sessions", "/Users/u/.pi/agent/sessions/s.jsonl", "pi"},
		{"pi sessions", "/Users/u/.pi/sessions/s.jsonl", "pi"},
		{"pi testdata", "testdata/replay/pi/01.jsonl", "pi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := detectAdapter(c.path)
			if err != nil {
				t.Fatalf("detectAdapter(%q): %v", c.path, err)
			}
			if got != c.want {
				t.Errorf("detectAdapter(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}

	t.Run("unknown path returns error", func(t *testing.T) {
		if _, err := detectAdapter("/tmp/random.jsonl"); err == nil {
			t.Error("expected error for unrecognized path, got nil")
		}
	})
}

// TestLoadAllLifecycleEvents_SkipsMalformedLines verifies that a sidecar
// with a garbage line in the middle still loads the valid events before
// and after it. We expect stderr output for the bad line but no fatal
// error (the replay should keep going rather than aborting mid-session).
func TestLoadAllLifecycleEvents_SkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mixed.events.jsonl")
	body := `{"seq":1,"ts":"2026-04-11T10:00:00Z","kind":"transcript_new","session_id":"s"}
not json at all
{"seq":2,"ts":"2026-04-11T10:00:01Z","kind":"transcript_activity","session_id":"s","file_size":100}
{malformed:
{"seq":3,"ts":"2026-04-11T10:00:02Z","kind":"process_exited","session_id":"s"}
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	events, err := loadAllLifecycleEvents(path)
	if err != nil {
		t.Fatalf("loadAllLifecycleEvents: %v", err)
	}
	if got, want := len(events), 3; got != want {
		t.Errorf("events: got %d, want %d (2 malformed lines should be skipped)", got, want)
	}
	// Sequence numbers should still be in order.
	for i := range events {
		if events[i].Seq != int64(i+1) {
			t.Errorf("event[%d].Seq = %d, want %d", i, events[i].Seq, i+1)
		}
	}
}
