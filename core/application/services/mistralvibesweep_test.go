package services

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/chromecookies"
	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// Issue #2012: the Mistral Vibe plan reading, bound to vibe sessions by the
// user-asserted browser-session permission.

// vibeProbeAfterOneTurn is the 115-byte 200 body issue #2012's live probe
// captured at 2026-11-21T13:38:33Z
// (core/adapters/outbound/mistralvibe/testdata/live_probe_2026-11-21T133833Z_usage_after_one_turn.json).
const vibeProbeAfterOneTurn = `[{"result":{"data":{"json":{"usage_percentage":0.081567,"payg_enabled":false,"reset_at":"2026-12-01T00:00:00Z"}}}}]`

// fakeBrowserSession stands in for mistralvibe.Session. It counts cookie
// reads (Resolve), which stand for reads of the Chrome profile's cookies.
type fakeBrowserSession struct {
	account string
	reads   atomic.Int64
	err     error
}

func (b *fakeBrowserSession) Account() string { return b.account }
func (b *fakeBrowserSession) Resolve(context.Context) (outbound.Credential, error) {
	b.reads.Add(1)
	if b.err != nil {
		return outbound.Credential{}, b.err
	}
	return outbound.NewCredentialWithCompanion("csrftoken=c; ory_session_x=s", "c"), nil
}

func vibeTransport(status func(n int) error) *fakeTransport {
	return &fakeTransport{fn: func(n int, _ outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		if err := status(n); err != nil {
			return outbound.AccountQuotaResponse{}, err
		}
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte(vibeProbeAfterOneTurn)}, nil
	}}
}

func authRejected() error {
	return &outbound.QuotaError{Reason: outbound.QuotaFailureAuthRejected, Detail: "status 401"}
}

// rejectedAfter is a transport status that serves the first k fetches and
// rejects every later one with a 401.
func rejectedAfter(k int) func(int) error {
	return func(n int) error {
		if n > k {
			return authRejected()
		}
		return nil
	}
}

// refreshVibe is MistralVibeRefresh for session v1.
func refreshVibe(poller *AccountPoller, browser MistralBrowserSession, transport outbound.AccountQuotaTransport, granted func() bool) (*session.RateLimitSnapshot, error) {
	return MistralVibeRefresh(context.Background(), MistralVibeRefreshRequest{
		Poller: poller, Browser: browser, Transport: transport, Granted: granted, SessionID: "v1",
	})
}

// TestMistralVibeRefresh_NeverPublishesZeroOnAuthFailure is #2012 §7.2's
// target: a signed-out browser (401) is a classified failure on the first
// poll and on the retries the backoff serves from cache, never a 0 %
// reading.
func TestMistralVibeRefresh_NeverPublishesZeroOnAuthFailure(t *testing.T) {
	poller := NewAccountPoller()
	browser := &fakeBrowserSession{account: "chrome-profile:/p/Default"}
	transport := vibeTransport(func(int) error { return authRejected() })

	for attempt := 1; attempt <= 2; attempt++ {
		snap, err := refreshVibe(poller, browser, transport, alwaysGranted)
		if snap != nil {
			t.Fatalf("attempt %d: a reading was published after a 401: %+v", attempt, *snap)
		}
		if err == nil {
			t.Fatalf("attempt %d: expected an error on the immediate retry, got none", attempt)
		}
		if !strings.Contains(err.Error(), string(outbound.QuotaFailureAuthRejected)) {
			t.Fatalf("attempt %d: err = %v, want the auth_rejected classification", attempt, err)
		}
	}
	if transport.callCount() != 1 {
		t.Fatalf("transport called %d times, want 1 (the retry is served from backoff)", transport.callCount())
	}
}

// TestMistralVibeRefresh_StaleReadingKeepsItsTimeOnAuthFailure: after an
// earlier success, a 401 keeps that reading, at its original time, marked
// stale with the classified failure.
func TestMistralVibeRefresh_StaleReadingKeepsItsTimeOnAuthFailure(t *testing.T) {
	poller := NewAccountPoller()
	t0 := time.Date(2026, 11, 21, 13, 38, 33, 0, time.UTC)
	now := t0
	poller.now = func() time.Time { return now }
	browser := &fakeBrowserSession{account: "chrome-profile:/p/Default"}
	transport := vibeTransport(rejectedAfter(1))
	if _, err := refreshVibe(poller, browser, transport, alwaysGranted); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	now = t0.Add(pollFreshWindow + time.Second)
	snap, err := refreshVibe(poller, browser, transport, alwaysGranted)
	if err == nil || snap == nil {
		t.Fatalf("after a 401: snap=%v err=%v, want the stale reading and an error", snap, err)
	}
	checkStaleVibeReading(t, snap, t0)
}

