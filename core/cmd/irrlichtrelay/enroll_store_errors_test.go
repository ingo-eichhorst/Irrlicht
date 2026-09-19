package main

// A verification mechanism must fail loudly when it cannot run (AGENTS.md).
// Before this file's fix, a broken enrollment code store — root-owned after
// `sudo irrlichtrelay enroll new`, corrupt JSON, or anything else that makes
// fileEnrollStore.Load return a real I/O error rather than a clean record
// set — was indistinguishable from a wrong code: handleEnrollRedeem mapped
// ANY non-ErrRateLimited error (onetimecode.ErrCodeInvalid and a genuine
// store I/O failure alike) to the same 401, and handleMintEnroll mapped any
// non-ErrTooManyCodes error to the same 500 — both with zero bytes logged.
// An operator watching only the relay's log had no way to tell "someone
// tried a wrong code" from "the code store cannot be read at all", so a
// code minted thirty seconds ago via a `sudo`-run `enroll new` (root-owned
// file, service uid gets EACCES forever) would silently and permanently
// fail every redeem while the log stayed empty.
//
// These tests use a directory at the code store's path (not a chmod, which
// is unreliable for a non-root test process and meaningless on Windows) to
// force a real, deterministic I/O error out of Load on every platform.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenEnrollStoreEnv builds an auth-enabled relay whose enrollment code
// store path is a directory, not a file, so every Load/Save through mgr
// fails with a real I/O error.
func brokenEnrollStoreEnv(t *testing.T) (srv *httptest.Server, mintToken string) {
	t.Helper()
	ddir := t.TempDir()
	tokensPath := filepath.Join(ddir, tokensFilename)
	_, plaintext := mustIssueToken(t, tokensPath, "cli", "acme")
	store, err := newAuthStore(tokensPath)
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}
	if err := os.MkdirAll(resolveEnrollCodesPath(ddir), 0o700); err != nil {
		t.Fatal(err)
	}
	mgr := newEnrollManager(ddir, nil)
	s := httptest.NewServer(buildMux(newHubWithAuth(store, nil, defaultLimits()), relayServices{store: store, pairing: resolvePairingHandoff(""), enroll: mgr}))
	t.Cleanup(s.Close)
	return s, plaintext
}

// TestEnrollRedeemLogsStoreErrorAndStaysUniform is the redeem-side proof:
// a broken code store answers the exact same uniform failure a genuinely
// wrong code gets (the no-oracle property is not negotiable — an anonymous
// caller must not be able to distinguish "wrong code" from "storage
// broken" from the response alone), but logs the failure server-side so an
// operator can.
func TestEnrollRedeemLogsStoreErrorAndStaysUniform(t *testing.T) {
	// The canonical uniform-failure response, from a normal (working-store)
	// unknown code — TestEnrollRedeemUniformFailure already proves this is
	// what unknown/expired/used codes all share.
	workingEnv := newEnrollEnv(t, nil, tokenSeed{label: "cli"})
	wantStatus, wantBody := doPush(t, http.MethodPost, workingEnv.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": "ABCD-EFGH"})

	srv, _ := brokenEnrollStoreEnv(t)
	logBuf := captureLog(t)

	gotStatus, gotBody := doPush(t, http.MethodPost, srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": "ABCD-EFGH"})

	if gotStatus != wantStatus || string(gotBody) != string(wantBody) {
		t.Fatalf("redeem against a broken code store = %d %q, want the byte-identical uniform failure %d %q", gotStatus, gotBody, wantStatus, wantBody)
	}
	if !strings.Contains(logBuf.String(), "enroll") {
		t.Fatalf("redeem against a broken code store logged nothing naming enrollment; log = %q", logBuf.String())
	}
}

// TestEnrollMintLogsStoreError is the mint-side proof: an authenticated
// mint against a broken code store still answers its existing 500 (that
// status already correctly signals failure — no wire-shape change needed
// here, unlike redeem's uniform-401 case), but now logs it.
func TestEnrollMintLogsStoreError(t *testing.T) {
	srv, mintToken := brokenEnrollStoreEnv(t)
	logBuf := captureLog(t)

	status, body := doPush(t, http.MethodPost, srv.URL+"/api/v1/enroll/requests", mintToken, nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("mint against a broken code store = %d, want 500: %s", status, body)
	}
	if !strings.Contains(logBuf.String(), "enroll") {
		t.Fatalf("mint against a broken code store logged nothing naming enrollment; log = %q", logBuf.String())
	}
}

// TestEnrollRedeemUniformFailureStillHoldsWithLogging re-runs the existing
// uniform-failure property (unknown/expired/used all share one body) to
// confirm the logging added above changes nothing about it — logging is
// gated on "not ErrCodeInvalid", so a genuinely invalid code must still log
// nothing and answer exactly as before.
func TestEnrollRedeemUniformFailureStillHoldsWithLogging(t *testing.T) {
	env := newEnrollEnv(t, nil, tokenSeed{label: "cli"})
	logBuf := captureLog(t)

	status, body := doPush(t, http.MethodPost, env.srv.URL+"/api/v1/enroll/redeem", "", map[string]string{"code": "ABCD-EFGH"})
	if status != http.StatusUnauthorized {
		t.Fatalf("redeem of an unknown code = %d, want 401: %s", status, body)
	}
	if !strings.Contains(string(body), enrollCodeInvalidMsg) {
		t.Fatalf("redeem of an unknown code = %q, want the uniform failure message", body)
	}
	if logBuf.String() != "" {
		t.Fatalf("redeem of a genuinely unknown code logged something; a wrong code is not a store error: log = %q", logBuf.String())
	}
}
