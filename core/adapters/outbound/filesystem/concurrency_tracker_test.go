package filesystem

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// seedRecording writes lifecycle events to <dir>/<name> in the JSONL shape the
// recorder produces, so AgentsSeries reads them back the same way it reads a
// real recording.
func seedRecording(t *testing.T, dir, name string, events []lifecycle.Event) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

// transcriptNew is a session-appearance event carrying the CWD (the source of
// the project label).
func transcriptNew(seq, ts int64, sid, cwd string) lifecycle.Event {
	return lifecycle.Event{Seq: seq, Timestamp: time.Unix(ts, 0), Kind: lifecycle.KindTranscriptNew, SessionID: sid, CWD: cwd}
}

// activity is a transcript-activity event (no state change), used to push a
// session's last-event timestamp forward.
func activity(seq, ts int64, sid, cwd string) lifecycle.Event {
	return lifecycle.Event{Seq: seq, Timestamp: time.Unix(ts, 0), Kind: lifecycle.KindTranscriptActivity, SessionID: sid, CWD: cwd}
}

func transition(seq, ts int64, sid, newState string) lifecycle.Event {
	return lifecycle.Event{Seq: seq, Timestamp: time.Unix(ts, 0), Kind: lifecycle.KindStateTransition, SessionID: sid, NewState: newState}
}

func processExited(seq, ts int64, sid string) lifecycle.Event {
	return lifecycle.Event{Seq: seq, Timestamp: time.Unix(ts, 0), Kind: lifecycle.KindProcessExited, SessionID: sid}
}

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestAgentsSeries_SingleSession: one session active [100,200) in a 300-wide
// window bucketed at 60s lands its peak (1) in the buckets it overlaps, with an
// exact time-weighted average and no lingering "current".
func TestAgentsSeries_SingleSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 200, "s1", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 1 {
		t.Errorf("peak: want 1, got %v", res.Peak)
	}
	if res.Current != 0 {
		t.Errorf("current: want 0 (ended before window end), got %v", res.Current)
	}
	if !approxEq(res.Average, 100.0/300.0) {
		t.Errorf("average: want %.6f, got %v", 100.0/300.0, res.Average)
	}
	// Buckets: 0=[0,60) 1=[60,120) 2=[120,180) 3=[180,240) 4=[240,300).
	// Interval [100,200) covers buckets 1,2,3.
	want := []float64{0, 1, 1, 1, 0}
	got := res.ByKey["projX"]
	if len(got) != len(want) {
		t.Fatalf("byKey[projX]: want len %d, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("byKey[projX][%d]: want %v, got %v", i, want[i], got[i])
		}
	}
	if res.PeakByKey["projX"] != 1 {
		t.Errorf("peakByKey[projX]: want 1, got %v", res.PeakByKey["projX"])
	}
}

// TestAgentsSeries_OverlapSameProject: two sessions in the same project that
// overlap drive the total peak to 2.
func TestAgentsSeries_OverlapSameProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 250, "s1", session.StateReady),
		transcriptNew(4, 140, "s2", "/home/me/projX"),
		transition(5, 150, "s2", session.StateWorking),
		transition(6, 300, "s2", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 360, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 2 {
		t.Errorf("peak: want 2 (overlap [150,250)), got %v", res.Peak)
	}
	if res.PeakByKey["projX"] != 2 {
		t.Errorf("peakByKey[projX]: want 2, got %v", res.PeakByKey["projX"])
	}
}

// TestAgentsSeries_IdleWaitingCounts: a session that finished its turn and is
// idling in waiting — its last recorded event being the waiting transition, with
// no later activity — still contributes its bounded active span. Regression for
// the dangling-bound guard dropping the whole span when the trailing state is
// active.
func TestAgentsSeries_IdleWaitingCounts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 200, "s1", session.StateWaiting), // last event; idle thereafter
	})
	tr := NewConcurrencyTrackerWithDir(dir)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 1 {
		t.Errorf("peak: want 1 (active [100,200) bounded at last event), got %v", res.Peak)
	}
	if res.PeakByKey["projX"] != 1 {
		t.Errorf("peakByKey[projX]: want 1, got %v", res.PeakByKey["projX"])
	}
	// Last known alive at 200 < window end 300 → not counted as current.
	if res.Current != 0 {
		t.Errorf("current: want 0, got %v", res.Current)
	}
}