// checkStaleVibeReading: snap is the reading fetched at t0, at its original
// time, marked stale with the auth_rejected classification.
func checkStaleVibeReading(t *testing.T, snap *session.RateLimitSnapshot, t0 time.Time) {
	t.Helper()
	if snap.SampledAt != t0.Unix() || snap.LastSuccessAt != t0.Unix() {
		t.Errorf("stale reading SampledAt=%d LastSuccessAt=%d, want its original time %d", snap.SampledAt, snap.LastSuccessAt, t0.Unix())
	}
	if snap.RetrievalFailure != string(outbound.QuotaFailureAuthRejected) {
		t.Errorf("RetrievalFailure = %q, want auth_rejected", snap.RetrievalFailure)
	}
	if snap.Windows[0].UsedPercent != 0.081567 {
		t.Errorf("UsedPercent = %v, want the earlier reading 0.081567", snap.Windows[0].UsedPercent)
	}
}

type vibeSweepHarness struct {
	sweeper      *MistralVibeSweeper
	writer       *fakeRateLimitWriter
	transport    *fakeTransport
	browser      *fakeBrowserSession
	browserCalls *atomic.Int64
	granted      *atomic.Bool
	poller       *AccountPoller
}

func newVibeSweepHarness(sessions sessionLister, transport *fakeTransport) *vibeSweepHarness {
	h := &vibeSweepHarness{
		writer:       &fakeRateLimitWriter{},
		transport:    transport,
		browser:      &fakeBrowserSession{account: "chrome-profile:/p/Default"},
		browserCalls: &atomic.Int64{},
		granted:      &atomic.Bool{},
		poller:       NewAccountPoller(),
	}
	h.granted.Store(true)
	h.sweeper = NewMistralVibeSweeper(MistralVibeSweeperDeps{
		Sessions: sessions,
		Writer:   h.writer,
		Poller:   h.poller,
		Browser: func() (MistralBrowserSession, error) {
			h.browserCalls.Add(1)
			return h.browser, nil
		},
		Transport: transport,
		Granted:   h.granted.Load,
		Adapter:   "mistral-vibe",
		Log:       &sweepLogger{},
		Interval:  time.Minute,
	})
	return h
}

func mistralListed() *session.SessionMetrics {
	return &session.SessionMetrics{RateLimit: &session.RateLimitSnapshot{Provider: session.ProviderMistral,
		Windows: []session.RateLimitWindow{{UsedPercent: 1}}, AttributionQuality: session.AttributionQualityConfirmed}}
}

// mistralBoundSessions is the set of sessions writes bound a Mistral reading
// to; a write that is not such a binding fails the test.
func mistralBoundSessions(t *testing.T, writes []rateLimitWrite) map[string]bool {
	t.Helper()
	bound := map[string]bool{}
	for _, w := range writes {
		if !isMistralBinding(w) {
			t.Fatalf("write = %+v, want a Mistral snapshot", w)
		}
		bound[w.sessionID] = true
	}
	return bound
}

func isMistralBinding(w rateLimitWrite) bool {
	return w.provider == session.ProviderMistral && w.snap != nil && w.snap.Provider == session.ProviderMistral
}

// mistralClearedSessions is the set of sessions writes cleared the Mistral
// reading from; a write that binds a reading fails the test.
func mistralClearedSessions(t *testing.T, writes []rateLimitWrite) map[string]bool {
	t.Helper()
	cleared := map[string]bool{}
	for _, w := range writes {
		if w.snap != nil {
			t.Fatalf("a vibe session was bound while the permission was not granted: %+v", w)
		}
		if w.provider == session.ProviderMistral {
			cleared[w.sessionID] = true
		}
	}
	return cleared
}

// isOneMistralClear reports whether writes is exactly one clear of id's
// Mistral reading.
func isOneMistralClear(writes []rateLimitWrite, id string) bool {
	return len(writes) == 1 && writes[0].sessionID == id && writes[0].provider == session.ProviderMistral && writes[0].snap == nil
}

