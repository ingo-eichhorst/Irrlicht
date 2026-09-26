package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/museaccountapi"
	"irrlicht/core/ports/outbound"
)

// Issue #2057 review: the defect #2057 fixed was "no production caller", so
// the builder that hands the daemon its sweep is pinned here. It does not
// reach startBackgroundLoops' `go ...Run` line; that start is covered only by
// the live check named in the PR.
func TestMuseAccountAPI_SweeperIsBuiltWhenTheTransportIs(t *testing.T) {
	api := museAccountAPIEffects(e2eLog{})
	if api.transport == nil {
		t.Fatal("museAccountAPIEffects built no transport for the fixed Muse destination")
	}
	if api.sweeper(nil, nil, nil, e2eLog{}) == nil {
		t.Fatal("sweeper() returned nil with a transport; the daemon would start no sweep")
	}
}

func TestMuseAccountAPI_NoSweeperWithoutATransport(t *testing.T) {
	if got := (museAccountAPI{}).sweeper(nil, nil, nil, e2eLog{}); got != nil {
		t.Fatal("sweeper() built a sweep with no transport to poll through")
	}
}

// countingCredentials counts the reads the wiring makes; each one is a macOS
// Keychain dialog on Muse's Keychain route (issue #2062).
type countingCredentials struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingCredentials) Resolve(context.Context) (outbound.Credential, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return outbound.Credential{}, c.err
	}
	return outbound.NewCredential("tok"), nil
}

func (c *countingCredentials) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// waitForReads polls until c has made want reads, failing at a deadline.
func waitForReads(t *testing.T, c *countingCredentials, want int) {
	t.Helper()
	if !pollUntil(t, 5*time.Second, 5*time.Millisecond, func() bool { return c.count() >= want }) {
		t.Fatalf("after 5s: %d credential reads, want %d", c.count(), want)
	}
}

// Issue #2062: a grant (Start) reads the credential once, and every resolve
// the sweep then makes is served from that read; a revoke (Stop) drops it, so
// the next grant reads it again.
func TestMuseAccountAPI_ReadsCredentialOncePerGrant(t *testing.T) {
	creds := &countingCredentials{}
	api := newMuseAccountAPI(e2eLog{}, creds)
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	waitForReads(t, creds, 1)
	for i := 0; i < 5; i++ {
		if _, err := api.resolver.Resolve(context.Background()); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if n := creds.count(); n != 1 {
		t.Fatalf("credential read %d times after one grant and five resolves, want 1", n)
	}

	if err := api.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	waitForReads(t, creds, 2)
	if _, err := api.resolver.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := creds.count(); n != 2 {
		t.Fatalf("credential read %d times after a revoke and a re-grant, want 2", n)
	}
}

// Issue #2062: a failed Keychain read is not retried by later resolves, so
// an unanswered dialog is not raised again until the permission is revoked
// and granted again.
func TestMuseAccountAPI_FailedReadIsStickyUntilRegrant(t *testing.T) {
	creds := &countingCredentials{err: fmt.Errorf("%w: lookup: security did not answer", museaccountapi.ErrKeychainRead)}
	api := newMuseAccountAPI(e2eLog{}, creds)
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	waitForReads(t, creds, 1)
	for i := 0; i < 5; i++ {
		if _, err := api.resolver.Resolve(context.Background()); err == nil {
			t.Fatalf("resolve %d succeeded; want the cached failure", i)
		}
	}
	if n := creds.count(); n != 1 {
		t.Fatalf("credential read %d times after a failed read, want 1", n)
	}
	if err := api.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	waitForReads(t, creds, 2)
}

// Issue #2062 review: a failure that raised no dialog — Muse not logged in
// yet when the permission was granted — is read again on the next resolve,
// so a later `muse login` is picked up without a revoke and re-grant.
func TestMuseAccountAPI_FailureWithoutADialogIsRetried(t *testing.T) {
	creds := &countingCredentials{err: errors.New("museaccountapi: reading auth.json: no such file")}
	api := newMuseAccountAPI(e2eLog{}, creds)
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	waitForReads(t, creds, 1)
	for i := 0; i < 3; i++ {
		_, _ = api.resolver.Resolve(context.Background())
	}
	if n := creds.count(); n != 4 {
		t.Fatalf("credential read %d times, want 4: a failure with no dialog was kept", n)
	}
}

// Issue #2062: a revoke (Stop) drops the cached credential by itself, without
// waiting for a later grant to replace it.
// tools/lib/muse-account-sweep-mutations_test.sh removes Stop's reset.
func TestMuseAccountAPI_RevokeDropsTheCredential(t *testing.T) {
	creds := &countingCredentials{}
	api := newMuseAccountAPI(e2eLog{}, creds)
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	waitForReads(t, creds, 1)
	if err := api.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := api.resolver.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := creds.count(); n != 2 {
		t.Fatalf("credential read %d times, want 2: a resolve after a revoke was served the credential cached before it", n)
	}
}

// Issue #2062: Apply (Start) resets the cache itself rather than relying on a
// Remove before it, so an Apply re-run while already granted — the #1362
// retry path — reads the credential again.
// tools/lib/muse-account-sweep-mutations_test.sh removes Start's reset.
func TestMuseAccountAPI_ApplyRereadsTheCredential(t *testing.T) {
	creds := &countingCredentials{}
	api := newMuseAccountAPI(e2eLog{}, creds)
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	waitForReads(t, creds, 1)
	if err := api.Start(); err != nil {
		t.Fatal(err)
	}
	waitForReads(t, creds, 2)
}
