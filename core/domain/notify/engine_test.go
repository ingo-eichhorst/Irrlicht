package notify

import (
	"testing"
	"time"
)

// t0 is an arbitrary fixed instant; every test advances from it so failure
// output reads as offsets.
var t0 = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

func upd(id string, st State) Event {
	return Event{Kind: EventSessionUpdate, SessionID: id, State: st}
}

func updL(id string, st State, label string) Event {
	ev := upd(id, st)
	ev.Label = label
	return ev
}

func wantNone(t *testing.T, got []Push, context string) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("%s: want no pushes, got %d: %+v", context, len(got), got)
	}
}

func wantOne(t *testing.T, got []Push, context string) Push {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%s: want exactly one push, got %d: %+v", context, len(got), got)
	}
	return got[0]
}

func TestDefaultConfigMatchesPolicyTable(t *testing.T) {
	got := DefaultConfig()
	want := Config{
		HoldDown:       7 * time.Second,
		Cooldown:       60 * time.Second,
		BurstWindow:    20 * time.Second,
		BurstThreshold: 3,
		TTLWaiting:     time.Hour,
		TTLReady:       10 * time.Minute,
		TTLDaemon:      10 * time.Minute,
		DaemonGrace:    60 * time.Second,
	}
	if got != want {
		t.Fatalf("DefaultConfig() = %+v, want %+v", got, want)
	}
}

func TestNewFillsZeroFieldsWithDefaults(t *testing.T) {
	e := New(Config{HoldDown: 3 * time.Second})
	if e.cfg.HoldDown != 3*time.Second {
		t.Fatalf("explicit HoldDown overridden: %v", e.cfg.HoldDown)
	}
	def := DefaultConfig()
	def.HoldDown = 3 * time.Second
	if e.cfg != def {
		t.Fatalf("zero fields not defaulted: %+v", e.cfg)
	}
}

func TestWaitingPushesImmediately(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(Event{Kind: EventSessionUpdate, SessionID: "s1", State: StateWorking, Label: "lbl", Project: "proj"}, t0), "seed")
	p := wantOne(t, e.Handle(Event{Kind: EventSessionUpdate, SessionID: "s1", State: StateWaiting, Label: "lbl", Project: "proj"}, t0.Add(time.Second)), "waiting edge")
	if p.Topic != "s1" || p.TTL != time.Hour || p.Urgency != UrgencyHigh || !p.Renotify {
		t.Fatalf("waiting push headers wrong: %+v", p)
	}
	want := Payload{Version: 1, Kind: PushSession, SessionID: "s1", Label: "lbl", Project: "proj", State: StateWaiting, At: t0.Add(time.Second).Unix()}
	if p.Payload.Version != want.Version || p.Payload.Kind != want.Kind || p.Payload.SessionID != want.SessionID ||
		p.Payload.Label != want.Label || p.Payload.Project != want.Project || p.Payload.State != want.State || p.Payload.At != want.At {
		t.Fatalf("waiting payload = %+v, want %+v", p.Payload, want)
	}
}

func TestReadyPushesAfterHoldDown(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantNone(t, e.Handle(upd("s1", StateReady), t0), "entering ready must not push before the hold-down")
	wantNone(t, e.Tick(t0.Add(7*time.Second-time.Millisecond)), "one ms early")
	p := wantOne(t, e.Tick(t0.Add(7*time.Second)), "hold-down due")
	if p.Topic != "s1" || p.TTL != 10*time.Minute || p.Urgency != UrgencyNormal || !p.Renotify {
		t.Fatalf("ready push headers wrong: %+v", p)
	}
	if p.Payload.Kind != PushSession || p.Payload.State != StateReady || p.Payload.At != t0.Add(7*time.Second).Unix() {
		t.Fatalf("ready payload wrong: %+v", p.Payload)
	}
}

func TestEnteringWorkingNeverPushes(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWaiting), t0), "seed waiting (first sighting)")
	wantNone(t, e.Handle(upd("s1", StateWorking), t0.Add(time.Second)), "waiting -> working")
	wantNone(t, e.Handle(upd("s2", StateReady), t0), "seed ready (first sighting)")
	wantNone(t, e.Handle(upd("s2", StateWorking), t0.Add(time.Second)), "ready -> working")
	wantNone(t, e.Tick(t0.Add(2*time.Minute)), "nothing pending afterwards")
}

func TestFirstSightingIsSilent(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("w", StateWorking), t0), "first sighting working")
	wantNone(t, e.Handle(upd("a", StateWaiting), t0), "first sighting waiting")
	wantNone(t, e.Handle(upd("r", StateReady), t0), "first sighting ready")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("first sighting in ready must not arm a hold-down")
	}
	wantNone(t, e.Tick(t0.Add(time.Minute)), "nothing fires later either")
}

func TestSameStateUpdateIsNoOp(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantOne(t, e.Handle(upd("s1", StateWaiting), t0.Add(time.Second)), "the real edge")
	// The daemon emits session_updated on every metrics tick: a run of
	// same-state updates must stay silent, including one past the cooldown
	// (which would push if same-state re-ran the edge).
	wantNone(t, e.Handle(upd("s1", StateWaiting), t0.Add(2*time.Second)), "metrics tick 1")
	wantNone(t, e.Handle(upd("s1", StateWaiting), t0.Add(3*time.Second)), "metrics tick 2")
	wantNone(t, e.Handle(upd("s1", StateWaiting), t0.Add(70*time.Second)), "metrics tick past cooldown")
}

