package webpush_test

import (
	"encoding/base64"
	"math/rand"
	"strings"
	"testing"

	"irrlicht/core/pkg/webpush"
)

// TestSubscriptionValidate drives Validate — the registration-time gate the
// relay's registry runs — across the endpoint and key shapes an untrusted
// client POST can carry. Every rejection must name the offending field, so
// a phone's 400 points at what to fix.
func TestSubscriptionValidate(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	uaKey := randomECDHKey(t, rng)
	goodPoint := base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes())
	goodAuth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))

	compressed := make([]byte, 33) // SEC1 compressed: right curve, wrong wire form
	compressed[0] = 0x02

	cases := []struct {
		name      string
		sub       webpush.Subscription
		wantField string // "" = must validate
	}{
		{"valid https", sub("https://push.example/v2/abc", goodPoint, goodAuth), ""},
		{"valid http loopback (dev loop, §7)", sub("http://127.0.0.1:7837/push", goodPoint, goodAuth), ""},
		{"padded base64 keys", sub("https://push.example/v2/abc", goodPoint+"=", goodAuth+"=="), ""},
		{"empty endpoint", sub("", goodPoint, goodAuth), "endpoint"},
		{"relative endpoint", sub("/v2/abc", goodPoint, goodAuth), "endpoint"},
		{"non-http scheme", sub("ftp://push.example/v2/abc", goodPoint, goodAuth), "endpoint"},
		{"scheme without host", sub("https://", goodPoint, goodAuth), "endpoint"},
		{"p256dh 33-byte compressed point", sub("https://push.example/v2/abc", base64.RawURLEncoding.EncodeToString(compressed), goodAuth), "p256dh"},
		{"p256dh truncated point", sub("https://push.example/v2/abc", base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes()[:64]), goodAuth), "p256dh"},
		{"p256dh invalid base64", sub("https://push.example/v2/abc", "!!!", goodAuth), "p256dh"},
		{"auth wrong length", sub("https://push.example/v2/abc", goodPoint, base64.RawURLEncoding.EncodeToString(make([]byte, 8))), "auth"},
		{"auth invalid base64", sub("https://push.example/v2/abc", goodPoint, "$$$"), "auth"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.sub.Validate()
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate rejected a valid subscription: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate accepted a malformed subscription")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("error %q does not name the %q field", err, tc.wantField)
			}
		})
	}
}

// sub builds a Subscription literal for the table above.
func sub(endpoint, p256dh, auth string) webpush.Subscription {
	return webpush.Subscription{
		Endpoint: endpoint,
		Keys:     webpush.SubscriptionKeys{P256dh: p256dh, Auth: auth},
	}
}
