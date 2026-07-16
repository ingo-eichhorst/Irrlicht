package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// fixturePath returns an absolute path to a recording artifact under the
// repo-root replaydata/agents/<adapter>/<subtree>/<scenario>/recordings/<name>/
// tree. The test binary runs from the package directory (tools/onboarding-factory/cmd/replay), so
// we walk up three parents.
//
// Callers pass "<adapter>/<scenario>/<basename>" — e.g.
// "claudecode/basic-turn/transcript.jsonl". The subtree segment (scenarios/ or
// regression/) is auto-detected: scenarios/ first, then regression/. Every
// recording lives under recordings/<name>/, so the basename is resolved inside
// the NEWEST recording (lexicographically-greatest name). If nothing matches,
// the scenarios/ recordings path is returned so the caller surfaces a clear
// "no such file" error pointing at the expected location.
func fixturePath(t *testing.T, rel string) string {
	t.Helper()
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) != 3 {
		// No scenario/basename split — treat rel as a literal agents-relative path.
		return mustAbs(t, filepath.Join("..", "..", "..", "..", "replaydata", "agents", rel))
	}
	adapter, scenario, base := parts[0], parts[1], parts[2]
	for _, subtree := range []string{"scenarios", "regressions"} {
		if cand, ok := findNewestFixture(subtree, adapter, scenario, base); ok {
			return mustAbs(t, cand)
		}
	}
	// Nothing found — return a scenarios/ recordings path for a clear error.
	return mustAbs(t, filepath.Join("..", "..", "..", "..", "replaydata", "agents", adapter, "scenarios", scenario, "recordings", base))
}

// mustAbs resolves path to an absolute path, failing the test on error.
func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs fixture path: %v", err)
	}
	return abs
}

// findNewestFixture looks for base inside the newest (lexicographically-
// greatest name) recordings/<name>/ directory under
// replaydata/agents/<adapter>/<subtree>/<scenario>/recordings/, returning
// (path, false) when the subtree or a matching recording doesn't exist.
func findNewestFixture(subtree, adapter, scenario, base string) (string, bool) {
	cellDir := filepath.Join("..", "..", "..", "..", "replaydata", "agents", adapter, subtree, scenario)
	recsDir := filepath.Join(cellDir, "recordings")
	entries, err := os.ReadDir(recsDir)
	if err != nil {
		return "", false
	}
	// Newest-first by name (timestamp-prefixed → chronological).
	names := recordingDirNames(entries)
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names {
		cand := filepath.Join(recsDir, name, base)
		if _, err := os.Stat(cand); err == nil {
			return cand, true
		}
	}
	return "", false
}

