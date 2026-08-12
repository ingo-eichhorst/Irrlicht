package main

import (
	"testing"
	"time"
)

// Issue #1478: three of the four recordings #1342 left pinned in
// knownZeroTransition are rescued by the cluster extension in
// readBoundaryFor. Each is asserted by name below, seen red before the fix.

// TestReplayWithSidecar_Issue1478_PiBasicTurn is the issue's own named case:
// 2556 bytes across 4 fs events spanning 3ms.
func TestReplayWithSidecar_Issue1478_PiBasicTurn(t *testing.T) {
	assertReproducesRecordedTransitions(t, pinnedRecording(t,
		"pi/scenarios/2-1_basic-turn/recordings/2026-05-25-01-39-59_irrlichd-0.4.7+7b5218c/transcript.jsonl"))
}

func TestReplayWithSidecar_Issue1478_PiSessionEnd(t *testing.T) {
	assertReproducesRecordedTransitions(t, pinnedRecording(t,
		"pi/scenarios/1-2_session-end/recordings/2026-05-25-01-48-39_irrlichd-0.4.7+597f655/transcript.jsonl"))
}

func TestReplayWithSidecar_Issue1478_PiFullLifecycleToolcall(t *testing.T) {
	assertReproducesRecordedTransitions(t, pinnedRecording(t,
		"pi/regressions/full-lifecycle-toolcall/recordings/2026-04-26-11-31-28_irrlichd-unknown/transcript.jsonl"))
}

// checkOf replays one pinned recording at the current readBoundaryClusterWindow
// and returns its extended check.
func checkOf(t *testing.T, rel string) *extendedCheck {
	t.Helper()
	transcript := pinnedRecording(t, rel)
	tp, sp, useSidecar := resolveInputPaths(transcript)
	if !useSidecar {
		t.Fatalf("resolveInputPaths did not pair %s with its sidecar", rel)
	}
	report, err := runReplay(tp, sp, useSidecar, replaySettingsForTest(t, tp))
	if err != nil {
		t.Fatalf("runReplay(%s): %v", rel, err)
	}
	if report.ExtendedCheck == nil {
		t.Fatalf("%s produced no extended check", rel)
	}
	return report.ExtendedCheck
}

// withClusterWindow runs fn with readBoundaryClusterWindow set to w.
func withClusterWindow(t *testing.T, w time.Duration, fn func()) {
	t.Helper()
	prev := readBoundaryClusterWindow
	readBoundaryClusterWindow = w
	defer func() { readBoundaryClusterWindow = prev }()
	fn()
}

// The three recordings #1478 rescues, and the one it deliberately does not.
var issue1478Rescued = []string{
	"pi/scenarios/2-1_basic-turn/recordings/2026-05-25-01-39-59_irrlichd-0.4.7+7b5218c/transcript.jsonl",
	"pi/scenarios/1-2_session-end/recordings/2026-05-25-01-48-39_irrlichd-0.4.7+597f655/transcript.jsonl",
	"pi/regressions/full-lifecycle-toolcall/recordings/2026-04-26-11-31-28_irrlichd-unknown/transcript.jsonl",
}

// ceilingWitness is the recording that fabricates first as the window widens.
// It is ALSO one of the two goldens #1342's rejected guard-narrowing broke —
// two unrelated mechanisms reaching the same wall, which is why the ceiling is
// treated as a property of the catalog rather than of this heuristic.
const ceilingWitness = "codex/scenarios/2-1_basic-turn/recordings/2026-05-23-18-54-06_irrlichd-0.4.7+723609a/transcript.jsonl"

// TestReadBoundaryClusterWindow_BothWallsAreMeasured is the calibration's
// mutation evidence. readBoundaryClusterWindow is a number this change ADDS,
// so it has no "before the fix" to be run against; per AGENTS.md the standing
// obligation is instead to break what it protects and see that go red. Both
// directions are broken here, because a window justified only from below could
// be raised to 1s without anything objecting, and one justified only from
// above could be dropped to 0 the same way.
func TestReadBoundaryClusterWindow_BothWallsAreMeasured(t *testing.T) {
	// The vacuity guard. If these do not hold at the shipped value, every
	// assertion below is measuring something other than the shipped rule.
	t.Run("at the shipped window all three rescues hold and nothing fabricates", func(t *testing.T) {
		for _, rel := range issue1478Rescued {
			if ec := checkOf(t, rel); ec.ReplayedCount == 0 {
				t.Errorf("%s reproduces zero transitions at the shipped window (daemon recorded %d)",
					rel, ec.RecordedCount)
			}
		}
		ec := checkOf(t, ceilingWitness)
		if ec.RecordedCount != 0 {
			t.Fatalf("%s: daemon recorded %d transitions — this recording is the ceiling witness "+
				"BECAUSE its daemon recorded none; the witness has been replaced and the "+
				"assertion below no longer means what it says", ceilingWitness, ec.RecordedCount)
		}
		if ec.ReplayedCount != 0 {
			t.Errorf("%s fabricates %d transitions at the shipped window — the ceiling has been "+
				"crossed", ceilingWitness, ec.ReplayedCount)
		}
	})

	// FLOOR. Below 3ms the window stops reaching the bursts it exists for.
	t.Run("below the floor a rescue is lost", func(t *testing.T) {
		withClusterWindow(t, 2*time.Millisecond, func() {
			var lost []string
			for _, rel := range issue1478Rescued {
				if checkOf(t, rel).ReplayedCount == 0 {
					lost = append(lost, rel)
				}
			}
			if len(lost) == 0 {
				t.Error("a 2ms window still rescues all three — the floor is not where the " +
					"constant's doc comment says it is, so the value is unjustified from below")
			} else {
				t.Logf("2ms window loses %d of %d rescues: %v", len(lost), len(issue1478Rescued), lost)
			}
		})
	})

	// CEILING. At 28ms replay classifies on bytes the daemon provably had not
	// read, in a recording whose daemon held ready for the session's whole life.
	t.Run("at the ceiling replay fabricates", func(t *testing.T) {
		withClusterWindow(t, 28*time.Millisecond, func() {
			ec := checkOf(t, ceilingWitness)
			if ec.ReplayedCount == 0 {
				t.Errorf("%s still fabricates nothing at a 28ms window — the ceiling is not "+
					"where the constant's doc comment says it is, so the value is unjustified "+
					"from above", ceilingWitness)
				return
			}
			t.Logf("28ms window makes %s invent %d transition(s) (%v) its daemon never logged",
				ceilingWitness, ec.ReplayedCount, ec.ExtraKinds)
		})
	})
}
