package notify

import (
	"testing"
	"time"
)

func TestErrorPushesImmediately(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantOne(t, e.Handle(upd("s1", StateError), t0.Add(time.Second)), "error edge")
}

func TestErrorCarriesItsOwnStateUrgencyAndTTL(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(updL("s1", StateWorking, "lbl"), t0), "seed")
	p := wantOne(t, e.Handle(updL("s1", StateError, "lbl"), t0.Add(time.Second)), "error edge")
	if p.Payload.State != StateError {
		t.Fatalf("payload state = %q, want %q", p.Payload.State, StateError)
	}
	if p.Urgency != UrgencyHigh {
		t.Fatalf("urgency = %q, want %q: a broken session wants a human as much as a waiting one", p.Urgency, UrgencyHigh)
	}
	if p.TTL != DefaultConfig().TTLError {
		t.Fatalf("TTL = %v, want %v", p.TTL, DefaultConfig().TTLError)
	}
	if p.Topic != "s1" {
		t.Fatalf("topic = %q, want the session id", p.Topic)
	}
}

func TestErrorIsNotHeldDown(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantOne(t, e.Handle(upd("s1", StateError), t0.Add(time.Second)), "error edge")
	// An error is cleared by the next SUCCESSFUL turn and by nothing else
	// (session.StateError), so unlike ready it cannot flap back within a
	// hold-down and there is nothing left pending for a tick to fire.
	wantNone(t, e.Tick(t0.Add(time.Minute)), "tick after the error push")
}

func TestErrorCooldownIsSeparateFromWaitingAndReady(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantOne(t, e.Handle(upd("s1", StateWaiting), t0.Add(time.Second)), "waiting edge")
	// Well inside the 60s cooldown: a waiting push must not silence the
	// error edge, which is a different thing to tell the user about.
	wantOne(t, e.Handle(upd("s1", StateError), t0.Add(2*time.Second)), "error edge inside the waiting cooldown")
}

func TestASecondErrorInsideTheCooldownIsSuppressed(t *testing.T) {
	e := New(Config{})
	wantNone(t, e.Handle(upd("s1", StateWorking), t0), "seed")
	wantOne(t, e.Handle(upd("s1", StateError), t0.Add(time.Second)), "first error")
	// Recovered and broke again inside the cooldown.
	wantNone(t, e.Handle(upd("s1", StateWorking), t0.Add(2*time.Second)), "recovery is never a push")
	wantNone(t, e.Handle(upd("s1", StateError), t0.Add(3*time.Second)), "second error inside cooldown")
}

func TestSubagentErrorNeverNotifies(t *testing.T) {
	e := New(Config{})
	seed := upd("child", StateWorking)
	seed.ParentID = "parent"
	wantNone(t, e.Handle(seed, t0), "seed")
	ev := upd("child", StateError)
	ev.ParentID = "parent"
	wantNone(t, e.Handle(ev, t0.Add(time.Second)), "a subagent's error is the parent's to report")
}
