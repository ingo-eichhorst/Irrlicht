package main

// The VAPID `sub` claim: what the relay's production sender puts in the JWT
// every push POST carries.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"irrlicht/core/cmd/irrlichtrelay/push"
	"irrlicht/core/pkg/webpush"
)

// vapidClaims POSTs one push through sender at a fake push service and
// returns the decoded claims of the RFC 8292 JWT the request carried.
func vapidClaims(t *testing.T, sender *webpush.Sender) map[string]any {
	t.Helper()
	var auth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(ts.Close)

	sub := browserSubscription(t, ts.URL+"/wpush/v1/device")
	if err := sender.Send(context.Background(), sub, []byte(`{"v":1}`), webpush.Options{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// "vapid t=<jwt>, k=<pub>" (RFC 8292 §3.3).
	_, rest, ok := strings.Cut(auth, "t=")
	if !ok {
		t.Fatalf("Authorization header %q carries no VAPID token", auth)
	}
	jwt, _, _ := strings.Cut(rest, ",")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("VAPID token %q is not a three-part JWS", jwt)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding JWT claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("JWT claims are not JSON: %v (%s)", err, raw)
	}
	return claims
}

// TestProductionSenderSignsWithASubClaim: RFC 8292 §2.1 makes `sub` a
// contact URI the push service can use to reach the application server's
// operator, and it is not decoration — Apple's Web Push service and
// Mozilla's autopush both reject a JWT without one. iOS is where the PWA
// path is not optional (arc42 ADR-2), so a relay signing without `sub`
// delivers to nobody on the platform the feature exists for, and does it
// with a 4xx per push and no other symptom.
func TestProductionSenderSignsWithASubClaim(t *testing.T) {
	svc, err := push.NewService(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("push.NewService: %v", err)
	}
	claims := vapidClaims(t, newRelayPushSender(svc, ""))

	sub, _ := claims["sub"].(string)
	if sub == "" {
		t.Fatalf("VAPID JWT carries no sub claim (%v) — Apple Web Push and Mozilla autopush reject it (RFC 8292 §2.1)", claims)
	}
	if !strings.HasPrefix(sub, "mailto:") && !strings.HasPrefix(sub, "https://") {
		t.Fatalf("sub = %q, want the mailto: or https: URI RFC 8292 §2.1 requires", sub)
	}
}

// TestResolveVAPIDSubject pins both halves of the decision an operator who
// sets nothing, or sets nonsense, runs into: absence resolves to a valid
// self-describing default and never stops the relay, while a value RFC 8292
// §2.1 does not admit stops it at startup rather than at a 4xx per push on
// somebody's phone.
func TestResolveVAPIDSubject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "unset falls back to the self-describing default", in: "", want: defaultVAPIDSubject},
		{name: "whitespace is not a value", in: "   ", want: defaultVAPIDSubject},
		{name: "operator mailto", in: "mailto:ops@example.com", want: "mailto:ops@example.com"},
		{name: "operator https", in: "https://relay.example/contact", want: "https://relay.example/contact"},
		{name: "bare address is not a URI", in: "ops@example.com", wantErr: true},
		{name: "http is not admitted", in: "http://relay.example", wantErr: true},
		{name: "a scheme RFC 8292 does not name", in: "tel:+15550100", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveVAPIDSubject(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveVAPIDSubject(%q) = %q, want an error naming RFC 8292 §2.1", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveVAPIDSubject(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("resolveVAPIDSubject(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
