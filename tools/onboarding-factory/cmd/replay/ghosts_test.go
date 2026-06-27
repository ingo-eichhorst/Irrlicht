package main

import (
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// TestBuildGhostTimelines_DetectsPID0Ghost is the CI-grade proof for the
// antigravity headline case (issue #757): a PID=0 session that is classified
// ready, never does substantive work, and is reaped on the stale-transcript
// path must be flagged IsGhost with its removal reason and lifetime, while a
// substantive sibling that bound a PID and worked must not.
func TestBuildGhostTimelines_DetectsPID0Ghost(t *testing.T) {
	base := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	const ghostReason = "ghost reaped: PID=0, ready, stale transcript >30s — pre-session never bound a process"

	events := []lifecycle.Event{
		// Ghost: minted, classified ready, never worked, reaped 33s later.
		{Seq: 1, Timestamp: base, Kind: lifecycle.KindTranscriptNew, SessionID: "proc-0", Adapter: "antigravity"},
		{Seq: 2, Timestamp: base.Add(1 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "proc-0",
			PrevState: session.StateWorking, NewState: session.StateReady, Reason: "agent turn complete",
			Inputs: &lifecycle.ClassifierInputs{NoSubstantiveActivity: true, LastEventType: "assistant"}},
		{Seq: 3, Timestamp: base.Add(33 * time.Second), Kind: lifecycle.KindTranscriptRemoved, SessionID: "proc-0",
			Adapter: "antigravity", Reason: ghostReason},

		// Substantive sibling: bound a PID, worked, exited cleanly.
		{Seq: 4, Timestamp: base.Add(10 * time.Second), Kind: lifecycle.KindTranscriptNew, SessionID: "sess-real", Adapter: "antigravity"},
		{Seq: 5, Timestamp: base.Add(11 * time.Second), Kind: lifecycle.KindPIDDiscovered, SessionID: "sess-real", PID: 4242},
		{Seq: 6, Timestamp: base.Add(12 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "sess-real",
			PrevState: session.StateReady, NewState: session.StateWorking, Reason: "transcript activity"},
		{Seq: 7, Timestamp: base.Add(40 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "sess-real",
			PrevState: session.StateWorking, NewState: session.StateReady, Reason: "agent turn complete"},
		{Seq: 8, Timestamp: base.Add(45 * time.Second), Kind: lifecycle.KindProcessExited, SessionID: "sess-real",
			PID: 4242, Reason: "pid exited (ESRCH)"},
	}

	timelines := buildGhostTimelines(events)
	byID := make(map[string]sessionTimeline, len(timelines))
	for _, tl := range timelines {
		byID[tl.SessionID] = tl
	}

	ghost, ok := byID["proc-0"]
	if !ok {
		t.Fatalf("no timeline for ghost session")
	}
	if !ghost.IsGhost {
		t.Errorf("proc-0 should be flagged IsGhost")
	}
	if ghost.HadSubstantive {
		t.Errorf("proc-0 should have HadSubstantive=false")
	}
	if ghost.RemovalReason != ghostReason {
		t.Errorf("proc-0 RemovalReason = %q, want %q", ghost.RemovalReason, ghostReason)
	}
	if ghost.FinalReason != "agent turn complete" {
		t.Errorf("proc-0 FinalReason = %q, want %q", ghost.FinalReason, "agent turn complete")
	}
	if ghost.RemovedAt == nil {
		t.Fatalf("proc-0 RemovedAt should be set")
	}
	if ghost.LifetimeMs != 33_000 {
		t.Errorf("proc-0 LifetimeMs = %d, want 33000", ghost.LifetimeMs)
	}

	real, ok := byID["sess-real"]
	if !ok {
		t.Fatalf("no timeline for substantive session")
	}
	if real.IsGhost {
		t.Errorf("sess-real should NOT be flagged IsGhost (bound a PID and worked)")
	}
	if !real.HadSubstantive {
		t.Errorf("sess-real should have HadSubstantive=true")
	}

	// The text view must name the ghost, its reason, its lifetime, and the
	// classifier inputs that drove the final classification.
	out := renderGhostTimeline("test.events.jsonl", timelines, lastTransitionInputs(events))
	for _, want := range []string{"1 ghost(s)", "proc-0", "GHOST", ghostReason, "33s", "no_substantive_activity", "sess-real"} {
		if !strings.Contains(out, want) {
			t.Errorf("ghost timeline output missing %q\n---\n%s", want, out)
		}
	}
}

// TestBuildGhostTimelines_NoGhostsLeaveReportFieldsZero guards byte-identity:
// for a fully substantive session the ghost fields must stay zero so the JSON
// report path (which never calls buildGhostTimelines) cannot drift.
func TestBuildGhostTimelines_NoGhostsLeaveReportFieldsZero(t *testing.T) {
	base := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	events := []lifecycle.Event{
		{Seq: 1, Timestamp: base, Kind: lifecycle.KindTranscriptNew, SessionID: "s", Adapter: "claude-code"},
		{Seq: 2, Timestamp: base.Add(time.Second), Kind: lifecycle.KindPIDDiscovered, SessionID: "s", PID: 99},
		{Seq: 3, Timestamp: base.Add(2 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "s",
			PrevState: session.StateReady, NewState: session.StateWorking, Reason: "transcript activity"},
	}
	// Standard (report) path must never set ghost fields.
	for _, tl := range buildSessionTimelines(events) {
		if tl.IsGhost || tl.RemovalReason != "" || tl.RemovedAt != nil || tl.LifetimeMs != 0 || tl.FinalReason != "" || tl.HadSubstantive {
			t.Errorf("report-path timeline carries ghost fields: %+v", tl)
		}
	}
}

// TestBuildGhostTimelines_SubagentNotGhost guards the child exclusion: a
// completed subagent (ParentSessionID set) reaped via "parent deleted" — no PID
// and no working/waiting transition of its own — must NOT be flagged a ghost,
// even though it has the same removed && !substantive shape as a real ghost.
func TestBuildGhostTimelines_SubagentNotGhost(t *testing.T) {
	base := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	events := []lifecycle.Event{
		{Seq: 1, Timestamp: base, Kind: lifecycle.KindTranscriptNew, SessionID: "child", Adapter: "claude-code"},
		{Seq: 2, Timestamp: base.Add(time.Second), Kind: lifecycle.KindParentLinked, SessionID: "child", ParentSessionID: "parent"},
		{Seq: 3, Timestamp: base.Add(2 * time.Second), Kind: lifecycle.KindStateTransition, SessionID: "child",
			PrevState: session.StateWorking, NewState: session.StateReady, Reason: "subagent completed (parent task-notification)"},
		{Seq: 4, Timestamp: base.Add(3 * time.Second), Kind: lifecycle.KindTranscriptRemoved, SessionID: "child", Reason: "parent deleted"},
	}
	for _, tl := range buildGhostTimelines(events) {
		if tl.SessionID == "child" && tl.IsGhost {
			t.Errorf("completed subagent (ParentSessionID set) must not be flagged IsGhost")
		}
	}
}
