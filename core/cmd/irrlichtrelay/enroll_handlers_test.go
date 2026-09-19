package main

// Tests for the desktop enrollment HTTP surface (#1963): the redeem route,
// the auth-off guard, and the /enroll/{code} URL form. push/codes_test.go
// (unedited — the lock on the #1901 lift into core/pkg/onetimecode) already
// proves the shared Manager's uniform-failure, cap and failure-window
// properties in general; what is new and needs its own proof here is that
// enrollment's specific wiring — the file-backed Store, and the HTTP route
// on top of it — actually delivers those properties end to end, including
// across two independent processes.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"irrlicht/core/pkg/onetimecode"
)

// enrollClock is the injected clock an enrollEnv and its test share. No test
// in this file sleeps — TTLs and the rate-limit window are driven entirely
// by advancing it.
type enrollClock struct {
	mu sync.Mutex
	t  time.Time
}

func newEnrollClock() *enrollClock {
	return &enrollClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *enrollClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *enrollClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// enrollEnv is an auth-enabled relay wired for enrollment exactly as
// runServe does: tokens file, auth store, an onetimecode.Manager over the
// real file-backed store, buildMux. now is the injected clock (nil = real
// time).
type enrollEnv struct {
	srv    *httptest.Server
	store  *authStore
	ddir   string
	mgr    *onetimecode.Manager
	tokens map[string]string // label -> plaintext, for seeded client tokens
}

func newEnrollEnv(t *testing.T, now func() time.Time, seeds ...tokenSeed) *enrollEnv {
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
	mgr := newEnrollManager(ddir, now)
	h := newHubWithAuth(store, nil, defaultLimits())
	srv := httptest.NewServer(buildMux(h, relayServices{store: store, pairing: resolvePairingHandoff(""), enroll: mgr}))
	t.Cleanup(srv.Close)
	return &enrollEnv{srv: srv, store: store, ddir: ddir, mgr: mgr, tokens: tokens}
}

// enrollRedeemResp is the wire shape of a successful POST /api/v1/enroll/redeem.
type enrollRedeemResp struct {
	Token   string `json:"token"`
	TokenID string `json:"token_id"`
}

// TestEnrollRequiresAuthGuard is the guard test for the enrollment-requires-
// auth rule: with --auth off there is no token store to mint an enrolled
// desktop's token into, so redeem is a 403 naming the fix — mirroring
// TestPushRequiresAuthGuard, but independent of push (services.push stays
// nil here too, which is the whole point: enrollment must not depend on
// push being enabled).
func TestEnrollRequiresAuthGuard(t *testing.T) {
	srv := httptest.NewServer(buildMux(newHub(defaultLimits()), relayServices{pairing: resolvePairingHandoff("")}))
	t.Cleanup(srv.Close)

	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/enroll/redeem"},
		{http.MethodPost, "/api/v1/enroll/requests"},
	}
	for _, rt := range routes {
		status, body := doPush(t, rt.method, srv.URL+rt.path, "", map[string]string{"code": "ABCD-EFGH"})
		if status != http.StatusForbidden {
			t.Fatalf("%s %s with auth off = %d, want 403: %s", rt.method, rt.path, status, body)
		}
		if !strings.Contains(string(body), "--auth tokens-file") {
			t.Fatalf("%s %s refusal %q does not name the fix (--auth tokens-file)", rt.method, rt.path, body)
		}
	}
}

// TestEnrollRedeemUniformFailure drives an unknown, an expired and an
// already-redeemed code through the live redeem route and requires the
// exact same status and body from all three — the no-oracle property
// unknown/expired/used codes must share (mirrors push's
// TestRedeemFailureIsUniform, now proven at the HTTP surface enrollment adds
// on top of the shared Manager).
func TestEnrollRedeemUniformFailure(t *testing.T) {
	clk := newEnrollClock()
	env := newEnrollEnv(t, clk.now, tokenSeed{label: "cli"})

	unknownStatus, unknownBody := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": "ABCD-EFGH"})

	expiredCode, _, err := env.mgr.Mint("acme", "laptop")
	if err != nil {
		t.Fatalf("mint (expired case): %v", err)
	}
	clk.advance(onetimecode.CodeTTL)
	expiredStatus, expiredBody := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": expiredCode})

	usedCode, _, err := env.mgr.Mint("acme", "laptop")
	if err != nil {
		t.Fatalf("mint (used case): %v", err)
	}
	if status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": usedCode}); status != http.StatusOK {
		t.Fatalf("first redeem of the to-be-used code: %d: %s", status, body)
	}
	usedStatus, usedBody := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": usedCode})

	cases := []struct {
		name   string
		status int
		body   []byte
	}{
		{"unknown", unknownStatus, unknownBody},
		{"expired", expiredStatus, expiredBody},
		{"used", usedStatus, usedBody},
	}
	for _, c := range cases {
		if c.status != http.StatusUnauthorized {
			t.Fatalf("%s code: status %d, want 401: %s", c.name, c.status, c.body)
		}
		if string(c.body) != string(unknownBody) {
			t.Fatalf("%s code body %q differs from the unknown-code body %q — redeem is not a uniform failure", c.name, c.body, unknownBody)
		}
	}
}

