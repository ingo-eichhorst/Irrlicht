package push

// Shared fixtures for the push package tests: an injected clock (no test
// here ever sleeps — TTLs and rate-limit windows are driven by advancing
// it) and a real browser-shaped subscription.

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"irrlicht/core/pkg/webpush"
)

// testClock is the injectable clock handed to NewService.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newTestService builds a Service over a fresh temp dir on the injected
// clock.
func newTestService(t *testing.T) (*Service, *testClock, string) {
	t.Helper()
	dir := t.TempDir()
	clk := newTestClock()
	svc, err := NewService(dir, clk.now)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, clk, dir
}

// testSubscription mints a valid browser-shaped subscription: a real P-256
// point for p256dh and a 16-byte auth secret, both unpadded base64url as
// browsers emit them.
func testSubscription(t *testing.T, endpoint string) webpush.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate subscription key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}
	return webpush.Subscription{
		Endpoint: endpoint,
		Keys: webpush.SubscriptionKeys{
			P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(auth),
		},
	}
}

// mustMint mints a code, failing the test on error.
func mustMint(t *testing.T, svc *Service, workspace string) string {
	t.Helper()
	code, ttl, err := svc.MintCode(workspace)
	if err != nil {
		t.Fatalf("MintCode: %v", err)
	}
	if ttl != CodeTTL {
		t.Fatalf("MintCode ttl = %v, want %v", ttl, CodeTTL)
	}
	return code
}