// TestMistralVibeSweep_OnePollBindsEveryVibeSession: three vibe sessions on
// one profile cost one request, and each gets the reading; another adapter's
// session and a vibe subagent get nothing.
func TestMistralVibeSweep_OnePollBindsEveryVibeSession(t *testing.T) {
	sessions := staticSessions{
		{SessionID: "v1", Adapter: "mistral-vibe"},
		{SessionID: "v2", Adapter: "mistral-vibe"},
		{SessionID: "v3", Adapter: "mistral-vibe"},
		{SessionID: "v1-sub", Adapter: "mistral-vibe", ParentSessionID: "v1"},
		{SessionID: "c1", Adapter: "claude-code"},
	}
	h := newVibeSweepHarness(sessions, vibeTransport(func(int) error { return nil }))
	h.sweeper.sweep(context.Background())
	h.sweeper.sweep(context.Background())

	if n := h.transport.callCount(); n != 1 {
		t.Fatalf("transport called %d times for three vibe sessions over two ticks, want 1", n)
	}
	bound := mistralBoundSessions(t, h.writer.snapshot())
	if !maps.Equal(bound, map[string]bool{"v1": true, "v2": true, "v3": true}) {
		t.Fatalf("bound sessions = %v, want exactly v1, v2, v3", bound)
	}
}

// TestMistralVibeSweep_RevokedReadsAndSendsNothing is #2012 §7.3's target:
// once the permission is not granted, a tick neither resolves the Chrome
// profile nor reads its cookies nor sends a request.
func TestMistralVibeSweep_RevokedReadsAndSendsNothing(t *testing.T) {
	h := newVibeSweepHarness(staticSessions{{SessionID: "v1", Adapter: "mistral-vibe"}}, vibeTransport(func(int) error { return nil }))
	h.sweeper.sweep(context.Background())
	if h.transport.callCount() != 1 || h.browserCalls.Load() != 1 {
		t.Fatalf("vacuity: the granted tick made %d request(s) and %d profile read(s), want 1 and 1",
			h.transport.callCount(), h.browserCalls.Load())
	}

	h.granted.Store(false)
	h.poller.Revoke(session.ProviderMistral) // what the daemon's Stop effect does
	for range 3 {
		h.sweeper.sweep(context.Background())
	}
	if n := h.browserCalls.Load(); n != 1 {
		t.Fatalf("the sweep read the Chrome profile %d more time(s) after the permission was revoked", n-1)
	}
	if n := h.browser.reads.Load(); n != 1 {
		t.Fatalf("the sweep read the browser cookies %d more time(s) after the permission was revoked", n-1)
	}
	if n := h.transport.callCount(); n != 1 {
		t.Fatalf("the sweep sent %d more request(s) after the permission was revoked", n-1)
	}
}

// TestMistralVibeSweep_NotGrantedUnbindsEverySession is the target of #2012
// testing-strategy mutation 4 ("bind a vibe session while the user-asserted
// permission is not granted"): every vibe session carrying a Mistral reading
// loses it on the first tick without the grant, and nothing is fetched.
func TestMistralVibeSweep_NotGrantedUnbindsEverySession(t *testing.T) {
	sessions := staticSessions{
		{SessionID: "v1", Adapter: "mistral-vibe", Metrics: mistralListed()},
		{SessionID: "v2", Adapter: "mistral-vibe", Metrics: mistralListed()},
		{SessionID: "v3", Adapter: "mistral-vibe"},
	}
	h := newVibeSweepHarness(sessions, vibeTransport(func(int) error { return nil }))
	h.granted.Store(false)
	h.sweeper.sweep(context.Background())

	cleared := mistralClearedSessions(t, h.writer.snapshot())
	if !cleared["v1"] || !cleared["v2"] {
		t.Fatalf("cleared = %v: a vibe session stayed bound to the Mistral reading while the permission was not granted", cleared)
	}
	if cleared["v3"] {
		t.Fatal("v3 carried no reading and was written anyway")
	}
	if h.transport.callCount() != 0 || h.browserCalls.Load() != 0 {
		t.Fatal("the not-granted tick read the browser or sent a request")
	}
}

