package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/session"
	outbound "irrlicht/core/ports/outbound"
)

// Issue #2057: the sweep is what finally calls MuseAccountRefresh for live
// Muse sessions and hands the result to the detector.

type rateLimitWrite struct {
	sessionID, provider string
	snap                *session.RateLimitSnapshot
}

type fakeRateLimitWriter struct {
	mu     sync.Mutex
	writes []rateLimitWrite
}

func (w *fakeRateLimitWriter) SetProviderRateLimit(id, provider string, snap *session.RateLimitSnapshot) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, rateLimitWrite{id, provider, snap})
	return true
}

func (w *fakeRateLimitWriter) snapshot() []rateLimitWrite {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]rateLimitWrite(nil), w.writes...)
}

type staticSessions []*session.SessionState

func (s staticSessions) ListAll() ([]*session.SessionState, error) { return s, nil }

type sweepLogger struct {
	mu   sync.Mutex
	info []string
	errs []string
}

func (l *sweepLogger) LogInfo(_, _, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.info = append(l.info, msg)
}
func (l *sweepLogger) LogError(_, _, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errs = append(l.errs, msg)
}
func (*sweepLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (*sweepLogger) Close() error                                            { return nil }

type countingResolver struct {
	mu    sync.Mutex
	calls int
}

func (r *countingResolver) Resolve(context.Context) (outbound.Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return outbound.NewCredential("tok"), nil
}

func newTestSweeper(sessions sessionLister, transport outbound.AccountQuotaTransport, granted func() bool) (*MuseAccountSweeper, *fakeRateLimitWriter) {
	w := &fakeRateLimitWriter{}
	return NewMuseAccountSweeper(MuseAccountSweeperDeps{
		Sessions:  sessions,
		Writer:    w,
		Poller:    NewAccountPoller(),
		Resolver:  fakeResolver{secret: "tok"},
		Transport: transport,
		Granted:   granted,
		Adapter:   "muse",
		Log:       &sweepLogger{},
		Interval:  time.Minute,
	}), w
}

func TestMuseAccountSweep_GrantedWritesMetaSnapshot(t *testing.T) {
	sw, w := newTestSweeper(staticSessions{{SessionID: "m1", Adapter: "muse"}}, museFixtureTransport(), alwaysGranted)
	sw.sweep(context.Background())

	writes := w.snapshot()
	if len(writes) != 1 {
		t.Fatalf("writes = %+v, want exactly one for m1", writes)
	}
	got := writes[0]
	if got.sessionID != "m1" || got.provider != session.ProviderMeta || got.snap == nil || got.snap.Provider != session.ProviderMeta {
		t.Fatalf("write = %+v, want a meta snapshot for m1", got)
	}
}

// Consent gate: without the grant nothing is fetched, and the sweep clears
// any meta snapshot it may have written earlier — that clear is what makes a
// revoked permission take the chip away. A session listed without one is
// left alone, so a never-granted daemon does no per-session writes.
func TestMuseAccountSweep_NotGrantedClearsAndNeverFetches(t *testing.T) {
	transport := museFixtureTransport()
	sessions := staticSessions{
		{SessionID: "m1", Adapter: "muse", Metrics: &session.SessionMetrics{RateLimit: &session.RateLimitSnapshot{Provider: session.ProviderMeta}}},
		{SessionID: "m2", Adapter: "muse"},
	}
	sw, w := newTestSweeper(sessions, transport, func() bool { return false })
	sw.sweep(context.Background())

	if n := transport.callCount(); n != 0 {
		t.Fatalf("transport called %d times without the grant", n)
	}
	writes := w.snapshot()
	if len(writes) != 1 || writes[0].sessionID != "m1" || writes[0].provider != session.ProviderMeta || writes[0].snap != nil {
		t.Fatalf("writes = %+v, want one meta clear for m1", writes)
	}
}

// Lock: a failed poll publishes nothing — no zero snapshot, and no clear of a
// reading the session may already carry.
// tools/lib/muse-account-sweep-mutations_test.sh mutates the failure branch.
func TestMuseAccountSweep_FailureWritesNothing(t *testing.T) {
	failing := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		return outbound.AccountQuotaResponse{}, errors.New("boom")
	}}
	sw, w := newTestSweeper(staticSessions{{SessionID: "m1", Adapter: "muse"}}, failing, alwaysGranted)
	sw.sweep(context.Background())

	if writes := w.snapshot(); len(writes) != 0 {
		t.Fatalf("writes after a failed poll = %+v, want none", writes)
	}
}

// Only top-level Muse sessions are polled: other adapters never are, and a
// Muse subagent shares its parent's account, so the parent's chip covers it.
func TestMuseAccountSweep_OnlyTopLevelMuseSessions(t *testing.T) {
	sessions := staticSessions{
		{SessionID: "m1", Adapter: "muse"},
		{SessionID: "m1-sub", Adapter: "muse", ParentSessionID: "m1"},
		{SessionID: "c1", Adapter: "claude-code"},
	}
	transport := museFixtureTransport()
	sw, w := newTestSweeper(sessions, transport, alwaysGranted)
	sw.sweep(context.Background())

	writes := w.snapshot()
	if len(writes) != 1 || writes[0].sessionID != "m1" {
		t.Fatalf("writes = %+v, want exactly one for m1", writes)
	}
	if n := transport.callCount(); n != 1 {
		t.Fatalf("transport called %d times, want 1", n)
	}
}

