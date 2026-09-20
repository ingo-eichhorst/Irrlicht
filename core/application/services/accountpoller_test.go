package services

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/ports/outbound"
)

// fakeTransport is an in-memory outbound.AccountQuotaTransport — nothing in
// this file's tests touches a real socket, matching AGENTS.md's "the suite
// must never leave the machine": here that's not merely a network policy
// tests happen to satisfy, it's structural, since this package's own tests
// never construct an *http.Client at all. The adapter package
// (core/adapters/outbound/accountquota) carries the httptest-based proof
// that the REAL transport enforces the same properties over the wire.
type fakeTransport struct {
	mu       sync.Mutex
	calls    int
	fn       func(callNum int, req outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error)
	entered  chan struct{} // closed on the FIRST call, if non-nil
	release  chan struct{} // if non-nil, blocked on before returning
	sawCreds []string
}

func (f *fakeTransport) Fetch(ctx context.Context, req outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.sawCreds = append(f.sawCreds, req.Credential.Reveal())
	entered := f.entered
	f.mu.Unlock()

	if n == 1 && entered != nil {
		close(entered)
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return outbound.AccountQuotaResponse{}, ctx.Err()
		}
	}
	return f.fn(n, req)
}

func (f *fakeTransport) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeResolver struct {
	secret string
	err    error
}

func (r fakeResolver) Resolve(context.Context) (outbound.Credential, error) {
	if r.err != nil {
		return outbound.Credential{}, r.err
	}
	return outbound.NewCredential(r.secret), nil
}

func alwaysGranted() bool { return true }

func TestAccountPoller_UnknownAccountNeverPolls(t *testing.T) {
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		t.Fatal("transport.Fetch called for an unconfirmed account")
		return outbound.AccountQuotaResponse{}, nil
	}}
	p := NewAccountPoller()
	obs, err := p.Poll(context.Background(), PollRequest{
		Key:            AccountQuotaKey{Provider: "muse", Account: "", Scope: "default"},
		Granted:        alwaysGranted,
		Resolver:       fakeResolver{secret: "s"},
		Transport:      transport,
		DestinationKey: "primary",
	})
	if !errors.Is(err, errUnknownAccount) {
		t.Fatalf("err = %v, want errUnknownAccount", err)
	}
	if obs.HasValue {
		t.Fatal("expected no value for an unknown account")
	}
	if transport.callCount() != 0 {
		t.Fatalf("transport was called %d time(s), want 0", transport.callCount())
	}
}

func TestAccountPoller_PendingOrDeniedNeverPolls(t *testing.T) {
	// A pending permission and a denied one are indistinguishable to Poll —
	// both read as "not granted" (#2003 §1.1: "a pending permission permits
	// nothing. A denied permission permits nothing.") — so one
	// always-false Granted covers both.
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		t.Fatal("transport.Fetch called while not granted")
		return outbound.AccountQuotaResponse{}, nil
	}}
	p := NewAccountPoller()
	_, err := p.Poll(context.Background(), PollRequest{
		Key:            AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"},
		Granted:        func() bool { return false },
		Resolver:       fakeResolver{secret: "s"},
		Transport:      transport,
		DestinationKey: "primary",
	})
	if !errors.Is(err, errNotGranted) {
		t.Fatalf("err = %v, want errNotGranted", err)
	}
}

func TestAccountPoller_NilGrantedIsTreatedAsNotGranted(t *testing.T) {
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		t.Fatal("transport.Fetch called with a nil Granted")
		return outbound.AccountQuotaResponse{}, nil
	}}
	p := NewAccountPoller()
	_, err := p.Poll(context.Background(), PollRequest{
		Key:            AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"},
		Resolver:       fakeResolver{secret: "s"},
		Transport:      transport,
		DestinationKey: "primary",
	})
	if !errors.Is(err, errNotGranted) {
		t.Fatalf("err = %v, want errNotGranted", err)
	}
}