// TestMistralVibeSweep_NoVibeSessionReadsNothing: with the grant but no live
// vibe session, nothing is read from the browser.
func TestMistralVibeSweep_NoVibeSessionReadsNothing(t *testing.T) {
	h := newVibeSweepHarness(staticSessions{{SessionID: "c1", Adapter: "claude-code"}}, vibeTransport(func(int) error { return nil }))
	h.sweeper.sweep(context.Background())
	if h.browserCalls.Load() != 0 || h.transport.callCount() != 0 {
		t.Fatal("a tick with no vibe session read the Chrome profile or sent a request")
	}
}

// TestMistralVibeSweep_AuthFailureWritesNoZero: a 401 with no earlier
// reading writes nothing at all, and a session's existing reading is left
// for the stale path rather than cleared or zeroed.
func TestMistralVibeSweep_AuthFailureWritesNoZero(t *testing.T) {
	h := newVibeSweepHarness(staticSessions{{SessionID: "v1", Adapter: "mistral-vibe"}}, vibeTransport(func(int) error { return authRejected() }))
	h.sweeper.sweep(context.Background())
	if writes := h.writer.snapshot(); len(writes) != 0 {
		t.Fatalf("writes after a 401 = %+v, want none", writes)
	}
}

// TestMistralVibeSweep_ProfileSwitchIsANewAccount: a different Chrome
// profile is a different account context, polled on its own and never
// served the old profile's reading.
func TestMistralVibeSweep_ProfileSwitchIsANewAccount(t *testing.T) {
	h := newVibeSweepHarness(staticSessions{{SessionID: "v1", Adapter: "mistral-vibe"}}, vibeTransport(func(int) error { return nil }))
	h.sweeper.sweep(context.Background())
	h.browser = &fakeBrowserSession{account: "chrome-profile:/p/Profile 2"}
	h.sweeper.sweep(context.Background())
	if n := h.transport.callCount(); n != 2 {
		t.Fatalf("transport called %d times across a profile switch, want 2", n)
	}
	if h.browser.reads.Load() != 1 {
		t.Fatal("the new profile's cookies were not the ones read for its poll")
	}
}

// TestMistralVibeSweep_ProfileSwitchUnbindsTheOldReading (review of #2012,
// finding 1): when Chrome's last-used profile changes, the previous
// profile's reading is another account context's and leaves every vibe
// session, even when the new profile's first poll fails.
func TestMistralVibeSweep_ProfileSwitchUnbindsTheOldReading(t *testing.T) {
	sessions := staticSessions{{SessionID: "v1", Adapter: "mistral-vibe", Metrics: mistralListed()}}
	h := newVibeSweepHarness(sessions, vibeTransport(func(int) error { return nil }))
	h.sweeper.sweep(context.Background())
	before := len(h.writer.snapshot())

	h.browser = &fakeBrowserSession{account: "chrome-profile:/p/Profile 2", err: errors.New("no Mistral session in this profile")}
	h.sweeper.sweep(context.Background())

	after := h.writer.snapshot()[before:]
	if !isOneMistralClear(after, "v1") {
		t.Fatalf("writes after the profile switch = %+v, want one Mistral clear for v1 — the old profile's reading stayed bound", after)
	}
}

// TestMistralVibeSweep_UnconfirmedReadingIsCleared (review of #2012, finding
// 1, as QA round 1 finding 3 re-decided it): with the grant and a vibe session
// but no reading the sweep can serve for the profile it resolves this tick,
// the reading a session already carries is cleared, never left looking
// current. First seen red as "writes = []" before any clearing existed; the
// stale-marking that followed is replaced by this rule.
func TestMistralVibeSweep_UnconfirmedReadingIsCleared(t *testing.T) {
	for _, tc := range []struct {
		name       string
		browserErr error
		transport  func(int) error
	}{
		{"first poll after a restart is rejected", nil, func(int) error { return authRejected() }},
		{"no Chrome profile to read", errors.New("chromecookies: Chrome profile not found"), func(int) error { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed := mistralListed()
			sessions := staticSessions{{SessionID: "v1", Adapter: "mistral-vibe", Metrics: listed}}
			h := newVibeSweepHarness(sessions, vibeTransport(tc.transport))
			if tc.browserErr != nil {
				err := tc.browserErr
				h.sweeper.deps.Browser = func() (MistralBrowserSession, error) { return nil, err }
			}
			h.sweeper.sweep(context.Background())
			writes := h.writer.snapshot()
			if !isOneMistralClear(writes, "v1") {
				t.Fatalf("writes = %+v, want one Mistral clear: an unconfirmed reading was left looking current", writes)
			}
		})
	}
}

