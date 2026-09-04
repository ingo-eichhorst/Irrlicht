package webpush_test

// Boundary pins added after review: each test here exists because a
// deliberate mutation of the code it covers left the rest of the suite
// green (the repo rule: anything a change adds owes a mutation seen red).

import (
	"context"
	"encoding/base64"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"irrlicht/core/pkg/webpush"
)

func TestEndpointOriginDropsDefaultPorts(t *testing.T) {
	// RFC 8292 §2's aud is the RFC 6454 origin serialization, which omits
	// a scheme-default port: an endpoint spelled with an explicit :443
	// must yield the same aud a browser-issued one does, or a strictly
	// validating push service rejects the JWT.
	cases := []struct{ endpoint, want string }{
		{"https://push.example.com:443/wp/token", "https://push.example.com"},
		{"http://push.example.com:80/wp/token", "http://push.example.com"},
		{"https://push.example.com:8443/wp/token", "https://push.example.com:8443"},
		{"http://push.example.com:443/wp/token", "http://push.example.com:443"},
		{"https://push.example.com/wp/token", "https://push.example.com"},
		{"https://[2001:db8::1]:443/wp/token", "https://[2001:db8::1]"},
		{"https://[2001:db8::1]:8443/wp/token", "https://[2001:db8::1]:8443"},
	}
	for _, c := range cases {
		got, err := webpush.EndpointOrigin(c.endpoint)
		if err != nil {
			t.Errorf("EndpointOrigin(%q): %v", c.endpoint, err)
			continue
		}
		if got != c.want {
			t.Errorf("EndpointOrigin(%q) = %q, want %q", c.endpoint, got, c.want)
		}
	}
}

func TestDefaultClientTimeoutIsBounded(t *testing.T) {
	if got := webpush.DefaultClient.Timeout; got != 10*time.Second {
		t.Fatalf("defaultClient.Timeout = %v, want the documented 10s — a Sender with a nil Client must never hang a relay dispatch loop on a stalled push service", got)
	}
}

func TestEncrypt_PadToCeiling(t *testing.T) {
	// A single sealed record (padTo + the GCM tag) may not exceed the
	// header's rs=4096 (RFC 8188 §2); 4080 is the last legal size.
	rng := rand.New(rand.NewSource(7))
	uaKey := randomECDHKey(t, rng)
	eph := randomECDHKey(t, rng)
	salt := make([]byte, 16)
	rng.Read(salt)
	auth := make([]byte, 16)
	rng.Read(auth)
	if _, err := webpush.Encrypt(eph, salt, uaKey.PublicKey().Bytes(), auth, []byte("x"), 4080); err != nil {
		t.Fatalf("padTo=4080 (rs minus the GCM tag) must fit: %v", err)
	}
	if _, err := webpush.Encrypt(eph, salt, uaKey.PublicKey().Bytes(), auth, []byte("x"), 4081); err == nil {
		t.Fatal("padTo=4081 makes the sealed record exceed the header's rs=4096 — an invalid RFC 8188 stream; encrypt must refuse")
	}
}

func TestSend_RejectsStdAlphabetKeysEvenWhenOtherwiseValid(t *testing.T) {
	// The malformed-keys table's standard-alphabet case decodes to garbage
	// that fails the point check anyway, so it cannot tell strictness from
	// leniency. This one feeds a VALID 65-byte point whose standard-base64
	// spelling genuinely contains '+' or '/': only alphabet strictness can
	// reject it, and a RawStdEncoding fallback would accept it — the
	// interop split decodeB64's strictness exists to prevent.
	rng := rand.New(rand.NewSource(11))
	svc := newFakePushService(t, http.StatusCreated)
	var enc string
	for {
		uaKey := randomECDHKey(t, rng)
		enc = base64.RawStdEncoding.EncodeToString(uaKey.PublicKey().Bytes())
		if strings.ContainsAny(enc, "+/") {
			break
		}
	}
	auth := make([]byte, 16)
	rng.Read(auth)
	sub := webpush.Subscription{
		Endpoint: svc.srv.URL + "/push/v2/tok",
		Keys: webpush.SubscriptionKeys{
			P256dh: enc,
			Auth:   base64.RawURLEncoding.EncodeToString(auth),
		},
	}
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := &webpush.Sender{Key: key, Client: svc.srv.Client()}
	err = sender.Send(context.Background(), sub, []byte("{}"), webpush.Options{})
	if err == nil || !strings.Contains(err.Error(), "p256dh") {
		t.Fatalf("standard-alphabet p256dh must be rejected with an error naming the field, got %v", err)
	}
}
