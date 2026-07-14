package filesystem

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/git"
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
	tr := NewConcurrencyTrackerWithDir(dir, nil)

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
	tr := NewConcurrencyTrackerWithDir(dir, nil)

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
	tr := NewConcurrencyTrackerWithDir(dir, nil)

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
	tr := NewConcurrencyTrackerWithDir(dir, nil)

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
	tr := NewConcurrencyTrackerWithDir(dir, nil)

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
	tr := NewConcurrencyTrackerWithDir(dir, nil)

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
	tr := NewConcurrencyTrackerWithDir(dir, nil)

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
	tr := NewConcurrencyTrackerWithDir(filepath.Join(t.TempDir(), "recordings"), nil)
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

// TestAgentsSeries_SoleTransitionStillCounts: a session whose only transition
// is its (still-active) working transition — with no later heartbeat, so
// lastEventTS lands exactly on that transition's own timestamp — must still
// contribute an interval instead of vanishing. Regression for #983: the
// synthetic "last known alive" bound used to collapse to a zero-width
// interval in exactly this case, and the loop building intervals silently
// dropped it.
func TestAgentsSeries_SoleTransitionStillCounts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projY"),
		transition(2, 100, "s1", session.StateWorking), // last event; no later activity
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if res.Peak != 1 {
		t.Errorf("peak: want 1, got %v", res.Peak)
	}
	if res.PeakByKey["projY"] != 1 {
		t.Errorf("peakByKey[projY]: want 1, got %v", res.PeakByKey["projY"])
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
	tr := NewConcurrencyTrackerWithDir(dir, nil)

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

// TestStateSeries_ActiveAtEndIsNotReady: a session still working at the
// window end (no ready transition recorded) must not count toward the ready
// histogram — the "last known alive" bound that closes its working interval
// is a synthetic sentinel, not a real transition. Regression: an earlier
// version leaked that sentinel into readyAt, so every still-active session
// spuriously showed a ready count in whatever bucket contained "now".
func TestStateSeries_ActiveAtEndIsNotReady(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		activity(3, 280, "s1", "/home/me/projX"), // last event; still working, no exit
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)

	res, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	if got := res.ByState[session.StateReady]["projX"]; got != nil {
		t.Errorf("ready[projX]: want no ready series (session never went ready), got %v", got)
	}
	if peak := maxOf(res.ByState[session.StateWorking]["projX"]); peak != 1 {
		t.Errorf("working[projX] peak: want 1, got %v", peak)
	}
}

// TestStateSeries_WorkingThenWaiting: a session works [100,150) then idles
// waiting [150,220) before going ready — the per-state split AgentsSeries
// can't make. Working and waiting must land in their own buckets, and ready
// must count once in the bucket containing the ready transition (220).
func TestStateSeries_WorkingThenWaiting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 150, "s1", session.StateWaiting),
		transition(4, 220, "s1", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)

	res, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	// Buckets: 0=[0,60) 1=[60,120) 2=[120,180) 3=[180,240) 4=[240,300).
	// Working [100,150) covers buckets 1,2. Waiting [150,220) covers buckets 2,3.
	wantWorking := []float64{0, 1, 1, 0, 0}
	wantWaiting := []float64{0, 0, 1, 1, 0}
	if got := res.ByState[session.StateWorking]["projX"]; !floatsEq(got, wantWorking) {
		t.Errorf("working[projX]: want %v, got %v", wantWorking, got)
	}
	if got := res.ByState[session.StateWaiting]["projX"]; !floatsEq(got, wantWaiting) {
		t.Errorf("waiting[projX]: want %v, got %v", wantWaiting, got)
	}
	// Ready transition at ts=220 falls in bucket 3 ([180,240)).
	wantReady := []float64{0, 0, 0, 1, 0}
	if got := res.ByState[session.StateReady]["projX"]; !floatsEq(got, wantReady) {
		t.Errorf("ready[projX]: want %v, got %v", wantReady, got)
	}
}

// TestStateSeries_ReadyIsTransitionCount: two sessions going ready in the same
// bucket both count, distinguishing the ready histogram from a concurrency
// count (which would never exceed however many sessions exist at once, but
// specifically confirms multiple transitions accumulate rather than the last
// one winning).
func TestStateSeries_ReadyIsTransitionCount(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 130, "s1", session.StateReady),
		transcriptNew(4, 90, "s2", "/home/me/projX"),
		transition(5, 95, "s2", session.StateWorking),
		transition(6, 140, "s2", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)

	res, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 180, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	// Both ready transitions (130, 140) land in bucket 2 ([120,180)).
	want := []float64{0, 0, 2}
	if got := res.ByState[session.StateReady]["projX"]; !floatsEq(got, want) {
		t.Errorf("ready[projX]: want %v, got %v", want, got)
	}
}

