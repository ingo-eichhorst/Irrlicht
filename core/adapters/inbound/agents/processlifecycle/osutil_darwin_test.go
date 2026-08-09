//go:build darwin

package processlifecycle

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestTopLevelAppPath(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		// Real top-level apps: the wrapping .app is not nested.
		{"obsidian main", "/Applications/Obsidian.app/Contents/MacOS/Obsidian", "/Applications/Obsidian.app"},
		{"vscode main", "/Applications/Visual Studio Code.app/Contents/MacOS/Code", "/Applications/Visual Studio Code.app"},
		{"iterm", "/Applications/iTerm.app/Contents/MacOS/iTerm2", "/Applications/iTerm.app"},
		{"user-dir app", "/Users/ingo/Applications/GoLand.app/Contents/MacOS/goland", "/Users/ingo/Applications/GoLand.app"},
		// Electron helper: wrapping .app sits inside Contents/Frameworks — skip.
		{"obsidian renderer helper", "/Applications/Obsidian.app/Contents/Frameworks/Obsidian Helper (Renderer).app/Contents/MacOS/Obsidian Helper (Renderer)", ""},
		{"vscode code helper", "/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper", ""},
		// Framework-embedded interpreter (the ghostty-terminal Python PTY
		// helper): wrapping .app sits inside a .framework — skip, even though
		// the outer Xcode.app and inner Python.app are both real GUI bundles.
		{"python in xcode framework", "/Applications/Xcode.app/Contents/Developer/Library/Frameworks/Python3.framework/Versions/3.9/Resources/Python.app/Contents/MacOS/Python", ""},
		// Not the main executable of a bundle: no .app/Contents/MacOS/ marker.
		{"resources binary", "/Applications/Obsidian.app/Contents/Resources/helper", ""},
		{"plain shell", "/bin/zsh", ""},
		{"versioned binary", "/Users/ingo/.local/share/claude/versions/2.1.114", ""},
		{"app-like fragment", "/tmp/not.appended/bin/thing", ""},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		if got := topLevelAppPath(tc.in); got != tc.want {
			t.Errorf("%s: topLevelAppPath(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestResolveHostBundleIDFromAncestry_Self walks the ancestry of the running
// test binary. We don't know what launched the developer's `go test`, so we
// only assert the helper returns cleanly — a plausibly-shaped bundle id (it
// contains a dot) or "" — never errors or panics. The deterministic path
// logic is covered by TestTopLevelAppPath.
func TestResolveHostBundleIDFromAncestry_Self(t *testing.T) {
	bid, hostPID := resolveHostBundleIDFromAncestry(os.Getpid())
	if bid == "" {
		return // no top-level .app ancestor (e.g. CI/tmux/ssh) — valid.
	}
	if !strings.Contains(bid, ".") || hostPID <= 1 {
		t.Errorf("resolveHostBundleIDFromAncestry = (%q, %d); want a dotted bundle id and a real host PID", bid, hostPID)
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

// TestIsKnownInteractiveHostFrom exercises the allow-list decision with
// synthetic ancestry results — no live process chain needed — so the CodexBar
// exclusion (#784) and the Obsidian carve-out (#728) are both deterministic,
// not dependent on whatever happens to have launched `go test`.
func TestIsKnownInteractiveHostFrom(t *testing.T) {
	tests := []struct {
		name                 string
		termProgram          string
		bundleID             string
		wantKnownInteractive bool
	}{
		{"curated terminal", "iTerm.app", "", true},
		{"curated IDE", "vscode", "", true},
		{"obsidian via generic top-level-app fallback", "", "md.obsidian", true},
		{"codexbar is a real .app but not allow-listed", "", "com.steipete.codexbar", false},
		{"no ancestry resolved at all", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isKnownInteractiveHostFrom(tc.termProgram, tc.bundleID); got != tc.wantKnownInteractive {
				t.Errorf("isKnownInteractiveHostFrom(%q, %q) = %v, want %v", tc.termProgram, tc.bundleID, got, tc.wantKnownInteractive)
			}
		})
	}
}

// TestIsKnownInteractiveHost_Self mirrors the other "_Self" ancestry tests:
// we don't know what launched `go test`, so only assert it returns cleanly.
func TestIsKnownInteractiveHost_Self(t *testing.T) {
	_ = IsKnownInteractiveHost(os.Getpid())
}

func reverseLookup(termProgram string) string {
	for k, v := range termProgramByAppName {
		if v == termProgram {
			return k
		}
	}
	return ""
}

// --- herdr client discovery (#1350) -----------------------------------------
//
// Darwin-only because client discovery is: it walks lsof's FD table and then
// the process ancestry, neither of which the linux/other builds implement.

// spawnHerdrClient starts a sleeper that stands in for an attached herdr
// client: it holds the session's client log open for writing (which is what
// identifies a client) and carries the host terminal's identity in its own env.
// Waits until the open fd is actually visible to the discovery helper so the
// caller isn't racing the child's OpenFile.
func spawnHerdrClient(t *testing.T, socketPath string, env ...string) int {
	t.Helper()
	pid := spawnSleeperWithEnv(t, append([]string{
		"PATH=/usr/bin:/bin",
		"GO_WANT_LAUNCHER_HELPER_HOLD=" + herdrClientLogPath(socketPath),
	}, env...))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, got := range herdrClientPIDs(socketPath) {
			if got == pid {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("client pid %d never became visible as a writer of the client log", pid)
	return 0
}

// TestReadLauncherEnv_Herdr_ResolvesHostFromAttachedClient pins the defect in
// #1350: a herdr pane reached the macOS app with no term_program and no
// host_bundle_id, so resolveActivator returned nil and a click did nothing.
// The window belongs to the attached *client*, so the host identity has to be
// read from that process — one indirection past the pane's own env, which
// describes the (detached, reparented) server and is deliberately suppressed.
func TestReadLauncherEnv_Herdr_ResolvesHostFromAttachedClient(t *testing.T) {
	socketPath := newHerdrSessionDir(t)
	spawnHerdrClient(t, socketPath,
		"TERM_PROGRAM=iTerm.app",
		"ITERM_SESSION_ID=w0t0p0-CLIENT",
	)
	agentPID := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"HERDR_PANE_ID=w1:p2",
		"HERDR_SOCKET_PATH=" + socketPath,
		// The stale, inherited identity a real pane carries — it describes the
		// server's launch environment and must stay suppressed (#1348).
		"TERM_PROGRAM=tmux",
		"TMUX=/tmp/foreign-tmux,95058,0",
		"TMUX_PANE=%0",
	})

	l := ReadLauncherEnv(agentPID)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	if l.TermProgram != "iTerm.app" {
		t.Errorf("TermProgram: want the attached client's iTerm.app, got %q", l.TermProgram)
	}
	if l.ITermSessionID != "w0t0p0-CLIENT" {
		t.Errorf("ITermSessionID: want the client's window selector, got %q", l.ITermSessionID)
	}
	// The pane address must survive the merge — it is what the activator
	// focuses, and what resolveBackend keys the control path on.
	if l.HerdrPaneID != "w1:p2" || l.HerdrSocketPath != socketPath {
		t.Errorf("herdr address lost: pane=%q socket=%q", l.HerdrPaneID, l.HerdrSocketPath)
	}
	// The client's env carried no tmux, so the pane's inherited tmux identity
	// must not reappear by this route — resolveBackend requires both herdr
	// fields, and a surviving TmuxPane is the #1348 misroute.
	if l.TmuxPane != "" || l.TmuxSocket != "" {
		t.Errorf("inherited tmux identity survived: pane=%q socket=%q", l.TmuxPane, l.TmuxSocket)
	}
}

// TestHerdrClientPIDs_MultipleClientsAreOrderedNewestFirst covers open question
// 3 of #1350. herdr supports attaching from more than one place at once;
// verified live (two clients on one session → two writers on the client log).
// The newest attach is the window the user most recently chose, so the
// discovery helper reports descending PID and the resolver takes the first
// candidate that yields a host.
func TestHerdrClientPIDs_MultipleClientsAreOrderedNewestFirst(t *testing.T) {
	socketPath := newHerdrSessionDir(t)
	first := spawnHerdrClient(t, socketPath)
	second := spawnHerdrClient(t, socketPath)

	pids := herdrClientPIDs(socketPath)
	if len(pids) < 2 {
		t.Fatalf("want both attached clients, got %v (first=%d second=%d)", pids, first, second)
	}
	for i := 1; i < len(pids); i++ {
		if pids[i-1] < pids[i] {
			t.Errorf("want descending pids (newest attach first), got %v", pids)
			break
		}
	}
	if pids[0] != max(first, second) {
		t.Errorf("newest client must win: got %d, want %d", pids[0], max(first, second))
	}
}

// TestHerdrClientWriters covers the FD-column rule: "holds the client log open"
// is not the predicate, "holds it open for writing" is. A read-only holder — a
// developer running `tail -f` on the log to debug herdr — is not a client, and
// adopting its terminal as the herdr host would be the #1348 misroute with a
// new cause. The reader is deliberately given the higher PID here, because the
// caller sorts descending and would otherwise prefer it.
func TestHerdrClientWriters(t *testing.T) {
	const out = `COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF   NODE NAME
herdr     26932 ingo    3w   REG   1,18      372 136170 /cfg/herdr/herdr-client.log
tail      99999 ingo    3r   REG   1,18      372 136170 /cfg/herdr/herdr-client.log
herdr     26940 ingo    9u   REG   1,18      372 136170 /cfg/herdr/herdr-client.log
herdr     26941 ingo    5uW  REG   1,18      372 136170 /cfg/herdr/herdr-client.log
irrlichd    777 ingo    4w   REG   1,18      372 136170 /cfg/herdr/herdr-client.log`

	got := herdrClientWriters(out, 777)
	want := map[int]bool{26932: true, 26940: true, 26941: true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want exactly the writers %v", got, want)
	}
	for _, pid := range got {
		if !want[pid] {
			t.Errorf("pid %d must not be reported: readers are not clients, and the daemon is not its own client", pid)
		}
	}
}

// TestHerdrClientWriters_NoHolders is the detached case as lsof reports it —
// header only, or nothing at all.
func TestHerdrClientWriters_NoHolders(t *testing.T) {
	for name, out := range map[string]string{
		"empty":       "",
		"header only": "COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF   NODE NAME",
	} {
		if got := herdrClientWriters(out, 1); len(got) != 0 {
			t.Errorf("%s: want no clients, got %v", name, got)
		}
	}
}

// TestLsofFDMode pins the mode parse, including the lock-character case: lsof
// appends a lock flag *after* the mode, so "5uW" is a read/write handle with a
// write lock — reading the last byte would call its mode 'W' and miss it.
func TestLsofFDMode(t *testing.T) {
	cases := map[string]byte{
		"14w": 'w', "8299r": 'r', "9u": 'u', "5uW": 'u', "3wR": 'w', "cwd": 'c', "12": 0,
	}
	for fd, want := range cases {
		if got := (lsofFD{FD: fd}).Mode(); got != want {
			t.Errorf("lsofFD{FD: %q}.Mode() = %q, want %q", fd, got, want)
		}
	}
}