func TestSameStateUpdateRefreshesStoredLabel(t *testing.T) {
	e := New(Config{})
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		wantNone(t, e.Handle(updL(id, StateWorking, "old-"+id), t0), "seed "+id)
	}
	wantOne(t, e.Handle(updL("m1", StateWaiting, "old-m1"), t0.Add(time.Second)), "m1 edge")
	// Rule: a same-state update still refreshes label/project/parent. The
	// stored label is only observable through a later summary, which reads
	// the record rather than the triggering event.
	wantNone(t, e.Handle(updL("m1", StateWaiting, "NEW-m1"), t0.Add(2*time.Second)), "label refresh")
	wantOne(t, e.Handle(updL("m2", StateWaiting, "old-m2"), t0.Add(3*time.Second)), "m2 edge")
	wantOne(t, e.Handle(updL("m3", StateWaiting, "old-m3"), t0.Add(4*time.Second)), "m3 edge")
	p := wantOne(t, e.Handle(updL("m4", StateWaiting, "old-m4"), t0.Add(5*time.Second)), "m4 trips the summary")
	if p.Payload.Kind != PushSummary {
		t.Fatalf("4th candidate should summarize, got %+v", p)
	}
	found := false
	for _, l := range p.Payload.Sessions {
		if l == "NEW-m1" {
			found = true
		}
		if l == "old-m1" {
			t.Fatalf("summary carries the stale label: %v", p.Payload.Sessions)
		}
	}
	if !found {
		t.Fatalf("summary missing refreshed label NEW-m1: %v", p.Payload.Sessions)
	}
}

func TestHoldDownCancelledOnReWorking(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantNone(t, e.Handle(upd("s1", StateReady), t0.Add(time.Second)), "arm")
	wantNone(t, e.Handle(upd("s1", StateWorking), t0.Add(4*time.Second)), "re-working within hold-down")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("hold-down survived the re-working cancel")
	}
	wantNone(t, e.Tick(t0.Add(time.Minute)), "cancelled hold-down must never fire")
}

func TestHoldDownCancelledByWaiting(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantNone(t, e.Handle(upd("s1", StateReady), t0.Add(time.Second)), "arm")
	p := wantOne(t, e.Handle(upd("s1", StateWaiting), t0.Add(3*time.Second)), "waiting supersedes the pending ready")
	if p.Payload.State != StateWaiting {
		t.Fatalf("expected the waiting push, got %+v", p)
	}
	wantNone(t, e.Tick(t0.Add(time.Minute)), "the superseded hold-down must not fire")
}

func TestHoldDownSurvivesUnrelatedSessions(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed s1")
	wantNone(t, e.Handle(upd("s2", StateWorking), t0), "seed s2")
	wantNone(t, e.Handle(upd("s1", StateReady), t0), "arm s1")
	wantOne(t, e.Handle(upd("s2", StateWaiting), t0.Add(time.Second)), "s2 edge")
	e.Handle(Event{Kind: EventSessionDelete, SessionID: "s2"}, t0.Add(2*time.Second))
	p := wantOne(t, e.Tick(t0.Add(7*time.Second)), "s1 hold-down unaffected")
	if p.Payload.SessionID != "s1" || p.Payload.State != StateReady {
		t.Fatalf("wrong push: %+v", p)
	}
}

func TestHoldDownReplacedOnReArm(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantNone(t, e.Handle(upd("s1", StateReady), t0), "arm at t0")
	wantNone(t, e.Handle(upd("s1", State("compacting")), t0.Add(2*time.Second)), "unknown state leaves ready: cancels")
	wantNone(t, e.Handle(upd("s1", StateReady), t0.Add(4*time.Second)), "re-arm at t0+4")
	wantNone(t, e.Tick(t0.Add(7*time.Second)), "the original arm time must be gone")
	p := wantOne(t, e.Tick(t0.Add(11*time.Second)), "replacement fires 7s after the re-arm")
	if p.Payload.SessionID != "s1" || p.Payload.State != StateReady {
		t.Fatalf("wrong push: %+v", p)
	}
}

func TestUnknownStateValueIsSilent(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantNone(t, e.Handle(upd("s1", State("compacting")), t0.Add(time.Second)), "unknown state records silently")
	wantNone(t, e.Tick(t0.Add(2*time.Minute)), "and arms nothing")
	p := wantOne(t, e.Handle(upd("s1", StateWaiting), t0.Add(3*time.Minute)), "a genuine diff out of it notifies")
	if p.Payload.State != StateWaiting {
		t.Fatalf("wrong push: %+v", p)
	}
}

func TestSubagentNeverPushes(t *testing.T) {
	e := New(Config{})
	sub := func(st State) Event {
		return Event{Kind: EventSessionUpdate, SessionID: "sub1", ParentID: "parent1", State: st}
	}
	wantNone(t, e.Handle(sub(StateWorking), t0), "seed subagent")
	wantNone(t, e.Handle(sub(StateWaiting), t0.Add(time.Second)), "subagent waiting")
	wantNone(t, e.Handle(sub(StateWorking), t0.Add(2*time.Second)), "back to working")
	wantNone(t, e.Handle(sub(StateReady), t0.Add(3*time.Second)), "subagent ready")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("subagent ready must not arm a hold-down")
	}
	wantNone(t, e.Tick(t0.Add(time.Minute)), "nothing ever fires for a subagent")
}

