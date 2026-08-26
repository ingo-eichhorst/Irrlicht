package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// #1817: the offline sidecar replayed a mid-turn process death as `ready`.
//
// The measured cause was NOT the one the issue named. `case timelineProcessExit`
// does set ready unconditionally, but it is unreachable for a mid-turn death:
// PIDManager.HandleProcessExit records KindProcessExited only on the branch
// that DELETES the row, and #1800's retain path returns before it. So the
// crash recording carried no process event at all, and the only trace of the
// death was the state_transition it produced — the classifier's OUTPUT, which
// is precisely what the extended check grades the replay against, so it cannot
// be fed back in as an input without making the check circular.
//
// The fix records the observation under its own Kind (KindProcessDiedMidTurn)
// and teaches the sidecar to run it through the ONE shared predicate,
// session.SessionState.DiedMidTurn. These tests pin both halves of that.

// deathFixture is a recording rebuilt as a post-fix daemon would have written
// it: the committed 2-25 crash recording, plus the one event the daemon of the
// day did not record. Returns the paths of the rebuilt transcript + sidecar.
//
// It deliberately rebuilds from the REAL recording rather than hand-rolling a
// transcript, because the claim under test is specifically that the committed
// crash fixture replays green only for want of that event — a synthetic
// transcript could not support that claim.
type deathFixture struct {
	transcript string
	sidecar    string
	sessionID  string
	deathAt    time.Time
}

