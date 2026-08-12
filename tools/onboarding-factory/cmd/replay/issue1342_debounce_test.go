package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Issue #1342: sidecar-driven replay produced ZERO state transitions for 20
// committed recordings whose daemon logged 1–4, leaving those goldens holding
// nothing but the synthetic init row — a regression net that had stopped being
// able to fail, the same shape as #1326.
//
// Two independent causes were found; both are fixed in replay_sidecar.go and
// each has its own defect test below, seen red before its own fix.
//
//  1. process_exited discarded a debounce window whose deadline had already
//     passed in virtual time — the daemon's timer had fired it and logged the
//     transitions.
//  2. classifyAt clamped the transcript to the fswatcher's recorded stat, which
//     for a codex/pi transcript can land exactly on the end of a multi-kilobyte
//     session_meta and manufacture a "header line only" pass the daemon never
//     had. readBoundaryFor widens that one shape.
//
// A third cause remains open, tracked by knownZeroTransition below.

// replaySettingsForTest mirrors the flags tools/replay-fixtures.sh runs with,
// so a divergence here can never be an artifact of test-only settings.
func replaySettingsForTest(t *testing.T, transcript string) reportSettings {
	t.Helper()
	adapter, err := detectAdapter(transcript)
	if err != nil {
		t.Fatalf("detectAdapter(%s): %v", transcript, err)
	}
	return reportSettings{
		Adapter:            adapter,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	}
}

// replaydataRoot is replaydata/agents, the catalog these tests read.
func replaydataRoot(t *testing.T) string {
	t.Helper()
	return mustAbs(t, filepath.Join("..", "..", "..", "..", "replaydata", "agents"))
}

// pinnedRecording addresses ONE named recording by its catalog-relative path —
// the same notation knownZeroTransition below is keyed by — rather than
// fixturePath's
// "newest wins" resolution. The two defect tests below quote per-event
// timestamps and byte offsets as their evidence, and under fixturePath every
// one of those figures would silently stop describing the file being read the
// next time a recording is promoted into the same cell — which is exactly what
// happened to the first draft of this file.
func pinnedRecording(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(replaydataRoot(t), filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("pinned recording is gone (retired or renamed?): %v", err)
	}
	return p
}

// assertReproducesRecordedTransitions is the shared body of the two fixture
// tests: replay the recording and hold its extended check to the daemon's own
// log, in BOTH directions. The surplus direction matters as much as the
// deficit one — an earlier draft of the #1342 fix drove the deficit to zero by
// classifying on no evidence, and fabricated a ready→working in two goldens
// whose daemon had recorded none.
func assertReproducesRecordedTransitions(t *testing.T, transcript string) {
	t.Helper()

	tp, sp, useSidecar := resolveInputPaths(transcript)
	if !useSidecar {
		t.Fatalf("resolveInputPaths did not pair %s with its sibling %s", transcript, eventsSidecarName)
	}
	report, err := runReplay(tp, sp, useSidecar, replaySettingsForTest(t, tp))
	if err != nil {
		t.Fatalf("runReplay: %v", err)
	}
	ec := report.ExtendedCheck
	if ec == nil {
		t.Fatal("ExtendedCheck is nil — the recording did not drive a sidecar replay at all")
	}
	if ec.RecordedCount == 0 {
		t.Fatal("recorded_transition_count is 0 — this fixture cannot witness the defect (vacuous)")
	}
	if ec.ReplayedCount < ec.RecordedCount {
		t.Errorf("replayed %d transitions, daemon recorded %d", ec.ReplayedCount, ec.RecordedCount)
	}
	if len(ec.MissingKinds) > 0 {
		t.Errorf("missing_kinds = %v, want empty (daemon kinds %v vs replayed %v)",
			ec.MissingKinds, ec.RecordedUniqueKinds, ec.ReplayedUniqueKinds)
	}
	if len(ec.ExtraKinds) > 0 {
		t.Errorf("extra_kinds = %v, want empty — replay invented a transition the daemon never logged",
			ec.ExtraKinds)
	}
}