func TestAccountPoller_TenSessionsOneConfirmedAccountIsOnePoll(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	transport := &fakeTransport{
		entered: entered,
		release: release,
		fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
			return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte(`{"units":1}`)}, nil
		},
	}
	p := NewAccountPoller()
	key := AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"}
	req := func(sessionID string) PollRequest {
		return PollRequest{Key: key, SessionID: sessionID, Granted: alwaysGranted, Resolver: fakeResolver{secret: "s"}, Transport: transport, DestinationKey: "primary"}
	}

	results := make([]Observation, 10)
	errs := make([]error, 10)
	var wg sync.WaitGroup

	// Start the leader first and wait until its request has actually reached
	// the transport — Poll sets pollEntry.inflight BEFORE calling the
	// transport, so by the time `entered` fires, every later Poll call for
	// this key is guaranteed to observe a non-nil inflight and join it
	// rather than racing to become a second leader.
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], errs[0] = p.Poll(context.Background(), req("s0"))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the leader's request to reach the transport")
	}

	for i := 1; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = p.Poll(context.Background(), req("s"+string(rune('0'+i))))
		}(i)
	}
	close(release)
	wg.Wait()

	if got := transport.callCount(); got != 1 {
		t.Fatalf("transport was called %d time(s), want exactly 1 (ten sessions, one confirmed account)", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("result[%d]: unexpected error %v", i, err)
		}
		if !results[i].HasValue || !bytes.Equal(results[i].Body, []byte(`{"units":1}`)) {
			t.Errorf("result[%d] = %+v, want the shared successful observation", i, results[i])
		}
	}
}

func TestAccountPoller_TwoConfirmedAccountsAreTwoPollsNoSharedResult(t *testing.T) {
	transport := &fakeTransport{fn: func(_ int, req outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		// Echo the credential back so the test can verify each account's
		// result carries only ITS OWN body — proving isolation, not merely
		// counting calls.
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte(req.Credential.Reveal())}, nil
	}}
	p := NewAccountPoller()

	obs1, err1 := p.Poll(context.Background(), PollRequest{
		Key:     AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"},
		Granted: alwaysGranted, Resolver: fakeResolver{secret: "secret-1"}, Transport: transport, DestinationKey: "primary",
	})
	obs2, err2 := p.Poll(context.Background(), PollRequest{
		Key:     AccountQuotaKey{Provider: "muse", Account: "acct-2", Scope: "default"},
		Granted: alwaysGranted, Resolver: fakeResolver{secret: "secret-2"}, Transport: transport, DestinationKey: "primary",
	})
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if transport.callCount() != 2 {
		t.Fatalf("transport was called %d time(s), want 2", transport.callCount())
	}
	if string(obs1.Body) != "secret-1" || string(obs2.Body) != "secret-2" {
		t.Fatalf("results were not isolated per account: obs1=%q obs2=%q", obs1.Body, obs2.Body)
	}
}

func TestAccountPoller_AuthFailureNeverPublishesZeroUsage(t *testing.T) {
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureAuthRejected, Detail: "status 401"}
	}}
	p := NewAccountPoller()
	obs, err := p.Poll(context.Background(), PollRequest{
		Key:     AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"},
		Granted: alwaysGranted, Resolver: fakeResolver{secret: "s"}, Transport: transport, DestinationKey: "primary",
	})
	if err == nil {
		t.Fatal("expected an error for an auth-rejected fetch")
	}
	if obs.HasValue {
		t.Fatalf("expected no published value after a 401, got %+v", obs)
	}
	if obs.FailureReason != string(outbound.QuotaFailureAuthRejected) {
		t.Fatalf("FailureReason = %q, want %q", obs.FailureReason, outbound.QuotaFailureAuthRejected)
	}
}