// recordingDirNames returns the directory names among entries (recording
// folders live directly under recordings/; any plain file there is ignored).
func recordingDirNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// TestReplayWithSidecar_GoldenFixture locks in the regression oracle: the
// committed 10-full-lifecycle-839f0678 fixture must replay to the exact
// set of state transitions the daemon recorded in the sidecar, with no
// ordered-diff mismatches, AND the replay's own transition sequence must
// match the expected lifecycle.
func TestReplayWithSidecar_GoldenFixture(t *testing.T) {
	transcript := fixturePath(t, "claudecode/10-full-lifecycle-839f0678/transcript.jsonl")
	sidecar := fixturePath(t, "claudecode/10-full-lifecycle-839f0678/events.jsonl")

	report, err := replayWithSidecar(transcript, sidecar, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replayWithSidecar: %v", err)
	}

	check, err := runExtendedCheck(sidecar, report.Transitions)
	if err != nil {
		t.Fatalf("runExtendedCheck: %v", err)
	}

	// The sidecar (recorded by a pre-#329 daemon) ends with a spurious
	// `ready→working→ready` pair at seq 297/298 — 1ms apart, the classic
	// away_summary-class flicker. With the #329 fix, processActivity
	// short-circuits skip-only passes and no longer emits that pair, so
	// OrderedMatches < RecordedCount is expected. The first 8 transitions
	// still match the daemon recording exactly.
	const (
		wantRecorded = 10
		wantMatches  = 8
	)
	if check.RecordedCount != wantRecorded {
		t.Errorf("recorded transitions: got %d, want %d", check.RecordedCount, wantRecorded)
	}
	if check.OrderedMatches != wantMatches {
		t.Errorf("ordered matches: got %d, want %d", check.OrderedMatches, wantMatches)
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
	transcript := fixturePath(t, "claudecode/13-full-lifecycle-continue-8a525d27/transcript.jsonl")
	sidecar := fixturePath(t, "claudecode/13-full-lifecycle-continue-8a525d27/events.jsonl")

	report, err := replayWithSidecar(transcript, sidecar, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replayWithSidecar: %v", err)
	}

	// No replayed transition may fall inside the process-exit gap
	// (between lifetime-1 exit at 20:56:47.276 and lifetime-2 restart
	// at 21:11:40.970) — no daemon was attached then.
	gapStart := mustParseRFC3339(t, "2026-04-11T20:56:47.276869+02:00")
	gapEnd := mustParseRFC3339(t, "2026-04-11T21:11:40.970771+02:00")
	assertNoTransitionsInGap(t, report.Transitions, gapStart, gapEnd)

	// Lifetime 2's recorded sequence (pre-#329 daemon) ends with a
	// spurious ready→working→ready pair at the same timestamp
	// (21:11:50.310406) — the same skip-only-pass flicker the #329 fix
	// eliminates. Post-fix the replay produces only the first two
	// lifetime-2 transitions; the recorded pair at seq 724/725 is gone.
	lifetime2Want := []wantTransition{
		{"2026-04-11T21:11:45.046448+02:00", session.StateReady, session.StateWorking},
		{"2026-04-11T21:11:47.76431+02:00", session.StateWorking, session.StateReady},
	}
	assertLifetime2Transitions(t, report.Transitions, gapEnd, lifetime2Want)

	// The sidecar still has 10 recorded transitions (pre-#329 daemon).
	// Two effects shape OrderedMatches:
	//   * #329 fix dropped same-timestamp ready→working→ready flicker pairs
	//     in both lifetimes.
	//   * #381 fix widened IsWaitingForUserInput to catch imperative cues,
	//     so the first foreground-wave wrap-up — "All waves complete (6
	//     foreground subagents). Check the UI for the full picture." — now
	//     classifies as working→waiting (cue: verb+determiner "Check the
	//     UI") where the pre-fix daemon recorded working→ready. The
	//     classifier-emitted waiting episode and its subsequent unwind
	//     shift index alignment, so only the very first (ready→working)
	//     transition still lines up by index against the recording.
	check, err := runExtendedCheck(sidecar, report.Transitions)
	if err != nil {
		t.Fatalf("runExtendedCheck: %v", err)
	}
	if check.RecordedCount != 10 {
		t.Errorf("recorded transitions: got %d, want 10", check.RecordedCount)
	}
	if check.OrderedMatches != 1 {
		t.Errorf("ordered matches: got %d, want 1", check.OrderedMatches)
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

// assertNoTransitionsInGap fails the test if any replayed transition's
// virtual time falls strictly inside (gapStart, gapEnd) — the process-exit
// gap where no daemon was attached, so no transition should have been
// classified there (issue #144).
func assertNoTransitionsInGap(t *testing.T, transitions []transition, gapStart, gapEnd time.Time) {
	t.Helper()
	for _, tr := range transitions {
		if tr.VirtualTime.After(gapStart) && tr.VirtualTime.Before(gapEnd) {
			t.Errorf("ghost transition inside process-exit gap: idx=%d %s→%s at %s",
				tr.Index, tr.PrevState, tr.NewState,
				tr.VirtualTime.Format(time.RFC3339Nano))
		}
	}
}

// wantTransition is one expected (timestamp, prevState, newState) triple
// asserted by assertLifetime2Transitions.
type wantTransition struct {
	ts        string
	prevState string
	newState  string
}

// assertLifetime2Transitions collects the replayed transitions after gapEnd
// (lifetime 2) and checks them against want, by index, on both timestamp and
// state pair.
func assertLifetime2Transitions(t *testing.T, transitions []transition, gapEnd time.Time, want []wantTransition) {
	t.Helper()
	var got []transition
	for _, tr := range transitions {
		if tr.VirtualTime.After(gapEnd) {
			got = append(got, tr)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("lifetime 2 transitions: got %d, want %d — %+v", len(got), len(want), got)
	}
	for i, w := range want {
		wantTime := mustParseRFC3339(t, w.ts)
		g := got[i]
		if !g.VirtualTime.Equal(wantTime) {
			t.Errorf("lifetime 2 transition[%d] time: got %s, want %s",
				i, g.VirtualTime.Format(time.RFC3339Nano),
				wantTime.Format(time.RFC3339Nano))
		}
		if g.PrevState != w.prevState || g.NewState != w.newState {
			t.Errorf("lifetime 2 transition[%d] states: got %s→%s, want %s→%s",
				i, g.PrevState, g.NewState, w.prevState, w.newState)
		}
	}
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

	_, err := replayWithSidecar(transcript, sidecar, reportSettings{
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

	replayed := []transition{
		{PrevState: "", NewState: "ready", Cause: causeInit},
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
	src := fixturePath(t, "claudecode/07-tool-denial-and-esc-db57d2ab/transcript.jsonl")
	report, err := replay(src, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
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
		"working→ready", // tool denial → ready
		"ready→working", // agent continues
		"working→ready", // agent finished turn
		"ready→working", // next turn
		"working→ready", // ESC interrupt → ready
		"ready→working", // activity after ESC
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
	src := fixturePath(t, "claudecode/16-ask-user-question-issue-150/transcript.jsonl")
	report, err := replay(src, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
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

// TestReplay_Issue1138_QuestionMarkerWaiting is the end-to-end regression for
// issue #1138, reproducing the real 71f27332 session: a turn ends asking the
// user a question, but the visible question sits EARLY in a long final message
// while the tail (which is all LastAssistantText keeps — 200 runes) is a
// declarative sentence plus the hidden irrlicht-question marker. Before the fix
// the prose heuristic saw no question in the tail and the session went straight
// to ready; after it, the parsed marker (PendingQuestionMarker) routes the
// finished turn to waiting.
//
// Modeled inline rather than as a replaydata cell on purpose: the fix lives in
// the tailer→domain conversion + classifier, and a full recording cell (manifest
// + events.jsonl goldens + `of validate`) would be disproportionate ceremony for
// a pure state-classification guard.
func TestReplay_Issue1138_QuestionMarkerWaiting(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	// Assistant turn: the real question ("Want me to run the spike now?") is far
	// enough from the end that it falls outside the 200-rune LastAssistantText
	// tail; the tail is the declarative sentence + the marker comment (whose own
	// trailing '?' is inside `"} -->` and is not a catchable sentence question).
	transcriptBody := `{"type":"user","timestamp":"2026-04-11T10:00:00Z","message":{"role":"user","content":"Should we run the OTel blocked_on_user spike?"}}
{"type":"assistant","timestamp":"2026-04-11T10:00:01Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Here is my read on the OTel blocked_on_user spike, weighed against the recommendation already written up in the design doc. Want me to run the spike now? I can stand up the throwaway OTLP collector sink locally and drive a real claudecode session through a permission prompt via tmux to capture the blocked_on_user payload end to end and measure its wall-clock latency.\n\n<!-- {\"marker\":\"irrlicht-question\",\"question\":\"Run the OTel blocked_on_user spike now, or just keep the recommendation?\"} -->"}]}}
{"type":"system","subtype":"turn_duration","timestamp":"2026-04-11T10:00:02Z"}
`
	if err := os.WriteFile(transcript, []byte(transcriptBody), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	report, err := replay(transcript, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	waitingIdx := -1
	for i := range report.Transitions {
		if report.Transitions[i].NewState == session.StateWaiting {
			waitingIdx = i
		}
	}
	if waitingIdx < 0 {
		t.Fatalf("no transition to waiting; the finished question turn was not detected as waiting. transitions: %+v", report.Transitions)
	}
	waiting := report.Transitions[waitingIdx]
	// The transcript has no open/blocking tool, so the ONLY route to waiting is
	// classifyAgentDone's question path — and with a declarative tail, the prose
	// heuristic sees no question, so the parsed irrlicht-question marker is what
	// flips it. Assert that exact route: turn done + the question-waiting reason.
	if !waiting.IsAgentDone {
		t.Errorf("waiting transition IsAgentDone = false, want true (should be the turn-done question route)")
	}
	const wantReason = "turn ended with question or cue → waiting"
	if waiting.Reason != wantReason {
		t.Errorf("waiting transition reason = %q, want %q (the agent-done question route, which only fires here via the marker)", waiting.Reason, wantReason)
	}
	if waiting.NeedsAttn {
		t.Errorf("waiting transition NeedsAttn = true, want false — reached waiting via a blocking tool, not the question marker")
	}
}

// TestReplay_Issue1150_WaitingCueBeyondTailWaiting is the end-to-end regression
// for issue #1150, the cue analogue of TestReplay_Issue1138_QuestionMarkerWaiting:
// a turn ends with an imperative waiting cue (no marker, no literal question)
// that sits EARLY in a long final message, while the tail (all LastAssistantText
// keeps — 200 runes) is a declarative padding sentence. Before the fix the prose
// heuristics saw only the truncated tail and the session went straight to ready;
// after it, the adapter parser scans the FULL assistant text, derives
// PendingWaitingCue, and the finished turn routes to waiting via the same
// turn-done cue path #1138 uses for the marker.
//
// Modeled inline rather than as a replaydata cell for the same reason #1138 is:
// the fix lives in the parser + tailer→domain conversion + classifier, and a
// full recording cell would be disproportionate ceremony for a state-classifier
// guard.
func TestReplay_Issue1150_WaitingCueBeyondTailWaiting(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	// Assistant turn: the cue ("Please review the diff and let me know before I
	// merge.") is the FIRST sentence, followed by a single ~290-rune declarative
	// sentence so the trailing 200 runes (all TruncateAssistantText keeps) fall
	// entirely inside the padding — the cue is outside the tail window. No
	// irrlicht-question marker, so only the full-text cue scan can flip it.
	transcriptBody := `{"type":"user","timestamp":"2026-04-11T10:00:00Z","message":{"role":"user","content":"Is the waiting-cue fix ready?"}}
{"type":"assistant","timestamp":"2026-04-11T10:00:01Z","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Please review the diff and let me know before I merge. I have already wired the full-text scan through the parser and the tailer and the ledger and the shared conversion so the classifier now receives an accurate signal on every pass instead of relying on the trailing fragment that used to hide this earlier sentence from the heuristics entirely."}]}}
{"type":"system","subtype":"turn_duration","timestamp":"2026-04-11T10:00:02Z"}
`
	if err := os.WriteFile(transcript, []byte(transcriptBody), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	report, err := replay(transcript, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	waitingIdx := -1
	for i := range report.Transitions {
		if report.Transitions[i].NewState == session.StateWaiting {
			waitingIdx = i
		}
	}
	if waitingIdx < 0 {
		t.Fatalf("no transition to waiting; the finished cue turn was not detected as waiting. transitions: %+v", report.Transitions)
	}
	waiting := report.Transitions[waitingIdx]
	// No open/blocking tool, and the cue sits outside the 200-rune tail, so the
	// ONLY route to waiting is classifyAgentDone's cue path fed by the full-text
	// PendingWaitingCue flag. Assert that exact route.
	if !waiting.IsAgentDone {
		t.Errorf("waiting transition IsAgentDone = false, want true (should be the turn-done cue route)")
	}
	const wantReason = "turn ended with question or cue → waiting"
	if waiting.Reason != wantReason {
		t.Errorf("waiting transition reason = %q, want %q (the agent-done cue route, which only fires here via the full-text scan)", waiting.Reason, wantReason)
	}
	if waiting.NeedsAttn {
		t.Errorf("waiting transition NeedsAttn = true, want false — reached waiting via a cue, not a blocking tool")
	}
}

// TestReplay_Issue1159_CodexWaitingCueBeyondTailWaiting is the codex analogue of
// TestReplay_Issue1150_WaitingCueBeyondTailWaiting (issue #1159): the beyond-tail
// waiting-cue fix that #1150 shipped for the claudecode adapter, applied to codex.
// A codex turn ends with an imperative waiting cue (no marker, no literal
// question) that sits EARLY in a long final message, while the trailing 200 runes
// (all LastAssistantText keeps) are declarative padding. Before the codex parser
// change the prose heuristics saw only the truncated tail and the finished turn
// went straight to ready; after it, parseCodexMessage scans the FULL assistant
// text, derives PendingWaitingCue, and the finished turn routes to waiting via the
// same turn-done cue path the claudecode test exercises.
//
// It also pins the codex-specific gate: codex emits a preliminary
// assistant_message before tools and settles only on the turn_done primary path
// (task_complete), so the cue routes to waiting from the LAST assistant text
// before turn_done — a mid-turn assistant_message cannot false-fire because
// IsAgentDone() excludes assistant_message from its fallback.
func TestReplay_Issue1159_CodexWaitingCueBeyondTailWaiting(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.jsonl")

	// Codex transcript shape: response_item/message payloads plus an event_msg
	// task_complete that the codex parser maps to turn_done. The cue ("Please
	// review the diff and let me know before I merge.") is the FIRST sentence,
	// followed by a single ~290-rune declarative sentence so the trailing 200
	// runes fall entirely inside the padding — the cue is outside the tail
	// window. No irrlicht-question marker, so only the full-text cue scan can
	// flip it.
	transcriptBody := `{"timestamp":"2026-04-11T10:00:00.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Is the codex waiting-cue fix ready?"}]}}
{"timestamp":"2026-04-11T10:00:01.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Please review the diff and let me know before I merge. I have already wired the full-text scan through the parser and the tailer and the ledger and the shared conversion so the classifier now receives an accurate signal on every pass instead of relying on the trailing fragment that used to hide this earlier sentence from the heuristics entirely."}],"phase":"final_answer"}}
{"timestamp":"2026-04-11T10:00:02.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","last_agent_message":"done","completed_at":1777195742,"duration_ms":1000}}
`
	if err := os.WriteFile(transcript, []byte(transcriptBody), 0644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	report, err := replay(transcript, reportSettings{
		Adapter:            codex.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	waitingIdx := -1
	for i := range report.Transitions {
		if report.Transitions[i].NewState == session.StateWaiting {
			waitingIdx = i
		}
	}
	if waitingIdx < 0 {
		t.Fatalf("no transition to waiting; the finished codex cue turn was not detected as waiting. transitions: %+v", report.Transitions)
	}
	waiting := report.Transitions[waitingIdx]
	// No open/blocking tool, and the cue sits outside the 200-rune tail, so the
	// ONLY route to waiting is classifyAgentDone's cue path fed by the full-text
	// PendingWaitingCue flag. Assert that exact route.
	if !waiting.IsAgentDone {
		t.Errorf("waiting transition IsAgentDone = false, want true (should be the turn-done cue route)")
	}
	const wantReason = "turn ended with question or cue → waiting"
	if waiting.Reason != wantReason {
		t.Errorf("waiting transition reason = %q, want %q (the agent-done cue route, which only fires here via the full-text scan)", waiting.Reason, wantReason)
	}
	if waiting.NeedsAttn {
		t.Errorf("waiting transition NeedsAttn = true, want false — reached waiting via a cue, not a blocking tool")
	}
}

func TestDetectAdapter(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"claude code session root", "/Users/u/.claude/projects/-Users-u-proj/abc.jsonl", "claude-code"},
		{"claude code replaydata", "replaydata/agents/claudecode/scenarios/07/transcript.jsonl", "claude-code"},
		{"codex session root", "/Users/u/.codex/sessions/2026/04/01/sess.jsonl", "codex"},
		{"codex replaydata", "replaydata/agents/codex/scenarios/01/transcript.jsonl", "codex"},
		{"pi agent sessions", "/Users/u/.pi/agent/sessions/s.jsonl", "pi"},
		{"pi sessions", "/Users/u/.pi/sessions/s.jsonl", "pi"},
		{"pi replaydata", "replaydata/agents/pi/scenarios/01/transcript.jsonl", "pi"},
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
{"seq":4,"ts":"2026-04-11T10:00:01.500Z","kind":"hook_received","session_id":"sess-1","hook_name":"PermissionRequest"}
`
	if err := os.WriteFile(sidecar, []byte(sidecarBody), 0644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	report, err := replayWithSidecar(transcript, sidecar, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replayWithSidecar: %v", err)
	}

	var foundHookWaiting bool
	for _, tr := range report.Transitions {
		if tr.Cause == causeHook && tr.NewState == "waiting" {
			foundHookWaiting = true
			break
		}
	}
	if !foundHookWaiting {
		t.Errorf("expected a hook-caused working→waiting transition; transitions: %+v", report.Transitions)
	}
}

// TestSessionFilter verifies that SessionFilter in reportSettings filters
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

	report, err := replayWithSidecar(transcript, sidecar, reportSettings{
		Adapter:            claudecode.AdapterName,
		SessionFilter:      "sess-B",
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replayWithSidecar with session filter: %v", err)
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

	report, err := replayWithSidecar(transcript, sidecar, reportSettings{
		Adapter:            claudecode.AdapterName,
		SessionFilter:      "sess-B",
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("replayWithSidecar with session filter: %v", err)
	}

	// A ready→working must fire off the first fs event; the alive-gate
	// would have suppressed it before the fallback.
	var sawReadyToWorking bool
	for _, tr := range report.Transitions {
		if tr.PrevState == session.StateReady && tr.NewState == session.StateWorking {
			sawReadyToWorking = true
			break
		}
	}
	if !sawReadyToWorking {
		t.Errorf("expected ready→working transition; got transitions: %+v", report.Transitions)
	}
}