// TestStateSeries_TwoProjectsIsolated: each project's per-state series stays
// keyed to its own project — no cross-contamination.
func TestStateSeries_TwoProjectsIsolated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projA"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 250, "s1", session.StateReady),
		transcriptNew(4, 90, "s2", "/home/me/projB"),
		transition(5, 100, "s2", session.StateWaiting),
		transition(6, 250, "s2", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)

	res, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 360, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	if v := res.ByState[session.StateWorking]["projB"]; v != nil {
		t.Errorf("projB should have no working series, got %v", v)
	}
	if v := res.ByState[session.StateWaiting]["projA"]; v != nil {
		t.Errorf("projA should have no waiting series, got %v", v)
	}
	if peak := maxOf(res.ByState[session.StateWorking]["projA"]); peak != 1 {
		t.Errorf("projA working peak: want 1, got %v", peak)
	}
	if peak := maxOf(res.ByState[session.StateWaiting]["projB"]); peak != 1 {
		t.Errorf("projB waiting peak: want 1, got %v", peak)
	}
}

// TestStateSeries_ScopeProject: a project scope filters to that project's
// sessions only, matching AgentsSeries' scoping convention.
func TestStateSeries_ScopeProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projA"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 250, "s1", session.StateReady),
		transcriptNew(4, 90, "s2", "/home/me/projB"),
		transition(5, 100, "s2", session.StateWorking),
		transition(6, 250, "s2", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)

	res, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 360, BucketSeconds: 60, ScopeField: "project", ScopeValue: "projA"})
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	if _, ok := res.ByState[session.StateWorking]["projB"]; ok {
		t.Errorf("projB should be filtered out, got %v", res.ByState[session.StateWorking])
	}
	if peak := maxOf(res.ByState[session.StateWorking]["projA"]); peak != 1 {
		t.Errorf("projA working peak: want 1, got %v", peak)
	}
}

// TestStateSeries_Empty: a missing recordings dir yields a valid empty
// result, not an error — same convention as AgentsSeries.
func TestStateSeries_Empty(t *testing.T) {
	tr := NewConcurrencyTrackerWithDir(filepath.Join(t.TempDir(), "recordings"), nil)
	res, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	if res.Peak != 0 || res.Average != 0 || res.Current != 0 {
		t.Errorf("want zero summary, got peak=%v avg=%v current=%v", res.Peak, res.Average, res.Current)
	}
	for _, state := range []string{session.StateWorking, session.StateWaiting, session.StateReady} {
		if len(res.ByState[state]) != 0 {
			t.Errorf("by_state[%s] should be empty, got %v", state, res.ByState[state])
		}
	}
	if res.BucketStarts == nil {
		t.Error("bucket_starts should be a non-nil slice")
	}
}

// TestStateSeries_SoleTransitionStillCounts: StateSeries' counterpart of
// TestAgentsSeries_SoleTransitionStillCounts — a session with exactly one
// recorded transition and no later event must still show up in the working
// series instead of vanishing entirely (#983).
func TestStateSeries_SoleTransitionStillCounts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projY"),
		transition(2, 100, "s1", session.StateWorking), // last event; no later activity
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)

	res, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	if peak := maxOf(res.ByState[session.StateWorking]["projY"]); peak != 1 {
		t.Errorf("working[projY] peak: want 1, got %v", peak)
	}
	if res.Peak != 1 {
		t.Errorf("peak: want 1, got %v", res.Peak)
	}
}

