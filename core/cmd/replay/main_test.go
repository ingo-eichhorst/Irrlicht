package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// fixturePath returns an absolute path to a fixture under the repo-root
// testdata/replay/<adapter>/ tree. The test binary runs from the package
// directory (core/cmd/replay), so we walk up three parents.
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
// match the expected lifecycle.
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

	wantSequence := []string{
		"→ready",
		"ready→working",
		"working→ready",
		"ready→working",
		"working→waiting",
		"waiting→working",
		"working→waiting",
		"waiting→working",
		"working→ready",
		"ready→working",
		"working→ready",
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

	if len(report.Sessions) == 0 {
		t.Error("sidecar replay should populate Sessions, got empty")
	}
}

// TestReplayWithSidecar_ContinueFixture is the regression test for issue
// #144. Fixture 13 is a /continue session whose session ID spans two
// daemon process lifetimes: process_exited at seq 673 (20:56:47), then
// lifecycle restart at seq 715 (21:11:40.970), then process_exited at
// seq 728. Before the fix, the single captured processExitAt silenced
// legitimate lifetime-2 debounce fires and let a gap fs event at
// seq 714 (21:11:40.931, before the lifecycle restart) drive the first
// lifetime-2 transition — a ghost moment when no daemon was attached.
//
// After the fix, no transition falls inside the process-exit gap and
// lifetime 2 replays exactly the four transitions the daemon recorded
// (seq 720/722/724/725) at their recorded wall-clock timestamps.
//
// Lifetime 1 still has spurious flicker extras from a separate detector
// feature the replay does not yet model (parent-hold while subagents
// are active, subagent-completion-driven parent re-evaluation). Those
// are out of scope for this issue; this test deliberately does not
// assert on them.
func TestReplayWithSidecar_ContinueFixture(t *testing.T) {
	transcript := fixturePath(t, "claudecode/13-full-lifecycle-continue-8a525d27.jsonl")
	sidecar := fixturePath(t, "claudecode/13-full-lifecycle-continue-8a525d27.events.jsonl")

	report, err := ReplayWithSidecar(transcript, sidecar, ReportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ReplayWithSidecar: %v", err)
	}

	// No replayed transition may fall inside the process-exit gap
	// (between lifetime-1 exit at 20:56:47.276 and lifetime-2 restart
	// at 21:11:40.970) — no daemon was attached then.
	gapStart := mustParseRFC3339(t, "2026-04-11T20:56:47.276869+02:00")
	gapEnd := mustParseRFC3339(t, "2026-04-11T21:11:40.970771+02:00")
	for _, tr := range report.Transitions {
		if tr.VirtualTime.After(gapStart) && tr.VirtualTime.Before(gapEnd) {
			t.Errorf("ghost transition inside process-exit gap: idx=%d %s→%s at %s",
				tr.Index, tr.PrevState, tr.NewState,
				tr.VirtualTime.Format(time.RFC3339Nano))
		}
	}

	// Lifetime 2 must produce exactly the four recorded transitions, at
	// the four lifetime-2 fs-event timestamps (seq 719/721/723/726).
	// Each fires slightly before the recorded state_transition (seq
	// 720/722/724/725) because the daemon emits the state_transition
	// after classify completes — microsecond skew, not a semantic
	// difference.
	lifetime2Want := []struct {
		ts        string
		prevState string
		newState  string
	}{
		{"2026-04-11T21:11:45.046448+02:00", "ready", "working"},
		{"2026-04-11T21:11:47.76431+02:00", "working", "ready"},
		{"2026-04-11T21:11:50.310406+02:00", "ready", "working"},
		{"2026-04-11T21:11:50.310406+02:00", "working", "ready"},
	}
	var lifetime2Got []Transition
	for _, tr := range report.Transitions {
		if tr.VirtualTime.After(gapEnd) {
			lifetime2Got = append(lifetime2Got, tr)
		}
	}
	if len(lifetime2Got) != len(lifetime2Want) {
		t.Fatalf("lifetime 2 transitions: got %d, want %d — %+v",
			len(lifetime2Got), len(lifetime2Want), lifetime2Got)
	}
	for i, w := range lifetime2Want {
		wantTime := mustParseRFC3339(t, w.ts)
		got := lifetime2Got[i]
		if !got.VirtualTime.Equal(wantTime) {
			t.Errorf("lifetime 2 transition[%d] time: got %s, want %s",
				i, got.VirtualTime.Format(time.RFC3339Nano),
				wantTime.Format(time.RFC3339Nano))
		}
		if got.PrevState != w.prevState || got.NewState != w.newState {
			t.Errorf("lifetime 2 transition[%d] states: got %s→%s, want %s→%s",
				i, got.PrevState, got.NewState, w.prevState, w.newState)
		}
	}

	// The 10 recorded transitions must still be seen and the first 10
	// replayed transitions must ordered-match them by state.
	check, err := runExtendedCheck(sidecar, report.Transitions)
	if err != nil {
		t.Fatalf("runExtendedCheck: %v", err)
	}
	if check.RecordedCount != 10 {
		t.Errorf("recorded transitions: got %d, want 10", check.RecordedCount)
	}
	if check.OrderedMatches != 10 {
		t.Errorf("ordered matches: got %d, want 10", check.OrderedMatches)
	}
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// TestReplayWithSidecar_NoTranscriptNew verifies that a sidecar with no
// transcript_new events (and thus no identifiable primary session) errors.
func TestReplayWithSidecar_NoTranscriptNew(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "fake.jsonl")
	sidecar := filepath.Join(dir, "fake.events.jsonl")

	if err := os.WriteFile(transcript, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
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
		t.Fatal("expected error for sidecar with no transcript_new, got nil")
	}
}

