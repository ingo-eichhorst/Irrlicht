//go:build darwin || linux

package processlifecycle

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Built for darwin and linux only: those are the two observers that can see
// another process's descriptors. process_other.go's stub answers every call
// with an error by contract, so the held-file rows below would fail there
// rather than test anything.

// holdsDeadline bounds every wait on the helper's descriptors. One darwin
// probe is allowed shelloutTimeout (2s), so the deadline leaves room for
// several slow probes on a loaded runner.
const holdsDeadline = 10 * time.Second

// awaitHolds polls HoldsForWriting(pid, path) until it answers (want, nil),
// and fails with the elapsed time and the last answer otherwise. It observes
// the method under test itself rather than WriterOf, so readiness is the
// subject's own answer and not a side effect of a different probe.
func awaitHolds(t *testing.T, pid int, path string, want bool) {
	t.Helper()
	start := time.Now()
	for {
		held, err := HoldsForWriting(pid, path)
		if err == nil && held == want {
			return
		}
		if time.Since(start) > holdsDeadline {
			t.Fatalf("after %v HoldsForWriting(%d, %s) = (%v, %v), want (%v, nil)",
				time.Since(start).Round(time.Millisecond), pid, path, held, err, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// holdsFixture is one helper process holding three files with three access
// modes, plus a fourth file it never opens.
type holdsFixture struct {
	writeOnly, readOnly, readWrite, unheld string
	holder                                 int
	stdin                                  io.WriteCloser
}

// newHoldsFixture starts a /bin/sh child that opens writeOnly on fd 3
// (append, write-only), readOnly on fd 4 (read-only) and readWrite on fd 5
// (read/write), then blocks in the `read` builtin; one line on stdin closes
// fds 3 and 5 and blocks again, so the process stays alive with the files
// released. read is a builtin, so the holder never forks a second process
// that would inherit the descriptors.
//
// The redirections run left to right, so fd 5 is the last one opened: the
// fixture is ready once readWrite reads as held.
func newHoldsFixture(t *testing.T) *holdsFixture {
	t.Helper()
	dir := t.TempDir()
	f := &holdsFixture{
		writeOnly: filepath.Join(dir, "write-only.jsonl"),
		readOnly:  filepath.Join(dir, "read-only.jsonl"),
		readWrite: filepath.Join(dir, "read-write.jsonl"),
		unheld:    filepath.Join(dir, "unheld.jsonl"),
	}
	for _, p := range []string{f.writeOnly, f.readOnly, f.readWrite, f.unheld} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", "-c",
		`exec 3>>"$1" 4<"$2" 5<>"$3"; read line; exec 3>&- 5>&-; read line`,
		"sh", f.writeOnly, f.readOnly, f.readWrite)
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
	f.holder, f.stdin = cmd.Process.Pid, stdin
	awaitHolds(t, f.holder, f.readWrite, true)
	return f
}

// TestHoldsForWriting drives the probe's three answers against a real holder:
// held, then released while the holder lives on, plus every way a call can name
// nothing that holds the file.
func TestHoldsForWriting(t *testing.T) {
	f := newHoldsFixture(t)
	dir := filepath.Dir(f.writeOnly)

	// The calling process is NOT excluded (the port's contract): this test
	// process holds selfHeld open for writing, so asking about itself is a
	// real hit, unlike WriterOf, which never reports the caller.
	selfHeld := filepath.Join(dir, "self-held.jsonl")
	fh, err := os.OpenFile(selfHeld, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fh.Close() })

	for _, tc := range []struct {
		name string
		pid  int
		path string
		want bool
	}{
		{name: "held write-only by the asked pid", pid: f.holder, path: f.writeOnly, want: true},
		{name: "held read-write by the asked pid", pid: f.holder, path: f.readWrite, want: true},
		// The mode decides: a read-only descriptor is not a writer.
		{name: "held read-only by the asked pid", pid: f.holder, path: f.readOnly},
		// The holder writes OTHER files (fds 3 and 5, and /dev/null on
		// stdout/stderr), so a probe that asked about the pid without scoping
		// to the path would answer true here.
		{name: "a file the asked pid does not hold", pid: f.holder, path: f.unheld},
		{name: "held by a different pid", pid: os.Getpid(), path: f.writeOnly},
		{name: "held for writing by the calling process", pid: os.Getpid(), path: selfHeld, want: true},
		{name: "missing file", pid: f.holder, path: filepath.Join(dir, "missing.jsonl")},
		{name: "empty path", pid: f.holder, path: ""},
		{name: "zero pid", pid: 0, path: f.writeOnly},
		{name: "negative pid", pid: -1, path: f.writeOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, err := HoldsForWriting(tc.pid, tc.path)
			if err != nil {
				t.Fatalf("HoldsForWriting(%d, %q) errored: %v — every row here names a probe that can run", tc.pid, tc.path, err)
			}
			if held != tc.want {
				t.Fatalf("HoldsForWriting(%d, %q) = %v, want %v", tc.pid, tc.path, held, tc.want)
			}
		})
	}

	t.Run("pid that has exited", func(t *testing.T) {
		pid := deadPIDForScannerTest(t)
		if held, err := HoldsForWriting(pid, f.writeOnly); err != nil || held {
			t.Fatalf("HoldsForWriting(%d, %q) = (%v, %v), want (false, nil)", pid, f.writeOnly, held, err)
		}
	})

	t.Run("released while the holder lives on", func(t *testing.T) {
		if _, err := io.WriteString(f.stdin, "\n"); err != nil {
			t.Fatalf("signal holder: %v", err)
		}
		awaitHolds(t, f.holder, f.writeOnly, false)
		awaitHolds(t, f.holder, f.readWrite, false)
		// The holder is blocked in its second read, so the false above is
		// "released", not "pid gone". Signal 0 checks that without touching it.
		if err := syscall.Kill(f.holder, 0); err != nil {
			t.Fatalf("holder pid %d is gone (%v), so this case did not test a release", f.holder, err)
		}
	})
}

// TestHoldsForWritingUnstattablePathIsNeverANo covers a file inside a
// directory this user may not search. darwin's lsof cannot stat it and prints
// no rows, exiting 1 as it does for "nothing to report"; linux cannot resolve
// it with filepath.EvalSymlinks. Neither is a "does not hold": a held file
// must read as held or as an error, and an unheld one as an error.
func TestHoldsForWritingUnstattablePathIsNeverANo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which searches a mode-000 directory anyway, so the path stays stattable")
	}
	f := newHoldsFixture(t)
	locked := filepath.Dir(f.writeOnly)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	// Registered after newHoldsFixture's t.TempDir, so it runs first and the
	// directory is searchable again when TempDir removes it.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.Stat(f.writeOnly); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("stat %s after chmod 000 of its directory = %v, want a permission error — without it this case tests nothing", f.writeOnly, err)
	}

	t.Run("held", func(t *testing.T) {
		if held, err := HoldsForWriting(f.holder, f.writeOnly); err == nil && !held {
			t.Fatalf("HoldsForWriting(holder, %s) = (false, nil) — a path the probe could not stat collapsed into \"does not hold\"", f.writeOnly)
		}
	})
	t.Run("unheld", func(t *testing.T) {
		if held, err := HoldsForWriting(f.holder, f.unheld); err == nil {
			t.Fatalf("HoldsForWriting(holder, %s) = (%v, nil), want an error: nothing about an unstattable path is known", f.unheld, held)
		}
	})
}