func TestAccountPoller_CachedValueGoesStaleButKeepsOriginalObservationTime(t *testing.T) {
	fail := atomic.Bool{}
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		if fail.Load() {
			return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureNetwork}
		}
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte("good")}, nil
	}}
	p := NewAccountPoller()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return t0 }

	key := AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"}
	req := PollRequest{Key: key, Granted: alwaysGranted, Resolver: fakeResolver{secret: "s"}, Transport: transport, DestinationKey: "primary"}

	obs, err := p.Poll(context.Background(), req)
	if err != nil || !obs.HasValue || obs.Stale {
		t.Fatalf("initial poll: obs=%+v err=%v, want a fresh success", obs, err)
	}

	// Advance past both the fresh window AND (defensively) let a first
	// failure's backoff be irrelevant by jumping straight to a time beyond
	// pollFreshWindow.
	fail.Store(true)
	p.now = func() time.Time { return t0.Add(pollFreshWindow + time.Second) }

	obs2, err2 := p.Poll(context.Background(), req)
	if err2 == nil {
		t.Fatal("expected an error once the poll starts failing")
	}
	if !obs2.HasValue {
		t.Fatalf("expected the STALE prior value to be retained, got HasValue=false: %+v", obs2)
	}
	if string(obs2.Body) != "good" {
		t.Fatalf("Body = %q, want the original successful body retained", obs2.Body)
	}
	if !obs2.FetchedAt.Equal(t0) {
		t.Fatalf("FetchedAt = %v, want the ORIGINAL observation time %v", obs2.FetchedAt, t0)
	}
	if !obs2.Stale {
		t.Fatal("expected Stale=true after a failed poll")
	}
	if obs2.FailureReason == "" {
		t.Fatal("expected a non-empty FailureReason")
	}
}

func TestAccountPoller_BackoffSuppressesImmediateRetry(t *testing.T) {
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		return outbound.AccountQuotaResponse{}, &outbound.QuotaError{Reason: outbound.QuotaFailureNetwork}
	}}
	p := NewAccountPoller()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return t0 }
	req := PollRequest{
		Key:     AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"},
		Granted: alwaysGranted, Resolver: fakeResolver{secret: "s"}, Transport: transport, DestinationKey: "primary",
	}

	if _, err := p.Poll(context.Background(), req); err == nil {
		t.Fatal("expected the first poll to fail")
	}
	if got := transport.callCount(); got != 1 {
		t.Fatalf("calls after first poll = %d, want 1", got)
	}

	// Same instant: still within backoff — must NOT call the transport again.
	if _, err := p.Poll(context.Background(), req); err != nil {
		t.Fatalf("a backoff-suppressed poll should return the cached state with no error, got %v", err)
	}
	if got := transport.callCount(); got != 1 {
		t.Fatalf("calls while backoff is in effect = %d, want still 1", got)
	}

	// Advance past the backoff window — must retry.
	p.now = func() time.Time { return t0.Add(pollFailureBackoff + time.Second) }
	if _, err := p.Poll(context.Background(), req); err == nil {
		t.Fatal("expected the retried poll to fail again (transport still failing)")
	}
	if got := transport.callCount(); got != 2 {
		t.Fatalf("calls after the backoff window elapsed = %d, want 2", got)
	}
}