// TestStateSeries_SummaryMatchesAgentsSeries: StateSeries' Peak/Average/Current
// summary (computed over its own per-state interval reconstruction) must
// agree exactly with AgentsSeries' merged-state summary for the same query —
// both are built from the same underlying transitions, just split
// differently, and sweepIntervals treats abutting per-state sub-intervals as
// continuous (see stateReconstruction / activeIntervals), so they must not
// diverge.
func TestStateSeries_SummaryMatchesAgentsSeries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projA"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 150, "s1", session.StateWaiting), // mid-session state flip
		transition(4, 250, "s1", session.StateReady),
		transcriptNew(5, 140, "s2", "/home/me/projA"),
		transition(6, 145, "s2", session.StateWorking),
		activity(7, 280, "s2", "/home/me/projA"), // still active at window end
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)
	q := outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60}

	agents, err := tr.AgentsSeries(q)
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	states, err := tr.StateSeries(q)
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	if !approxEq(agents.Peak, states.Peak) {
		t.Errorf("peak mismatch: agents=%v states=%v", agents.Peak, states.Peak)
	}
	if !approxEq(agents.Average, states.Average) {
		t.Errorf("average mismatch: agents=%v states=%v", agents.Average, states.Average)
	}
	if agents.Current != states.Current {
		t.Errorf("current mismatch: agents=%v states=%v", agents.Current, states.Current)
	}
}

// floatsEq compares two float64 slices for exact equality (the values under
// test here are all small integer counts, so no tolerance is needed).
func floatsEq(got, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func maxOf(vs []float64) float64 {
	m := 0.0
	for _, v := range vs {
		if v > m {
			m = v
		}
	}
	return m
}

// TestConcurrencyProject_NoCWDLeftRaw: a session whose events never carry a
// CWD is keyed by the raw "" project, not a literal "unknown" — the eager
// substitution moved out of this package to handlers.go's share-based
// resolveUnknownConcurrencyProject/resolveUnknownStateProject (#1046), so the
// tracker itself must behave exactly like CostSeriesResult here.
func TestConcurrencyProject_NoCWDLeftRaw(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		{Seq: 1, Timestamp: time.Unix(90, 0), Kind: lifecycle.KindTranscriptNew, SessionID: "s1"}, // no CWD
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 200, "s1", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(dir, nil)

	agents, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if _, ok := agents.PeakByKey["unknown"]; ok {
		t.Errorf("AgentsSeries must not label a CWD-less session \"unknown\" itself, got keys %+v", agents.PeakByKey)
	}
	if _, ok := agents.PeakByKey[""]; !ok {
		t.Errorf("AgentsSeries should key a CWD-less session by raw \"\", got %+v", agents.PeakByKey)
	}

	states, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	if _, ok := states.ByState[session.StateWorking]["unknown"]; ok {
		t.Errorf("StateSeries must not label a CWD-less session \"unknown\" itself, got %+v", states.ByState[session.StateWorking])
	}
	if _, ok := states.ByState[session.StateWorking][""]; !ok {
		t.Errorf("StateSeries should key a CWD-less session by raw \"\", got %+v", states.ByState[session.StateWorking])
	}
}

// gitInitRepo creates a minimal git repo at a fresh temp dir (no commits
// needed — GetGitRoot only requires a valid .git, not any history).
func gitInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return dir
}

// TestConcurrencyProject_WorktreeFoldsIntoRealRepo: a session cwd'd into a
// path shaped like the ir:exec skill's .claude/worktrees/<N>-<slug>/ dir
// resolves (via the injected git.Adapter) to its parent repo's name, not the
// worktree directory's own basename — the #1046 root-cause fix.
func TestConcurrencyProject_WorktreeFoldsIntoRealRepo(t *testing.T) {
	repoDir := gitInitRepo(t)
	repoName := filepath.Base(repoDir)
	worktreeDir := filepath.Join(repoDir, ".claude", "worktrees", "1046-activity-matrix-cleanup")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree dir: %v", err)
	}

	recDir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, recDir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", worktreeDir),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 200, "s1", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(recDir, git.New())

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if _, ok := res.PeakByKey[repoName]; !ok {
		t.Errorf("want session folded into repo project %q, got keys %+v", repoName, res.PeakByKey)
	}
	if _, ok := res.PeakByKey["1046-activity-matrix-cleanup"]; ok {
		t.Errorf("worktree dir name should not appear as its own project, got %+v", res.PeakByKey)
	}
}

// TestConcurrencyProject_DeletedWorktreeStillFoldsIn: the same fold-in must
// still work when the worktree directory was never created (or has since been
// `git worktree remove`'d) — GetGitRoot walks up to the nearest existing
// ancestor before resolving, so this doesn't depend on the exact leaf dir
// still being on disk (the common case for old ir:exec worktrees by the time
// anyone looks at the Activity Matrix chart).
func TestConcurrencyProject_DeletedWorktreeStillFoldsIn(t *testing.T) {
	repoDir := gitInitRepo(t)
	repoName := filepath.Base(repoDir)
	// Note: never created on disk.
	worktreeDir := filepath.Join(repoDir, ".claude", "worktrees", "1018-onboa-followups")

	recDir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, recDir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", worktreeDir),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 200, "s1", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(recDir, git.New())

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if _, ok := res.PeakByKey[repoName]; !ok {
		t.Errorf("want session folded into repo project %q even with a non-existent worktree dir, got keys %+v", repoName, res.PeakByKey)
	}
}