// TestEnrollMintCapAt32ThroughFileStore drives the outstanding-code cap
// against the real file-backed Store enrollment uses — push/codes_test.go's
// TestMintCapAt32SweepsExpiredFirst proves the shared algorithm; this proves
// enrollment's file store actually persists enough, across repeated Mint
// calls in one process, for that cap to hold.
func TestEnrollMintCapAt32ThroughFileStore(t *testing.T) {
	env := newEnrollEnv(t, nil, tokenSeed{label: "cli"})
	for i := range 32 {
		if _, _, err := env.mgr.Mint("acme", "laptop"); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	if _, _, err := env.mgr.Mint("acme", "laptop"); !errors.Is(err, onetimecode.ErrTooManyCodes) {
		t.Fatalf("mint #33 = %v, want ErrTooManyCodes", err)
	}
}

// TestEnrollRedeemFailureWindowAnswers429ThroughRoute drives the rolling
// redeem-failure window through the live route: ten wrong codes are each a
// uniform 401, the eleventh attempt — even with the CORRECT code — is a
// 429, and the window drains by clock alone.
func TestEnrollRedeemFailureWindowAnswers429ThroughRoute(t *testing.T) {
	clk := newEnrollClock()
	env := newEnrollEnv(t, clk.now, tokenSeed{label: "cli"})
	good, _, err := env.mgr.Mint("acme", "laptop")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	for i := range 10 {
		status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": "ZZZZ-ZZZZ"})
		if status != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d, want 401: %s", i, status, body)
		}
	}
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": good})
	if status != http.StatusTooManyRequests {
		t.Fatalf("redeem(correct) while the window is saturated = %d, want 429: %s", status, body)
	}

	clk.advance(onetimecode.FailureWindow + time.Second)
	status, body = doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": good})
	if status != http.StatusOK {
		t.Fatalf("redeem after the window drained = %d, want 200: %s", status, body)
	}
}

