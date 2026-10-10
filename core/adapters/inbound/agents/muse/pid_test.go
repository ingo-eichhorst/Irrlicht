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

// holdOpen starts a child that holds path open for writing and returns its
// PID once DiscoverPID reports it as transcript's owner, polled to a deadline.
// A child, because WriterOf never reports the calling process.
func holdOpen(t *testing.T, path, transcript string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `exec 3>>"$1"; read line`, "sh", path)
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
	// Each poll is up to two WriterOf probes (lsof on darwin, a /proc scan on
	// linux); one lsof may take up to processlifecycle's 2s shelloutTimeout,
	// so leave room for several on a loaded runner.
	const deadline = 10 * time.Second
	start := time.Now()
	for time.Since(start) < deadline {
		if got, err := DiscoverPID("", transcript, nil); err == nil && got == cmd.Process.Pid {
			return cmd.Process.Pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("after %v pid %d is still not the owner of %s", time.Since(start).Round(time.Millisecond), cmd.Process.Pid, transcript)
	return 0
}

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

func TestOwnsSharedPID(t *testing.T) {
	root := t.TempDir()
	lockHeld := newSessionDir(t, filepath.Join(root, "01a1181a-0000-7000-8000-000000000001"))
	transcriptHeld := newSessionDir(t, filepath.Join(root, "01a1181a-0000-7000-8000-000000000002"))
	unheld := newSessionDir(t, filepath.Join(root, "01a1181a-0000-7000-8000-000000000003"))
	lockHolder := holdOpen(t, filepath.Join(filepath.Dir(lockHeld), sessionLockFilename), lockHeld)
	// DiscoverPID's fallback: a lock with no writer, a transcript with one.
	transcriptHolder := holdOpen(t, transcriptHeld, transcriptHeld)

	for _, tc := range []struct {
		name string
		path string
		pid  int
		want bool
	}{
		{name: "lock held by the asked pid", path: lockHeld, pid: lockHolder, want: true},
		{name: "lock held by a different pid", path: lockHeld, pid: os.Getpid()},
		{name: "lock unheld, transcript held by the asked pid", path: transcriptHeld, pid: transcriptHolder, want: true},
		{name: "lock unheld, transcript held by a different pid", path: transcriptHeld, pid: lockHolder},
		// On the unheld session DiscoverPID answers 0, so only the pid <= 0
		// guard keeps a zero pid from "matching" no writer at all.
		{name: "zero pid", path: unheld, pid: 0},
		{name: "negative pid", path: unheld, pid: -1},
		{name: "session nobody holds", path: unheld, pid: lockHolder},
		{name: "missing session directory", path: filepath.Join(root, "missing", transcriptFilename), pid: lockHolder},
		{name: "empty transcript path", path: "", pid: lockHolder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := OwnsSharedPID("", tc.path, tc.pid); got != tc.want {
				t.Fatalf("OwnsSharedPID(%q, %d) = %v, want %v", tc.path, tc.pid, got, tc.want)
			}
		})
	}
}
