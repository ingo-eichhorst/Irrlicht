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
	g.b = NewAccountBinder(map[string]outbound.SessionAccountReader{"claude-code": g.r}, g.store, &gateLogger{},
		func() time.Time { return time.Unix(g.now, 0) })
}

func snapState(sampledAt int64, binding *session.AccountBinding) *session.SessionState {
	return &session.SessionState{
		SessionID: "s1", Adapter: "claude-code", FirstSeen: 100, AccountBinding: binding,
		Metrics: &session.SessionMetrics{RateLimit: &session.RateLimitSnapshot{SampledAt: sampledAt}},
	}
}

func refOf(s *session.SessionState) string { return s.Metrics.RateLimit.ConfirmedAccountRef }

// expectRef reports (non-fatally) when s's confirmed ref is not want; format
// takes the actual ref as its one %q.
func expectRef(t *testing.T, s *session.SessionState, want, format string) {
	t.Helper()
	if got := refOf(s); got != want {
		t.Errorf(format, got)
	}
}

// requireRef is expectRef that stops the test.
func requireRef(t *testing.T, s *session.SessionState, want, format string) {
	t.Helper()
	if got := refOf(s); got != want {
		t.Fatalf(format, got)
	}
}

// R1 (red-first against a stamper that writes the home's CURRENT ref, and
// again, as A2-1, against the upper-bound ClearedAt): the account changes
// mid-session. The switch mtime (300) is only an UPPER bound on when it
// happened, so a reading sampled after the binder's last confirmation (250)
// is unattributable; a reading at or before it keeps its ref; switching back
// never re-binds.
func TestAccountBinder_MidSessionSwitchClearsStickily(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-A", 50)
	g.now = 200
	s := snapState(150, nil)
	g.b.Stamp(s)
	requireRef(t, s, "acct-A", "before any switch: ref = %q, want acct-A")
	if s.Metrics.RateLimit.Product != "claude_team" {
		t.Errorf("product = %q, want claude_team alongside a ref", s.Metrics.RateLimit.Product)
	}

	g.now = 250 // last pass that still saw acct-A: the confirmation
	s = snapState(240, s.AccountBinding)
	g.b.Stamp(s)

	g.r.set("acct-B", 300)
	g.now = 310
	atConfirm, afterConfirm, afterSwitch := snapState(250, s.AccountBinding), snapState(260, s.AccountBinding), snapState(300, s.AccountBinding)
	g.b.Stamp(atConfirm)
	g.b.Stamp(afterConfirm)
	g.b.Stamp(afterSwitch)
	expectRef(t, atConfirm, "acct-A", "reading at the last confirmation: ref = %q, want acct-A kept")
	expectRef(t, afterConfirm, "", "reading after the last confirmation but before the switch mtime: ref = %q, want empty (the mtime is an upper bound)")
	expectRef(t, afterSwitch, "", "reading at the switch mtime: ref = %q, want empty")

	g.r.set("acct-A", 400)
	g.now = 410
	back := snapState(450, afterSwitch.AccountBinding)
	g.b.Stamp(back)
	expectRef(t, back, "", "after switching back: ref = %q, want empty (never re-binds)")
}