// TestReplayWithSidecar_Issue1342_ExpiredWindowSurvivesProcessExit is the
// issue's own named reproduction, asserted rather than eyeballed.
//
// In this recording the 12 fs events span 20:36:37.440→20:36:39.129, i.e. 1.69s
// — inside the 2s debounce window — so nothing flushes mid-timeline and the
// window's deadline falls at 20:36:41.129. The daemon logged its two
// transitions at 20:36:41.1317 and 20:36:41.1325, 2ms past that deadline, and
// process_exited did not arrive until 20:36:44.492 — 3.4s later. The window
// this branch used to discard had unambiguously already fired.
func TestReplayWithSidecar_Issue1342_ExpiredWindowSurvivesProcessExit(t *testing.T) {
	assertReproducesRecordedTransitions(t, pinnedRecording(t,
		"codex/scenarios/2-1_basic-turn/recordings/2026-05-23-20-36-20_irrlichd-0.4.7+723609a/transcript.jsonl"))
}

// TestReplayWithSidecar_Issue1342_HeaderOnlyFirstPassIsWidened covers the
// second, independent cause, on a recording the debounce fix alone does not
// reach: here process_exited (11:29:02.384) arrives BEFORE the pending window's
// deadline (11:29:03.961), so that window is genuinely cancelled and the daemon
// logged nothing there either.
//
// Its single recorded transition is the FIRST pass's ready→working at
// 11:29:00.354. Replay lost it because the first fs event reports
// file_size=15401 — exactly the end of the 15401-byte session_meta header — so
// the clamped pass parsed one non-substantive line and #329's guard skipped it.
// The daemon never had that pass: it read to EOF ~57ms later, by which point
// the file had reached 28252 bytes and carried a user message.
func TestReplayWithSidecar_Issue1342_HeaderOnlyFirstPassIsWidened(t *testing.T) {
	assertReproducesRecordedTransitions(t, pinnedRecording(t,
		"codex/regressions/baseline-hello/recordings/2026-04-26-11-28-57_irrlichd-unknown/transcript.jsonl"))
}

// knownZeroTransition lists the recordings that still reproduce zero
// transitions, with the reason each is out of reach. They are pinned rather
// than tolerated: the test below fails if the set GROWS, and equally if an
// entry is fixed and left here to rot.
//
// #1342 left four here; #1478's time-aware cluster extension
// (readBoundaryClusterWindow) reproduced three of them and they were deleted
// from this list. The one that remains is NOT a smaller version of the same
// problem — it is on the far side of a measured wall, and the distinction is
// the whole finding of #1478.
//
// Its burst spans 68.875ms, where the three that were rescued span 2-3ms. A
// window wide enough to reach it is 2.5x past the point where replay starts
// FABRICATING: at 28ms codex/2-1_basic-turn's 18-54-06 recording gains a
// ready→working its daemon never logged, and at 68ms codex/1-1_session-start
// — the sole recording for a core-twelve scenario, whose daemon correctly held
// ready for the session's entire life — joins it. Those are precisely the two
// goldens #1342's rejected guard-narrowing broke, reached here by a completely
// different mechanism.
//
// So the trade is explicit and it is refused: this entry could be cleared
// today at the cost of two goldens that would then assert something false,
// which is strictly worse than one that asserts nothing. Clearing it honestly
// needs a re-recording, not a wider window — see #1478.
var knownZeroTransition = map[string]string{
	"codex/regressions/agent-question-pending/recordings/2026-04-26-11-57-03_irrlichd-unknown/transcript.jsonl": "68.875ms burst — unreachable without fabricating in codex/2-1_basic-turn and codex/1-1_session-start (#1478)",
}

// knownFabricated lists recordings where replay emits transitions the daemon
// never logged. The single entry PRE-DATES #1342 — it is present on main, and
// neither fix in this change moves it — but it is pinned here because the
// natural repair for #1342's deficit is to classify more eagerly, and that is
// precisely how this counter grows.
var knownFabricated = map[string]string{
	"copilot/scenarios/2-19_tool-gate-permission-prompt/recordings/2026-08-03-18-02-54_irrlichd-0.5.9+c55d3a0/transcript.jsonl": "pre-existing on main, unrelated to #1342",
}