// TestMistralVibeSweep_UnparseableAfterASuccessClears: once the current
// profile's reading has been bound, a later body the parser refuses leaves
// nothing current, so the bound reading goes rather than stay looking
// current.
func TestMistralVibeSweep_UnparseableAfterASuccessClears(t *testing.T) {
	repo := newReflectingRepo("v1")
	poller := NewAccountPoller()
	t0 := time.Date(2026, 11, 21, 13, 38, 33, 0, time.UTC)
	now := t0
	poller.now = func() time.Time { return now }
	transport := &fakeTransport{fn: func(n int, _ outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		if n == 1 {
			return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte(vibeProbeAfterOneTurn)}, nil
		}
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte(`[{"error":{"json":{"message":"changed"}}}]`)}, nil
	}}
	sw := NewMistralVibeSweeper(MistralVibeSweeperDeps{
		Sessions: repo, Writer: repo, Poller: poller,
		Browser: func() (MistralBrowserSession, error) {
			return &fakeBrowserSession{account: "chrome-profile:/p/Default"}, nil
		},
		Transport: transport, Granted: alwaysGranted, Adapter: "mistral-vibe", Log: &sweepLogger{}, Interval: time.Minute,
	})
	sw.sweep(context.Background())
	if repo.current("v1") == nil {
		t.Fatal("vacuity: the first tick bound no reading")
	}
	now = t0.Add(pollFreshWindow + time.Second)
	sw.sweep(context.Background())
	if transport.callCount() != 2 {
		t.Fatalf("vacuity: %d fetches, want 2", transport.callCount())
	}
	if got := repo.current("v1"); got != nil {
		t.Fatalf("a reading stayed bound with nothing current to back it: %+v", *got)
	}
}

// TestMistralVibeRefresh_PassesTheGrantToThePoller (review of #2012,
// finding 3): the grant MistralVibeRefresh is given reaches AccountPoller's
// admission and publish checks, so a revoke that lands between the sweep's
// own check and the fetch reads no cookie and sends nothing, and one that
// lands during the fetch publishes nothing.
func TestMistralVibeRefresh_PassesTheGrantToThePoller(t *testing.T) {
	t.Run("revoked before admission", testVibeRefreshRevokedBeforeAdmission)
	t.Run("revoked during a fetch while the poller holds a stale reading", testVibeRefreshRevokedWhileStale)
	t.Run("revoked during the fetch", testVibeRefreshRevokedDuringTheFetch)
}

func testVibeRefreshRevokedBeforeAdmission(t *testing.T) {
	browser := &fakeBrowserSession{account: "chrome-profile:/p/Default"}
	transport := vibeTransport(func(int) error { return nil })
	snap, err := refreshVibe(NewAccountPoller(), browser, transport, func() bool { return false })
	if snap != nil || err == nil {
		t.Fatalf("a revoked refresh returned snap=%v err=%v, want nothing and an error", snap, err)
	}
	if browser.reads.Load() != 0 || transport.callCount() != 0 {
		t.Fatalf("a revoked refresh read the cookies %d time(s) and sent %d request(s), want none",
			browser.reads.Load(), transport.callCount())
	}
}

// testVibeRefreshRevokedWhileStale is QA round 1, finding 5: the poller's
// notPublished hands back the reading it already holds, stale, alongside
// errRevokedBeforePublish; a revoked refresh must still return nothing.
func testVibeRefreshRevokedWhileStale(t *testing.T) {
	poller := NewAccountPoller()
	t0 := time.Date(2026, 11, 21, 13, 38, 33, 0, time.UTC)
	now := t0
	poller.now = func() time.Time { return now }
	var granted atomic.Bool
	granted.Store(true)
	browser := &fakeBrowserSession{account: "chrome-profile:/p/Default"}
	transport := vibeTransport(staleThenRevoked(&granted))
	if snap, err := refreshVibe(poller, browser, transport, granted.Load); snap == nil || err != nil {
		t.Fatalf("first poll: snap=%v err=%v", snap, err)
	}
	now = t0.Add(pollFreshWindow + time.Second)
	if snap, _ := refreshVibe(poller, browser, transport, granted.Load); snap == nil || snap.RetrievalFailure == "" {
		t.Fatalf("vacuity: the second poll did not leave a stale reading: %+v", snap)
	}
	now = now.Add(pollFailureBackoffCeiling + time.Second)
	snap, _ := refreshVibe(poller, browser, transport, granted.Load)
	if transport.callCount() != 3 {
		t.Fatalf("vacuity: %d fetches, want 3", transport.callCount())
	}
	if snap != nil {
		t.Fatalf("a refresh revoked mid-fetch returned the held stale reading: %+v", *snap)
	}
}

