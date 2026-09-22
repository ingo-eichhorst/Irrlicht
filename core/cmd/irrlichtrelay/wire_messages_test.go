package main

// Wire-message assertions for the #1963 lift: core/pkg/onetimecode's
// sentinel errors (ErrTooManyCodes, ErrRateLimited) carry one generic,
// flow-agnostic .Error() text ("onetimecode: ..."), shared between pairing
// and enrollment. Before the lift, push/codes.go's own sentinels carried
// pairing-specific text ("push: too many outstanding pairing codes", "push:
// too many failed pairing attempts, retry later") that reached the wire
// verbatim via `pushError(w, 429, err.Error())` — nothing asserted on it, so
// the lift silently changed a shipped 429 body with no test noticing.
//
// Mutation evidence (run this session, reverted after capture): both
// `err.Error()` call sites in push_handlers.go (handleMintPairing,
// handlePair) were temporarily restored — undoing pairTooManyCodesMsg/
// pairRateLimitedMsg — and TestPairingMintCapWireMessage /
// TestPairingRedeemRateLimitWireMessage both went red, each showing the
// generic "onetimecode: ..." body in place of the "push: ..." text the
// assertion required. Reverting the same two call sites in
// enroll_handlers.go reddened TestEnrollMintCapWireMessage /
// TestEnrollRedeemRateLimitWireMessage the same way, showing "onetimecode:"
// in a body that must not carry an internal package name.

import (
	"net/http"
	"strings"
	"testing"
)

// TestPairingMintCapWireMessage pins pairing's restored mint-cap 429 text.
func TestPairingMintCapWireMessage(t *testing.T) {
	env := newPushEnv(t, tokenSeed{label: "cli"})
	for i := range 32 {
		status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pairings", env.tokens["cli"], nil)
		if status != http.StatusCreated {
			t.Fatalf("mint %d: status %d: %s", i, status, body)
		}
	}
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pairings", env.tokens["cli"], nil)
	if status != http.StatusTooManyRequests {
		t.Fatalf("mint past the outstanding-code cap: status %d, want 429: %s", status, body)
	}
	if !strings.Contains(string(body), pairTooManyCodesMsg) {
		t.Fatalf("mint-cap 429 body %q does not carry pairing's own wire text %q", body, pairTooManyCodesMsg)
	}
}

// TestPairingRedeemRateLimitWireMessage pins pairing's restored
// rate-limited 429 text.
func TestPairingRedeemRateLimitWireMessage(t *testing.T) {
	env := newPushEnv(t)
	for i := range 10 {
		status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pair", "", map[string]string{"code": "WRON-GGGG"})
		if status != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d, want 401: %s", i, status, body)
		}
	}
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/push/pair", "", map[string]string{"code": "WRON-GGGG"})
	if status != http.StatusTooManyRequests {
		t.Fatalf("redeem past the failure window: status %d, want 429: %s", status, body)
	}
	if !strings.Contains(string(body), pairRateLimitedMsg) {
		t.Fatalf("rate-limit 429 body %q does not carry pairing's own wire text %q", body, pairRateLimitedMsg)
	}
}

// TestEnrollMintCapWireMessage pins enrollment's own mint-cap 429 text —
// its own flow-specific wording, and never the shared leaf's internal
// package name.
func TestEnrollMintCapWireMessage(t *testing.T) {
	env := newEnrollEnv(t, nil, tokenSeed{label: "cli"})
	for i := range 32 {
		if _, _, err := env.mgr.Mint("acme", "laptop"); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/requests", env.tokens["cli"], nil)
	if status != http.StatusTooManyRequests {
		t.Fatalf("mint past the outstanding-code cap: status %d, want 429: %s", status, body)
	}
	if strings.Contains(string(body), "onetimecode:") {
		t.Fatalf("mint-cap 429 body leaks the internal package name: %s", body)
	}
	if !strings.Contains(string(body), enrollTooManyCodesMsg) {
		t.Fatalf("mint-cap 429 body %q does not carry enrollment's own wire text %q", body, enrollTooManyCodesMsg)
	}
}

// TestEnrollRedeemRateLimitWireMessage pins enrollment's own rate-limited
// 429 text.
func TestEnrollRedeemRateLimitWireMessage(t *testing.T) {
	env := newEnrollEnv(t, nil, tokenSeed{label: "cli"})
	for i := range 10 {
		status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": "ZZZZ-ZZZZ"})
		if status != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d, want 401: %s", i, status, body)
		}
	}
	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": "ZZZZ-ZZZZ"})
	if status != http.StatusTooManyRequests {
		t.Fatalf("redeem past the failure window: status %d, want 429: %s", status, body)
	}
	if strings.Contains(string(body), "onetimecode:") {
		t.Fatalf("rate-limit 429 body leaks the internal package name: %s", body)
	}
	if !strings.Contains(string(body), enrollRateLimitedMsg) {
		t.Fatalf("rate-limit 429 body %q does not carry enrollment's own wire text %q", body, enrollRateLimitedMsg)
	}
}