func TestCooldownBoundary(t *testing.T) {
	t.Run("just_under_is_suppressed", func(t *testing.T) {
		e := New(Config{})
		wantNone(t, e.Handle(upd("a", StateWorking), t0), "seed")
		wantOne(t, e.Handle(upd("a", StateWaiting), t0), "first push stamps t0")
		wantNone(t, e.Handle(upd("a", StateWorking), t0.Add(time.Second)), "flap back")
		wantNone(t, e.Handle(upd("a", StateWaiting), t0.Add(60*time.Second-time.Millisecond)), "59.999s: suppressed")
	})
	t.Run("exactly_cooldown_is_allowed", func(t *testing.T) {
		e := New(Config{})
		wantNone(t, e.Handle(upd("b", StateWorking), t0), "seed")
		wantOne(t, e.Handle(upd("b", StateWaiting), t0), "first push stamps t0")
		wantNone(t, e.Handle(upd("b", StateWorking), t0.Add(time.Second)), "flap back")
		wantOne(t, e.Handle(upd("b", StateWaiting), t0.Add(60*time.Second)), "60s exactly: allowed")
	})
	t.Run("suppressed_candidate_does_not_refresh_the_stamp", func(t *testing.T) {
		e := New(Config{})
		wantNone(t, e.Handle(upd("c", StateWorking), t0), "seed")
		wantOne(t, e.Handle(upd("c", StateWaiting), t0), "first push stamps t0")
		wantNone(t, e.Handle(upd("c", StateWorking), t0.Add(30*time.Second)), "flap back")
		wantNone(t, e.Handle(upd("c", StateWaiting), t0.Add(50*time.Second)), "50s: suppressed")
		wantNone(t, e.Handle(upd("c", StateWorking), t0.Add(55*time.Second)), "flap back again")
		// 60s from the PUSH, only 10s from the suppressed candidate: if the
		// suppression had stamped, this would still be inside the cooldown.
		wantOne(t, e.Handle(upd("c", StateWaiting), t0.Add(60*time.Second)), "60s after the push: allowed")
	})
}

func TestCooldownIsPerEdge(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantOne(t, e.Handle(upd("s1", StateWaiting), t0), "waiting push stamps its own edge")
	wantNone(t, e.Handle(upd("s1", StateWorking), t0.Add(time.Second)), "back to working")
	wantNone(t, e.Handle(upd("s1", StateReady), t0.Add(2*time.Second)), "arm")
	p := wantOne(t, e.Tick(t0.Add(9*time.Second)), "ready fires inside the waiting edge's cooldown")
	if p.Payload.State != StateReady {
		t.Fatalf("wrong push: %+v", p)
	}
	// Same edge, though, is cooled down: another ready within 60s of the
	// ready push is suppressed at fire time.
	wantNone(t, e.Handle(upd("s1", StateWorking), t0.Add(10*time.Second)), "back to working")
	wantNone(t, e.Handle(upd("s1", StateReady), t0.Add(11*time.Second)), "re-arm")
	wantNone(t, e.Tick(t0.Add(18*time.Second)), "second ready inside its edge cooldown")
}

func TestBurstBoundary(t *testing.T) {
	e := New(Config{})
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9"} {
		wantNone(t, e.Handle(upd(id, StateWorking), t0.Add(-time.Second)), "seed "+id)
	}
	// Three candidates inside the window stay individual.
	for i, id := range []string{"s1", "s2", "s3"} {
		p := wantOne(t, e.Handle(upd(id, StateWaiting), t0.Add(time.Duration(i)*time.Second)), id)
		if p.Payload.Kind != PushSession || p.Topic != id {
			t.Fatalf("candidate %d should stay individual, got %+v", i+1, p)
		}
	}
	// The 4th becomes a summary, buzzing (first of the episode).
	p := wantOne(t, e.Handle(upd("s4", StateWaiting), t0.Add(3*time.Second)), "4th candidate")
	if p.Payload.Kind != PushSummary || p.Topic != "summary" || !p.Renotify || p.Payload.Count != 4 {
		t.Fatalf("4th candidate should be a renotifying summary of 4, got %+v", p)
	}
	// A 5th refreshes the summary silently.
	p = wantOne(t, e.Handle(upd("s5", StateWaiting), t0.Add(4*time.Second)), "5th candidate")
	if p.Payload.Kind != PushSummary || p.Renotify || p.Payload.Count != 5 {
		t.Fatalf("5th candidate should be a silent summary refresh of 5, got %+v", p)
	}
	// Once the window drains, a candidate pushes individually again and
	// re-arms the next episode's buzz.
	p = wantOne(t, e.Handle(upd("s6", StateWaiting), t0.Add(30*time.Second)), "after the window drained")
	if p.Payload.Kind != PushSession {
		t.Fatalf("drained window should push individually, got %+v", p)
	}
	wantOne(t, e.Handle(upd("s7", StateWaiting), t0.Add(31*time.Second)), "s7")
	wantOne(t, e.Handle(upd("s8", StateWaiting), t0.Add(32*time.Second)), "s8")
	p = wantOne(t, e.Handle(upd("s9", StateWaiting), t0.Add(33*time.Second)), "next episode's first summary")
	if p.Payload.Kind != PushSummary || !p.Renotify {
		t.Fatalf("new episode's first summary should buzz again, got %+v", p)
	}
}