// staleThenRevoked is a transport status whose second fetch is rejected, so
// the held reading goes stale, and whose third revokes granted while it is
// on the wire.
func staleThenRevoked(granted *atomic.Bool) func(int) error {
	return func(n int) error {
		switch n {
		case 2:
			return authRejected()
		case 3:
			granted.Store(false)
		}
		return nil
	}
}

func testVibeRefreshRevokedDuringTheFetch(t *testing.T) {
	var granted atomic.Bool
	granted.Store(true)
	browser := &fakeBrowserSession{account: "chrome-profile:/p/Default"}
	transport := vibeTransport(func(int) error { granted.Store(false); return nil })
	snap, err := refreshVibe(NewAccountPoller(), browser, transport, granted.Load)
	if snap != nil || err == nil {
		t.Fatalf("a reading fetched across a revoke was published: snap=%v err=%v", snap, err)
	}
}

// reflectingRepo is a vibe-session repository whose listed readings follow
// the sweep's writes, so a later tick sees what an earlier one wrote (or a
// test's stand-in for a transcript pass overwriting it).
type reflectingRepo struct {
	mu     sync.Mutex
	snaps  map[string]*session.RateLimitSnapshot
	writes []rateLimitWrite
}

func newReflectingRepo(ids ...string) *reflectingRepo {
	r := &reflectingRepo{snaps: map[string]*session.RateLimitSnapshot{}}
	for _, id := range ids {
		r.snaps[id] = nil
	}
	return r
}

func (r *reflectingRepo) ListAll() ([]*session.SessionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*session.SessionState
	for id, snap := range r.snaps {
		out = append(out, &session.SessionState{SessionID: id, Adapter: "mistral-vibe", Metrics: &session.SessionMetrics{RateLimit: snap}})
	}
	return out, nil
}

func (r *reflectingRepo) SetProviderRateLimit(id, provider string, snap *session.RateLimitSnapshot) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps[id] = snap
	r.writes = append(r.writes, rateLimitWrite{id, provider, snap})
	return true
}

func (r *reflectingRepo) current(id string) *session.RateLimitSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snaps[id]
}

func (r *reflectingRepo) set(id string, snap *session.RateLimitSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps[id] = snap
}

func profileAReading() *session.RateLimitSnapshot {
	return &session.RateLimitSnapshot{Provider: session.ProviderMistral, AttributionQuality: session.AttributionQualityConfirmed,
		Windows: []session.RateLimitWindow{{UsedPercent: 42, WindowMinutes: 43200}}, SampledAt: 1_700_000_000}
}

func newReflectingSweeper(repo *reflectingRepo, browser func() (MistralBrowserSession, error), transport *fakeTransport, granted func() bool) *MistralVibeSweeper {
	return NewMistralVibeSweeper(MistralVibeSweeperDeps{
		Sessions: repo, Writer: repo, Poller: NewAccountPoller(), Browser: browser,
		Transport: transport, Granted: granted, Adapter: "mistral-vibe", Log: &sweepLogger{}, Interval: time.Minute,
	})
}

// TestMistralVibeSweep_RestartNeverShowsAnotherProfilesReading is QA round 1
// finding 3(a): after a restart the sweep cannot tell which Chrome profile a
// persisted reading came from, so when the current profile has no reading
// yet, the persisted one is cleared, never re-stamped stale and shown as this
// profile's.
func TestMistralVibeSweep_RestartNeverShowsAnotherProfilesReading(t *testing.T) {
	repo := newReflectingRepo("v1")
	repo.set("v1", profileAReading())
	profileB := &fakeBrowserSession{account: "chrome-profile:/p/Profile 2", err: chromecookiesAbsent()}
	sw := newReflectingSweeper(repo, func() (MistralBrowserSession, error) { return profileB, nil }, vibeTransport(func(int) error { return nil }), alwaysGranted)
	sw.sweep(context.Background())
	if got := repo.current("v1"); got != nil {
		t.Fatalf("after a restart, profile A's persisted reading is still shown for profile B: %+v", *got)
	}
}

