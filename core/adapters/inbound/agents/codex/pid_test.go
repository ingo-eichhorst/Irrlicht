//go:build darwin || linux

package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Built for darwin and linux only: those are the two process observers whose
// WriterOf can see another process's handles (process_other.go's stub reports
// no writer, which would fail the held-file cases below rather than test them).

// holdRolloutOpen starts a child that holds path open for writing and returns
// its PID once DiscoverPID reports it as the writer, polled to a deadline. A
// child, because WriterOf never reports the calling process.
func holdRolloutOpen(t *testing.T, path string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `exec 3>>"$1"; read line`, "sh", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rollout holder: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	// One lsof may take up to processlifecycle's 2s shelloutTimeout; leave
	// room for several on a loaded runner.
	const deadline = 10 * time.Second
	start := time.Now()
	for time.Since(start) < deadline {
		if got, err := DiscoverPID("", path, nil); err == nil && got == cmd.Process.Pid {
			return cmd.Process.Pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("after %v pid %d is still not the writer of %s", time.Since(start).Round(time.Millisecond), cmd.Process.Pid, path)
	return 0
}

func TestOwnsSharedPID(t *testing.T) {
	dir := t.TempDir()
	held := filepath.Join(dir, "rollout-held.jsonl")
	unheld := filepath.Join(dir, "rollout-unheld.jsonl")
	for _, p := range []string{held, unheld} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	holder := holdRolloutOpen(t, held)

	for _, tc := range []struct {
		name string
		path string
		pid  int
		want bool
	}{
		{name: "held by the asked pid", path: held, pid: holder, want: true},
		{name: "held by a different pid", path: held, pid: os.Getpid()},
		// A non-positive pid names no process, so it holds nothing (the
		// HoldsForWriting port contract; processlifecycle's TestHoldsForWriting
		// pins its zero and negative pid rows on darwin and linux).
		{name: "zero pid", path: unheld, pid: 0},
		{name: "negative pid", path: unheld, pid: -1},
		{name: "file nobody holds open", path: unheld, pid: holder},
		{name: "missing file", path: filepath.Join(dir, "rollout-missing.jsonl"), pid: holder},
		{name: "empty transcript path", path: "", pid: holder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := OwnsSharedPID("", tc.path, tc.pid); got != tc.want {
				t.Fatalf("OwnsSharedPID(%q, %d) = %v, want %v", tc.path, tc.pid, got, tc.want)
			}
		})
	}
}

// TestReleasedPID pins ReleasedPID's one-way contract (#2080): true only when
// the per-pid probe ran and the asked pid does not hold the rollout. A held
// rollout, and anything that names no process or no file, is not "released".
func TestReleasedPID(t *testing.T) {
	dir := t.TempDir()
	held := filepath.Join(dir, "rollout-held.jsonl")
	unheld := filepath.Join(dir, "rollout-unheld.jsonl")
	for _, p := range []string{held, unheld} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	holder := holdRolloutOpen(t, held)

	for _, tc := range []struct {
		name string
		path string
		pid  int
		want bool
	}{
		{name: "held by the asked pid", path: held, pid: holder},
		{name: "held by a different pid", path: held, pid: os.Getpid(), want: true},
		{name: "file nobody holds open", path: unheld, pid: holder, want: true},
		// HoldsForWriting answers (false, nil) for a file that does not exist:
		// the probe ran, and the pid holds nothing by that name.
		{name: "missing file", path: filepath.Join(dir, "rollout-missing.jsonl"), pid: holder, want: true},
		// HoldsForWriting answers (false, nil) here too, which would read as
		// "released" without ReleasedPID's own guard.
		{name: "zero pid", path: unheld},
		{name: "negative pid", path: unheld, pid: -1},
		{name: "empty transcript path", path: "", pid: holder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReleasedPID("", tc.path, tc.pid); got != tc.want {
				t.Fatalf("ReleasedPID(%q, %d) = %v, want %v", tc.path, tc.pid, got, tc.want)
			}
		})
	}
}