func TestBurstWindowPruneBoundary(t *testing.T) {
	seed := func(e *Engine) {
		for _, id := range []string{"s1", "s2", "s3", "s4"} {
			wantNone(t, e.Handle(upd(id, StateWorking), t0.Add(-time.Second)), "seed "+id)
		}
		for _, id := range []string{"s1", "s2", "s3"} {
			wantOne(t, e.Handle(upd(id, StateWaiting), t0), "candidate "+id)
		}
	}
	t.Run("entry_at_exactly_window_age_is_pruned", func(t *testing.T) {
		e := New(Config{})
		seed(e)
		p := wantOne(t, e.Handle(upd("s4", StateWaiting), t0.Add(20*time.Second)), "4th at exactly 20s")
		if p.Payload.Kind != PushSession {
			t.Fatalf("the t0 entries are exactly window-old and pruned; expected an individual push, got %+v", p)
		}
	})
	t.Run("entry_just_inside_the_window_counts", func(t *testing.T) {
		e := New(Config{})
		seed(e)
		p := wantOne(t, e.Handle(upd("s4", StateWaiting), t0.Add(20*time.Second-time.Millisecond)), "4th at 19.999s")
		if p.Payload.Kind != PushSummary {
			t.Fatalf("the t0 entries are still inside the window; expected a summary, got %+v", p)
		}
	})
}

func TestSummaryContent(t *testing.T) {
	e := New(Config{})
	labels := map[string]string{"m1": "z4", "m2": "z3", "m3": "z2", "m4": "z1", "m5": "", "m6": "", "m7": ""}
	for id, l := range labels {
		wantNone(t, e.Handle(updL(id, StateWorking, l), t0.Add(-time.Second)), "seed "+id)
	}
	var last Push
	for i, id := range []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7"} {
		got := e.Handle(updL(id, StateWaiting, labels[id]), t0.Add(time.Duration(i)*time.Second))
		last = wantOne(t, got, id)
	}
	if last.Payload.Kind != PushSummary || last.Payload.Count != 7 {
		t.Fatalf("7th candidate should summarize 7 distinct sessions, got %+v", last)
	}
	// Labels sorted with id fallback for the unlabeled, capped at 6:
	// sorted full list is [m5 m6 m7 z1 z2 z3 z4].
	want := []string{"m5", "m6", "m7", "z1", "z2", "z3"}
	if len(last.Payload.Sessions) != len(want) {
		t.Fatalf("summary sessions = %v, want %v", last.Payload.Sessions, want)
	}
	for i := range want {
		if last.Payload.Sessions[i] != want[i] {
			t.Fatalf("summary sessions = %v, want %v", last.Payload.Sessions, want)
		}
	}
}

func TestSnapshotReconcileOnlyDiffNotifies(t *testing.T) {
	e := New(Config{})
	for _, id := range []string{"a", "b", "c", "d"} {
		wantNone(t, e.Handle(upd(id, StateWorking), t0), "snapshot seed "+id)
	}
	// A reconnect replays the snapshot: three unchanged, one genuinely
	// different (§6.3). Only the diff notifies.
	var pushes []Push
	rt := t0.Add(10 * time.Second)
	pushes = append(pushes, e.Handle(upd("a", StateWorking), rt)...)
	pushes = append(pushes, e.Handle(upd("b", StateWorking), rt)...)
	pushes = append(pushes, e.Handle(upd("c", StateWorking), rt)...)
	pushes = append(pushes, e.Handle(upd("d", StateWaiting), rt)...)
	if len(pushes) != 1 || pushes[0].Payload.SessionID != "d" || pushes[0].Payload.State != StateWaiting {
		t.Fatalf("reconcile should notify exactly the one diff, got %+v", pushes)
	}
}

func TestDeleteCancelsHoldDown(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantNone(t, e.Handle(upd("s1", StateReady), t0), "arm")
	wantNone(t, e.Handle(Event{Kind: EventSessionDelete, SessionID: "s1"}, t0.Add(2*time.Second)), "delete is silent")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("delete must cancel the pending hold-down")
	}
	wantNone(t, e.Tick(t0.Add(time.Minute)), "deleted session's hold-down must not fire")
}

func TestDeleteForgetsCooldown(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantOne(t, e.Handle(upd("s1", StateWaiting), t0), "push stamps the cooldown")
	wantNone(t, e.Handle(Event{Kind: EventSessionDelete, SessionID: "s1"}, t0.Add(time.Second)), "delete")
	wantNone(t, e.Handle(upd("s1", StateWorking), t0.Add(2*time.Second)), "recreation is a first sighting")
	// 3s after the first push: inside the cooldown, but the delete forgot it.
	wantOne(t, e.Handle(upd("s1", StateWaiting), t0.Add(3*time.Second)), "recreated session pushes fresh")
}