// TestEnrollCrossProcessMintRedeem is the storage-decision proof: two
// independent onetimecode.Manager instances over one data dir, never
// sharing Go state — process A mints (the shape `enroll new` runs in, no
// relay involved), process B redeems through a live relay's HTTP route. A
// RAM store cannot pass this; the file store is why the design picked one.
func TestEnrollCrossProcessMintRedeem(t *testing.T) {
	ddir := t.TempDir()
	tokensPath := filepath.Join(ddir, tokensFilename)
	mustIssueToken(t, tokensPath, "cli", "")
	store, err := newAuthStore(tokensPath)
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}

	mgrA := newEnrollManager(ddir, nil) // stands in for `enroll new`
	mgrB := newEnrollManager(ddir, nil) // stands in for the serving relay

	code, _, err := mgrA.Mint("acme", "laptop")
	if err != nil {
		t.Fatalf("mint in process A: %v", err)
	}

	srv := httptest.NewServer(buildMux(newHubWithAuth(store, nil, defaultLimits()), relayServices{store: store, pairing: resolvePairingHandoff(""), enroll: mgrB}))
	t.Cleanup(srv.Close)

	status, body := doPush(t, http.MethodPost, srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": code})
	if status != http.StatusOK {
		t.Fatalf("redeem in process B of a code minted in process A: %d: %s", status, body)
	}
	var resp enrollRedeemResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" || resp.TokenID == "" {
		t.Fatalf("redeem response incomplete: %+v", resp)
	}

	// Single use, cross-process: the second redeem of the same code fails.
	status, body = doPush(t, http.MethodPost, srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": code})
	if status != http.StatusUnauthorized {
		t.Fatalf("second redeem of the same code (process B) = %d, want 401: %s", status, body)
	}

	// The operational case #1964 actually drives: B's fileEnrollStore is
	// already warm (loaded=true from the two redeems above) when A mints a
	// SECOND, independent code — an `enroll new` run against an already-
	// running relay's data dir. B must notice the file's mtime advanced and
	// re-read it, not keep serving its first snapshot.
	code2, _, err := mgrA.Mint("acme", "laptop-2")
	if err != nil {
		t.Fatalf("second mint in process A: %v", err)
	}
	status, body = doPush(t, http.MethodPost, srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": code2})
	if status != http.StatusOK {
		t.Fatalf("redeem in process B (warm cache) of a code minted afterward in process A: %d: %s", status, body)
	}
}

// TestEnrollHandoffRejectsInvalidCode is the reflection guard on GET
// /enroll/{code}: a code that is not the exact presented form Mint emits is
// refused before the handler ever writes it into the response body.
func TestEnrollHandoffRejectsInvalidCode(t *testing.T) {
	handoff := resolvePairingHandoff("https://relay.example.com")
	srv := httptest.NewServer(buildMux(newHub(defaultLimits()), relayServices{pairing: handoff}))
	t.Cleanup(srv.Close)

	status, body := doPush(t, http.MethodGet, srv.URL+"/enroll/not-a-code", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("GET /enroll/not-a-code = %d, want 404: %s", status, body)
	}
}

// TestEnrollHandoffReflectsValidCode is the positive counterpart: a
// correctly-shaped code passes the guard and is echoed into the body, which
// is what a person following the link by hand needs to see.
func TestEnrollHandoffReflectsValidCode(t *testing.T) {
	handoff := resolvePairingHandoff("https://relay.example.com")
	srv := httptest.NewServer(buildMux(newHub(defaultLimits()), relayServices{pairing: handoff}))
	t.Cleanup(srv.Close)

	status, body := doPush(t, http.MethodGet, srv.URL+"/enroll/ABCD-EFGH", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /enroll/ABCD-EFGH = %d, want 200: %s", status, body)
	}
	if !strings.Contains(string(body), "ABCD-EFGH") {
		t.Fatalf("response does not contain the enrollment code: %s", body)
	}
}

// TestEnrollMintRequiresToken is the Phase 2 counterpart of
// TestPushMintRequiresToken: POST /api/v1/enroll/requests sits behind
// requireToken, so no bearer or an invalid one is 401.
func TestEnrollMintRequiresToken(t *testing.T) {
	env := newEnrollEnv(t, nil, tokenSeed{label: "client", workspace: ""})
	if status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/requests", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("mint without token = %d, want 401: %s", status, body)
	}
	if status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/requests", "not-a-token", nil); status != http.StatusUnauthorized {
		t.Fatalf("mint with invalid token = %d, want 401: %s", status, body)
	}
}

// TestEnrollMintEndToEnd drives the full Phase 2 flow in workspace acme:
// client token -> authenticated mint -> unauthenticated redeem -> device
// token, and confirms the device token inherits the minter's workspace —
// mirroring TestPairingInheritsMinterWorkspace for push.
func TestEnrollMintEndToEnd(t *testing.T) {
	env := newEnrollEnv(t, nil, tokenSeed{label: "dashboard", workspace: "acme"})

	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/requests", env.tokens["dashboard"], nil)
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
	if mint.Code == "" || mint.ExpiresIn != 600 {
		t.Fatalf("mint response = %+v", mint)
	}

	status, body = doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": mint.Code})
	if status != http.StatusOK {
		t.Fatalf("redeem = %d: %s", status, body)
	}
	var resp enrollRedeemResp
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" || resp.TokenID == "" {
		t.Fatalf("redeem response incomplete: %+v", resp)
	}

	recs, err := sortedRecords(filepath.Join(env.ddir, tokensFilename))
	if err != nil {
		t.Fatal(err)
	}
	var device *TokenRecord
	for i := range recs {
		if recs[i].ID == resp.TokenID {
			device = &recs[i]
		}
	}
	if device == nil {
		t.Fatalf("device token %q not in the tokens file", resp.TokenID)
	}
	if device.Workspace != "acme" {
		t.Fatalf("device token workspace = %q, want %q (inherited from the minter)", device.Workspace, "acme")
	}
}

