package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"irrlicht/core/cmd/irrlichtrelay/push"
	"irrlicht/core/domain/notify"
	"irrlicht/core/pkg/webpush"
)

// pushEnv is an auth-enabled relay assembled exactly as runServe does —
// tokens file, auth store, push service, buildMux — over a temp data dir.
// No token-watch or prune goroutine runs, which is deliberate: anything that
// only works via a background tick must fail here.
type pushEnv struct {
	srv        *httptest.Server
	store      *authStore
	svc        *push.Service
	ddir       string
	tokensPath string
	tokens     map[string]string // label → plaintext
}

// tokenSeed is one pre-issued token for a pushEnv.
type tokenSeed struct {
	label     string
	workspace string
}

func newPushEnv(t *testing.T, seeds ...tokenSeed) *pushEnv {
	t.Helper()
	ddir := t.TempDir()
	tokensPath := filepath.Join(ddir, tokensFilename)
	tokens := make(map[string]string, len(seeds))
	for _, s := range seeds {
		_, plaintext := mustIssueToken(t, tokensPath, s.label, s.workspace)
		tokens[s.label] = plaintext
	}
	store, err := newAuthStore(tokensPath)
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}
	svc, err := push.NewService(ddir, nil)
	if err != nil {
		t.Fatalf("push.NewService: %v", err)
	}
	h := newHubWithAuth(store, nil, defaultLimits())
	// The mux runServe builds carries the dispatcher, because the
	// test-notification route sends through it (§8.3). A fake sender here:
	// nothing in this file asks for a delivery, and one that reached the
	// network would be the loudest possible way to find out.
	obs := newPushObserver(svc, store, &fakeSender{}, notify.Config{}, nil)
	srv := httptest.NewServer(buildMux(h, store, svc, obs))
	t.Cleanup(srv.Close)
	return &pushEnv{srv: srv, store: store, svc: svc, ddir: ddir, tokensPath: tokensPath, tokens: tokens}
}

// doPush performs one push API request, JSON-encoding body when non-nil and
// attaching bearer as an Authorization header when non-empty.
func doPush(t *testing.T, method, url, bearer string, body any) (int, []byte) {
	t.Helper()
	var buf io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		buf = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, buf)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}

// browserSubscription mints a valid PushSubscription.toJSON() body.
func browserSubscription(t *testing.T, endpoint string) webpush.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return webpush.Subscription{
		Endpoint: endpoint,
		Keys: webpush.SubscriptionKeys{
			P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
			Auth:   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
		},
	}
}

// pairResponse is the wire shape of a successful POST /api/v1/push/pair.
type pairResponse struct {
	Token          string `json:"token"`
	TokenID        string `json:"token_id"`
	VAPIDPublicKey string `json:"vapid_public_key"`
}

// pairDevice runs mint → pair with the given client token and returns the
// device credentials.
func pairDevice(t *testing.T, env *pushEnv, clientToken string) pairResponse {
	t.Helper()
	status, body := doPush(t, "POST", env.srv.URL+"/api/v1/push/pairings", clientToken, nil)
	if status != http.StatusCreated {
		t.Fatalf("mint = %d: %s", status, body)
	}
	var mint struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &mint); err != nil {
		t.Fatal(err)
	}
	status, body = doPush(t, "POST", env.srv.URL+"/api/v1/push/pair", "", map[string]string{"code": mint.Code})
	if status != http.StatusOK {
		t.Fatalf("pair = %d: %s", status, body)
	}
	var pr pairResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.Token == "" || pr.TokenID == "" || pr.VAPIDPublicKey == "" {
		t.Fatalf("pair response incomplete: %+v", pr)
	}
	return pr
}

