package services

import (
	"fmt"
	"os/exec"
	"path/filepath"
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

	// Give the shell a moment to open the file before probing.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !anyLiveOutputWriter([]string{out}) {
		time.Sleep(20 * time.Millisecond)
	}
	if !anyLiveOutputWriter([]string{out}) {
		t.Fatalf("probe reported no live writer while background process is running")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill background process: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// After exit the fd is closed; the probe should report dead.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && anyLiveOutputWriter([]string{out}) {
		time.Sleep(20 * time.Millisecond)
	}
	if anyLiveOutputWriter([]string{out}) {
		t.Fatalf("probe still reports a live writer after the background process exited")
	}
}

func TestAnyLiveOutputWriter_EmptyAndMissing(t *testing.T) {
	if anyLiveOutputWriter(nil) {
		t.Error("nil paths should report no live writer")
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist.output")
	if anyLiveOutputWriter([]string{missing}) {
		t.Error("a non-existent output file should report no live writer")
	}
}
