package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	outbound "irrlicht/core/ports/outbound"
)

// blockingResolver counts reads and holds each one until release is closed.
type blockingResolver struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{} // receives once per read, when it starts
	release chan struct{}
}

func (r *blockingResolver) Resolve(context.Context) (outbound.Credential, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	r.entered <- struct{}{}
	<-r.release
	return outbound.NewCredential("tok"), nil
}

func (r *blockingResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// waitDone fails the test when ch is not closed within a deadline.
func waitDone(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("after 5s: %s", what)
	}
}

// Issue #2062: Apply's warm-up read and the sweep's first tick can overlap.
// The second caller joins the read in flight rather than starting another,
// which would be a second Keychain dialog.
func TestGrantCredentialCache_ConcurrentCallersShareOneRead(t *testing.T) {
	inner := &blockingResolver{entered: make(chan struct{}, 4), release: make(chan struct{})}
	cache := NewGrantCredentialCache(inner, keepAllFailures)

	const callers = 3
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	resolve := func() {
		defer wg.Done()
		_, err := cache.Resolve(context.Background())
		errs <- err
	}
	wg.Add(1)
	go resolve()
	waitDone(t, inner.entered, "the first read never started")
	// The read is now in flight, so every later caller finds it cached and
	// can only wait on it: no sleep is needed to line them up.
	for i := 1; i < callers; i++ {
		wg.Add(1)
		go resolve()
	}
	close(inner.release)
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	waitDone(t, allDone, "callers still blocked after the read finished")
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if n := inner.count(); n != 1 {
		t.Fatalf("%d reads for %d concurrent callers, want 1", n, callers)
	}
}

// Reset runs synchronously on the revoke path (the HTTP answer handler); it
// must not wait for a read in flight, which can be an open Keychain dialog.
func TestGrantCredentialCache_ResetDoesNotWaitForAReadInFlight(t *testing.T) {
	inner := &blockingResolver{entered: make(chan struct{}, 4), release: make(chan struct{})}
	defer close(inner.release)
	cache := NewGrantCredentialCache(inner, keepAllFailures)
	go func() { _, _ = cache.Resolve(context.Background()) }()
	waitDone(t, inner.entered, "the read never started")

	reset := make(chan struct{})
	go func() { cache.Reset(); close(reset) }()
	waitDone(t, reset, "Reset blocked on a read in flight")
}

// A caller waiting on someone else's read gives up with its own context.
func TestGrantCredentialCache_WaiterHonorsItsOwnContext(t *testing.T) {
	inner := &blockingResolver{entered: make(chan struct{}, 4), release: make(chan struct{})}
	defer close(inner.release)
	cache := NewGrantCredentialCache(inner, keepAllFailures)
	go func() { _, _ = cache.Resolve(context.Background()) }()
	waitDone(t, inner.entered, "the read never started")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.Resolve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter err = %v, want context.Canceled", err)
	}
}

// A failure is served from the cache until Reset.
// tools/lib/muse-account-sweep-mutations_test.sh stops Resolve caching it.
func TestGrantCredentialCache_FailureIsStickyUntilReset(t *testing.T) {
	inner := &countingResolver{err: errors.New("security did not answer")}
	cache := NewGrantCredentialCache(inner, keepAllFailures)
	for i := 0; i < 3; i++ {
		if _, err := cache.Resolve(context.Background()); err == nil {
			t.Fatalf("resolve %d succeeded, want the cached failure", i)
		}
	}
	if n := inner.count(); n != 1 {
		t.Fatalf("%d reads after a failure, want 1", n)
	}
	cache.Reset()
	_, _ = cache.Resolve(context.Background())
	if n := inner.count(); n != 2 {
		t.Fatalf("%d reads after a reset, want 2", n)
	}
}

// InvalidateOnce drops the credential once per Reset.
func TestGrantCredentialCache_InvalidateOnceIsRearmedByReset(t *testing.T) {
	cache := NewGrantCredentialCache(&countingResolver{}, keepAllFailures)
	if !cache.InvalidateOnce() {
		t.Fatal("first InvalidateOnce after construction did nothing")
	}
	if cache.InvalidateOnce() {
		t.Fatal("second InvalidateOnce dropped the credential again")
	}
	cache.Reset()
	if !cache.InvalidateOnce() {
		t.Fatal("InvalidateOnce after a Reset did nothing")
	}
}

// A panicking resolver still completes its read, so later callers get an
// error instead of blocking forever on a read that never finishes.
func TestGrantCredentialCache_PanicCompletesTheRead(t *testing.T) {
	cache := NewGrantCredentialCache(panickingResolver{}, keepAllFailures)
	func() {
		defer func() { _ = recover() }()
		_, _ = cache.Resolve(context.Background())
	}()
	done := make(chan struct{})
	var err error
	go func() { _, err = cache.Resolve(context.Background()); close(done) }()
	waitDone(t, done, "a caller blocked on a read whose resolver panicked")
	if !errors.Is(err, errCredentialReadPanicked) {
		t.Fatalf("err = %v, want errCredentialReadPanicked", err)
	}
}

func keepAllFailures(error) bool { return true }

// Issue #2062 review: a failure that raised no dialog (no auth.json yet, on
// the file route) is not kept, so a login made after the grant is picked up
// without a revoke and re-grant.
func TestGrantCredentialCache_UnkeptFailureIsReadAgain(t *testing.T) {
	inner := &countingResolver{err: errors.New("reading auth.json: no such file")}
	cache := NewGrantCredentialCache(inner, func(error) bool { return false })
	for i := 0; i < 3; i++ {
		_, _ = cache.Resolve(context.Background())
	}
	if n := inner.count(); n != 3 {
		t.Fatalf("%d reads for three resolves after unkept failures, want 3", n)
	}
}

// Issue #2062 review: a rejection that arrives while a fresh read is still
// running (a revoke and re-grant raced the sweep's fetch) came from an older
// credential. It neither orphans that read — which would start a second one,
// a second dialog — nor uses up the grant's one re-read.
func TestGrantCredentialCache_InvalidateOnceLeavesAReadInFlight(t *testing.T) {
	inner := &blockingResolver{entered: make(chan struct{}, 4), release: make(chan struct{})}
	cache := NewGrantCredentialCache(inner, keepAllFailures)
	done := make(chan struct{})
	go func() { _, _ = cache.Resolve(context.Background()); close(done) }()
	waitDone(t, inner.entered, "the read never started")

	if cache.InvalidateOnce() {
		t.Fatal("InvalidateOnce dropped a read still in flight")
	}
	close(inner.release)
	waitDone(t, done, "the read never finished")
	if _, err := cache.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := inner.count(); n != 1 {
		t.Fatalf("%d reads, want 1: the rejection orphaned the read in flight", n)
	}
	if !cache.InvalidateOnce() {
		t.Fatal("the grant's one re-read was used up by a rejection that dropped nothing")
	}
}