// TestPushRequiresAuthGuard is the guard test for the push-requires-auth
// rule (docs/mobile-notifications-arc42.md §8.1): with --auth off the info
// endpoint says why push is off, every other push route is a 403 naming the
// fix, and no VAPID key material is created for a mode that cannot use it.
func TestPushRequiresAuthGuard(t *testing.T) {
	ddir := t.TempDir()
	svc := buildPushService(nil, ddir)
	if svc != nil {
		t.Fatal("buildPushService must return nil with auth off")
	}
	srv := httptest.NewServer(buildMux(newHub(defaultLimits()), nil, svc, nil))
	t.Cleanup(srv.Close)

	status, body := doPush(t, "GET", srv.URL+"/api/v1/push/info", "", nil)
	if status != http.StatusOK {
		t.Fatalf("info = %d: %s", status, body)
	}
	var info struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if info.Enabled {
		t.Fatal("info reports push enabled on an anonymous relay")
	}
	if !strings.Contains(info.Reason, "--auth tokens-file") {
		t.Fatalf("info reason %q does not name the fix (--auth tokens-file)", info.Reason)
	}

	routes := []struct{ method, path string }{
		{"POST", "/api/v1/push/pairings"},
		{"POST", "/api/v1/push/pair"},
		{"POST", "/api/v1/push/subscriptions"},
		{"DELETE", "/api/v1/push/subscriptions"},
		{"POST", "/api/v1/push/test"},
	}
	for _, rt := range routes {
		status, body := doPush(t, rt.method, srv.URL+rt.path, "", nil)
		if status != http.StatusForbidden {
			t.Fatalf("%s %s = %d, want 403: %s", rt.method, rt.path, status, body)
		}
		if !strings.Contains(string(body), "--auth tokens-file") {
			t.Fatalf("%s %s refusal %q does not name the fix (--auth tokens-file)", rt.method, rt.path, body)
		}
	}

	// The service was never constructed, so no key material exists.
	if _, err := os.Stat(filepath.Join(ddir, "vapid-keys.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("vapid-keys.json exists on an anonymous relay (stat err=%v)", err)
	}
}

// TestPairingEndToEnd drives the full §6.1 flow in workspace acme: client
// token → code → device token → subscription registration. Step 3 pins the
// synchronous store reload in handlePair — no watch goroutine runs here, so
// the device token can only authenticate if the pair handler reloaded.
func TestPairingEndToEnd(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "dashboard", workspace: "acme"})

	// 1. Mint with the client token.
	status, body := doPush(t, "POST", env.srv.URL+"/api/v1/push/pairings", env.tokens["dashboard"], nil)
	if status != http.StatusCreated {
		t.Fatalf("mint = %d: %s", status, body)
	}
	var mint struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &mint); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Z2-9]{4}-[A-Z2-9]{4}$`).MatchString(mint.Code) {
		t.Fatalf("code %q is not XXXX-XXXX shaped", mint.Code)
	}
	if mint.ExpiresIn != 600 {
		t.Fatalf("expires_in = %d, want 600", mint.ExpiresIn)
	}

	// 2. Redeem — the code is the credential, no bearer.
	status, body = doPush(t, "POST", env.srv.URL+"/api/v1/push/pair", "", map[string]string{"code": mint.Code})
	if status != http.StatusOK {
		t.Fatalf("pair = %d: %s", status, body)
	}
	var pr pairResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatal(err)
	}

	// 3. The returned device token authenticates a subscription POST
	// immediately (the synchronous reload; the 2 s watch tick is too late
	// and does not run in this test at all).
	sub := browserSubscription(t, "https://push.example/v2/e2e")
	status, body = doPush(t, "POST", env.srv.URL+"/api/v1/push/subscriptions", pr.Token, sub)
	if status != http.StatusNoContent {
		t.Fatalf("subscribe with fresh device token = %d, want 204: %s", status, body)
	}

	// 4. Registry entry keyed by the new token id.
	entries := env.svc.Subscriptions()
	if len(entries) != 1 || entries[0].TokenID != pr.TokenID {
		t.Fatalf("registry = %+v, want one entry for %q", entries, pr.TokenID)
	}
	if entries[0].Subscription.Endpoint != sub.Endpoint {
		t.Fatalf("stored endpoint = %q, want %q", entries[0].Subscription.Endpoint, sub.Endpoint)
	}

	// 5. `token list`'s data source shows the device token, workspace acme,
	// default label.
	recs, err := sortedRecords(env.tokensPath)
	if err != nil {
		t.Fatal(err)
	}
	var device *TokenRecord
	for i := range recs {
		if recs[i].ID == pr.TokenID {
			device = &recs[i]
		}
	}
	if device == nil {
		t.Fatalf("device token %q not in the tokens file", pr.TokenID)
	}
	if device.Workspace != "acme" {
		t.Fatalf("device token workspace = %q, want %q (inherited from the minter)", device.Workspace, "acme")
	}
	if device.Label != "beacon" {
		t.Fatalf("device token label = %q, want the default %q", device.Label, "beacon")
	}
}

func TestPushMintRequiresToken(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "client", workspace: ""})
	if status, body := doPush(t, "POST", env.srv.URL+"/api/v1/push/pairings", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("mint without token = %d, want 401: %s", status, body)
	}
	if status, body := doPush(t, "POST", env.srv.URL+"/api/v1/push/pairings", "not-a-token", nil); status != http.StatusUnauthorized {
		t.Fatalf("mint with invalid token = %d, want 401: %s", status, body)
	}
}

func TestPairGarbageCodeIsUniform401(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "client", workspace: ""})
	for _, body := range []any{map[string]string{"code": "NOPE-NOPE"}, "not json"} {
		status, resp := doPush(t, "POST", env.srv.URL+"/api/v1/push/pair", "", body)
		if status != http.StatusUnauthorized {
			t.Fatalf("pair(%v) = %d, want 401: %s", body, status, resp)
		}
		if !strings.Contains(string(resp), "invalid or expired pairing code") {
			t.Fatalf("pair failure %q is not the uniform message", resp)
		}
	}
}

func TestSubscriptionBadBodyNamesField(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "client", workspace: ""})
	sub := browserSubscription(t, "https://push.example/v2/x")
	sub.Keys.P256dh = base64.RawURLEncoding.EncodeToString(append([]byte{0x02}, make([]byte, 32)...)) // 33-byte compressed
	status, body := doPush(t, "POST", env.srv.URL+"/api/v1/push/subscriptions", env.tokens["client"], sub)
	if status != http.StatusBadRequest {
		t.Fatalf("bad subscription = %d, want 400: %s", status, body)
	}
	if !strings.Contains(string(body), "p256dh") {
		t.Fatalf("400 body %q does not name the p256dh field", body)
	}
	if entries := env.svc.Subscriptions(); len(entries) != 0 {
		t.Fatalf("registry stored a dud: %+v", entries)
	}
}

func TestDeleteSubscriptionIsIdempotent(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "client", workspace: ""})
	pr := pairDevice(t, env, env.tokens["client"])
	sub := browserSubscription(t, "https://push.example/v2/del")
	if status, body := doPush(t, "POST", env.srv.URL+"/api/v1/push/subscriptions", pr.Token, sub); status != http.StatusNoContent {
		t.Fatalf("subscribe = %d: %s", status, body)
	}
	for i := 0; i < 2; i++ {
		if status, body := doPush(t, "DELETE", env.srv.URL+"/api/v1/push/subscriptions", pr.Token, nil); status != http.StatusNoContent {
			t.Fatalf("delete #%d = %d, want 204: %s", i+1, status, body)
		}
	}
	if entries := env.svc.Subscriptions(); len(entries) != 0 {
		t.Fatalf("registry after delete: %+v", entries)
	}
}

// TestRevocationCouplingPrunesSubscription pins the §8.1 revocation story:
// `token revoke <device>` kills WS access, and one prune tick drops the
// delivery address.
func TestRevocationCouplingPrunesSubscription(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "client", workspace: ""})
	pr := pairDevice(t, env, env.tokens["client"])
	sub := browserSubscription(t, "https://push.example/v2/rev")
	if status, body := doPush(t, "POST", env.srv.URL+"/api/v1/push/subscriptions", pr.Token, sub); status != http.StatusNoContent {
		t.Fatalf("subscribe = %d: %s", status, body)
	}

	revoked, err := revokeToken(env.tokensPath, pr.TokenID)
	if err != nil || !revoked {
		t.Fatalf("revoke: ok=%v err=%v", revoked, err)
	}
	if err := env.store.reload(); err != nil {
		t.Fatal(err)
	}
	if n := env.svc.Prune(env.store.valid); n != 1 {
		t.Fatalf("Prune dropped %d, want 1", n)
	}
	if entries := env.svc.Subscriptions(); len(entries) != 0 {
		t.Fatalf("subscription survived revocation: %+v", entries)
	}
}

// TestPairingInheritsMinterWorkspace pins the four-doors rule
// (docs/mobile-notifications-arc42.md §8.1): the pairing code inherits its
// minter's workspace and carries it into the device token.
func TestPairingInheritsMinterWorkspace(t *testing.T) {
	env := newPushEnv(t,
		tokenSeed{label: "client-a", workspace: "ws-a"},
		tokenSeed{label: "client-b", workspace: "ws-b"},
	)
	for label, want := range map[string]string{"client-a": "ws-a", "client-b": "ws-b"} {
		pr := pairDevice(t, env, env.tokens[label])
		ident, ok := env.store.identityOf(pr.TokenID)
		if !ok {
			t.Fatalf("device token %q not in the store", pr.TokenID)
		}
		if ident.workspace != want {
			t.Fatalf("device token minted via %s carries workspace %q, want %q", label, ident.workspace, want)
		}
	}
}

// TestPushInfoStableAcrossRebuild pins that /push/info serves the same
// VAPID public key the pair response carried, including across a relay
// restart on the same data dir.
func TestPushInfoStableAcrossRebuild(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "client", workspace: ""})

	var info struct {
		Enabled        bool   `json:"enabled"`
		VAPIDPublicKey string `json:"vapid_public_key"`
	}
	status, body := doPush(t, "GET", env.srv.URL+"/api/v1/push/info", "", nil)
	if status != http.StatusOK {
		t.Fatalf("info = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if !info.Enabled || info.VAPIDPublicKey == "" {
		t.Fatalf("info on an auth-enabled relay = %+v", info)
	}

	pr := pairDevice(t, env, env.tokens["client"])
	if pr.VAPIDPublicKey != info.VAPIDPublicKey {
		t.Fatalf("pair key %q != info key %q", pr.VAPIDPublicKey, info.VAPIDPublicKey)
	}

	// A rebuilt mux over the same data dir (a relay restart) serves the
	// same identity.
	svc2, err := push.NewService(env.ddir, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(buildMux(newHubWithAuth(env.store, nil, defaultLimits()), env.store, svc2, newPushObserver(svc2, env.store, &fakeSender{}, notify.Config{}, nil)))
	t.Cleanup(srv2.Close)
	status, body = doPush(t, "GET", srv2.URL+"/api/v1/push/info", "", nil)
	if status != http.StatusOK {
		t.Fatalf("info (restart) = %d: %s", status, body)
	}
	var info2 struct {
		VAPIDPublicKey string `json:"vapid_public_key"`
	}
	if err := json.Unmarshal(body, &info2); err != nil {
		t.Fatal(err)
	}
	if info2.VAPIDPublicKey != info.VAPIDPublicKey {
		t.Fatalf("restarted relay key %q != original %q", info2.VAPIDPublicKey, info.VAPIDPublicKey)
	}
}
