//go:build darwin || linux

package muse

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

// ownerDeadline bounds each wait for DiscoverPID to see the holder. Each poll
// is up to two WriterOf probes (lsof on darwin, a /proc scan on linux); one
// lsof may take up to processlifecycle's 2s shelloutTimeout, so the deadline
// leaves room for several on a loaded runner.
const ownerDeadline = 10 * time.Second

// newSessionDir lays down a muse session directory with a transcript and an
// unheld lock, and returns the transcript path.
func newSessionDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, transcriptFilename)
	for _, p := range []string{transcript, filepath.Join(dir, sessionLockFilename)} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return transcript
}

// startHolder starts ONE child — the `muse serve` shape — holding lockOf's
// .session.lock on fd 3 and transcriptOf's session.jsonl on fd 4, then blocking
// in the `read` builtin (no fork, so no second writer inherits the fds). It
// returns once DiscoverPID names the child for both sessions: lockOf through
// its lock, transcriptOf through the transcript fallback. A child, because
// WriterOf never reports the calling process.
func startHolder(t *testing.T, lockOf, transcriptOf string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `exec 3>>"$1" 4>>"$2"; read line`, "sh",
		sessionLockPath(lockOf), transcriptOf)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	for _, transcript := range []string{lockOf, transcriptOf} {
		start := time.Now()
		for {
			got, err := DiscoverPID("", transcript, nil)
			if err == nil && got == cmd.Process.Pid {
				break
			}
			if time.Since(start) > ownerDeadline {
				t.Fatalf("after %v DiscoverPID(%s) = %d (err %v), want holder pid %d",
					time.Since(start).Round(time.Millisecond), transcript, got, err, cmd.Process.Pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return cmd.Process.Pid
}

func TestOwnsSharedPID(t *testing.T) {
	root := t.TempDir()
	lockHeld := newSessionDir(t, filepath.Join(root, "01a1181a-0000-7000-8000-000000000001"))
	transcriptHeld := newSessionDir(t, filepath.Join(root, "01a1181a-0000-7000-8000-000000000002"))
	unheld := newSessionDir(t, filepath.Join(root, "01a1181a-0000-7000-8000-000000000003"))
	holder := startHolder(t, lockHeld, transcriptHeld)
	// This test process writes the session files with os.WriteFile, which
	// closes them, so it holds none of them open: asked about, it never owns.
	other := os.Getpid()

	for _, tc := range []struct {
		name string
		path string
		pid  int
		want bool
	}{
		{name: "lock held by the asked pid", path: lockHeld, pid: holder, want: true},
		{name: "lock held by a different pid", path: lockHeld, pid: other},
		// DiscoverPID's fallback: a lock with no writer, a transcript with one.
		{name: "lock unheld, transcript held by the asked pid", path: transcriptHeld, pid: holder, want: true},
		{name: "lock unheld, transcript held by a different pid", path: transcriptHeld, pid: other},
		// A non-positive pid names no process, so the probe answers "does not
		// hold" without asking lsof (processlifecycle's holdsForWritingVia
		// guard, pinned by TestHoldsForWritingNamesNothingStartsNoChild).
		{name: "zero pid", path: unheld, pid: 0},
		{name: "negative pid", path: unheld, pid: -1},
		{name: "session nobody holds", path: unheld, pid: holder},
		{name: "missing session directory", path: filepath.Join(root, "missing", transcriptFilename), pid: holder},
		{name: "empty transcript path", path: "", pid: holder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := OwnsSharedPID("", tc.path, tc.pid); got != tc.want {
				t.Fatalf("OwnsSharedPID(%q, %d) = %v, want %v", tc.path, tc.pid, got, tc.want)
			}
		})
	}
}
