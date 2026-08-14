package webpush_test

import (
	"context"
	"encoding/base64"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"irrlicht/core/pkg/webpush"
)

// TestSend_RejectsMalformedSubscriptionKeys drives the untrusted half of a
// Subscription — the browser-minted keys arrive in a client POST — through
// Send. Every case must produce an error that names the offending field and
// must not panic; validation happens before any network I/O, so no server is
// needed (the endpoint here resolves nowhere on purpose — reaching it would
// itself be a failure).
func TestSend_RejectsMalformedSubscriptionKeys(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	uaKey := randomECDHKey(t, rng)
	goodPoint := base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes())
	goodAuth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))

	compressed := make([]byte, 33) // SEC1 compressed form: rejected, only uncompressed is legal here
	compressed[0] = 0x02
	offCurve := make([]byte, 65) // right length, right prefix, not a curve point
	offCurve[0] = 0x04
	offCurve[64] = 0x01

	cases := []struct {
		name      string
		keys      webpush.SubscriptionKeys
		wantField string
	}{
		{"p256dh empty", webpush.SubscriptionKeys{P256dh: "", Auth: goodAuth}, "p256dh"},
		{"p256dh invalid base64", webpush.SubscriptionKeys{P256dh: "!!!not-base64!!!", Auth: goodAuth}, "p256dh"},
		{"p256dh standard-alphabet base64", webpush.SubscriptionKeys{P256dh: "abc+def/ghi", Auth: goodAuth}, "p256dh"},
		{"p256dh truncated point", webpush.SubscriptionKeys{P256dh: base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes()[:64]), Auth: goodAuth}, "p256dh"},
		{"p256dh compressed point", webpush.SubscriptionKeys{P256dh: base64.RawURLEncoding.EncodeToString(compressed), Auth: goodAuth}, "p256dh"},
		{"p256dh off-curve point", webpush.SubscriptionKeys{P256dh: base64.RawURLEncoding.EncodeToString(offCurve), Auth: goodAuth}, "p256dh"},
		{"auth empty", webpush.SubscriptionKeys{P256dh: goodPoint, Auth: ""}, "auth"},
		{"auth invalid base64", webpush.SubscriptionKeys{P256dh: goodPoint, Auth: "$$$"}, "auth"},
		{"auth too short", webpush.SubscriptionKeys{P256dh: goodPoint, Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 8))}, "auth"},
		{"auth too long", webpush.SubscriptionKeys{P256dh: goodPoint, Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}, "auth"},
	}

	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := &webpush.Sender{Key: key}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := webpush.Subscription{Endpoint: "https://push.invalid/v1/x", Keys: tc.keys}
			err := sender.Send(context.Background(), sub, []byte("payload"), webpush.Options{})
			if err == nil {
				t.Fatal("malformed subscription keys were accepted")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("error %q does not name the %q field", err, tc.wantField)
			}
		})
	}
}

// A padded (trailing '=') encoding of valid keys must be accepted: browsers
// emit unpadded base64url but client push libraries disagree, and both
// spellings decode to the same bytes.
func TestSend_AcceptsPaddedBase64Keys(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	uaKey := randomECDHKey(t, rng)
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	sub := webpush.Subscription{
		Endpoint: srv.URL + "/push/v1/padded",
		Keys: webpush.SubscriptionKeys{
			P256dh: base64.URLEncoding.EncodeToString(uaKey.PublicKey().Bytes()), // padded form
			Auth:   base64.URLEncoding.EncodeToString(make([]byte, 16)),
		},
	}
	sender := &webpush.Sender{Key: key, Client: srv.Client()}
	if err := sender.Send(context.Background(), sub, []byte("x"), webpush.Options{}); err != nil {
		t.Fatalf("padded base64url keys were rejected: %v", err)
	}
}

func TestSend_PayloadTooLargeForPadTo(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	uaKey := randomECDHKey(t, rng)
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sub := webpush.Subscription{
		Endpoint: "https://push.invalid/v1/x",
		Keys: webpush.SubscriptionKeys{
			P256dh: base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		},
	}
	sender := &webpush.Sender{Key: key}
	err = sender.Send(context.Background(), sub, make([]byte, webpush.DefaultPadTo), webpush.Options{})
	if !errors.Is(err, webpush.ErrPayloadTooLarge) {
		t.Fatalf("got %v, want ErrPayloadTooLarge", err)
	}
}
