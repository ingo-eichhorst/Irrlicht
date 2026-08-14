package webpush_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/pkg/webpush"
)

// fakePushService is the RFC 8030 receiving side: it captures every request
// so the test can grade method, headers, VAPID JWT and body against what a
// real push service would enforce, and answers with a configurable status.
type fakePushService struct {
	srv    *httptest.Server
	status int

	mu       sync.Mutex
	requests []capturedRequest
}

type capturedRequest struct {
	method string
	path   string
	header http.Header
	body   []byte
}

func newFakePushService(t *testing.T, status int) *fakePushService {
	t.Helper()
	f := &fakePushService{status: status}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := readAll(r)
		if err != nil {
			t.Errorf("reading push body: %v", err)
		}
		f.mu.Lock()
		f.requests = append(f.requests, capturedRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		f.mu.Unlock()
		w.WriteHeader(f.status)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func (f *fakePushService) last(t *testing.T) capturedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("the push service received no request")
	}
	return f.requests[len(f.requests)-1]
}

// testSubscription builds a full device: UA keypair + auth secret + the
// Subscription a browser would register. The endpoint carries a path
// segment on purpose — origin and full URL must be distinguishable, or the
// aud assertion below cannot tell them apart.
func testSubscription(t *testing.T, rng *rand.Rand, baseURL string) (webpush.Subscription, *ecdh.PrivateKey, []byte) {
	t.Helper()
	uaKey := randomECDHKey(t, rng)
	auth := make([]byte, 16)
	rng.Read(auth)
	sub := webpush.Subscription{
		Endpoint: baseURL + "/push/v2/some-subscription-token",
		Keys: webpush.SubscriptionKeys{
			P256dh: base64.RawURLEncoding.EncodeToString(uaKey.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(auth),
		},
	}
	return sub, uaKey, auth
}

func TestSend_AgainstFakePushService(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	svc := newFakePushService(t, http.StatusCreated)
	sub, uaKey, auth := testSubscription(t, rng, svc.srv.URL)

	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := &webpush.Sender{Key: key, Subject: "mailto:ops@example.com", Client: svc.srv.Client()}
	now := time.Now().Truncate(time.Second)
	sender.SetNow(func() time.Time { return now })

	payload := []byte(`{"session":"6f2c9a80","state":"waiting"}`)
	opts := webpush.Options{TTL: 90 * time.Second, Topic: topicUUID, Urgency: webpush.UrgencyHigh}
	if err := sender.Send(context.Background(), sub, payload, opts); err != nil {
		t.Fatalf("Send: %v", err)
	}

	req := svc.last(t)
	if req.method != http.MethodPost {
		t.Errorf("method %s, want POST", req.method)
	}
	if got := req.header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type %q, want application/octet-stream", got)
	}
	if got := req.header.Get("Content-Encoding"); got != "aes128gcm" {
		t.Errorf("Content-Encoding %q, want aes128gcm", got)
	}
	if got := req.header.Get("TTL"); got != "90" {
		t.Errorf("TTL %q, want \"90\"", got)
	}
	// The Topic must be the MAPPED value: a 36-char session UUID passed
	// through raw would be silently dropped or rejected by the push
	// service, and collapse would quietly stop working.
	if got := req.header.Get("Topic"); got != topicUUIDMapped {
		t.Errorf("Topic %q, want the TopicHeader mapping %q", got, topicUUIDMapped)
	}
	if got := req.header.Get("Urgency"); got != "high" {
		t.Errorf("Urgency %q, want high", got)
	}

	// VAPID (RFC 8292 §3): scheme "vapid", t = the JWT, k = the signing
	// public key the subscription was bound to.
	jwt, k := parseVAPIDAuth(t, req.header.Get("Authorization"))
	if k != key.PublicKey() {
		t.Errorf("k parameter %q, want PublicKey() %q", k, key.PublicKey())
	}
	claims := verifyJWT(t, jwt, key.PublicKey())
	origin := mustOrigin(t, svc.srv.URL)
	if claims.Aud != origin {
		t.Errorf("aud %q, want the endpoint's origin %q", claims.Aud, origin)
	}
	if claims.Aud == sub.Endpoint {
		t.Error("aud is the full endpoint URL — it must be the origin only (RFC 8292 §2)")
	}
	if want := now.Add(12 * time.Hour).Unix(); claims.Exp != want {
		t.Errorf("exp %d, want now+12h = %d", claims.Exp, want)
	}
	if cap := now.Add(24 * time.Hour).Unix(); claims.Exp > cap {
		t.Errorf("exp %d exceeds the RFC 8292 §2 24-hour cap %d", claims.Exp, cap)
	}
	if claims.Sub != "mailto:ops@example.com" {
		t.Errorf("sub %q, want the Sender.Subject", claims.Sub)
	}

	// The §8.2 fixed-size property on the wire: DefaultPadTo bodies are
	// always 2048+16+86 = 2150 bytes.
	if len(req.body) != 2150 {
		t.Errorf("body is %d bytes, want the constant 2150", len(req.body))
	}
	got, err := decryptBody(uaKey, auth, req.body)
	if err != nil {
		t.Fatalf("device-side decrypt: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("decrypted %q, want %q", got, payload)
	}
}

// TestSend_OmitsOptionalParts: no Topic, no Urgency, empty Subject — the
// headers and the sub claim must be absent rather than empty-valued.
func TestSend_OmitsOptionalParts(t *testing.T) {
	rng := rand.New(rand.NewSource(6))
	svc := newFakePushService(t, http.StatusCreated)
	sub, _, _ := testSubscription(t, rng, svc.srv.URL)
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := &webpush.Sender{Key: key, Client: svc.srv.Client()}
	if err := sender.Send(context.Background(), sub, []byte("x"), webpush.Options{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	req := svc.last(t)
	if _, present := req.header["Topic"]; present {
		t.Error("Topic header sent for an empty Options.Topic")
	}
	if _, present := req.header["Urgency"]; present {
		t.Error("Urgency header sent for an empty Options.Urgency")
	}
	// TTL is NOT optional: RFC 8030 §5.2 requires it on every request,
	// and 0 is a value, not an absence.
	if got, present := req.header["Ttl"]; !present || len(got) != 1 || got[0] != "0" {
		t.Errorf("TTL header %v, want exactly [\"0\"]", got)
	}
	jwt, _ := parseVAPIDAuth(t, req.header.Get("Authorization"))
	if claims := verifyJWT(t, jwt, key.PublicKey()); claims.Sub != "" {
		t.Errorf("sub claim %q, want it omitted for an empty Subject", claims.Sub)
	}
}

func TestSend_TTLRoundsUpToWholeSeconds(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	svc := newFakePushService(t, http.StatusCreated)
	sub, _, _ := testSubscription(t, rng, svc.srv.URL)
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := &webpush.Sender{Key: key, Client: svc.srv.Client()}
	if err := sender.Send(context.Background(), sub, []byte("x"), webpush.Options{TTL: 1500 * time.Millisecond}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := svc.last(t).header.Get("TTL"); got != "2" {
		t.Errorf("TTL %q for 1500ms, want \"2\" — rounding down would tighten the caller's deadline", got)
	}
}

// TestSend_FreshSaltAndEphemeralKeyPerMessage: two Sends of the SAME payload
// must differ in salt and in the header's ephemeral public key. Comparing
// whole bodies is not enough — a reused salt with a fresh ephemeral key
// still yields differing bodies, so each source of freshness is asserted on
// its own bytes (salt: body[0:16]; keyid: body[21:86], per RFC 8188 §2.1).
func TestSend_FreshSaltAndEphemeralKeyPerMessage(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	svc := newFakePushService(t, http.StatusCreated)
	sub, _, _ := testSubscription(t, rng, svc.srv.URL)
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := &webpush.Sender{Key: key, Client: svc.srv.Client()}
	payload := []byte("same payload twice")
	for i := 0; i < 2; i++ {
		if err := sender.Send(context.Background(), sub, payload, webpush.Options{}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.requests) != 2 {
		t.Fatalf("captured %d requests, want 2", len(svc.requests))
	}
	a, b := svc.requests[0].body, svc.requests[1].body
	if bytes.Equal(a[:16], b[:16]) {
		t.Error("salt reused across two Sends — RFC 8188 §2.1 wants a fresh salt per message")
	}
	if bytes.Equal(a[21:86], b[21:86]) {
		t.Error("ephemeral public key reused across two Sends — RFC 8291 §3.1 wants a fresh keypair per message")
	}
}

func TestSend_GoneStatusesMapToErrSubscriptionGone(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusNotFound} {
		rng := rand.New(rand.NewSource(9))
		svc := newFakePushService(t, status)
		sub, _, _ := testSubscription(t, rng, svc.srv.URL)
		key, err := webpush.GenerateVAPIDKey()
		if err != nil {
			t.Fatal(err)
		}
		sender := &webpush.Sender{Key: key, Client: svc.srv.Client()}
		err = sender.Send(context.Background(), sub, []byte("x"), webpush.Options{})
		if !errors.Is(err, webpush.ErrSubscriptionGone) {
			t.Errorf("status %d: got %v, want ErrSubscriptionGone — the caller prunes on this signal", status, err)
		}
	}
}

func TestSend_OtherStatusIsStatusError(t *testing.T) {
	rng := rand.New(rand.NewSource(10))
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("boom: " + strings.Repeat("x", 1000)))
	}))
	defer svc.Close()
	sub, _, _ := testSubscription(t, rng, svc.URL)
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := &webpush.Sender{Key: key, Client: svc.Client()}
	err = sender.Send(context.Background(), sub, []byte("x"), webpush.Options{})
	var se *webpush.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("got %v, want a *StatusError", err)
	}
	if se.Code != http.StatusBadRequest {
		t.Errorf("Code %d, want 400", se.Code)
	}
	if !strings.HasPrefix(se.Body, "boom: ") {
		t.Errorf("Body %q lost the service's diagnostic", se.Body)
	}
	if len(se.Body) > 256 {
		t.Errorf("Body is %d bytes, want it truncated to 256 for logging", len(se.Body))
	}
}