func TestAccountPoller_RevokeCancelsInFlightRequest(t *testing.T) {
	entered := make(chan struct{})
	blockUntilCanceled := make(chan struct{}) // never closed — the point is ctx cancellation, not a real response
	transport := &fakeTransport{
		entered: entered,
		release: blockUntilCanceled,
		fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
			t.Fatal("transport.fn reached — the request should have been cancelled first")
			return outbound.AccountQuotaResponse{}, nil
		},
	}
	p := NewAccountPoller()
	key := AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"}

	type result struct {
		obs Observation
		err error
	}
	done := make(chan result, 1)
	go func() {
		obs, err := p.Poll(context.Background(), PollRequest{Key: key, Granted: alwaysGranted, Resolver: fakeResolver{secret: "s"}, Transport: transport, DestinationKey: "primary"})
		done <- result{obs, err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the request to reach the transport")
	}

	p.Revoke("muse")

	select {
	case r := <-done:
		if r.err == nil {
			t.Fatal("expected an error from a cancelled in-flight request")
		}
		if r.obs.HasValue {
			t.Fatalf("expected no published value from a cancelled request, got %+v", r.obs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Poll to return after Revoke")
	}

	p.mu.Lock()
	_, exists := p.cache[key]
	p.mu.Unlock()
	if exists {
		t.Fatal("expected Revoke to wipe the cache entry entirely")
	}
}

// TestAccountPoller_RevokedDuringInFlightRequestNeverPublishes is the
// deterministic reproduction of #2003 §6's "a permission revoked during an
// in-flight request: ... its result is never published" — isolated from
// TestAccountPoller_RevokeCancelsInFlightRequest above on PURPOSE.
//
// That other test proves Revoke's context cancellation stops a request from
// ever completing. This one proves the SEPARATE guard doFetch's final
// req.Granted() check is: it lets the fetch complete SUCCESSFULLY (a real
// 200 with a real body reaches doFetch) and flips the caller's Granted
// predicate to false in between — without ever calling Revoke — modeling the
// narrow real-world race where a permission's state already reads denied but
// this fetch's own context was never the one Revoke would have cancelled
// (e.g. a different in-flight fetch for the same provider on another
// account). Deterministic throughout: channel signals gate every step, never
// a sleep.
func TestAccountPoller_RevokedDuringInFlightRequestNeverPublishes(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var granted atomic.Bool
	granted.Store(true)

	transport := &fakeTransport{
		entered: entered,
		release: release,
		fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
			return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte("should-never-be-published")}, nil
		},
	}
	p := NewAccountPoller()
	key := AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"}
	req := PollRequest{
		Key: key, Granted: granted.Load, Resolver: fakeResolver{secret: "s"}, Transport: transport, DestinationKey: "primary",
	}

	type result struct {
		obs Observation
		err error
	}
	done := make(chan result, 1)
	go func() {
		obs, err := p.Poll(context.Background(), req)
		done <- result{obs, err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the request to reach the transport")
	}

	// The permission is now denied, but nothing cancels this fetch's
	// context — it is allowed to complete normally.
	granted.Store(false)
	close(release)

	select {
	case r := <-done:
		if !errors.Is(r.err, errRevokedBeforePublish) {
			t.Fatalf("err = %v, want errRevokedBeforePublish", r.err)
		}
		if r.obs.HasValue {
			t.Fatalf("expected the successful fetch to be DISCARDED, got a published value: %+v", r.obs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Poll to return")
	}

	p.mu.Lock()
	e, exists := p.cache[key]
	p.mu.Unlock()
	if exists && e.obs.HasValue {
		t.Fatalf("the discarded fetch's body leaked into the cache: %+v", e.obs)
	}
}

func TestAccountPoller_RevokeWipesCachedValue(t *testing.T) {
	transport := &fakeTransport{fn: func(int, outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
		return outbound.AccountQuotaResponse{StatusCode: 200, Body: []byte("good")}, nil
	}}
	p := NewAccountPoller()
	key := AccountQuotaKey{Provider: "muse", Account: "acct-1", Scope: "default"}
	req := PollRequest{Key: key, Granted: alwaysGranted, Resolver: fakeResolver{secret: "s"}, Transport: transport, DestinationKey: "primary"}

	if _, err := p.Poll(context.Background(), req); err != nil {
		t.Fatalf("initial poll failed: %v", err)
	}
	p.Revoke("muse")

	p.mu.Lock()
	_, exists := p.cache[key]
	p.mu.Unlock()
	if exists {
		t.Fatal("expected Revoke to remove the cached observation")
	}
}