// TestEnrollMintCapAnswers429ThroughRoute mirrors push_hardening_test.go's
// TestMintCapAnswers429: 32 authenticated mints succeed, the 33rd is a 429.
func TestEnrollMintCapAnswers429ThroughRoute(t *testing.T) {
	env := newEnrollEnv(t, nil, tokenSeed{label: "cli"})
	for i := range 32 {
		status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/requests", env.tokens["cli"], nil)
		if status != http.StatusCreated {
			t.Fatalf("mint %d: status %d, want 201: %s", i, status, body)
		}
	}
	status, _ := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/requests", env.tokens["cli"], nil)
	if status != http.StatusTooManyRequests {
		t.Fatalf("mint past the outstanding-code cap: status %d, want 429", status)
	}
}

// deviceLabelFor looks up the stored label of the TokenRecord with the
// given id, failing the test if it is not found.
func deviceLabelFor(t *testing.T, tokensPath, tokenID string) string {
	t.Helper()
	recs, err := sortedRecords(tokensPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.ID == tokenID {
			return r.Label
		}
	}
	t.Fatalf("device token %q not in %s", tokenID, tokensPath)
	return ""
}

// TestEnrollRedeemLabelPreference pins the fallback order handleEnrollRedeem
// promises: the redeeming desktop's own label (request body) wins over the
// code's mint-time label, which wins over enrollDefaultLabel when neither is
// present — mirroring how a phone names itself at pairing time via
// pairRequest.Label, except enrollment also has a mint-time label to fall
// back to (`enroll new --label`).
func TestEnrollRedeemLabelPreference(t *testing.T) {
	env := newEnrollEnv(t, nil, tokenSeed{label: "cli"})
	tokensPath := filepath.Join(env.ddir, tokensFilename)

	// Redeem label overrides the mint-time label.
	codeA, _, err := env.mgr.Mint("acme", "mint-label")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": codeA, "label": "redeem-label"})
	if status != http.StatusOK {
		t.Fatalf("redeem: %d: %s", status, body)
	}
	var respA enrollRedeemResp
	if err := json.Unmarshal(body, &respA); err != nil {
		t.Fatal(err)
	}
	if got := deviceLabelFor(t, tokensPath, respA.TokenID); got != "redeem-label" {
		t.Fatalf("label = %q, want the redeem-time label %q", got, "redeem-label")
	}

	// No redeem label: falls back to the mint-time label.
	codeB, _, err := env.mgr.Mint("acme", "mint-label-2")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	status, body = doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": codeB})
	if status != http.StatusOK {
		t.Fatalf("redeem: %d: %s", status, body)
	}
	var respB enrollRedeemResp
	if err := json.Unmarshal(body, &respB); err != nil {
		t.Fatal(err)
	}
	if got := deviceLabelFor(t, tokensPath, respB.TokenID); got != "mint-label-2" {
		t.Fatalf("label = %q, want the mint-time label %q", got, "mint-label-2")
	}

	// Neither: falls back to enrollDefaultLabel.
	codeC, _, err := env.mgr.Mint("acme", "")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	status, body = doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": codeC})
	if status != http.StatusOK {
		t.Fatalf("redeem: %d: %s", status, body)
	}
	var respC enrollRedeemResp
	if err := json.Unmarshal(body, &respC); err != nil {
		t.Fatal(err)
	}
	if got := deviceLabelFor(t, tokensPath, respC.TokenID); got != enrollDefaultLabel {
		t.Fatalf("label = %q, want the default %q", got, enrollDefaultLabel)
	}
}

// TestEnrollHandoffWithoutPublicURLIs404 mirrors
// TestPairingMintWithoutPublicURLKeepsManualCode's guard shape: with no
// --public-url, enrollURL is empty for every code, so the route 404s
// regardless of shape.
func TestEnrollHandoffWithoutPublicURLIs404(t *testing.T) {
	srv := httptest.NewServer(buildMux(newHub(defaultLimits()), relayServices{pairing: resolvePairingHandoff("")}))
	t.Cleanup(srv.Close)

	status, _ := doPush(t, http.MethodGet, srv.URL+"/enroll/ABCD-EFGH", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("GET /enroll/ABCD-EFGH with no --public-url = %d, want 404", status)
	}
}