func TestSend_ContextCancellationAborts(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the body first: an HTTP/1.1 server only watches for the
		// client hanging up once the request body is consumed, and
		// without that watch the context below would never fire — the
		// handler (and the deferred Close) would hang instead.
		if _, err := readAll(r); err != nil {
			return
		}
		<-r.Context().Done() // hold the request open until the client gives up
	}))
	defer svc.Close()
	sub, _, _ := testSubscription(t, rng, svc.URL)
	key, err := webpush.GenerateVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := &webpush.Sender{Key: key, Client: svc.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = sender.Send(ctx, sub, []byte("x"), webpush.Options{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Send took %v after cancellation — it must abort, not wait the request out", elapsed)
	}
}

// --- VAPID verification helpers (the receiving side of RFC 8292) ---

type vapidClaims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub"`
}

func parseVAPIDAuth(t *testing.T, authz string) (jwt, k string) {
	t.Helper()
	const scheme = "vapid "
	if !strings.HasPrefix(authz, scheme) {
		t.Fatalf("Authorization %q does not use the vapid scheme", authz)
	}
	for _, part := range strings.Split(authz[len(scheme):], ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "t="):
			jwt = part[2:]
		case strings.HasPrefix(part, "k="):
			k = part[2:]
		}
	}
	if jwt == "" || k == "" {
		t.Fatalf("Authorization %q lacks t= or k=", authz)
	}
	return jwt, k
}

