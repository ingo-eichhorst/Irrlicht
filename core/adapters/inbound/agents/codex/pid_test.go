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
		{name: "zero pid", path: held, pid: 0},
		{name: "negative pid", path: held, pid: -1},
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