// buildDeathFixture copies the committed 2-25 recording into a temp dir and
// splices in the KindProcessDiedMidTurn event it lacks. Every step that could
// silently find nothing fails loudly instead: an absent crash transition, a
// zero event count, or an unreadable transcript all abort rather than
// producing a fixture that would pass for the wrong reason.
func buildDeathFixture(t *testing.T) deathFixture {
	t.Helper()

	src := fixturePath(t, "claudecode/2-25_agent-process-crash-midturn/transcript.jsonl")
	srcSidecar := filepath.Join(filepath.Dir(src), eventsSidecarName)

	transcriptBytes, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read committed crash transcript %s: %v", src, err)
	}
	sidecarBytes, err := os.ReadFile(srcSidecar)
	if err != nil {
		t.Fatalf("read committed crash sidecar %s: %v", srcSidecar, err)
	}

	lines := strings.Split(strings.TrimRight(string(sidecarBytes), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("%s is empty — the fixture cannot exercise anything", srcSidecar)
	}
	crash := findCrashVerdict(t, lines, srcSidecar)

	// This is the whole of what the daemon of the day failed to record. The
	// daemon records it immediately before the transition it causes, so the
	// crash transition's own timestamp is the faithful stamp for it.
	deathAt := crash.Timestamp
	death, err := json.Marshal(lifecycle.Event{
		Seq:       crash.Seq - 1,
		Timestamp: deathAt,
		Kind:      lifecycle.KindProcessDiedMidTurn,
		SessionID: crash.SessionID,
		PID:       30535,
		Reason:    "pid exited (ESRCH)",
	})
	if err != nil {
		t.Fatalf("marshal death event: %v", err)
	}

	dir := t.TempDir()
	fx := deathFixture{
		transcript: filepath.Join(dir, "transcript.jsonl"),
		sidecar:    filepath.Join(dir, eventsSidecarName),
		sessionID:  crash.SessionID,
		deathAt:    deathAt,
	}
	if err := os.WriteFile(fx.transcript, transcriptBytes, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	rebuilt := strings.Join(append(lines, string(death)), "\n") + "\n"
	if err := os.WriteFile(fx.sidecar, []byte(rebuilt), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	return fx
}

// findCrashVerdict locates the daemon's own working→error process-death
// transition in a committed sidecar, and refuses to return anything ambiguous.
//
// Both of its exits are deliberate. Its PRESENCE is what makes this recording
// the right fixture, so an absent verdict is fatal rather than a skip — the
// recording changed under us and every assertion downstream would be
// meaningless. A sidecar that ALREADY carries a KindProcessDiedMidTurn is the
// opposite case and is a skip: fixturePath resolves to the NEWEST recording of
// the cell, so once 2-25 is re-recorded on a daemon carrying #1817 the event is
// already there, splicing a second one would leave the caller asserting against
// a timestamp the replay has no reason to use, and that recording's own golden
// is the right assertion instead.
func findCrashVerdict(t *testing.T, lines []string, srcSidecar string) lifecycle.Event {
	t.Helper()
	var crash lifecycle.Event
	var found bool
	for _, line := range lines {
		var ev lifecycle.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("committed sidecar has an unparseable line %q: %v", line, err)
		}
		if ev.Kind == lifecycle.KindProcessDiedMidTurn {
			t.Skipf("%s already carries a %s event (the cell has been re-recorded) — "+
				"this helper only reconstructs what a pre-#1817 daemon failed to record; "+
				"assert against the recording's own golden instead",
				srcSidecar, lifecycle.KindProcessDiedMidTurn)
		}
		if ev.Kind == lifecycle.KindStateTransition && ev.NewState == session.StateError &&
			strings.Contains(ev.Reason, "died mid-turn") {
			crash, found = ev, true
		}
	}
	if !found {
		t.Fatalf("%s carries no working→error process-death transition — this recording no longer exercises #1817", srcSidecar)
	}
	if crash.SessionID == "" || crash.Timestamp.IsZero() {
		t.Fatalf("crash transition is missing a session id or timestamp: %+v", crash)
	}
	return crash
}

// replayDeathFixture runs the fixture through the same resolveInputPaths →
// runReplay path main() and the byte-identity goldens use.
func replayDeathFixture(t *testing.T, fx deathFixture) *replayReport {
	t.Helper()
	transcript, sidecar, useSidecar := resolveInputPaths(fx.transcript)
	if !useSidecar {
		t.Fatalf("resolveInputPaths did not pair %s with its sibling %s", fx.transcript, eventsSidecarName)
	}
	// Not replaySettingsForTest: it derives the adapter via detectAdapter,
	// which infers from the replaydata path shape and cannot resolve a
	// t.TempDir() copy. Same two knobs, named explicitly.
	report, err := runReplay(transcript, sidecar, useSidecar, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("runReplay: %v", err)
	}
	if report.ExtendedCheck == nil {
		t.Fatal("ExtendedCheck is nil — the replay degraded to transcript-only and grades nothing")
	}
	return report
}

// TestSidecarReplaysAMidTurnProcessDeathAsError is the #1817 defect test.
//
// RED BEFORE THE FIX, for the right reason: without the sidecar's
// timelineProcessDeath branch the spliced event is an unrecognised Kind that
// bucketPrimaryEventByKind drops, so the replay stops at ready→working and the
// extended check reports missing_kinds ["working→error"] — byte for byte the
// divergence the committed 2-25 golden carries today.
func TestSidecarReplaysAMidTurnProcessDeathAsError(t *testing.T) {
	fx := buildDeathFixture(t)
	report := replayDeathFixture(t, fx)

	var got []string
	found := false
	for _, tr := range report.Transitions {
		got = append(got, string(tr.Cause)+":"+tr.PrevState+"→"+tr.NewState)
		if tr.PrevState == session.StateWorking && tr.NewState == session.StateError {
			found = true
			if tr.Cause != causeProcessDeath {
				t.Errorf("working→error has cause %q, want %q", tr.Cause, causeProcessDeath)
			}
			if !tr.VirtualTime.Equal(fx.deathAt) {
				t.Errorf("working→error fired at %s, want the death's own time %s", tr.VirtualTime, fx.deathAt)
			}
		}
	}
	if !found {
		t.Fatalf("replay produced no working→error for a mid-turn process death; transitions were %v", got)
	}

	// The point of the exercise: with the observation recorded, the replay
	// reproduces the daemon exactly rather than diverging from it.
	if ec := report.ExtendedCheck; len(ec.MissingKinds) > 0 || len(ec.OrderedMismatches) > 0 {
		t.Errorf("extended check still diverges: missing_kinds=%v ordered_mismatches=%+v",
			ec.MissingKinds, ec.OrderedMismatches)
	}
}

// TestSidecarKeepsAProcessDeathFailClosedForASubagent is the other half, and
// it is a LOCK rather than a defect test: it passes by construction on a
// correct predicate and exists so the branch above cannot be "fixed" into
// asserting error unconditionally. Reaching for StateError regardless of the
// session's shape is exactly the change lifecycle.KindProcessDiedMidTurn's doc
// measured.
//
// It exercises DiedMidTurn's SUBAGENT clause, and does so through the sidecar's
// parentSessionID plumbing — which is the only fail-closed clause that is both
// reachable at the crash instant and expressible in the sidecar's own input
// language. That distinction is load-bearing: an earlier cut of this test dated
// the death BEFORE the session's first transcript read, which fails closed on
// the nil-metrics guard long before any clause is consulted — so it stayed
// green even with DiedMidTurn's state clause deleted, i.e. it was a check that
// could not fail. This one discriminates: the only difference from the test
// above is the parent_linked event, and that test proves the same fixture
// otherwise DOES produce working→error.
func TestSidecarKeepsAProcessDeathFailClosedForASubagent(t *testing.T) {
	fx := buildDeathFixture(t)
	linkPrimaryToAParent(t, fx)
	report := replayDeathFixture(t, fx)

	for _, tr := range report.Transitions {
		if tr.NewState == session.StateError {
			t.Fatalf("a subagent's process death still produced %s at %s (cause %q) — DiedMidTurn's ParentSessionID clause is not being consulted",
				session.StateError, tr.VirtualTime, tr.Cause)
		}
	}
}

// linkPrimaryToAParent appends a parent_linked event naming the primary session
// as somebody's child, so bucketPrimaryEvents records a non-empty
// parentSessionID for it.
func linkPrimaryToAParent(t *testing.T, fx deathFixture) {
	t.Helper()
	link, err := json.Marshal(lifecycle.Event{
		Seq:             1,
		Timestamp:       fx.deathAt.Add(-time.Hour),
		Kind:            lifecycle.KindParentLinked,
		SessionID:       fx.sessionID,
		ParentSessionID: "some-parent-session",
	})
	if err != nil {
		t.Fatalf("marshal parent link: %v", err)
	}
	existing, err := os.ReadFile(fx.sidecar)
	if err != nil {
		t.Fatalf("read rebuilt sidecar: %v", err)
	}
	if err := os.WriteFile(fx.sidecar, append(existing, append(link, '\n')...), 0o600); err != nil {
		t.Fatalf("append parent link: %v", err)
	}
}

// TestProcessDiedMidTurnIsNotATeardownKind pins the distinction the fix rests
// on. KindProcessExited means the row was DELETED and every consumer removes
// the session on it; KindProcessDiedMidTurn means the row SURVIVED as `error`.
// Collapsing the two is what lifecycle.KindProcessDiedMidTurn's doc measured,
// so the separation is asserted rather than left to a reader of two switches.
func TestProcessDiedMidTurnIsNotATeardownKind(t *testing.T) {
	if lifecycle.KindProcessDiedMidTurn == lifecycle.KindProcessExited {
		t.Fatal("the retained and teardown edges share a Kind — every process_exited in the committed corpus would be reinterpreted")
	}

	b := bucketSidecarEvents([]lifecycle.Event{
		{Seq: 1, Kind: lifecycle.KindTranscriptNew, SessionID: "s1", FileSize: 10},
		{Seq: 2, Kind: lifecycle.KindProcessDiedMidTurn, SessionID: "s1", PID: 7},
		{Seq: 3, Kind: lifecycle.KindProcessExited, SessionID: "s1", PID: 7},
	}, "s1")
	if len(b.processDeaths) != 1 {
		t.Errorf("processDeaths = %d, want 1 — the retained edge is not being bucketed", len(b.processDeaths))
	}
	if len(b.processExits) != 1 {
		t.Errorf("processExits = %d, want 1 — the teardown edge stopped being bucketed", len(b.processExits))
	}
}

// TestSidecarReleasesTheDeathHoldOnTeardown pins applyProcessTeardown's
// signals.Release, which mirrors retainAsProcessDeath's expiry branch
// (session_detector_lifecycle.go:390 — it releases immediately before returning
// false and letting HandleProcessExit reap the row and write process_exited).
//
// The shape is real, not hypothetical: a crash retained as `error` whose 12h
// processDeathRetention expires is reaped exactly that way, so a long recording
// can carry death → teardown → a fresh lifetime. SignalProcessDeath is the ONE
// policy with no ceiling, so without the release nothing ever drops it, and the
// next lifetime's first classify overlays it onto fresh metrics and pins a
// brand-new session to `error`.
//
// Discriminating: delete the Release in applyProcessTeardown and the second
// lifetime comes back `error`.
func TestSidecarReleasesTheDeathHoldOnTeardown(t *testing.T) {
	fx := buildDeathFixture(t)

	// The reap the daemon performs once the retention window closes, followed
	// by a fresh lifetime for the same session id — a resumed transcript, which
	// is what a lifecycle start means to the sidecar.
	appendSidecarEvents(t, fx,
		lifecycle.Event{
			Seq: 9001, Timestamp: fx.deathAt.Add(12 * time.Hour),
			Kind: lifecycle.KindProcessExited, SessionID: fx.sessionID, PID: 30535,
			Reason: "pid exited (ESRCH)",
		},
		lifecycle.Event{
			Seq: 9002, Timestamp: fx.deathAt.Add(12*time.Hour + time.Second),
			Kind: lifecycle.KindTranscriptNew, SessionID: fx.sessionID, FileSize: 271,
		},
		lifecycle.Event{
			Seq: 9003, Timestamp: fx.deathAt.Add(12*time.Hour + 2*time.Second),
			Kind: lifecycle.KindTranscriptActivity, SessionID: fx.sessionID, FileSize: 35644,
		},
	)

	report := replayDeathFixture(t, fx)

	// Everything up to the teardown is lifetime 1 and legitimately ends in
	// error. Only what the SECOND lifetime produces is under test.
	teardownAt := fx.deathAt.Add(12 * time.Hour)
	sawDeath := false
	for _, tr := range report.Transitions {
		if tr.NewState == session.StateError && !tr.VirtualTime.After(teardownAt) {
			sawDeath = true
			continue
		}
		if tr.NewState == session.StateError {
			t.Fatalf("a lifetime that began AFTER the teardown was classified %s at %s (cause %q) — "+
				"the process-death hold outlived the teardown that reaped it, and SignalProcessDeath has no ceiling to drop it",
				session.StateError, tr.VirtualTime, tr.Cause)
		}
	}
	if !sawDeath {
		t.Fatal("lifetime 1 never reached error — the fixture no longer places a hold, so the release below is untested")
	}
}

// appendSidecarEvents appends events to a fixture's rebuilt sidecar.
func appendSidecarEvents(t *testing.T, fx deathFixture, events ...lifecycle.Event) {
	t.Helper()
	existing, err := os.ReadFile(fx.sidecar)
	if err != nil {
		t.Fatalf("read rebuilt sidecar: %v", err)
	}
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal %s: %v", ev.Kind, err)
		}
		existing = append(existing, append(line, '\n')...)
	}
	if err := os.WriteFile(fx.sidecar, existing, 0o600); err != nil {
		t.Fatalf("append sidecar events: %v", err)
	}
}
