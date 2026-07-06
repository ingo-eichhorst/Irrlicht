package pathutil

import "testing"

func TestResolveFindsKnownSystemBinary(t *testing.T) {
	// "ls" ships in /bin or /usr/bin on every macOS and Linux install.
	p, err := Resolve("ls")
	if err != nil {
		t.Fatalf("Resolve(\"ls\") failed: %v", err)
	}
	if p == "" {
		t.Fatal("Resolve(\"ls\") returned empty path with no error")
	}
}

func TestResolveMissesUnknownBinary(t *testing.T) {
	if p, err := Resolve("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatalf("Resolve unexpectedly found %q at %q", "definitely-not-a-real-binary-xyz", p)
	}
}

func TestMustResolveFallsBackToNameOnMiss(t *testing.T) {
	if got := MustResolve("definitely-not-a-real-binary-xyz"); got != "definitely-not-a-real-binary-xyz" {
		t.Fatalf("MustResolve fallback = %q, want the bare name", got)
	}
}
