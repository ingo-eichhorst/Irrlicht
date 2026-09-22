package main

// Security properties of enroll-codes.json at rest, mirroring the sibling
// credential-file guards: tokens_test.go's TestTokensFileHashedAtRest/
// TestTokensFileMode0600, push_observer_test.go's roster-file mode check,
// push/service_test.go's TestVAPIDFileMode0600, push/registry_test.go's
// registry-file mode check. Nothing in core/ opened enroll-codes.json in a
// test before this file — a mutation making fileEnrollStore.Key return the
// plaintext AND relaxing the file's mode left the whole relay suite green.

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
)

// TestEnrollCodesFileHashedAtRestMode0600 pins both properties the design,
// the PR body and the arc42 §8.6 row assert about enroll-codes.json: mode
// 0600, and SHA-256 hashes only — never the plaintext code, in either its
// presented (XXXX-XXXX) or normalized form.
func TestEnrollCodesFileHashedAtRestMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes not meaningful on Windows")
	}
	ddir := t.TempDir()
	mgr := newEnrollManager(ddir, nil)
	presented, _, err := mgr.Mint("acme", "laptop")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	normalized := strings.ReplaceAll(presented, "-", "")

	path := resolveEnrollCodesPath(ddir)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("enroll-codes.json mode = %o, want 600", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("enroll-codes.json is empty")
	}
	if contains(data, presented) {
		t.Fatalf("presented plaintext code %q leaked into enroll-codes.json at rest:\n%s", presented, data)
	}
	if contains(data, normalized) {
		t.Fatalf("normalized plaintext code %q leaked into enroll-codes.json at rest:\n%s", normalized, data)
	}

	var recs []enrollCodeRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("enroll-codes.json has %d record(s), want 1", len(recs))
	}
	if want := hashEnrollCode(normalized); recs[0].Hash != want {
		t.Fatalf("stored hash = %q, want hashEnrollCode(normalized) = %q", recs[0].Hash, want)
	}
}