// forEachSidecarRecording replays every sidecar-driven recording in the catalog
// and hands each one's extended check to visit, keyed by its catalog-relative
// transcript path. It returns how many were visited.
//
// Shared because two catalog gates now ask the same question of the same
// population and only differ in the verdict they draw — this one and #1480's
// timing measurement. The walk is mechanism (what counts as a sidecar-driven
// recording, how a transcript pairs with its sidecar, which recordings
// legitimately have no extended check); the verdicts are policy and stay in
// each caller's callback with its own known-lists and messages. Keeping the two
// replay runs separate is deliberate — sharing the walk costs ~2.8s of re-run
// and buys full independence between the gates, which is the right trade.
//
// The zero-recordings vacuity guard lives here rather than in each caller: it
// is exactly the check that must not be forgotten by the third one, and it was
// already written twice with two different wordings.
func forEachSidecarRecording(t *testing.T, visit func(name string, ec *extendedCheck)) int {
	t.Helper()
	root := replaydataRoot(t)

	var checked int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != eventsSidecarName {
			return nil
		}
		transcript := filepath.Join(filepath.Dir(path), "transcript.jsonl")
		if _, statErr := os.Stat(transcript); statErr != nil {
			return nil
		}
		tp, sp, useSidecar := resolveInputPaths(transcript)
		if !useSidecar {
			return nil
		}
		report, runErr := runReplay(tp, sp, useSidecar, replaySettingsForTest(t, tp))
		if runErr != nil {
			t.Errorf("runReplay(%s): %v", rel(root, transcript), runErr)
			return nil
		}
		// A process-owned-store adapter records no fswatcher fires and
		// legitimately degrades to transcript-only, so it has no extended
		// check to compare.
		if report.ExtendedCheck == nil {
			return nil
		}
		checked++
		visit(rel(root, transcript), report.ExtendedCheck)
		return nil
	})
	if err != nil {
		t.Fatalf("walk replaydata/agents: %v", err)
	}
	if checked == 0 {
		t.Fatal("no sidecar-driven recording found under replaydata/agents — the check is vacuous")
	}
	return checked
}

// TestSidecarReplayAgreesWithTheDaemonsOwnLog walks the whole catalog and holds
// every sidecar-driven recording to its own recording in both directions: it
// must not reproduce ZERO transitions where the daemon logged some, and it must
// not invent transitions where the daemon logged none.
//
// Catalog-wide rather than a list of paths, so a newly recorded fixture is
// covered by existing rather than by somebody remembering to add it.
//
// Scope note: this asserts only the two absolute failures, not
// replayed >= recorded generally. 145 of 309 sidecar-driven recordings still
// show milder extended-check divergence (dominant kind: a missing terminal
// working→ready); that is a separate population #1342 does not claim to fix,
// and asserting on it here would fail for reasons this ticket is not about.
func TestSidecarReplayAgreesWithTheDaemonsOwnLog(t *testing.T) {
	seenZero := map[string]bool{}
	seenFabricated := map[string]bool{}
	var newZero, newFabricated []string

	checked := forEachSidecarRecording(t, func(name string, ec *extendedCheck) {
		switch {
		case ec.RecordedCount > 0 && ec.ReplayedCount == 0:
			seenZero[name] = true
			if _, known := knownZeroTransition[name]; !known {
				newZero = append(newZero, fmt.Sprintf("%s (daemon recorded %d, replay reproduced 0)",
					name, ec.RecordedCount))
			}
		case ec.RecordedCount == 0 && ec.ReplayedCount > 0:
			seenFabricated[name] = true
			if _, known := knownFabricated[name]; !known {
				newFabricated = append(newFabricated, fmt.Sprintf("%s (daemon recorded 0, replay invented %d)",
					name, ec.ReplayedCount))
			}
		}
	})

	report := func(label string, items []string) {
		if len(items) == 0 {
			return
		}
		sort.Strings(items)
		t.Errorf("%d %s (of %d sidecar-driven recordings):\n  %s",
			len(items), label, checked, strings.Join(items, "\n  "))
	}
	report("recordings reproduce zero transitions where the daemon recorded at least one, "+
		"so their goldens assert nothing — add to knownZeroTransition only with a reason", newZero)
	report("recordings invent transitions the daemon never logged, so their goldens assert "+
		"something false — this is what over-eager classification looks like", newFabricated)

	// Stale entries rot the lists into permanent excuses, so a fixed
	// recording must be removed rather than left behind.
	for name := range knownZeroTransition {
		if !seenZero[name] {
			t.Errorf("knownZeroTransition entry %q no longer reproduces zero transitions — delete it", name)
		}
	}
	for name := range knownFabricated {
		if !seenFabricated[name] {
			t.Errorf("knownFabricated entry %q no longer fabricates — delete it", name)
		}
	}
	t.Logf("checked %d sidecar-driven recordings", checked)
}

func rel(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return r
	}
	return path
}