// Review finding (#2057): every session used to resolve the credential on
// its own, and the Keychain route measured ~9.5s per read. One sweep now
// resolves it at most once, however many sessions need a fetch.
func TestMuseAccountSweep_ResolvesCredentialOncePerSweep(t *testing.T) {
	sessions := staticSessions{{SessionID: "m1", Adapter: "muse"}, {SessionID: "m2", Adapter: "muse"}, {SessionID: "m3", Adapter: "muse"}}
	transport := museFixtureTransport()
	sw, _ := newTestSweeper(sessions, transport, alwaysGranted)
	resolver := &countingResolver{}
	sw.deps.Resolver = resolver
	sw.sweep(context.Background())

	if n := transport.callCount(); n != 3 {
		t.Fatalf("transport called %d times, want one fetch per session (3)", n)
	}
	if resolver.calls != 1 {
		t.Fatalf("credential resolved %d times in one sweep, want 1", resolver.calls)
	}
}

// Review finding (#2057): after a success, a failed fetch must reach the
// session as the old reading stamped with the failure, not be dropped.
func TestMuseAccountSweep_FailureAfterSuccessWritesStaleReading(t *testing.T) {
	fail := false
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		if fail {
			return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureAuthRejected, Detail: "status 401"}
		}
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte(museFixtureBody)}, nil
	}}
	sw, w := newTestSweeper(staticSessions{{SessionID: "m1", Adapter: "muse"}}, transport, alwaysGranted)
	clock := time.Unix(10_000, 0)
	sw.deps.Poller.now = func() time.Time { return clock }
	sw.sweep(context.Background())

	clock = clock.Add(pollFreshWindow + time.Second)
	fail = true
	sw.sweep(context.Background())

	writes := w.snapshot()
	if len(writes) != 2 {
		t.Fatalf("writes = %+v, want the fresh reading then the stale one", writes)
	}
	if got := writes[1].snap; got == nil || got.RetrievalFailure == "" || len(got.Windows) == 0 {
		t.Fatalf("second write = %+v, want the old windows stamped with a retrieval failure", got)
	}
}

// Review finding (#2057): a persistent failure alternates between the
// fetching tick's error and the backoff tick's error. It is logged once per
// failure streak, not once per message change.
func TestMuseAccountSweep_PersistentFailureLoggedOnce(t *testing.T) {
	failing := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureAuthRejected, Detail: "status 401"}
	}}
	sw, _ := newTestSweeper(staticSessions{{SessionID: "m1", Adapter: "muse"}}, failing, alwaysGranted)
	log := sw.deps.Log.(*sweepLogger)
	clock := time.Unix(10_000, 0)
	sw.deps.Poller.now = func() time.Time { return clock }
	for i := 0; i < 6; i++ {
		sw.sweep(context.Background())
		clock = clock.Add(20 * time.Second)
	}
	if failing.callCount() < 2 {
		t.Fatalf("transport called %d times; the test needs both fetching and backoff ticks", failing.callCount())
	}
	if n := len(log.info); n != 1 {
		t.Fatalf("logged %d failure lines over one failure streak, want 1: %q", n, log.info)
	}
}

// Review finding (#2057): the poller's cache entry for a session that is gone
// is dropped, so a long-running daemon doesn't keep one per session ever seen.
func TestMuseAccountSweep_ForgetsEndedSessions(t *testing.T) {
	sessions := &staticSessions{{SessionID: "m1", Adapter: "muse"}}
	sw, _ := newTestSweeper(listerFunc(func() ([]*session.SessionState, error) { return *sessions, nil }), museFixtureTransport(), alwaysGranted)
	sw.sweep(context.Background())
	if !sw.deps.Poller.hasEntry(MuseAccountQuotaKey("m1")) {
		t.Fatal("no poller entry after polling m1; the test cannot observe eviction")
	}
	*sessions = nil
	sw.sweep(context.Background())
	if sw.deps.Poller.hasEntry(MuseAccountQuotaKey("m1")) {
		t.Fatal("poller still holds m1's entry after the session ended")
	}
}

type listerFunc func() ([]*session.SessionState, error)

func (f listerFunc) ListAll() ([]*session.SessionState, error) { return f() }

// hasEntry reports whether key has a poller cache entry — test-only
// visibility into AccountPoller.Forget.
func (p *AccountPoller) hasEntry(key AccountQuotaKey) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.cache[key]
	return ok
}

// Review finding (#2057): a failed session listing is logged, not dropped.
func TestMuseAccountSweep_ListFailureIsLogged(t *testing.T) {
	sw, _ := newTestSweeper(listerFunc(func() ([]*session.SessionState, error) { return nil, errors.New("disk gone") }), museFixtureTransport(), alwaysGranted)
	sw.sweep(context.Background())
	if log := sw.deps.Log.(*sweepLogger); len(log.errs) != 1 {
		t.Fatalf("errors logged = %q, want the listing failure once", log.errs)
	}
}

// A tick whose reading matches the snapshot the session is already listed
// with makes no write, so a cached poll result costs no lock or disk read.
// tools/lib/muse-account-sweep-mutations_test.sh drops the check.
func TestMuseAccountSweep_UnchangedReadingIsNotRewritten(t *testing.T) {
	sw, w := newTestSweeper(staticSessions{{SessionID: "m1", Adapter: "muse"}}, museFixtureTransport(), alwaysGranted)
	sw.sweep(context.Background())
	first := w.snapshot()
	if len(first) != 1 || first[0].snap == nil {
		t.Fatalf("first sweep writes = %+v, want one reading", first)
	}
	sw.deps.Sessions = staticSessions{{SessionID: "m1", Adapter: "muse", Metrics: &session.SessionMetrics{RateLimit: first[0].snap}}}
	sw.sweep(context.Background())
	if writes := w.snapshot(); len(writes) != 1 {
		t.Fatalf("writes = %d after a tick with an unchanged reading, want still 1", len(writes))
	}
}
