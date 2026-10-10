//go:build darwin || linux

package dsh

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Built for darwin and linux only: those are the two process observers that
// can see another process's handles (process_other.go's stub answers every
// HoldsForWriting with an error, which would fail the held-lock case rather
// than test it).

// awaitOwns polls OwnsSharedPID to a deadline and fails with the elapsed time
// otherwise. One darwin lsof may take up to processlifecycle's 2s ceiling, so
// the deadline leaves room for several on a loaded runner.
func awaitOwns(t *testing.T, transcript string, pid int, want bool) {
	t.Helper()
	const deadline = 10 * time.Second
	start := time.Now()
	for OwnsSharedPID("", transcript, pid) != want {
		if time.Since(start) > deadline {
			t.Fatalf("after %v OwnsSharedPID(%s, %d) is still %v, want %v",
				time.Since(start).Round(time.Millisecond), transcript, pid, !want, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestOwnsSharedPIDHeldLock drives the held side dsh's ownership probe had no
// test for: a child holding session.lock open for writing owns it, any other
// pid does not, and once the child closes the lock (while staying alive) it
// owns it no longer.
func TestOwnsSharedPIDHeldLock(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	lock := filepath.Join(dir, sessionLockFilename)
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// read is a shell builtin, so the holder never forks a second process
	// that would inherit fd 3.
	cmd := exec.Command("/bin/sh", "-c", `exec 3>>"$1"; read line; exec 3>&-; read line`, "sh", lock)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	holder := cmd.Process.Pid

	awaitOwns(t, transcript, holder, true)
	if OwnsSharedPID("", transcript, os.Getpid()) {
		t.Fatal("a pid that does not hold session.lock confirmed ownership")
	}

	if _, err := io.WriteString(stdin, "\n"); err != nil {
		t.Fatalf("signal lock holder: %v", err)
	}
	awaitOwns(t, transcript, holder, false)
}