// TestAgentsSeries_ExitAtEndNotCurrent: a session that goes ready exactly at the
// window end is not "current" — it terminated, it isn't active now.
func TestAgentsSeries_ExitAtEndNotCurrent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 300, "s1", session.StateReady), // ready exactly at End
	})
	tr := NewConcurrencyTrackerWithDir(dir)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Current != 0 {
		t.Errorf("current: want 0 (terminated at End), got %v", res.Current)
	}
	if res.Peak != 1 {
		t.Errorf("peak: want 1, got %v", res.Peak)
	}
}

// TestAgentsSeries_TwoProjects: overlapping sessions in different projects sum
// to a total peak of 2 while each project's own peak stays 1.
func TestAgentsSeries_TwoProjects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projA"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 250, "s1", session.StateReady),
		transcriptNew(4, 140, "s2", "/home/me/projB"),
		transition(5, 150, "s2", session.StateWorking),
		transition(6, 300, "s2", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 360, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 2 {
		t.Errorf("total peak: want 2, got %v", res.Peak)
	}
	if res.PeakByKey["projA"] != 1 || res.PeakByKey["projB"] != 1 {
		t.Errorf("per-project peaks: want 1/1, got %v/%v", res.PeakByKey["projA"], res.PeakByKey["projB"])
	}
}

// TestAgentsSeries_CurrentReachesEnd: a session still active at the window's end
// (no exit recorded, but later activity) is bounded at its last event and, when
// that reaches the window end, counts toward "current".
func TestAgentsSeries_CurrentReachesEnd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 240, "s1", "/home/me/projX"),
		transition(2, 250, "s1", session.StateWorking),
		activity(3, 350, "s1", "/home/me/projX"), // last event after window end
	})
	tr := NewConcurrencyTrackerWithDir(dir)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 1 {
		t.Errorf("peak: want 1, got %v", res.Peak)
	}
	if res.Current != 1 {
		t.Errorf("current: want 1 (active at window end), got %v", res.Current)
	}
}

// TestAgentsSeries_ProcessExitEndsConcurrency: a process_exited event ends the
// active interval even with no explicit state_transition to ready.
func TestAgentsSeries_ProcessExitEndsConcurrency(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		processExited(3, 160, "s1"),
		activity(4, 400, "s1", "/home/me/projX"), // stray late event must not revive it
	})
	tr := NewConcurrencyTrackerWithDir(dir)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 480, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 1 {
		t.Errorf("peak: want 1, got %v", res.Peak)
	}
	if res.Current != 0 {
		t.Errorf("current: want 0 (exited at 160), got %v", res.Current)
	}
	if !approxEq(res.Average, 60.0/480.0) {
		t.Errorf("average: want %.6f (active [100,160)), got %v", 60.0/480.0, res.Average)
	}
}

// TestAgentsSeries_Empty: a missing recordings dir yields a valid empty result,
// not an error (the common case — --record is opt-in).
func TestAgentsSeries_Empty(t *testing.T) {
	tr := NewConcurrencyTrackerWithDir(filepath.Join(t.TempDir(), "recordings"))
	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 0 || res.Average != 0 || res.Current != 0 {
		t.Errorf("want zero summary, got peak=%v avg=%v current=%v", res.Peak, res.Average, res.Current)
	}
	if len(res.ByKey) != 0 || len(res.PeakByKey) != 0 {
		t.Errorf("want empty maps, got byKey=%v peakByKey=%v", res.ByKey, res.PeakByKey)
	}
	if res.BucketStarts == nil {
		t.Error("bucket_starts should be a non-nil slice")
	}
}

// TestAgentsSeries_ScopeProject: a project scope filters to that project's
// sessions only.
func TestAgentsSeries_ScopeProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projA"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 250, "s1", session.StateReady),
		transcriptNew(4, 90, "s2", "/home/me/projB"),
		transition(5, 100, "s2", session.StateWorking),
		transition(6, 250, "s2", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 360, BucketSeconds: 60, ScopeField: "project", ScopeValue: "projA"})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 1 {
		t.Errorf("scoped peak: want 1, got %v", res.Peak)
	}
	if _, ok := res.PeakByKey["projB"]; ok {
		t.Errorf("projB should be filtered out, got %v", res.PeakByKey)
	}
	if res.PeakByKey["projA"] != 1 {
		t.Errorf("peakByKey[projA]: want 1, got %v", res.PeakByKey["projA"])
	}
}