// TestRunExtendedCheck_DetectsDrift proves the check actually reports
// mismatches when the replay diverges from the sidecar.
func TestRunExtendedCheck_DetectsDrift(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "test.events.jsonl")

	body := `{"seq":1,"ts":"2026-04-11T10:00:00Z","kind":"state_transition","session_id":"s","prev_state":"ready","new_state":"working"}
{"seq":2,"ts":"2026-04-11T10:00:01Z","kind":"state_transition","session_id":"s","prev_state":"working","new_state":"waiting"}
{"seq":3,"ts":"2026-04-11T10:00:02Z","kind":"state_transition","session_id":"s","prev_state":"waiting","new_state":"working"}
{"seq":4,"ts":"2026-04-11T10:00:03Z","kind":"state_transition","session_id":"s","prev_state":"working","new_state":"ready"}
`
	if err := os.WriteFile(sidecar, []byte(body), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	replayed := []Transition{
		{PrevState: "", NewState: "ready", Cause: CauseInit},
		{PrevState: "ready", NewState: "working"},
		{PrevState: "working", NewState: "ready"},
		{PrevState: "waiting", NewState: "working"},
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

// TestReplay_GoldenFixture locks in the non-sidecar line-timestamp batching path.
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

	// Updated for #108: tool denial now triggers working→ready, adding 2
	// transitions but keeping flicker count at 1.
	const (
		wantTransitions = 8
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
		"working→ready",   // tool denial → ready
		"ready→working",   // agent continues
		"working→ready",   // agent finished turn
		"ready→working",   // next turn
		"working→ready",   // ESC interrupt → ready
		"ready→working",   // activity after ESC
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

	if report.ExtendedCheck != nil {
		t.Errorf("non-sidecar replay produced ExtendedCheck: %+v", report.ExtendedCheck)
	}
	if report.Sessions != nil {
		t.Errorf("non-sidecar replay produced Sessions: %+v", report.Sessions)
	}
}

// TestReplay_Issue150_AskUserQuestion is the regression test for issue #150.
// The 7b1f6cf4 session contains 6 AskUserQuestion tool_use events. Before
// the fix, 2 of them collapsed into a single batch with their tool_result
// (a debounce-window coincidence) and the brief working→waiting episode
// was never emitted — the session went straight working→ready on denial.
// After the fix, every AskUserQuestion pair must be represented by a
// waiting episode (natural "user-blocking tool open → waiting" on the
// tool_use, or synthetic on same-pass collapse).
func TestReplay_Issue150_AskUserQuestion(t *testing.T) {
	src := fixturePath(t, "claudecode/16-ask-user-question-issue-150.jsonl")
	report, err := Replay(src, ReportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	var naturalWaiting, syntheticWaiting int
	for _, tr := range report.Transitions {
		if tr.PrevState != session.StateWorking || tr.NewState != session.StateWaiting {
			continue
		}
		switch tr.Reason {
		case "user-blocking tool open → waiting":
			naturalWaiting++
		case services.SyntheticWaitingReason:
			syntheticWaiting++
		}
	}

	const wantAskUserQuestionCount = 6
	got := naturalWaiting + syntheticWaiting
	if got != wantAskUserQuestionCount {
		t.Errorf("waiting episodes for AskUserQuestion: got %d (natural=%d, synthetic=%d), want %d",
			got, naturalWaiting, syntheticWaiting, wantAskUserQuestionCount)
	}
	// At least one synthetic must fire — the fixture was chosen because it
	// contains a same-pass collapse that triggers the fix path. If future
	// parser changes eliminate the collapse, this guard flags that the
	// fixture no longer exercises issue #150.
	if syntheticWaiting == 0 {
		t.Error("expected at least one synthetic waiting transition; fixture may no longer exercise same-pass collapse")
	}
}

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
	for i := range events {
		if events[i].Seq != int64(i+1) {
			t.Errorf("event[%d].Seq = %d, want %d", i, events[i].Seq, i+1)
		}
	}
}

// TestReplayWithSidecar_HookEvents verifies that hook_received events in
// the sidecar produce working→waiting transitions during replay.
func TestReplayWithSidecar_HookEvents(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")
	sidecar := filepath.Join(dir, "session.events.jsonl")

	transcriptBody := `{"type":"user","timestamp":"2026-04-11T10:00:00Z","message":{"role":"user","content":"hello"}}
{"type":"assistant","timestamp":"2026-04-11T10:00:01Z","message":{"role":"assistant","content":"Let me check."}}
`
	if err := os.WriteFile(transcript, []byte(transcriptBody), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	sidecarBody := `{"seq":1,"ts":"2026-04-11T10:00:00Z","kind":"transcript_new","session_id":"sess-1","adapter":"claude-code"}
{"seq":2,"ts":"2026-04-11T10:00:00.500Z","kind":"transcript_activity","session_id":"sess-1","file_size":93}
{"seq":3,"ts":"2026-04-11T10:00:01Z","kind":"transcript_activity","session_id":"sess-1","file_size":192}
{"seq":4,"ts":"2026-04-11T10:00:01.500Z","kind":"hook_received","session_id":"sess-1","hook_name":"PreToolUse"}
`
	if err := os.WriteFile(sidecar, []byte(sidecarBody), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	report, err := ReplayWithSidecar(transcript, sidecar, ReportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ReplayWithSidecar: %v", err)
	}

	var foundHookWaiting bool
	for _, tr := range report.Transitions {
		if tr.Cause == CauseHook && tr.NewState == "waiting" {
			foundHookWaiting = true
			break
		}
	}
	if !foundHookWaiting {
		t.Errorf("expected a hook-caused working→waiting transition; transitions: %+v", report.Transitions)
	}
}

// TestSessionFilter verifies that SessionFilter in ReportSettings filters
// sidecar events to the specified session ID.
func TestSessionFilter(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")
	sidecar := filepath.Join(dir, "session.events.jsonl")

	transcriptBody := `{"type":"user","timestamp":"2026-04-11T10:00:00Z","message":{"role":"user","content":"hi"}}
`
	if err := os.WriteFile(transcript, []byte(transcriptBody), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	sidecarBody := `{"seq":1,"ts":"2026-04-11T10:00:00Z","kind":"transcript_new","session_id":"sess-A","adapter":"claude-code"}
{"seq":2,"ts":"2026-04-11T10:00:00Z","kind":"transcript_new","session_id":"sess-B","adapter":"claude-code"}
{"seq":3,"ts":"2026-04-11T10:00:00.500Z","kind":"transcript_activity","session_id":"sess-A","file_size":90}
{"seq":4,"ts":"2026-04-11T10:00:00.500Z","kind":"transcript_activity","session_id":"sess-B","file_size":90}
`
	if err := os.WriteFile(sidecar, []byte(sidecarBody), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	report, err := ReplayWithSidecar(transcript, sidecar, ReportSettings{
		Adapter:            claudecode.AdapterName,
		SessionFilter:      "sess-B",
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ReplayWithSidecar with session filter: %v", err)
	}

	if len(report.Transitions) == 0 {
		t.Error("expected at least init transition")
	}
}

// TestReplayWithSidecar_SessionFilterNoBirthMarker guards against a
// regression from the #144 fix: when --session targets a session that
// has fs events but no transcript_new and no session-creation
// state_transition in the sidecar (e.g. a subagent whose birth marker
// belongs to the parent), the alive-gate must not silently drop every
// fs event. Absent any lifecycle-start marker, replay should treat the
// sidecar as a single open lifetime.
func TestReplayWithSidecar_SessionFilterNoBirthMarker(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")
	sidecar := filepath.Join(dir, "session.events.jsonl")

	transcriptBody := `{"type":"user","timestamp":"2026-04-11T10:00:00Z","message":{"role":"user","content":"hi"}}
{"type":"assistant","timestamp":"2026-04-11T10:00:01Z","message":{"role":"assistant","content":"hello"}}
`
	if err := os.WriteFile(transcript, []byte(transcriptBody), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	// Parent session sess-A has the transcript_new; sess-B (our target)
	// has only fs events — no birth marker of its own.
	sidecarBody := `{"seq":1,"ts":"2026-04-11T10:00:00Z","kind":"transcript_new","session_id":"sess-A","adapter":"claude-code"}
{"seq":2,"ts":"2026-04-11T10:00:00.500Z","kind":"transcript_activity","session_id":"sess-B","file_size":93}
{"seq":3,"ts":"2026-04-11T10:00:01Z","kind":"transcript_activity","session_id":"sess-B","file_size":192}
`
	if err := os.WriteFile(sidecar, []byte(sidecarBody), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	report, err := ReplayWithSidecar(transcript, sidecar, ReportSettings{
		Adapter:            claudecode.AdapterName,
		SessionFilter:      "sess-B",
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ReplayWithSidecar with session filter: %v", err)
	}

	// A ready→working must fire off the first fs event; the alive-gate
	// would have suppressed it before the fallback.
	var sawReadyToWorking bool
	for _, tr := range report.Transitions {
		if tr.PrevState == "ready" && tr.NewState == "working" {
			sawReadyToWorking = true
			break
		}
	}
	if !sawReadyToWorking {
		t.Errorf("expected ready→working transition; got transitions: %+v", report.Transitions)
	}
}
