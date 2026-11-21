package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/chromecookies"
	"irrlicht/core/adapters/outbound/mistralvibe"
	"irrlicht/core/application/services"
	"irrlicht/core/ports/outbound"
)

// Issue #2012's daemon wiring. Nothing here dials: the one test that builds
// the real transport never polls through it, and every poll below goes
// through a fake transport.

const vibeFixturePassword = "irrlicht-test-safe-storage-password" // chromecookies/testdata/make_fixture.py

// fakeChromeDir is a Chrome user data dir whose Default profile holds the
// committed fixture store.
func fakeChromeDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Local State"), []byte(`{"profile":{"last_used":"Default"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := os.ReadFile("../../adapters/outbound/chromecookies/testdata/Cookies-v24")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Default", "Cookies"), store, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// keychainStandIn counts reads of the stand-in for the "Chrome Safe Storage"
// item. With fail set every read fails, as an unanswered dialog does.
type keychainStandIn struct {
	reads atomic.Int64
	fail  bool
}

func (k *keychainStandIn) source(context.Context) (chromecookies.Key, error) {
	k.reads.Add(1)
	if k.fail {
		return chromecookies.Key{}, chromecookies.ErrKeychain
	}
	return chromecookies.DeriveKey(vibeFixturePassword)
}

// testVibeAPI is the daemon's wiring over chromeDir and the stand-in
// Keychain; api.browser.Key reads through the grant's key cache.
func testVibeAPI(chromeDir string, kc *keychainStandIn) mistralVibeAPI {
	keys := chromecookies.NewKeyCache(kc.source)
	browser := mistralvibe.Browser{
		UserDataDir: func() (string, error) { return chromeDir, nil },
		Key:         keys.Key,
	}
	return newMistralVibeAPI(e2eLog{}, browser, keys)
}

func TestMistralVibeAPI_SweeperIsBuiltWhenTheTransportIs(t *testing.T) {
	api := mistralVibeEffects(e2eLog{})
	if api.transport == nil {
		t.Fatal("mistralVibeEffects built no transport for the fixed destination")
	}
	if api.sweeper(nil, nil, nil, e2eLog{}) == nil {
		t.Fatal("sweeper() returned nil with a transport; the daemon would start no sweep")
	}
	if (mistralVibeAPI{}).sweeper(nil, nil, nil, e2eLog{}) != nil {
		t.Fatal("sweeper() built a sweep with no transport to poll through")
	}
}

// TestMistralVibeAPI_GrantAndBootReadNothing is a LOCK (it passes by
// construction; it pins the review of #2012's finding 3): a grant, and the
// re-apply of a persisted grant at daemon boot, read neither Chrome nor the
// Keychain, even with a Chrome profile signed in to Mistral. Only the sweep
// reads, and only while a vibe session is live.
func TestMistralVibeAPI_GrantAndBootReadNothing(t *testing.T) {
	kc := &keychainStandIn{}
	chrome := fakeChromeDir(t)
	var profileReads atomic.Int64
	keys := chromecookies.NewKeyCache(kc.source)
	browser := mistralvibe.Browser{
		UserDataDir: func() (string, error) { profileReads.Add(1); return chrome, nil },
		Key:         keys.Key,
	}
	api := newMistralVibeAPI(e2eLog{}, browser, keys)
	for range 3 {
		mustRun(t, api.Start)
		mustRun(t, api.Stop)
	}
	mustRun(t, api.Start)
	if pollUntil(t, 300*time.Millisecond, 10*time.Millisecond, func() bool { return kc.reads.Load() > 0 || profileReads.Load() > 0 }) {
		t.Fatalf("a grant read the browser: %d Chrome profile read(s), %d Keychain read(s), want none", profileReads.Load(), kc.reads.Load())
	}
}

// blockingVibeTransport holds every fetch until its context ends and records
// that it ended.
type blockingVibeTransport struct {
	entered   chan struct{}
	once      sync.Once
	cancelled atomic.Int64
}

func (b *blockingVibeTransport) Fetch(ctx context.Context, _ outbound.AccountQuotaRequest) (outbound.AccountQuotaResponse, error) {
	b.once.Do(func() { close(b.entered) })
	<-ctx.Done()
	b.cancelled.Add(1)
	return outbound.AccountQuotaResponse{}, ctx.Err()
}

// TestMistralVibeAPI_RevokeCancelsTheFetchInFlight is a target of #2012
// §7.3 ("keep polling after the browser-session permission is revoked"): a
// request already on its way when the permission goes off is cancelled by
// the revoke, not left to finish.
func TestMistralVibeAPI_RevokeCancelsTheFetchInFlight(t *testing.T) {
	api := testVibeAPI(fakeChromeDir(t), &keychainStandIn{})
	transport := &blockingVibeTransport{entered: make(chan struct{})}
	browser, err := api.browser.Session()
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	var granted atomic.Bool
	granted.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = services.MistralVibeRefresh(context.Background(), api.poller, browser, transport, granted.Load, "v1")
	}()
	select {
	case <-transport.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("after 5s: the fetch never started")
	}
	granted.Store(false)
	mustRun(t, api.Stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("after 5s: the revoke left the request in flight running")
	}
	if transport.cancelled.Load() != 1 {
		t.Fatal("the in-flight request was not cancelled by the revoke")
	}
}

// TestMistralVibeAPI_GrantRetriesAFailedKeychainRead (review of #2012,
// finding 4): a failed Keychain read is kept so the dialog is not raised on
// every poll, and granting the permission again is the retry
// (chromecookies.KeyCache's doc).
func TestMistralVibeAPI_GrantRetriesAFailedKeychainRead(t *testing.T) {
	kc := &keychainStandIn{fail: true}
	api := testVibeAPI(t.TempDir(), kc)
	for range 2 {
		if _, err := api.browser.Key(t.Context()); err == nil {
			t.Fatal("the failing Keychain stand-in succeeded")
		}
	}
	if kc.reads.Load() != 1 {
		t.Fatalf("a failed Keychain read was retried %d time(s) before a new grant, want none", kc.reads.Load()-1)
	}
	mustRun(t, api.Start)
	_, _ = api.browser.Key(t.Context())
	if kc.reads.Load() != 2 {
		t.Fatalf("after a new grant: %d Keychain reads, want 2 — granting again did not retry the failed read", kc.reads.Load())
	}
}

// TestMistralVibeAPI_RevokeDropsTheKey (review of #2012, finding 4): a
// revoke drops the derived cookie key from memory, so a key read once is
// not kept while the permission is off.
func TestMistralVibeAPI_RevokeDropsTheKey(t *testing.T) {
	kc := &keychainStandIn{}
	api := testVibeAPI(t.TempDir(), kc)
	if _, err := api.browser.Key(t.Context()); err != nil {
		t.Fatal(err)
	}
	mustRun(t, api.Stop)
	if _, err := api.browser.Key(t.Context()); err != nil {
		t.Fatal(err)
	}
	if kc.reads.Load() != 2 {
		t.Fatalf("%d Keychain reads across a revoke, want 2 — the revoke kept the derived key cached", kc.reads.Load())
	}
}
