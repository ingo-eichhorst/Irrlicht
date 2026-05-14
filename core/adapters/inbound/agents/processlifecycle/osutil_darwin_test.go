//go:build darwin

package processlifecycle

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
)

func TestKittyAncestryPID_Self(t *testing.T) {
	// Same shape as TestResolveTermProgramFromAncestry_Self: we don't know what
	// app launched `go test`, so we only assert the helper returns cleanly.
	// When it returns non-zero, the result must point at a real kitty.app
	// process (or one that's since exited — the lookup is best-effort).
	got := kittyAncestryPID(os.Getpid())
	if got == 0 {
		return // legitimate when no kitty in ancestry
	}
	if got <= 1 {
		t.Errorf("kittyAncestryPID returned suspicious pid %d", got)
	}
}

func TestKittySocketCandidates(t *testing.T) {
	tests := []struct {
		name string
		pid  int
		want []string
	}{
		{"zero pid", 0, nil},
		{"negative pid", -1, nil},
		{"positive pid", 12345, []string{"/tmp/kitty-12345", "/private/tmp/kitty-12345"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kittySocketCandidates(tc.pid)
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i, p := range tc.want {
				if got[i] != p {
					t.Errorf("idx %d: got %q, want %q", i, got[i], p)
				}
			}
		})
	}
}

// uniqueTestPID returns a pid-shaped int unlikely to collide with any real
// process socket in /tmp. Combines getpid with a constant so two test runs
// don't fight each other.
func uniqueTestPID(t *testing.T) int {
	t.Helper()
	return os.Getpid()*10 + 7
}

func TestKittyListenOnFor_DetectsCurrentUIDSocket(t *testing.T) {
	pid := uniqueTestPID(t)
	path := fmt.Sprintf("/tmp/kitty-%d", pid)
	// Refuse to clobber a real kitty socket.
	if _, err := os.Stat(path); err == nil {
		t.Skipf("%s already exists (real kitty PID collision?) — skip", path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind unix socket at %s: %v", path, err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(path)
	})

	got := kittyListenOnFor(pid)
	want := "unix:" + path
	// On macOS /tmp is a symlink to /private/tmp; either spelling is acceptable
	// since both are returned by kittySocketCandidates.
	altWant := "unix:" + filepath.Join("/private", path)
	if got != want && got != altWant {
		t.Errorf("kittyListenOnFor(%d): got %q, want %q or %q", pid, got, want, altWant)
	}
}

func TestKittyListenOnFor_RejectsForeignUIDSocket(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("chown needs root; skip when running as non-root")
	}
	pid := uniqueTestPID(t) + 1
	path := fmt.Sprintf("/tmp/kitty-%d", pid)
	if _, err := os.Stat(path); err == nil {
		t.Skipf("%s already exists — skip", path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(path)
	})
	// Chown to nobody (uid 4294967294 / -2 on macOS).
	if err := syscall.Chown(path, -2, -2); err != nil {
		t.Fatalf("chown to nobody: %v", err)
	}
	if got := kittyListenOnFor(pid); got != "" {
		t.Errorf("foreign-uid socket: kittyListenOnFor returned %q, want \"\"", got)
	}
}

func TestKittyListenOnFor_RejectsRegularFile(t *testing.T) {
	pid := uniqueTestPID(t) + 2
	path := fmt.Sprintf("/tmp/kitty-%d", pid)
	if _, err := os.Stat(path); err == nil {
		t.Skipf("%s already exists — skip", path)
	}
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if got := kittyListenOnFor(pid); got != "" {
		t.Errorf("regular file at socket path: got %q, want \"\"", got)
	}
}

func TestKittyListenOnFor_NoFile(t *testing.T) {
	// Use a far-out PID that should have no socket on disk.
	pid := os.Getpid()*100 + 99999
	if got := kittyListenOnFor(pid); got != "" {
		t.Errorf("nonexistent socket: got %q, want \"\"", got)
	}
}

func TestKittyListenOnFor_ZeroPID(t *testing.T) {
	if got := kittyListenOnFor(0); got != "" {
		t.Errorf("pid=0: got %q, want \"\"", got)
	}
}

func TestParseKittenLsForPID(t *testing.T) {
	const sample = `[
	  {
	    "id": 1,
	    "tabs": [
	      {
	        "id": 1,
	        "windows": [
	          {"id": 11, "pid": 1000, "foreground_processes": [{"pid": 1000}, {"pid": 1001}]}
	        ]
	      },
	      {
	        "id": 2,
	        "windows": [
	          {"id": 22, "pid": 2000, "foreground_processes": [{"pid": 2050}, {"pid": 2051}]},
	          {"id": 23, "pid": 2100, "foreground_processes": []}
	        ]
	      }
	    ]
	  }
	]`

	tests := []struct {
		name string
		body []byte
		pid  int
		want string
	}{
		{"match on window.pid (shell)", []byte(sample), 1000, "11"},
		{"match on foreground_process pid", []byte(sample), 1001, "11"},
		{"match in second tab via fg", []byte(sample), 2050, "22"},
		{"match on second window in tab", []byte(sample), 2100, "23"},
		{"no match", []byte(sample), 9999, ""},
		{"empty body", []byte(""), 1000, ""},
		{"malformed json", []byte("not json"), 1000, ""},
		{"empty array", []byte("[]"), 1000, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseKittenLsForPID(tc.body, tc.pid)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// sanity: ensure the package-level kittenPath candidate set behaves on the
// dev machine — either kitten is installed at one of the known locations, or
// not. If installed, the resolved path must be executable.
func TestKittenPath_ExecutableOrEmpty(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip()
	}
	if kittenPath == "" {
		return
	}
	info, err := os.Stat(kittenPath)
	if err != nil {
		t.Fatalf("kittenPath %q: stat: %v", kittenPath, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("kittenPath %q has no executable bits: mode=%v", kittenPath, info.Mode())
	}
	// Confirm it's not a directory.
	if info.IsDir() {
		t.Errorf("kittenPath %q is a directory", kittenPath)
	}
	// Quick parse for sanity — strconv.Itoa just to keep the var live in case
	// linters strip unused imports.
	_ = strconv.Itoa(int(info.Size()))
}