func TestRekeyPreservesCooldown(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("proc-1", StateWorking), t0), "seed presession")
	wantOne(t, e.Handle(upd("proc-1", StateWaiting), t0.Add(time.Second)), "push stamps the presession")
	wantNone(t, e.Handle(upd("proc-1", StateWorking), t0.Add(2*time.Second)), "flap back")
	wantNone(t, e.Handle(Event{Kind: EventRekey, OldSessionID: "proc-1", SessionID: "real"}, t0.Add(3*time.Second)), "rekey is silent")
	// 4s after the presession's push: the moved stamp must still suppress
	// (#1002 class — the promotion must not restart cooldowns).
	wantNone(t, e.Handle(upd("real", StateWorking), t0.Add(4*time.Second)), "real id inherits state")
	wantNone(t, e.Handle(upd("real", StateWaiting), t0.Add(5*time.Second)), "inside the inherited cooldown")
	wantNone(t, e.Handle(upd("real", StateWorking), t0.Add(6*time.Second)), "flap back")
	p := wantOne(t, e.Handle(upd("real", StateWaiting), t0.Add(61*time.Second)), "cooldown over, counted from the presession's push")
	if p.Topic != "real" || p.Payload.SessionID != "real" {
		t.Fatalf("push must carry the new id, got %+v", p)
	}
}

func TestRekeyMovesHoldDownToNewID(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("proc-1", StateWorking), t0), "seed presession")
	wantNone(t, e.Handle(upd("proc-1", StateReady), t0.Add(time.Second)), "arm at t0+1")
	wantNone(t, e.Handle(Event{Kind: EventRekey, OldSessionID: "proc-1", SessionID: "real"}, t0.Add(3*time.Second)), "rekey")
	if wake, ok := e.NextWake(); !ok || !wake.Equal(t0.Add(8*time.Second)) {
		t.Fatalf("moved hold-down should still be pending for t0+8, got %v ok=%v", wake, ok)
	}
	p := wantOne(t, e.Tick(t0.Add(8*time.Second)), "fires under the new identity")
	if p.Topic != "real" || p.Payload.SessionID != "real" || p.Payload.State != StateReady {
		t.Fatalf("hold-down must fire under the NEW id/topic, got %+v", p)
	}
	wantNone(t, e.Handle(upd("proc-1", StateWorking), t0.Add(9*time.Second)), "old id is forgotten: a fresh first sighting")
}

func TestRekeyMergeKeepsNewerStateAndStamps(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("old", StateWorking), t0), "seed old")
	wantNone(t, e.Handle(upd("new", StateWorking), t0), "seed new")
	wantOne(t, e.Handle(upd("old", StateWaiting), t0.Add(time.Second)), "old stamps waiting at t0+1")
	wantNone(t, e.Handle(upd("old", StateWorking), t0.Add(2*time.Second)), "old back to working")
	wantOne(t, e.Handle(upd("new", StateWaiting), t0.Add(30*time.Second)), "new stamps waiting at t0+30")
	wantNone(t, e.Handle(upd("new", StateWorking), t0.Add(31*time.Second)), "new back to working")
	wantNone(t, e.Handle(Event{Kind: EventRekey, OldSessionID: "old", SessionID: "new"}, t0.Add(32*time.Second)), "merge")
	// The merged stamp is the max of both strands (t0+30): 35s later is
	// still cooled down, where old's own t0+1 stamp would already allow.
	wantNone(t, e.Handle(upd("new", StateWaiting), t0.Add(65*time.Second)), "inside the merged (max) cooldown")
	wantNone(t, e.Handle(upd("new", StateWorking), t0.Add(66*time.Second)), "flap back")
	wantOne(t, e.Handle(upd("new", StateWaiting), t0.Add(91*time.Second)), "61s past the max stamp: allowed")
}

func TestRekeyMergeLandingOffReadyCancelsHoldDown(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("old", StateWorking), t0), "seed old")
	wantNone(t, e.Handle(upd("old", StateReady), t0.Add(time.Second)), "old arms at t0+1")
	wantNone(t, e.Handle(upd("new", StateWorking), t0.Add(5*time.Second)), "new sighted working, later")
	wantNone(t, e.Handle(Event{Kind: EventRekey, OldSessionID: "old", SessionID: "new"}, t0.Add(6*time.Second)), "merge lands on working")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("a merge landing on a non-ready state must cancel the inherited hold-down")
	}
	wantNone(t, e.Tick(t0.Add(time.Minute)), "nothing fires for the merged identity")
}

func TestRekeyUnknownOldIsNoOp(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(Event{Kind: EventRekey, OldSessionID: "ghost", SessionID: "n1"}, t0), "unknown old id")
	// If the rekey had invented a record for n1, this update would be a
	// state change and push; a true no-op leaves it a first sighting.
	wantNone(t, e.Handle(upd("n1", StateWaiting), t0.Add(time.Second)), "still a silent first sighting")
}

func TestWatchdogFullCycle(t *testing.T) {
	e := New(Config{})
	down := Event{Kind: EventDaemonDown, DaemonID: "d1", DaemonLabel: "laptop"}
	up := Event{Kind: EventDaemonUp, DaemonID: "d1", DaemonLabel: "laptop"}
	wantNone(t, e.Handle(down, t0), "down arms the grace, no immediate push")
	wantNone(t, e.Tick(t0.Add(30*time.Second)), "mid-grace: still nothing")
	p := wantOne(t, e.Tick(t0.Add(60*time.Second)), "grace elapsed")
	if p.Topic != "daemon:d1" || p.TTL != 10*time.Minute || p.Urgency != UrgencyNormal || !p.Renotify {
		t.Fatalf("down push headers wrong: %+v", p)
	}
	if p.Payload.Kind != PushDaemonDown || p.Payload.DaemonID != "d1" || p.Payload.DaemonLabel != "laptop" {
		t.Fatalf("down payload wrong: %+v", p.Payload)
	}
	p = wantOne(t, e.Handle(up, t0.Add(70*time.Second)), "reconnect replaces the banner")
	if p.Payload.Kind != PushDaemonUp || p.Topic != "daemon:d1" || p.Renotify {
		t.Fatalf("up push must silently replace (Renotify=false), got %+v", p)
	}
	wantNone(t, e.Handle(up, t0.Add(71*time.Second)), "a second up has nothing to replace")
}

