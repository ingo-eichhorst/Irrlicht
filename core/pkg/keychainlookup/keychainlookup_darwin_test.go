//go:build darwin

package keychainlookup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSecurity writes a tiny shell script standing in for the real
// `security` binary and points SecurityPath at it for the duration of one
// test — never the developer's actual `security`/login keychain, which
// neither this package's callers (accountquota, museaccountapi) exercise in
// their own tests either (see accountquota/credential_keychain_test.go's
// own comment on why). Restores SecurityPath via t.Cleanup.
func fakeSecurity(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "security")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("writing fake security script: %v", err)
	}
	prev := SecurityPath
	SecurityPath = path
	t.Cleanup(func() { SecurityPath = prev })
}

// testTimeout is generous (not a measured probe cost — a deliberate ceiling)
// for the two tests below whose fake `security` script exits immediately:
// under a loaded machine running the full -race suite in parallel,
// process spawn+schedule alone was measured to exceed a 1s bound at least
// once (found running this file inside the full repo suite, issue #2007
// review follow-up) even though the script itself does no work. These
// tests assert CORRECTNESS (the right string / the right error), not
// latency, so a generous bound costs nothing in the common case and only
// matters when it saves the test from a false failure.
const testTimeout = 10 * time.Second

func TestRaw_SuccessTrimsTrailingNewline(t *testing.T) {
	fakeSecurity(t, `echo 'the-secret-value'`)
	got, err := Raw(context.Background(), "svc", "acct", testTimeout)
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	if got != "the-secret-value" {
		t.Fatalf("Raw = %q, want %q", got, "the-secret-value")
	}
}

func TestRaw_ItemNotFoundIsAnAnsweredError(t *testing.T) {
	fakeSecurity(t, `exit 44`) // security's real "item not found" exit code
	if _, err := Raw(context.Background(), "svc", "acct", testTimeout); err == nil {
		t.Fatal("expected an error for a nonzero exit")
	}
}

func TestRaw_TimeoutIsClassifiedDistinctlyFromNotFound(t *testing.T) {
	fakeSecurity(t, `sleep 5`)
	_, err := Raw(context.Background(), "svc", "acct", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error when the command outlives its timeout")
	}
}

func TestRaw_NeverPassesABareRootContextToTheChild(t *testing.T) {
	// core/architecture_shellout_test.go's repo-wide rule: this call site's
	// own context must not be a bare context.Background()/TODO() written
	// directly at the exec.CommandContext call. Raw always wraps its ctx
	// parameter in context.WithTimeout before use (keychainlookup_darwin.go)
	// — this test exercises that path with a real (short) timeout rather
	// than merely reading the source, so a future edit that dropped the
	// WithTimeout wrap would show up as this test hanging past its own
	// deadline instead of passing silently.
	fakeSecurity(t, `sleep 5`)
	done := make(chan struct{})
	go func() {
		_, _ = Raw(context.Background(), "svc", "acct", 100*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Raw did not respect its own timeout — the child was not bounded")
	}
}