// TestConcurrencyProject_NilResolverFallsBackToBasename: without a resolver
// (nil), the same worktree CWD keys by its own bare basename — the pre-#1046
// behavior — confirming the fold-in is specifically the resolver's doing, not
// some incidental effect of the directory layout.
func TestConcurrencyProject_NilResolverFallsBackToBasename(t *testing.T) {
	repoDir := gitInitRepo(t)
	worktreeDir := filepath.Join(repoDir, ".claude", "worktrees", "1046-activity-matrix-cleanup")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree dir: %v", err)
	}

	recDir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, recDir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", worktreeDir),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 200, "s1", session.StateReady),
	})
	tr := NewConcurrencyTrackerWithDir(recDir, nil)

	res, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60})
	if err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if _, ok := res.PeakByKey["1046-activity-matrix-cleanup"]; !ok {
		t.Errorf("nil resolver should fall back to bare basename, got keys %+v", res.PeakByKey)
	}
}

// countingResolver counts GetProjectName calls per distinct dir, so
// TestConcurrencyProject_MemoizesAcrossRequests can assert the tracker resolves
// each raw CWD once for its lifetime rather than once per event or request.
type countingResolver struct {
	mu    sync.Mutex
	calls map[string]int
	delay time.Duration
}

func (r *countingResolver) GetProjectName(dir string) string {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[dir]++
	delay := r.delay
	r.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return filepath.Base(dir)
}

func (r *countingResolver) Calls(dir string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[dir]
}

// TestConcurrencyProject_MemoizesAcrossRequests: two chart reads over sessions
// sharing CWDs must resolve each CWD once for the tracker lifetime. That keeps
// dashboard polling from re-running git rev-parse for every historical CWD.
func TestConcurrencyProject_MemoizesAcrossRequests(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		activity(2, 95, "s1", "/home/me/projX"),
		transition(3, 100, "s1", session.StateWorking),
		activity(4, 110, "s1", "/home/me/projX"),
		transition(5, 200, "s1", session.StateReady),
		transcriptNew(6, 140, "s2", "/home/me/projX"),
		transition(7, 150, "s2", session.StateWorking),
		transition(8, 250, "s2", session.StateReady),
		transcriptNew(9, 90, "s3", "/home/me/projY"),
		transition(10, 100, "s3", session.StateWorking),
		transition(11, 200, "s3", session.StateReady),
	})
	resolver := &countingResolver{}
	tr := NewConcurrencyTrackerWithDir(dir, resolver)

	if _, err := tr.AgentsSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60}); err != nil {
		t.Fatalf("AgentsSeries: %v", err)
	}
	if _, err := tr.StateSeries(outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60}); err != nil {
		t.Fatalf("StateSeries: %v", err)
	}
	if got := resolver.Calls("/home/me/projX"); got != 1 {
		t.Errorf("projX shared across 2 sessions/5 CWD-carrying events: want 1 resolver call, got %d", got)
	}
	if got := resolver.Calls("/home/me/projY"); got != 1 {
		t.Errorf("projY: want 1 resolver call, got %d", got)
	}
}

func TestConcurrencyProject_MemoizesConcurrentRequests(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	seedRecording(t, dir, "run.jsonl", []lifecycle.Event{
		transcriptNew(1, 90, "s1", "/home/me/projX"),
		transition(2, 100, "s1", session.StateWorking),
		transition(3, 200, "s1", session.StateReady),
	})
	resolver := &countingResolver{delay: 20 * time.Millisecond}
	tr := NewConcurrencyTrackerWithDir(dir, resolver)
	q := outbound.SeriesQuery{Start: 0, End: 300, BucketSeconds: 60}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := tr.AgentsSeries(q)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := tr.StateSeries(q)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent chart read: %v", err)
		}
	}
	if got := resolver.Calls("/home/me/projX"); got != 1 {
		t.Errorf("concurrent chart reads: want 1 resolver call, got %d", got)
	}
}
