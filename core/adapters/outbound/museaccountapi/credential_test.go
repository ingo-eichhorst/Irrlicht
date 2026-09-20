package museaccountapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeAuthJSON(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestCredentialResolver_FileRoute(t *testing.T) {
	dir := t.TempDir()
	path := writeAuthJSON(t, dir, `{"providers":{"meta":{"mechanism":"oauth","storage":"file","access_token":"  file-token-123  "}}}`)
	r := NewCredentialResolver(func() (string, error) { return path, nil })
	cred, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := cred.Reveal(); got != "file-token-123" {
		t.Fatalf("Reveal() = %q, want file-token-123", got)
	}
}

func TestCredentialResolver_KeychainRoute(t *testing.T) {
	dir := t.TempDir()
	// Matches THIS machine's own auth.json shape (#2007 probe, key names
	// only — no secret value was ever read from the real file): oauth +
	// keychain storage, no access_token key in the file at all.
	path := writeAuthJSON(t, dir, `{"providers":{"meta":{"mechanism":"oauth","storage":"keychain","obtained_via":"device_code","api_base_url":"https://api.meta.ai/v1","user_full_name":"Test User","user_email":"test@example.com"}}}`)

	var gotService, gotAccount string
	r := NewCredentialResolver(func() (string, error) { return path, nil })
	r.lookup = func(_ context.Context, service, account string) (string, error) {
		gotService, gotAccount = service, account
		return `{"access_token":"  keychain-token-456  "}`, nil
	}
	cred, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := cred.Reveal(); got != "keychain-token-456" {
		t.Fatalf("Reveal() = %q, want keychain-token-456", got)
	}
	if gotService != KeychainService || gotAccount != KeychainAccount {
		t.Fatalf("keychain lookup got service=%q account=%q, want %q/%q", gotService, gotAccount, KeychainService, KeychainAccount)
	}
}

func TestCredentialResolver_KeychainRouteMalformedBlob(t *testing.T) {
	dir := t.TempDir()
	path := writeAuthJSON(t, dir, `{"providers":{"meta":{"mechanism":"oauth","storage":"keychain"}}}`)
	r := NewCredentialResolver(func() (string, error) { return path, nil })
	r.lookup = func(_ context.Context, _, _ string) (string, error) { return "not json", nil }
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error for a malformed keychain blob")
	}
}

func TestCredentialResolver_KeychainLookupFails(t *testing.T) {
	dir := t.TempDir()
	path := writeAuthJSON(t, dir, `{"providers":{"meta":{"mechanism":"oauth","storage":"keychain"}}}`)
	r := NewCredentialResolver(func() (string, error) { return path, nil })
	r.lookup = func(_ context.Context, _, _ string) (string, error) { return "", os.ErrNotExist }
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error when the keychain lookup itself fails")
	}
}

func TestCredentialResolver_NonOAuthMechanismRefused(t *testing.T) {
	dir := t.TempDir()
	path := writeAuthJSON(t, dir, `{"providers":{"meta":{"mechanism":"api_key","api_key":"sk-test"}}}`)
	r := NewCredentialResolver(func() (string, error) { return path, nil })
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error for a non-oauth (api_key) login — it has no subscription to poll")
	}
}

func TestCredentialResolver_MissingFile(t *testing.T) {
	r := NewCredentialResolver(func() (string, error) { return "/nonexistent/muse/auth.json", nil })
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error for a missing auth.json")
	}
}

func TestCredentialResolver_NoPathResolver(t *testing.T) {
	r := CredentialResolver{}
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error when Path is nil")
	}
}

// TestCredentialResolver_NeverCallsReveal is this package's own version of
// accountquota.TestCredential_RevealHasOneCallSite: this package must never
// call outbound.Credential.Reveal() at all (it only ever CONSTRUCTS one via
// outbound.NewCredential) — see this package's doc comment. Grepped
// structurally, the same style as the guard it mirrors, via
// assertNoProductionFileContains (production_source_scan_test.go).
func TestCredentialResolver_NeverCallsReveal(t *testing.T) {
	// ".Reveal" + "()" split across a concatenation so this file's OWN
	// source never contains the literal substring being searched for — see
	// assertNoProductionFileContains's own doc comment on the self-match
	// risk this avoids (this file is a _test.go file and so is already
	// excluded from the scan, but the split keeps the intent visible at the
	// call site too).
	assertNoProductionFileContains(t, ".Reveal"+"()",
		"calls .Reveal() — this package must never unwrap a Credential, only construct one via outbound.NewCredential")
}
