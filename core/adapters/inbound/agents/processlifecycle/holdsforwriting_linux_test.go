//go:build linux

package processlifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The linux half of HoldsForWriting's coverage: the /proc entries a live
// holder cannot be arranged into (an fd directory this user may not read, an
// fdinfo with no or malformed flags, an fd closed between two reads), laid
// down as a fake procfs tree under t.TempDir(). holdsforwriting_test.go covers
// the answers a real holder produces against the live /proc.

// fakeProc is one fake procfs root holding a single process.
type fakeProc struct {
	root string
	pid  int
}

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	p := &fakeProc{root: t.TempDir(), pid: 4242}
	for _, sub := range []string{"fd", "fdinfo"} {
		if err := os.MkdirAll(filepath.Join(p.root, fmt.Sprint(p.pid), sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// openFD lays down fd as a link to target. fdinfo is written only when
// non-empty, so a caller can leave it missing.
func (p *fakeProc) openFD(t *testing.T, fd, target, fdinfo string) {
	t.Helper()
	dir := filepath.Join(p.root, fmt.Sprint(p.pid))
	if err := os.Symlink(target, filepath.Join(dir, "fd", fd)); err != nil {
		t.Fatal(err)
	}
	if fdinfo == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "fdinfo", fd), []byte(fdinfo), 0o644); err != nil {
		t.Fatal(err)
	}
}

// heldFile returns an existing file's path, so EvalSymlinks resolves it the
// way the probe does before comparing it with the fd links.
func heldFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "held.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// The flags values are real fdinfo spellings: octal, O_LARGEFILE (0100000)
// set as the kernel reports it on 64-bit, plus the access mode in the low two
// bits.
const (
	fdinfoReadOnly  = "pos:\t0\nflags:\t0100000\nmnt_id:\t1\n"
	fdinfoWriteOnly = "pos:\t0\nflags:\t0102001\nmnt_id:\t1\n"
	fdinfoReadWrite = "pos:\t0\nflags:\t0100002\nmnt_id:\t1\n"
)

func TestHoldsForWritingProcAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fdinfo string
		want   bool
	}{
		{name: "write-only fd", fdinfo: fdinfoWriteOnly, want: true},
		{name: "read-write fd", fdinfo: fdinfoReadWrite, want: true},
		{name: "read-only fd", fdinfo: fdinfoReadOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, path := newFakeProc(t), heldFile(t)
			p.openFD(t, "3", path, tc.fdinfo)
			held, err := holdsForWritingIn(p.root, p.pid, path)
			if err != nil || held != tc.want {
				t.Fatalf("holdsForWritingIn = (%v, %v), want (%v, nil)", held, err, tc.want)
			}
		})
	}

	t.Run("an fd on another file does not answer for this one", func(t *testing.T) {
		p, path := newFakeProc(t), heldFile(t)
		p.openFD(t, "3", heldFile(t), fdinfoWriteOnly)
		if held, err := holdsForWritingIn(p.root, p.pid, path); err != nil || held {
			t.Fatalf("holdsForWritingIn = (%v, %v), want (false, nil)", held, err)
		}
	})

	t.Run("a pid with no /proc entry does not hold it", func(t *testing.T) {
		p, path := newFakeProc(t), heldFile(t)
		if held, err := holdsForWritingIn(p.root, p.pid+1, path); err != nil || held {
			t.Fatalf("holdsForWritingIn = (%v, %v), want (false, nil)", held, err)
		}
	})

	t.Run("an fd closed before its fdinfo was read is skipped", func(t *testing.T) {
		p, path := newFakeProc(t), heldFile(t)
		p.openFD(t, "3", path, "")
		p.openFD(t, "4", path, fdinfoWriteOnly)
		if held, err := holdsForWritingIn(p.root, p.pid, path); err != nil || !held {
			t.Fatalf("holdsForWritingIn = (%v, %v), want (true, nil) from fd 4", held, err)
		}
	})
}

// TestHoldsForWritingProcCouldNotLook is the third answer as /proc spells it.
// Each row is an entry the probe could not read with confidence, so it is an
// error rather than a "does not hold" (#1537's line).
func TestHoldsForWritingProcCouldNotLook(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fdinfo string
	}{
		{name: "fdinfo without a flags line", fdinfo: "pos:\t0\nmnt_id:\t1\n"},
		{name: "fdinfo with unparseable flags", fdinfo: "pos:\t0\nflags:\tnot-octal\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, path := newFakeProc(t), heldFile(t)
			p.openFD(t, "3", path, tc.fdinfo)
			if held, err := holdsForWritingIn(p.root, p.pid, path); err == nil {
				t.Fatalf("holdsForWritingIn = (%v, nil), want an error", held)
			}
		})
	}

	t.Run("an fd directory this user may not read", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root, which reads a mode-000 directory anyway, so EACCES cannot be provoked")
		}
		p, path := newFakeProc(t), heldFile(t)
		fdDir := filepath.Join(p.root, fmt.Sprint(p.pid), "fd")
		if err := os.Chmod(fdDir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(fdDir, 0o755) })
		if held, err := holdsForWritingIn(p.root, p.pid, path); err == nil {
			t.Fatalf("holdsForWritingIn = (%v, nil) on an unreadable fd directory, want an error", held)
		}
	})
}