// A2-1 (red-first, the reviewer's scenario): bound to acct-X; consent is
// revoked; the user logs in as acct-Y inside the session, which ticks (a
// scrubbed reading) at 250 and goes idle; another session rewrites the
// identity file at 400; consent is re-granted. The reading sampled at 250 must
// not land on acct-X.
func TestAccountBinder_RevokeGapSwitchDoesNotLabelTheNewAccountsReading(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-X", 50)
	g.now = 200
	s := snapState(150, nil)
	g.b.Stamp(s)
	if refOf(s) != "acct-X" {
		t.Fatalf("setup: not bound")
	}

	g.r.granted = false
	g.now = 250
	g.b.Stamp(snapState(250, s.AccountBinding))

	g.r.set("acct-Y", 400)
	g.r.granted = true
	g.now = 410
	y := snapState(250, s.AccountBinding)
	g.b.Stamp(y)
	expectRef(t, y, "", "reading sampled during the revoke gap: ref = %q, want empty")
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
		requireRef(t, s, "", "undecided (no transcript start): ref = %q, want empty")
		g.r.start = 120
		g.b.Stamp(s)
		requireRef(t, s, "", "home Since 150 > session start 100: ref = %q, want empty")
		g.now = 900
		g.b.Stamp(s)
		requireRef(t, s, "", "later pass re-bound a session decided unconfirmed: ref = %q")
	})
	t.Run("transcript first event predates FirstSeen and Since", func(t *testing.T) {
		g := newBinderRig(60)
		g.r.set("acct-A", 80) // Since 80 <= FirstSeen 100, but > transcript start 60
		g.now = 200
		s := snapState(180, nil)
		g.b.Stamp(s)
		requireRef(t, s, "", "ref = %q, want empty: the transcript began before the account was observed")
	})
	t.Run("home observed before both binds", func(t *testing.T) {
		g := newBinderRig(90)
		g.r.set("acct-A", 50)
		g.now = 200
		s := snapState(180, nil)
		g.b.Stamp(s)
		requireRef(t, s, "acct-A", "ref = %q, want acct-A")
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
	before, after := snapState(200, s.AccountBinding), snapState(600, s.AccountBinding)
	g.b.Stamp(before)
	g.b.Stamp(after)
	expectRef(t, before, "acct-A", "reading at the last confirmation relabelled: ref = %q, want acct-A")
	expectRef(t, after, "", "reading after the switch: ref = %q, want empty (cleared, not re-bound)")

	// This session's start is 90 (transcript) < the new account's Since 500:
	// it began before acct-B was observed, so it never binds to it.
	older := snapState(600, nil)
	g.b.Stamp(older)
	expectRef(t, older, "", "session that began before the new account's Since: ref = %q, want empty")
}

// LOCK (passes by construction; written to pin the positive side): a session
// that genuinely starts after the switch was observed binds to the new account.
func TestAccountBinder_SessionStartedAfterSwitchBindsToNewAccount(t *testing.T) {
	g := newBinderRig(0)
	g.r.set("acct-B", 500)
	g.now = 510
	g.b.Stamp(snapState(520, nil)) // records the home: acct-B since 500
	g.r.start = 600
	s := snapState(650, nil)
	s.FirstSeen = 610
	g.now = 700
	g.b.Stamp(s)
	expectRef(t, s, "acct-B", "session started at 600 > Since 500: ref = %q, want acct-B")
}

// A2-2 (red-first): when consent is not granted, a bound session becomes
// permanently unconfirmed — the binding is serialized with the session, so it
// must not keep publishing the ref — and the snapshot is scrubbed. A re-grant
// does not re-bind it. An undecided binding stays undecided.
func TestAccountBinder_NotGrantedDropsTheRefAndStaysUnconfirmed(t *testing.T) {
	g := newBinderRig(90)
	g.r.set("acct-A", 50)
	g.now = 200
	s := snapState(150, nil)
	g.b.Stamp(s)
	if s.AccountBinding == nil || s.AccountBinding.Ref != "acct-A" {
		t.Fatalf("setup: not bound: %+v", s.AccountBinding)
	}

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
	if want := (session.AccountBinding{Decided: true}); *again.AccountBinding != want {
		t.Errorf("binding after a not-granted pass = %+v, want %+v (no ref on the wire)", *again.AccountBinding, want)
	}

	g.r.granted = true
	g.now = 300
	back := snapState(170, again.AccountBinding)
	g.b.Stamp(back)
	if refOf(back) != "" {
		t.Errorf("after regrant: ref = %q, want empty (a dropped binding never re-binds)", refOf(back))
	}

	// An undecided binding is left undecided by a not-granted pass.
	g2 := newBinderRig(90)
	g2.r.set("acct-A", 50)
	g2.r.granted = false
	u := snapState(150, nil)
	g2.b.Stamp(u)
	if u.AccountBinding != nil {
		t.Errorf("not-granted pass decided an undecided session: %+v", u.AccountBinding)
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
