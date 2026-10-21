package services

import (
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// fakeAccountReader is a SessionAccountReader whose observation, transcript
// start and consent the test moves between passes.
type fakeAccountReader struct {
	mu      sync.Mutex
	obs     outbound.HomeAccountObservation
	granted bool
	start   int64
}

func (f *fakeAccountReader) set(ref string, mtime int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = outbound.HomeAccountObservation{Home: "/h/.claude.json", Ref: ref, Product: "claude_team", ModTime: mtime}
}

func (f *fakeAccountReader) ReadHomeAccount(*session.SessionState) (outbound.HomeAccountObservation, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.obs, f.granted
}

func (f *fakeAccountReader) TranscriptStart(*session.SessionState) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.granted {
		return 0
	}
	return f.start
}

type memHomeStore struct {
	mu    sync.Mutex
	homes map[string]session.HomeAccount
	saves int
}

func (m *memHomeStore) Load() (map[string]session.HomeAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]session.HomeAccount{}
	for k, v := range m.homes {
		out[k] = v
	}
	return out, nil
}

func (m *memHomeStore) Save(h map[string]session.HomeAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	m.homes = map[string]session.HomeAccount{}
	for k, v := range h {
		m.homes[k] = v
	}
	return nil
}

type binderRig struct {
	r     *fakeAccountReader
	store *memHomeStore
	now   int64
	b     *AccountBinder
}

func newBinderRig(start int64) *binderRig {
	rig := &binderRig{r: &fakeAccountReader{granted: true, start: start}, store: &memHomeStore{}}
	rig.rebuild()
	return rig
}

// rebuild models a daemon restart: a fresh binder over the same store.
func (g *binderRig) rebuild() {
	g.b = NewAccountBinder(map[string]outbound.SessionAccountReader{"claude-code": g.r}, g.store, binderLog{},
		func() time.Time { return time.Unix(g.now, 0) })
}

func snapState(sampledAt int64, binding *session.AccountBinding) *session.SessionState {
	return &session.SessionState{
		SessionID: "s1", Adapter: "claude-code", FirstSeen: 100, AccountBinding: binding,
		Metrics: &session.SessionMetrics{RateLimit: &session.RateLimitSnapshot{SampledAt: sampledAt}},
	}
}

func refOf(s *session.SessionState) string { return s.Metrics.RateLimit.ConfirmedAccountRef }

// R1 (red-first against a stamper that writes the home's CURRENT ref): the
// account changes mid-session; a reading from before the change keeps its
// ref, readings at or after it do not, and switching back never re-binds.
func TestAccountBinder_MidSessionSwitchClearsStickily(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-A", 50)
	g.now = 200
	s := snapState(150, nil)
	g.b.Stamp(s)
	if got := refOf(s); got != "acct-A" {
		t.Fatalf("before any switch: ref = %q, want acct-A", got)
	}
	if s.Metrics.RateLimit.Product != "claude_team" {
		t.Errorf("product = %q, want claude_team alongside a ref", s.Metrics.RateLimit.Product)
	}

	g.r.set("acct-B", 300)
	g.now = 310
	older, newer := snapState(250, s.AccountBinding), snapState(300, s.AccountBinding)
	g.b.Stamp(older)
	g.b.Stamp(newer)
	if got := refOf(older); got != "acct-A" {
		t.Errorf("reading sampled before the switch: ref = %q, want acct-A kept", got)
	}
	if got := refOf(newer); got != "" {
		t.Errorf("reading sampled at the switch: ref = %q, want empty", got)
	}

	g.r.set("acct-A", 400)
	g.now = 410
	back := snapState(450, newer.AccountBinding)
	g.b.Stamp(back)
	if got := refOf(back); got != "" {
		t.Errorf("after switching back: ref = %q, want empty (never re-binds)", got)
	}
}

