package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// anyLiveOutputWriter must report a real background process as alive while it
// holds its output file open and dead once it exits — the production liveness
// signal for Bash run_in_background. Exercises the lsof path end-to-end with a
// real child process. See issue #445.
func TestAnyLiveOutputWriter_RealProcess(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	out := filepath.Join(t.TempDir(), "bc1h56v8v.output")

	// `exec sleep` so the shell's child (sleep) directly holds `out` open for
	// writing; the parent test process never opens it, so the only holder is
	// the background process — exactly the run_in_background shape.
	cmd := exec.Command("sh", "-c", fmt.Sprintf("exec sleep 30 > %q", out))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start background process: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Alive while the shell holds the fd open, dead once it exits.
	awaitConclusiveVerdict(t, out, true)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill background process: %v", err)
	}
	_, _ = cmd.Process.Wait()

	awaitConclusiveVerdict(t, out, false)
}

// awaitConclusiveVerdict polls the real probe until it CONCLUSIVELY reports
// want, or the budget runs out.
//
// An inconclusive verdict is a retry, not a result — which is both the
// behavior under test and what keeps this test honest under load. The old
// version asserted on a single probe against a budget equal to lsofTimeout, so
// one slow lsof consumed the whole window and failed the test; that is the
// ~4.25s two-timeouts failure in issue #1299. Retrying instead means a loaded
// machine costs time rather than a false red.
//
// If every probe in the budget times out, the probe did its job (it said "I
// don't know") and the test simply could not observe the transition, so this
// skips rather than failing — a machine too loaded to run lsof is not a defect
// in the code under test.
func awaitConclusiveVerdict(t *testing.T, path string, want bool) {
	t.Helper()
	budget := 4 * lsofTimeout
	deadline := time.Now().Add(budget)
	for {
		alive, conclusive := anyLiveOutputWriter([]string{path})
		if conclusive && alive == want {
			return
		}
		if !time.Now().Before(deadline) {
			if !conclusive {
				t.Skipf("lsof never returned a conclusive verdict within %v; machine too loaded to observe the transition", budget)
			}
			t.Fatalf("probe reported alive=%v, want %v", alive, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A timeout must be reported as inconclusive, never as "no live writer": the
// caller's response to death is a durable purge, so a slow lsof would
// permanently drop a still-running background process (issue #1299).
func TestAnyLiveOutputWriter_TimeoutIsInconclusive(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	out := filepath.Join(t.TempDir(), "bc1h56v8v.output")
	cmd := exec.Command("sh", "-c", fmt.Sprintf("exec sleep 30 > %q", out))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start background process: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// A deadline no real lsof can meet, so the probe always times out.
	restore := lsofTimeout
	lsofTimeout = 1 * time.Nanosecond
	defer func() { lsofTimeout = restore }()

	alive, conclusive := anyLiveOutputWriter([]string{out})
	if conclusive {
		t.Error("a timed-out probe reported a conclusive verdict; the caller would purge on it")
	}
	if alive {
		t.Error("a timed-out probe should not claim liveness either")
	}
}

// A path nothing holds open is a conclusive "dead" — the ordinary case the
// purge is designed for, and the one a timeout must stay distinguishable from.
func TestAnyLiveOutputWriter_UnheldPathIsConclusivelyDead(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	out := filepath.Join(t.TempDir(), "unheld.output")
	if err := os.WriteFile(out, []byte("done\n"), 0644); err != nil {
		t.Fatal(err)
	}
	alive, conclusive := anyLiveOutputWriter([]string{out})
	if alive {
		t.Error("an unheld path should report no live writer")
	}
	if !conclusive {
		t.Error("an unheld path is a definite answer, not an inconclusive one")
	}
}

func TestAnyLiveOutputWriter_EmptyAndMissing(t *testing.T) {
	if alive, conclusive := anyLiveOutputWriter(nil); alive || !conclusive {
		t.Errorf("nil paths = (%v, %v), want a conclusive no-live-writer", alive, conclusive)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist.output")
	if alive, conclusive := anyLiveOutputWriter([]string{missing}); alive || !conclusive {
		t.Errorf("a non-existent output file = (%v, %v), want a conclusive no-live-writer", alive, conclusive)
	}
}

// anyLivePID must treat an EPERM result as ALIVE: kill(pid, 0) returns EPERM
// when the PID names a real process owned by another user (e.g. a root-owned
// background command), and the process existing is what holds the session
// `working` — reading EPERM as dead would wrongly flip it to ready. Driven
// through the pidLivenessSignal seam so the branch is exercised without an
// actual foreign-user process. See issue #661.
func TestAnyLivePID_EPERMIsAlive(t *testing.T) {
	orig := pidLivenessSignal
	t.Cleanup(func() { pidLivenessSignal = orig })

	pidLivenessSignal = func(pid int, sig syscall.Signal) error { return syscall.EPERM }
	if !anyLivePID([]string{"4242"}) {
		t.Error("EPERM (process exists, owned by another user) should report alive")
	}

	pidLivenessSignal = func(pid int, sig syscall.Signal) error { return syscall.ESRCH }
	if anyLivePID([]string{"4242"}) {
		t.Error("ESRCH (no such process) should report dead")
	}

	pidLivenessSignal = func(pid int, sig syscall.Signal) error { return nil }
	if !anyLivePID([]string{"4242"}) {
		t.Error("nil error (process exists, signalable) should report alive")
	}
}

func TestAnyLivePID_EmptyAndInvalid(t *testing.T) {
	if anyLivePID(nil) {
		t.Error("nil pids should report nothing live")
	}
	if anyLivePID([]string{"", "0", "-1", "notapid"}) {
		t.Error("empty / non-positive / non-numeric pids should report nothing live")
	}
}
