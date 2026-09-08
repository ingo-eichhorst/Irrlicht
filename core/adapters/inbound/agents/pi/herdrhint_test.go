// herdrhint_test.go grades the confinement, not the plumbing. The hint
// arrives on a local, unauthenticated endpoint and the socket it names is
// afterwards dialed and scanned for the process that holds the pane's client
// log open, so "which paths are accepted" is the whole security surface of
// #1936 — every case below is one way a local process could otherwise choose
// which window irrlicht's click-to-focus raises.
package pi

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// shortHerdrRoot returns a herdr config root short enough that a unix socket
// can be bound inside it.
//
// Not t.TempDir(): sun_path is 104 bytes on darwin and t.TempDir() builds its
// path from the test's own name under /var/folders/..., so the more
// descriptively a test is named the sooner binding fails with EINVAL. The same
// constraint processlifecycle's shortTempDir documents, met the same way.
func shortHerdrRoot(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "irrpihint")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	root := filepath.Join(base, "herdr")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	prev := herdrConfigDirFn
	herdrConfigDirFn = func() string { return root }
	t.Cleanup(func() { herdrConfigDirFn = prev })
	return root
}

// listenAt binds a unix socket at path, creating its parent, and returns path.
func listenAt(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return path
}

// TestConfinedHerdrSocketPathAcceptsHerdrsOwnLayout covers both spellings the
// layout has — the default session's socket in the root itself, and a named
// session's one directory down. Rejecting either would make the confinement a
// silent disable of the feature for half the users rather than a guard.
func TestConfinedHerdrSocketPathAcceptsHerdrsOwnLayout(t *testing.T) {
	root := shortHerdrRoot(t)
	for _, path := range []string{
		listenAt(t, filepath.Join(root, "herdr.sock")),
		listenAt(t, filepath.Join(root, "sessions", "f", "herdr.sock")),
	} {
		if got := confinedHerdrSocketPath(path); got != path {
			t.Errorf("confinedHerdrSocketPath(%q) = %q, want it accepted", path, got)
		}
	}
}

func TestConfinedHerdrSocketPathRejects(t *testing.T) {
	root := shortHerdrRoot(t)
	good := listenAt(t, filepath.Join(root, "sessions", "f", "herdr.sock"))

	// A socket that is real, and outside the root. The symlink case below
	// points here, which is what makes that case a genuine escape attempt
	// rather than a broken link.
	outside := listenAt(t, filepath.Join(filepath.Dir(root), "elsewhere", "herdr.sock"))

	// A directory whose name merely starts with the root's. Catches a
	// confinement written as a bare string prefix.
	sibling := listenAt(t, filepath.Join(filepath.Dir(root), "herdrevil", "herdr.sock"))

	// CodeQL recognizes an inline ".." check as the taint barrier for the
	// Lstat sink. Pin that deliberately narrower accepted alphabet here so the
	// static-analysis guard cannot disappear as an apparent no-op.
	doubleDot := listenAt(t, filepath.Join(root, "sessions", "named..session", "herdr.sock"))

	link := filepath.Join(root, "sessions", "linked", "herdr.sock")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	plainFile := filepath.Join(root, "sessions", "plain", "herdr.sock")
	if err := os.MkdirAll(filepath.Dir(plainFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(plainFile, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, tc := range []struct {
		name, path, why string
	}{
		{"empty", "", "nothing to confine"},
		{"relative", "herdr.sock", "a relative path is resolved against whatever the daemon's cwd happens to be"},
		{"traversal", filepath.Join(root, "sessions", "..", "..", "elsewhere", "herdr.sock"),
			"an unclean path must be rejected rather than normalised into an acceptable one"},
		{"outside the root", outside, "a real socket is not a herdr socket"},
		{"sibling prefix", sibling, "…/herdrevil/ is not inside …/herdr/"},
		{"double dot", doubleDot, "a sink-local traversal guard rejects every double-dot spelling"},
		{"symlink out", link, "Lstat, not Stat: the link itself is not a socket"},
		{"not a socket", plainFile, "a regular file named herdr.sock"},
		{"wrong name", filepath.Join(root, "sessions", "f", "other.sock"), "one accepted file per directory"},
		{"missing", filepath.Join(root, "sessions", "nope", "herdr.sock"), "nothing is there"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := confinedHerdrSocketPath(tc.path); got != "" {
				t.Errorf("confinedHerdrSocketPath(%q) = %q, want rejected — %s", tc.path, got, tc.why)
			}
		})
	}

	// The guard reads something rather than rejecting everything: if this
	// stopped passing, every case above would still pass and mean nothing.
	if confinedHerdrSocketPath(good) != good {
		t.Fatal("the control case was rejected too, so the rejections above are not " +
			"evidence of anything")
	}
}

// TestConfinedHerdrSocketPathWithoutARoot pins the degradation direction: a
// machine whose home directory cannot be determined accepts nothing rather
// than falling back to a bare-name check.
func TestConfinedHerdrSocketPathWithoutARoot(t *testing.T) {
	prev := herdrConfigDirFn
	herdrConfigDirFn = func() string { return "" }
	t.Cleanup(func() { herdrConfigDirFn = prev })

	if got := confinedHerdrSocketPath("/anywhere/herdr.sock"); got != "" {
		t.Errorf("accepted %q with no root to confine to", got)
	}
}

func TestValidHerdrPaneID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"w1:p2", true},
		{"w10:p3", true},
		{"pane_1", true},
		{"pane-1", true},
		{"", false},
		{"w1 p2", false},                    // whitespace
		{"w1;p2", false},                    // shell metacharacter
		{"../../etc/passwd", false},         // path-shaped
		{"w1:p2\n", false},                  // the JSON-RPC framing is newline-delimited
		{"w1:p2/../x", false},               // separators that mean something to a path
		{"w1:pä", false},                    // a multi-byte rune is not in the alphabet
		{string([]byte{0xff, 0xfe}), false}, // invalid UTF-8 decodes to RuneError
		{string(make([]byte, maxHerdrPaneIDLen)), false}, // NULs, and at the ceiling
		{"w" + string(bytesOf('1', maxHerdrPaneIDLen)), false},
	} {
		if got := validHerdrPaneID(tc.in); got != tc.want {
			t.Errorf("validHerdrPaneID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestHerdrPaneHintFromNeedsBothHalves pins that neither half is usable alone:
// the socket says which server was meant, the pane says which pane on it.
func TestHerdrPaneHintFromNeedsBothHalves(t *testing.T) {
	root := shortHerdrRoot(t)
	sock := listenAt(t, filepath.Join(root, "herdr.sock"))

	if _, ok := herdrPaneHintFrom("w1:p1", ""); ok {
		t.Error("accepted a pane with no socket; the address exists on every running server")
	}
	if _, ok := herdrPaneHintFrom("", sock); ok {
		t.Error("accepted a socket with no pane")
	}
	hint, ok := herdrPaneHintFrom("w1:p1", sock)
	if !ok {
		t.Fatal("refused a pane and a socket that are both valid")
	}
	if hint.paneID != "w1:p1" {
		t.Errorf("paneID = %q, want w1:p1", hint.paneID)
	}
	if hint.socketPath != sock {
		t.Errorf("socketPath = %q, want %q", hint.socketPath, sock)
	}
}

func bytesOf(c byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return b
}