// TestMistralVibeSweep_LostClearIsClearedAgain is QA round 1 finding 3(b):
// the unbind on a profile switch is level-triggered, so a writer that saves an
// older copy over it gets cleared again on the next tick, not marked stale.
func TestMistralVibeSweep_LostClearIsClearedAgain(t *testing.T) {
	repo := newReflectingRepo("v1")
	var current atomic.Value
	profileA := &fakeBrowserSession{account: "chrome-profile:/p/Default"}
	profileB := &fakeBrowserSession{account: "chrome-profile:/p/Profile 2", err: chromecookiesAbsent()}
	current.Store(MistralBrowserSession(profileA))
	sw := newReflectingSweeper(repo, func() (MistralBrowserSession, error) { return current.Load().(MistralBrowserSession), nil },
		vibeTransport(func(int) error { return nil }), alwaysGranted)
	sw.sweep(context.Background())
	readingA := repo.current("v1")
	if readingA == nil {
		t.Fatal("vacuity: profile A's tick bound no reading")
	}
	current.Store(MistralBrowserSession(profileB))
	sw.sweep(context.Background())
	if repo.current("v1") != nil {
		t.Fatal("vacuity: the profile switch did not clear profile A's reading")
	}
	repo.set("v1", readingA) // a transcript pass saves its older copy over the clear
	sw.sweep(context.Background())
	if got := repo.current("v1"); got != nil {
		t.Fatalf("a resurrected profile-A reading survived the next tick under profile B: %+v", *got)
	}
}

// TestMistralVibeSweep_SwitchToAnIdenticalReadingKeepsIt is QA round 1
// finding 4: switching to a profile whose reading happens to be identical
// rebinds it in the same tick instead of leaving the session empty.
func TestMistralVibeSweep_SwitchToAnIdenticalReadingKeepsIt(t *testing.T) {
	repo := newReflectingRepo("v1")
	var current atomic.Value
	current.Store(MistralBrowserSession(&fakeBrowserSession{account: "chrome-profile:/p/Default"}))
	sw := newReflectingSweeper(repo, func() (MistralBrowserSession, error) { return current.Load().(MistralBrowserSession), nil },
		vibeTransport(func(int) error { return nil }), alwaysGranted)
	sw.sweep(context.Background())
	current.Store(MistralBrowserSession(&fakeBrowserSession{account: "chrome-profile:/p/Profile 2"}))
	sw.sweep(context.Background())
	if repo.current("v1") == nil {
		t.Fatal("switching to a profile with an identical reading left the session without one")
	}
}

// TestMistralVibeSweep_RevokeBeforeTheWriteWritesNothing is QA round 1
// finding 5's sweep half: a revoke landing after the refresh returned but
// before the sweep writes leaves the sessions unbound. The grant turns false
// on its fourth read in a tick that serves a cached reading: the sweep's own
// check (1), the poller's admission (2), MistralVibeRefresh's re-check (3),
// and the sweep's check just before it writes (4).
func TestMistralVibeSweep_RevokeBeforeTheWriteWritesNothing(t *testing.T) {
	repo := newReflectingRepo("v1")
	var calls atomic.Int64
	revokeAt := int64(1 << 30)
	granted := func() bool { return calls.Add(1) < revokeAt }
	sw := newReflectingSweeper(repo, func() (MistralBrowserSession, error) {
		return &fakeBrowserSession{account: "chrome-profile:/p/Default"}, nil
	}, vibeTransport(func(int) error { return nil }), granted)
	sw.sweep(context.Background())
	if repo.current("v1") == nil {
		t.Fatal("vacuity: the first tick bound no reading")
	}
	repo.set("v1", nil)
	calls.Store(0)
	revokeAt = 4
	sw.sweep(context.Background())
	if got := repo.current("v1"); got != nil {
		t.Fatalf("a reading was written after the permission was revoked mid-tick: %+v", *got)
	}
}

func chromecookiesAbsent() error {
	return fmt.Errorf("%w: ory_session_*", chromecookies.ErrCookieAbsent)
}