// verifyJWT checks the three-segment shape, the ES256 header, and the raw
// R||S signature (RFC 7518 §3.4 — 64 bytes exactly; ASN.1 DER is longer and
// push services reject it) against the given applicationServerKey.
func verifyJWT(t *testing.T, jwt, publicKey string) vapidClaims {
	t.Helper()
	segs := strings.Split(jwt, ".")
	if len(segs) != 3 {
		t.Fatalf("JWT has %d segments, want 3", len(segs))
	}
	var header struct {
		Typ string `json:"typ"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(mustB64(t, segs[0]), &header); err != nil {
		t.Fatalf("JWT header: %v", err)
	}
	if header.Typ != "JWT" || header.Alg != "ES256" {
		t.Fatalf("JWT header typ=%q alg=%q, want JWT/ES256", header.Typ, header.Alg)
	}
	var claims vapidClaims
	if err := json.Unmarshal(mustB64(t, segs[1]), &claims); err != nil {
		t.Fatalf("JWT claims: %v", err)
	}
	sig := mustB64(t, segs[2])
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want the raw 64-byte R||S (ASN.1 would be ~70)", len(sig))
	}
	point := mustB64(t, publicKey)
	if len(point) != 65 || point[0] != 0x04 {
		t.Fatalf("k parameter is not a 65-byte uncompressed point")
	}
	pub := ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(point[1:33]),
		Y:     new(big.Int).SetBytes(point[33:]),
	}
	digest := sha256.Sum256([]byte(segs[0] + "." + segs[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&pub, digest[:], r, s) {
		t.Fatal("JWT signature does not verify against the k parameter")
	}
	return claims
}

func mustOrigin(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Scheme + "://" + u.Host
}