func TestWatchdogCancelledByQuickReconnect(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(Event{Kind: EventDaemonDown, DaemonID: "d1"}, t0), "down")
	wantNone(t, e.Handle(Event{Kind: EventDaemonUp, DaemonID: "d1"}, t0.Add(10*time.Second)), "up within grace cancels silently")
	wantNone(t, e.Tick(t0.Add(2*time.Minute)), "no late push either")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("cancelled grace still pending")
	}
}

func TestRepeatedDownsDoNotRearmGrace(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(Event{Kind: EventDaemonDown, DaemonID: "d1"}, t0), "down arms at t0")
	wantNone(t, e.Handle(Event{Kind: EventDaemonDown, DaemonID: "d1"}, t0.Add(30*time.Second)), "repeat down must not re-arm")
	// If the repeat had re-armed, the fire time would be t0+90.
	wantOne(t, e.Tick(t0.Add(60*time.Second)), "fires at the original t0+60")
}

func TestDaemonUpUnknownRecordsSilently(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(Event{Kind: EventDaemonUp, DaemonID: "d9", DaemonLabel: "vps"}, t0), "unknown daemon up")
	wantNone(t, e.Tick(t0.Add(2*time.Minute)), "and arms nothing")
}

func TestDaemonPushesBypassSessionCoalescing(t *testing.T) {
	// Short grace so cycles fit inside one session cooldown period.
	e := New(Config{DaemonGrace: time.Second})
	for _, id := range []string{"s1", "s2", "s3"} {
		wantNone(t, e.Handle(upd(id, StateWorking), t0), "seed "+id)
	}
	wantNone(t, e.Handle(Event{Kind: EventDaemonDown, DaemonID: "d1"}, t0), "down")
	p := wantOne(t, e.Tick(t0.Add(time.Second)), "down push")
	if p.Payload.Kind != PushDaemonDown {
		t.Fatalf("expected down push, got %+v", p)
	}
	// The daemon push must not occupy the burst window: three session
	// candidates right after it still push individually.
	for i, id := range []string{"s1", "s2", "s3"} {
		p := wantOne(t, e.Handle(upd(id, StateWaiting), t0.Add(time.Duration(2+i)*time.Second)), id)
		if p.Payload.Kind != PushSession {
			t.Fatalf("daemon push leaked into the burst window: %s got %+v", id, p)
		}
	}
	// And daemon cycles ignore the 60s cooldown: grace is their debounce.
	wantOne(t, e.Handle(Event{Kind: EventDaemonUp, DaemonID: "d1"}, t0.Add(6*time.Second)), "up push")
	wantNone(t, e.Handle(Event{Kind: EventDaemonDown, DaemonID: "d1"}, t0.Add(7*time.Second)), "down again")
	p = wantOne(t, e.Tick(t0.Add(8*time.Second)), "second down push, well inside 60s of the first")
	if p.Payload.Kind != PushDaemonDown {
		t.Fatalf("expected second down push, got %+v", p)
	}
}

func TestTTLUrgencyPerKind(t *testing.T) {
	e := New(Config{})
	for _, id := range []string{"w1", "r1", "b1", "b2", "b3", "b4"} {
		wantNone(t, e.Handle(upd(id, StateWorking), t0.Add(-time.Hour)), "seed "+id)
	}
	waiting := wantOne(t, e.Handle(upd("w1", StateWaiting), t0), "waiting")
	wantNone(t, e.Handle(upd("r1", StateReady), t0), "arm")
	ready := wantOne(t, e.Tick(t0.Add(7*time.Second)), "ready")
	// Summary needs a burst: four fresh candidates in-window.
	var summary Push
	for i, id := range []string{"b1", "b2", "b3", "b4"} {
		summary = wantOne(t, e.Handle(upd(id, StateWaiting), t0.Add(time.Duration(8+i)*time.Second)), id)
	}
	wantNone(t, e.Handle(Event{Kind: EventDaemonDown, DaemonID: "d1"}, t0.Add(20*time.Second)), "down")
	daemonDown := wantOne(t, e.Tick(t0.Add(80*time.Second)), "daemon down")
	daemonUp := wantOne(t, e.Handle(Event{Kind: EventDaemonUp, DaemonID: "d1"}, t0.Add(81*time.Second)), "daemon up")

	cases := []struct {
		name     string
		p        Push
		kind     PushKind
		ttl      time.Duration
		urgency  Urgency
		renotify bool
	}{
		{"waiting", waiting, PushSession, time.Hour, UrgencyHigh, true},
		{"ready", ready, PushSession, 10 * time.Minute, UrgencyNormal, true},
		{"summary", summary, PushSummary, time.Hour, UrgencyHigh, false},
		{"daemon_down", daemonDown, PushDaemonDown, 10 * time.Minute, UrgencyNormal, true},
		{"daemon_up", daemonUp, PushDaemonUp, 10 * time.Minute, UrgencyNormal, false},
	}
	for _, c := range cases {
		if c.p.Payload.Kind != c.kind || c.p.TTL != c.ttl || c.p.Urgency != c.urgency || c.p.Renotify != c.renotify {
			t.Fatalf("%s: got kind=%s ttl=%v urgency=%s renotify=%v, want kind=%s ttl=%v urgency=%s renotify=%v",
				c.name, c.p.Payload.Kind, c.p.TTL, c.p.Urgency, c.p.Renotify, c.kind, c.ttl, c.urgency, c.renotify)
		}
		if c.p.Payload.Version != payloadVersion {
			t.Fatalf("%s: payload version = %d, want %d", c.name, c.p.Payload.Version, payloadVersion)
		}
	}
}

