package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIssueValidateRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")

	id, plaintext, err := issueToken(path, "laptop")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if id == "" || plaintext == "" {
		t.Fatalf("issue returned empty id/plaintext: %q %q", id, plaintext)
	}

	store, err := newAuthStore(path)
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}
	gotID, ok := store.validate(plaintext)
	if !ok || gotID != id {
		t.Fatalf("validate(plaintext) = %q,%v; want %q,true", gotID, ok, id)
	}
	if _, ok := store.validate("bogus"); ok {
		t.Fatal("validate accepted a bogus token")
	}
	if _, ok := store.validate(""); ok {
		t.Fatal("validate accepted an empty token")
	}
	if !store.valid(id) {
		t.Fatalf("valid(%q) = false before revoke", id)
	}

	// Revoke + reload: the token is no longer valid.
	revoked, err := revokeToken(path, id)
	if err != nil || !revoked {
		t.Fatalf("revoke: ok=%v err=%v", revoked, err)
	}
	if err := store.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := store.validate(plaintext); ok {
		t.Fatal("validate accepted a revoked token after reload")
	}
	if store.valid(id) {
		t.Fatalf("valid(%q) = true after revoke", id)
	}

	// Revoking a missing id is a clean false, not an error.
	if revoked, err := revokeToken(path, "deadbeef"); err != nil || revoked {
		t.Fatalf("revoke(missing) = %v,%v; want false,nil", revoked, err)
	}
}

func TestTokensFileHashedAtRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	_, plaintext, err := issueToken(path, "x")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("tokens file is empty")
	}
	if contains(data, plaintext) {
		t.Fatalf("plaintext token leaked into tokens file at rest:\n%s", data)
	}
	if !contains(data, hashToken(plaintext)) {
		t.Fatalf("tokens file is missing the token hash:\n%s", data)
	}
}

func TestTokensFileMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes not meaningful on Windows")
	}
	path := filepath.Join(t.TempDir(), "tokens.json")
	if _, _, err := issueToken(path, "x"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("tokens file mode = %o, want 600", perm)
	}
}

func TestLoadTokensMissingFileIsEmpty(t *testing.T) {
	recs, err := loadTokens(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("loadTokens(missing) errored: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("loadTokens(missing) = %d records, want 0", len(recs))
	}
}

func contains(haystack []byte, needle string) bool {
	return len(needle) > 0 && bytesIndex(haystack, needle) >= 0
}

func bytesIndex(h []byte, n string) int {
	hs, ns := string(h), n
	for i := 0; i+len(ns) <= len(hs); i++ {
		if hs[i:i+len(ns)] == ns {
			return i
		}
	}
	return -1
}
