//go:build darwin

package processlifecycle

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
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

// uniqueTestPID returns an integer well beyond any real macOS PID
// (kern.maxproc is bounded; values above 2^30 are guaranteed not to be a
// live process) so the canonical socket path `/tmp/kitty-{PID}` is free.
// Salted with `os.Getpid()` so two parallel test processes don't collide.
func uniqueTestPID(t *testing.T) int {
	t.Helper()
	return 1<<30 + os.Getpid()
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

// sanity: if kitten is installed at one of the candidate locations, the
// resolved path must point at an executable file (not a directory, not a
// non-executable regular file). If kitten isn't installed, kittenPath is
// "" and the helper paths short-circuit — also valid.
func TestKittenPath_ExecutableOrEmpty(t *testing.T) {
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
	if info.IsDir() {
		t.Errorf("kittenPath %q is a directory", kittenPath)
	}
}

func TestTermProgramForAppPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/Applications/Visual Studio Code.app/Contents/MacOS/Code", "vscode"},
		{"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper", "vscode"},
		{"/Applications/iTerm.app/Contents/MacOS/iTerm2", "iTerm.app"},
		{"/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal", "Apple_Terminal"},
		{"/Applications/Cursor.app/Contents/MacOS/Cursor", "cursor"},
		{"/Applications/Ghostty.app/Contents/MacOS/ghostty", "ghostty"},
		{"/Applications/Warp.app/Contents/MacOS/stable", "Warp"},
		{"/Applications/WezTerm.app/Contents/MacOS/wezterm-gui", "WezTerm"},
		{"/Applications/Hyper.app/Contents/MacOS/Hyper", "Hyper"},
		{"/Applications/Windsurf.app/Contents/MacOS/Windsurf", "windsurf"},
		// JetBrains IDEs
		{"/Users/ingo/Applications/GoLand.app/Contents/MacOS/goland", "jetbrains"},
		{"/Applications/IntelliJ IDEA.app/Contents/MacOS/idea", "jetbrains"},
		{"/Applications/IntelliJ IDEA CE.app/Contents/MacOS/idea", "jetbrains"},
		{"/Applications/PyCharm.app/Contents/MacOS/pycharm", "jetbrains"},
		{"/Applications/PyCharm CE.app/Contents/MacOS/pycharm", "jetbrains"},
		{"/Applications/WebStorm.app/Contents/MacOS/webstorm", "jetbrains"},
		{"/Applications/Rider.app/Contents/MacOS/rider", "jetbrains"},
		{"/Applications/CLion.app/Contents/MacOS/clion", "jetbrains"},
		{"/Applications/RustRover.app/Contents/MacOS/rustrover", "jetbrains"},
		// Additional hosts
		{"/Applications/Zed.app/Contents/MacOS/zed", "zed"},
		{"/Applications/kitty.app/Contents/MacOS/kitty", "kitty"},
		{"/Applications/Rio.app/Contents/MacOS/rio", "rio"},
		{"/Applications/Tabby.app/Contents/MacOS/tabby", "tabby"},
		{"/Applications/Wave.app/Contents/MacOS/wave", "waveterm"},
		{"/Applications/Alacritty.app/Contents/MacOS/alacritty", "alacritty"},
		{"/Applications/Nova.app/Contents/MacOS/nova", "nova"},
		{"/Applications/cmux.app/Contents/MacOS/cmux", "cmux"},
		// No .app segment: not a host we know.
		{"/bin/zsh", ""},
		{"/Users/ingo/.local/share/claude/versions/2.1.114", ""},
		{"/usr/bin/tmux", ""},
		// .app appears in a path fragment but not as a bundle boundary.
		{"/tmp/not.appended/bin/thing", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := termProgramForAppPath(tc.in); got != tc.want {
			t.Errorf("termProgramForAppPath(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolveTermProgramFromAncestry_Self walks the ancestry of the running
// test binary. We don't know what terminal launched the developer's `go test`
// invocation, so we only assert that the helper either finds a supported host
// (non-empty) or returns "" cleanly — never errors or panics.
func TestResolveTermProgramFromAncestry_Self(t *testing.T) {
	got := resolveTermProgramFromAncestry(os.Getpid())
	if got != "" {
		if _, known := termProgramByAppName[reverseLookup(got)]; !known {
			t.Errorf("resolveTermProgramFromAncestry returned unknown TermProgram %q", got)
		}
	}
}

func reverseLookup(termProgram string) string {
	for k, v := range termProgramByAppName {
		if v == termProgram {
			return k
		}
	}
	return ""
}