func TestNextWakeEarliestAndEmpty(t *testing.T) {
	e := New(Config{})
	if _, ok := e.NextWake(); ok {
		t.Fatalf("fresh engine has nothing pending")
	}
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantNone(t, e.Handle(upd("s1", StateReady), t0), "hold-down due t0+7")
	wantNone(t, e.Handle(Event{Kind: EventDaemonDown, DaemonID: "d1"}, t0.Add(time.Second)), "grace due t0+61")
	if wake, ok := e.NextWake(); !ok || !wake.Equal(t0.Add(7*time.Second)) {
		t.Fatalf("earliest should be the hold-down at t0+7, got %v ok=%v", wake, ok)
	}
	wantOne(t, e.Tick(t0.Add(7*time.Second)), "hold-down fires")
	if wake, ok := e.NextWake(); !ok || !wake.Equal(t0.Add(61*time.Second)) {
		t.Fatalf("next should be the grace at t0+61, got %v ok=%v", wake, ok)
	}
	wantNone(t, e.Handle(Event{Kind: EventDaemonUp, DaemonID: "d1"}, t0.Add(10*time.Second)), "up cancels the grace")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("nothing should remain pending")
	}
}

func TestHandleFiresDueTimersBeforeEvent(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed s1")
	wantNone(t, e.Handle(upd("s2", StateWorking), t0), "seed s2")
	wantNone(t, e.Handle(upd("s1", StateReady), t0), "arm s1 for t0+7")
	got := e.Handle(upd("s2", StateWaiting), t0.Add(8*time.Second))
	if len(got) != 2 {
		t.Fatalf("want the due hold-down plus the event's push, got %+v", got)
	}
	if got[0].Payload.SessionID != "s1" || got[0].Payload.State != StateReady {
		t.Fatalf("due-timer push must come first, got %+v", got)
	}
	if got[1].Payload.SessionID != "s2" || got[1].Payload.State != StateWaiting {
		t.Fatalf("event push must come second, got %+v", got)
	}
}

func TestDueHoldDownsFireInSortedSessionOrder(t *testing.T) {
	e := New(Config{})
	// Armed in reverse order; both due by the tick.
	wantNone(t, e.Handle(upd("zz", StateWorking), t0), "seed zz")
	wantNone(t, e.Handle(upd("aa", StateWorking), t0), "seed aa")
	wantNone(t, e.Handle(upd("zz", StateReady), t0), "arm zz")
	wantNone(t, e.Handle(upd("aa", StateReady), t0.Add(time.Second)), "arm aa")
	got := e.Tick(t0.Add(10 * time.Second))
	if len(got) != 2 || got[0].Payload.SessionID != "aa" || got[1].Payload.SessionID != "zz" {
		t.Fatalf("due hold-downs must fire in sorted session-id order, got %+v", got)
	}
}

func TestUntrustedInputIsIgnored(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(Event{Kind: EventSessionUpdate, State: StateWaiting}, t0), "empty session id")
	wantNone(t, e.Handle(Event{Kind: EventKind("bogus"), SessionID: "s1"}, t0), "unknown kind")
	wantNone(t, e.Handle(Event{Kind: EventSessionDelete, SessionID: "never-seen"}, t0), "delete of unknown id")
	wantNone(t, e.Handle(Event{Kind: EventRekey, SessionID: "x"}, t0), "rekey without old id")
	wantNone(t, e.Handle(Event{Kind: EventRekey, OldSessionID: "x"}, t0), "rekey without new id")
	wantNone(t, e.Handle(Event{Kind: EventDaemonDown}, t0), "daemon down without id")
	wantNone(t, e.Handle(Event{Kind: EventDaemonUp}, t0), "daemon up without id")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("garbage input must not arm timers")
	}
	// The engine still works normally afterwards.
	wantNone(t, e.Handle(upd("s1", StateWorking), t0.Add(time.Second)), "seed")
	wantOne(t, e.Handle(upd("s1", StateWaiting), t0.Add(2*time.Second)), "normal edge still pushes")
}

