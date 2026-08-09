package cliprobe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbe_ReadsVersionFromStdout(t *testing.T) {
	got, err := Probe(context.Background(), []string{"sh", "-c", `echo "2.1.226 (Claude Code)"`})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != "2.1.226" {
		t.Errorf("Probe = %q, want %q", got, "2.1.226")
	}
}

// TestProbe_MissingBinaryFailsImmediately pins the most common real-world
// case, not an exotic one: the daemon runs under launchd with a minimal PATH,
// so an agent CLI installed in ~/.local/bin is simply not findable. That must
// cost nothing and must not wait out Timeout.
func TestProbe_MissingBinaryFailsImmediately(t *testing.T) {
	start := time.Now()
	_, err := Probe(context.Background(), []string{"irrlicht-no-such-binary-1365"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Probe of a nonexistent binary returned no error")
	}
	if elapsed > Timeout/2 {
		t.Errorf("Probe took %v for a missing binary; PATH lookup should fail before "+
			"anything is spawned, well inside the %v budget", elapsed, Timeout)
	}
}

func TestProbe_HungBinaryIsBounded(t *testing.T) {
	start := time.Now()
	_, err := Probe(context.Background(), []string{"sh", "-c", "sleep 30"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Probe of a hung binary returned no error")
	}
	if elapsed > 3*Timeout {
		t.Errorf("Probe took %v to give up on a hung binary; the consent path cannot "+
			"wait that long", elapsed)
	}
}

// TestProbe_OrphanHoldingStdoutIsBounded is the failure mode Timeout alone
// does not cover: the CLI exits promptly but leaves a background child holding
// the stdout pipe, so exec waits for EOF that never comes. Without
// cmd.WaitDelay this test hangs for 30s rather than failing.
func TestProbe_OrphanHoldingStdoutIsBounded(t *testing.T) {
	start := time.Now()
	_, err := Probe(context.Background(), []string{"sh", "-c", "sleep 30 & echo no-version-here"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Probe returned no error for output carrying no version")
	}
	if elapsed > 3*Timeout {
		t.Errorf("Probe took %v when a grandchild held stdout open; WaitDelay is supposed "+
			"to bound exactly this", elapsed)
	}
}

func TestProbe_UnparseableOutputIsAnError(t *testing.T) {
	if _, err := Probe(context.Background(), []string{"sh", "-c", "echo hello there"}); err == nil {
		t.Error("Probe accepted output containing no version")
	}
}

func TestProbe_NonZeroExitIsAnError(t *testing.T) {
	if _, err := Probe(context.Background(), []string{"sh", "-c", "echo 1.2.3; exit 3"}); err == nil {
		t.Error("Probe ignored a non-zero exit status; a CLI that failed is not a CLI " +
			"whose version we read")
	}
}

func TestProbe_EmptyArgv(t *testing.T) {
	if _, err := Probe(context.Background(), nil); err == nil {
		t.Error("Probe accepted an empty command")
	}
}

// TestProbe_FloodingBinaryIsBounded pins the volume ceiling. Timeout bounds how
// long a misbehaving CLI runs, not how much it writes in that time — a process
// streaming to a pipe moves gigabytes in two seconds, and this runs inside a
// long-lived daemon. Without maxOutput the read below accumulates all of it.
func TestProbe_FloodingBinaryIsBounded(t *testing.T) {
	start := time.Now()
	_, err := Probe(context.Background(), []string{"sh", "-c", "yes flooding-with-no-version"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Probe accepted an endless stream carrying no version")
	}
	if elapsed > 3*Timeout {
		t.Errorf("Probe took %v against a flooding binary", elapsed)
	}
}

// TestProbe_CapTruncatesRatherThanBlinds pins that maxOutput drops the tail
// rather than the answer: a CLI that prints its version and then far more than
// the cap, but exits cleanly, is still read correctly.
func TestProbe_CapTruncatesRatherThanBlinds(t *testing.T) {
	got, err := Probe(context.Background(), []string{"sh", "-c", "echo 1.2.3; head -c 200000 /dev/zero"})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got != "1.2.3" {
		t.Errorf("Probe = %q, want 1.2.3", got)
	}
}

// TestProbe_ResolvesThroughTrustedDirs pins that argv[0] is not resolved from
// the inherited PATH: a hostile "sh" earlier on PATH must not be what runs.
func TestProbe_ResolvesThroughTrustedDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sh"), []byte("#!/bin/sh\necho 9.9.9\n"), 0o755); err != nil {
		t.Fatalf("write decoy: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := Probe(context.Background(), []string{"sh", "-c", "echo 1.2.3"})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got == "9.9.9" {
		t.Error("Probe ran the decoy sh from a PATH-prepended writable directory; " +
			"argv[0] must resolve through pathutil's trusted dirs (go:S4036)")
	}
	if got != "1.2.3" {
		t.Errorf("Probe = %q, want 1.2.3", got)
	}
}

// TestProbe_VersionFromAnUncleanRunIsNotTrusted pins the direction chosen when
// a CLI prints something version-shaped and then fails. The output is NOT
// trusted: a version-shaped fragment in an error message ("config schema 1.0.0
// unsupported") would otherwise be read as the CLI's version and could produce
// a FALSE REFUSAL — the one direction #1365's design says must never happen
// quietly. Reporting unknown instead fails open, which is recoverable.
func TestProbe_VersionFromAnUncleanRunIsNotTrusted(t *testing.T) {
	if _, err := Probe(context.Background(), []string{"sh", "-c", "echo 1.0.0; exit 3"}); err == nil {
		t.Error("Probe trusted a version printed by a run that then failed")
	}
}