// R2 (red-first against the same naive stamper): a session that began before
// the home's account was first observed is never bound, even if it is later
// observed steadily.
func TestAccountBinder_SessionOlderThanObservationNeverBinds(t *testing.T) {
	t.Run("home observed after FirstSeen", func(t *testing.T) {
		g := newBinderRig(0) // transcript start unknown -> stays undecided
		g.r.set("acct-A", 150)
		g.now = 200
		s := snapState(180, nil)
		g.b.Stamp(s)
		if got := refOf(s); got != "" {
			t.Fatalf("undecided (no transcript start): ref = %q, want empty", got)
		}
		g.r.start = 120
		g.b.Stamp(s)
		if got := refOf(s); got != "" {
			t.Fatalf("home Since 150 > session start 100: ref = %q, want empty", got)
		}
		g.now = 900
		g.b.Stamp(s)
		if got := refOf(s); got != "" {
			t.Fatalf("later pass re-bound a session decided unconfirmed: ref = %q", got)
		}
	})
	t.Run("transcript first event predates FirstSeen and Since", func(t *testing.T) {
		g := newBinderRig(60)
		g.r.set("acct-A", 80) // Since 80 <= FirstSeen 100, but > transcript start 60
		g.now = 200
		s := snapState(180, nil)
		g.b.Stamp(s)
		if got := refOf(s); got != "" {
			t.Fatalf("ref = %q, want empty: the transcript began before the account was observed", got)
		}
	})
	t.Run("home observed before both binds", func(t *testing.T) {
		g := newBinderRig(90)
		g.r.set("acct-A", 50)
		g.now = 200
		s := snapState(180, nil)
		g.b.Stamp(s)
		if got := refOf(s); got != "acct-A" {
			t.Fatalf("ref = %q, want acct-A", got)
		}
	})
}

// A switch made while the daemon was down shows up as a change after restart.
func TestAccountBinder_RestartSeesSwitchMadeWhileDown(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-A", 50)
	g.now = 200
	s := snapState(150, nil)
	g.b.Stamp(s)
	if refOf(s) != "acct-A" {
		t.Fatalf("setup: not bound")
	}

	g.rebuild()
	g.r.set("acct-B", 500)
	g.now = 510
	before, after := snapState(400, s.AccountBinding), snapState(600, s.AccountBinding)
	g.b.Stamp(before)
	g.b.Stamp(after)
	if got := refOf(before); got != "acct-A" {
		t.Errorf("earlier reading relabelled: ref = %q, want acct-A", got)
	}
	if got := refOf(after); got != "" {
		t.Errorf("reading after the switch: ref = %q, want empty (cleared, not re-bound)", got)
	}

	// A session that starts now was running under neither account's
	// observation window: the new account was first seen at 500 > start.
	fresh := snapState(600, nil)
	g.b.Stamp(fresh)
	if got := refOf(fresh); got != "" {
		t.Errorf("session older than the new account's Since: ref = %q, want empty", got)
	}
}

// Revoked consent decides and relabels nothing, and scrubs the stamped fields.
func TestAccountBinder_NotGrantedFailsClosedWithoutTouchingBinding(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-A", 50)
	g.now = 200
	s := snapState(150, nil)
	g.b.Stamp(s)
	bound := *s.AccountBinding

	g.r.granted = false
	again := snapState(160, s.AccountBinding)
	again.Metrics.RateLimit.ConfirmedAccountRef = "acct-A"
	again.Metrics.RateLimit.Product = "claude_team"
	shared := again.Metrics.RateLimit
	g.b.Stamp(again)
	if refOf(again) != "" || again.Metrics.RateLimit.Product != "" {
		t.Errorf("not granted: snapshot still carries ref/product: %+v", again.Metrics.RateLimit)
	}
	if shared.ConfirmedAccountRef != "acct-A" {
		t.Errorf("Stamp mutated a possibly shared snapshot in place")
	}
	if *again.AccountBinding != bound {
		t.Errorf("binding changed while not granted: %+v, want %+v", *again.AccountBinding, bound)
	}

	g.r.granted = true
	g.now = 300
	back := snapState(170, again.AccountBinding)
	g.b.Stamp(back)
	if refOf(back) != "acct-A" {
		t.Errorf("after regrant: ref = %q, want acct-A (a revoke relabels nothing)", refOf(back))
	}
}

func TestAccountBinder_UnknownAdapterAndNilAreNoOps(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-A", 50)
	s := snapState(150, nil)
	s.Adapter = "muse"
	s.Metrics.RateLimit.ConfirmedAccountRef = "keep"
	orig := s.Metrics.RateLimit
	g.b.Stamp(s)
	if s.Metrics.RateLimit != orig || s.AccountBinding != nil {
		t.Errorf("muse snapshot touched by the claude binder")
	}
	var nilBinder *AccountBinder
	nilBinder.Stamp(s) // must not panic
}

func TestAccountBinder_SameRefNewerMtimeKeepsSince(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-A", 50)
	g.now = 200
	g.b.Stamp(snapState(150, nil))
	g.r.set("acct-A", 190) // Claude rewrites the file on every run
	g.now = 210
	g.b.Stamp(snapState(150, nil))
	if got := g.store.homes["/h/.claude.json"]; got.Since != 50 {
		t.Errorf("Since = %d, want 50 (same account, newer mtime)", got.Since)
	}
}

type binderLog struct{}

func (binderLog) LogInfo(_, _, _ string)                                  {}
func (binderLog) LogError(_, _, _ string)                                 {}
func (binderLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (binderLog) Close() error                                            { return nil }