func TestParentFlipOnSameStateUpdateCancelsHoldDown(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantNone(t, e.Handle(upd("s1", StateReady), t0.Add(time.Second)), "arm hold-down")
	// events.md fixes parentage at creation, but wire input is untrusted: a
	// same-state update that flips ParentID re-records the session as a
	// subagent, and a session recorded as a subagent must never notify
	// (§8.4) — the pending hold-down has to die with the flip, or the fire
	// path pushes for a subagent.
	flip := upd("s1", StateReady)
	flip.ParentID = "p9"
	wantNone(t, e.Handle(flip, t0.Add(2*time.Second)), "parent flip on same state")
	wantNone(t, e.Tick(t0.Add(time.Minute)), "hold-down must not fire for a stored subagent")
	if _, ok := e.NextWake(); ok {
		t.Fatalf("hold-down still armed after the parent flip")
	}
}

func TestRekeySummaryCountsLogicalSessionOnce(t *testing.T) {
	cfg := Config{HoldDown: 50 * time.Millisecond, Cooldown: 800 * time.Millisecond, BurstWindow: 10 * time.Second, BurstThreshold: 2}
	e := New(cfg)
	wantNone(t, e.Handle(upd("proc", StateWorking), t0), "seed presession")
	wantOne(t, e.Handle(upd("proc", StateWaiting), t0.Add(100*time.Millisecond)), "waiting push under the presession id")
	e.Handle(Event{Kind: EventRekey, OldSessionID: "proc", SessionID: "real"}, t0.Add(200*time.Millisecond))
	// The real id earns a second candidate for the same logical session.
	wantNone(t, e.Handle(upd("real", StateWorking), t0.Add(300*time.Millisecond)), "working")
	wantNone(t, e.Handle(upd("real", StateReady), t0.Add(400*time.Millisecond)), "ready arms")
	wantOne(t, e.Tick(t0.Add(450*time.Millisecond)), "ready push under the real id")
	// A third session trips the summary. The window holds candidates from
	// both spellings of the rekeyed session; the banner's "N agents need
	// attention" must count it once — the count is user-visible on the
	// phone, and a rename is not a second agent.
	wantNone(t, e.Handle(upd("s2", StateWorking), t0.Add(500*time.Millisecond)), "seed s2")
	p := wantOne(t, e.Handle(upd("s2", StateWaiting), t0.Add(600*time.Millisecond)), "summary")
	if p.Payload.Kind != PushSummary {
		t.Fatalf("want a summary, got %+v", p)
	}
	if p.Payload.Count != 2 {
		t.Fatalf("rekeyed session double-counted: Count=%d Sessions=%v, want 2 logical sessions", p.Payload.Count, p.Payload.Sessions)
	}
}

func TestRekeyMergeKeepsEarlierHoldDown(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("proc", StateWorking), t0), "seed old strand")
	wantNone(t, e.Handle(upd("real", StateWorking), t0), "seed new strand")
	wantNone(t, e.Handle(upd("proc", StateReady), t0.Add(time.Second)), "old strand arms at t0+1")
	wantNone(t, e.Handle(upd("real", StateReady), t0.Add(3*time.Second)), "new strand arms at t0+3")
	e.Handle(Event{Kind: EventRekey, OldSessionID: "proc", SessionID: "real"}, t0.Add(4*time.Second))
	// The merged hold-down is the EARLIER of the two arms: the presession's
	// already-elapsed wait must not be postponed to the later strand's.
	p := wantOne(t, e.Tick(t0.Add(8*time.Second)), "fire at the old strand's arm time (t0+1+7s)")
	if p.Topic != "real" || p.Payload.State != StateReady {
		t.Fatalf("merged hold-down fired wrong: %+v", p)
	}
	wantNone(t, e.Tick(t0.Add(10*time.Second)), "the later arm must not fire a second push")
}

func TestRekeyMergeOldStrandNewerStateWins(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("real", StateWorking), t0), "new strand recorded working at t0")
	wantNone(t, e.Handle(upd("proc", StateWorking), t0), "seed old strand")
	wantOne(t, e.Handle(upd("proc", StateWaiting), t0.Add(2*time.Second)), "old strand's later transition")
	e.Handle(Event{Kind: EventRekey, OldSessionID: "proc", SessionID: "real"}, t0.Add(3*time.Second))
	// The old strand's state changed later, so the merged record is
	// waiting. A waiting update past the cooldown must therefore diff as
	// same-state — silent. A merge that kept the destination's stale
	// "working" would treat it as an edge and push a phantom notification.
	wantNone(t, e.Handle(upd("real", StateWaiting), t0.Add(90*time.Second)), "same state as the merged record")
}

func TestDueDaemonGracesFireInSortedOrder(t *testing.T) {
	e := New(Config{})
	ids := []string{"d5", "d2", "d9", "d1", "d7", "d4", "d8", "d3"}
	for _, id := range ids {
		e.Handle(Event{Kind: EventDaemonUp, DaemonID: id, DaemonLabel: id}, t0)
		e.Handle(Event{Kind: EventDaemonDown, DaemonID: id, DaemonLabel: id}, t0)
	}
	pushes := e.Tick(t0.Add(2 * time.Minute))
	if len(pushes) != len(ids) {
		t.Fatalf("want %d daemon_down pushes, got %d: %+v", len(ids), len(pushes), pushes)
	}
	for i, id := range []string{"d1", "d2", "d3", "d4", "d5", "d7", "d8", "d9"} {
		if pushes[i].Topic != "daemon:"+id {
			t.Fatalf("push %d topic %q, want daemon:%s — multiple due graces must fire in sorted daemon-id order", i, pushes[i].Topic, id)
		}
	}
}
