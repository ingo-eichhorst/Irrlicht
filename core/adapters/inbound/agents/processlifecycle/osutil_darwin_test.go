//go:build darwin

package processlifecycle

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/domain/session"
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
	bid, hostPID, complete := resolveHostBundleIDFromAncestry(os.Getpid())
	// The running test binary is alive, so every readProcInfo in its chain has
	// something to answer with — barring a `ps` slow enough to blow its own 2s
	// ceiling, which is the condition #1492 is about and which this assertion
	// would report as a failure rather than hide.
	if !complete {
		t.Error("the walk over a live process aborted — either a ps timed out, or the verdict is wrong")
	}
	if bid == "" {
		// "" is the right answer only when this chain HAS no top-level .app
		// ancestor (CI, tmux, ssh). Returning here unconditionally is what let a
		// rewiring of the production probe pair read as that same valid case —
		// and "" is exactly what a probe that never consults plutil produces, so
		// the escape hatch was the failure mode (found in review of #1524).
		//
		// Re-walk the identical chain with the identical readProcInfo, changing
		// only the bundle probe to one that always answers. A non-empty sentinel
		// means the walk DID reach a top-level app, so the production pair had
		// something to name and came back empty.
		sentinel, _, _ := resolveHostBundleIDVia(os.Getpid(), readProcInfo,
			func(string) (string, error) { return "sentinel.app", nil })
		if sentinel != "" {
			t.Error("the walk reached a top-level .app ancestor but resolveHostBundleIDFromAncestry " +
				"named nothing — either its production probe pair no longer reaches plutil, or every " +
				".app in this chain really has no CFBundleIdentifier (which LaunchServices would not launch)")
		}
		return
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
//
// The last two rows are the same pair of facts as the two live-process tests
// further down, restated at the pure layer: the empty ancestry means "read, and
// nothing there" when complete and "not read" when not, and only the second
// admits (#1513). Every other row carries complete=true, which is what keeps
// the fail-open arm from swallowing the allow-list.
func TestIsKnownInteractiveHostFrom(t *testing.T) {
	tests := []struct {
		name                 string
		termProgram          string
		bundleID             string
		complete             bool
		wantKnownInteractive bool
	}{
		{"curated terminal", "iTerm.app", "", true, true},
		{"curated IDE", "vscode", "", true, true},
		{"obsidian via generic top-level-app fallback", "", "md.obsidian", true, true},
		{"codexbar is a real .app but not allow-listed", "", "com.steipete.codexbar", true, false},
		{"no ancestry resolved at all", "", "", true, false},
		{"aborted walk resolved nothing and proves nothing", "", "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isKnownInteractiveHostFrom(tc.termProgram, tc.bundleID, tc.complete); got != tc.wantKnownInteractive {
				t.Errorf("isKnownInteractiveHostFrom(%q, %q, complete=%v) = %v, want %v", tc.termProgram, tc.bundleID, tc.complete, got, tc.wantKnownInteractive)
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
		pids, _ := herdrClientPIDs(socketPath)
		for _, got := range pids {
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

	l, _ := ReadLauncherEnv(agentPID)
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

	pids, _ := herdrClientPIDs(socketPath)
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

// --- probe tri-state (#1485) -------------------------------------------------

// TestLsofProbeRan is the committed corpus for the discrimination #1485 adds:
// which lsof failures mean "ran and found nothing" and which mean "did not
// run".
//
// The two directions are caught by different rows, and it is worth being exact
// about which, because the obvious summary is wrong. A revert to the pre-#1485
// `if err != nil { return nil }` turns exactly ONE row red — "exit 1", which
// that spelling misreports as a failure. Rows 3-6 stay green under it, because
// it and this classifier agree that those are not answers; they pin the
// opposite mutation, a classifier that calls every failure a completed probe,
// and each is a case the pre-#1485 code reported as a detached session.
// Together they bracket the behaviour, which is why they are a table of real,
// executed failures rather than a sentence in a PR body.
//
// The cases are constructed from a real child process rather than from
// hand-built *exec.ExitError values, because the classification's whole job is
// to be right about what os/exec actually hands back: a context deadline in
// particular is not obviously an ExitError at all (it depends on whether the
// kill or the deadline is observed first), and asserting the *verdict* rather
// than the error type is what keeps this honest across Go versions.
func TestLsofProbeRan(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) error {
		_, err := exec.CommandContext(ctx, name, args...).Output()
		return err
	}
	deadline, cancelDeadline := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancelDeadline()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"success", nil, true},
		{"exit 1 — lsof's nothing-to-report", run(context.Background(), "/bin/sh", "-c", "exit 1"), true},
		{"exit 152 — killed under an exhausted CPU limit", run(context.Background(), "/bin/sh", "-c", "exit 152"), false},
		{"killed by a signal", run(context.Background(), "/bin/sh", "-c", "kill -9 $$"), false},
		{"context deadline", run(deadline, "/bin/sleep", "5"), false},
		{"binary missing", run(context.Background(), "/nonexistent/lsof-1485"), false},
	}
	for _, tc := range cases {
		if got := lsofProbeRan(tc.err); got != tc.want {
			t.Errorf("%s: lsofProbeRan(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestHerdrClientPIDs_ProbeTriState pins the three outcomes end to end,
// through the real lsof. The detached row is the vacuity guard: a probe that
// reported "did not run" for everything would satisfy the other two rows and
// protect a stale host forever, so "the file exists and nobody holds it open"
// must still come back as an authoritative empty answer.
func TestHerdrClientPIDs_ProbeTriState(t *testing.T) {
	t.Run("attached", func(t *testing.T) {
		socketPath := newHerdrSessionDir(t)
		client := spawnHerdrClient(t, socketPath)
		pids, probed := herdrClientPIDs(socketPath)
		if !probed {
			t.Fatal("a successful lsof must report a probe that ran")
		}
		if len(pids) == 0 || pids[0] != client {
			t.Errorf("attached client %d not reported: %v", client, pids)
		}
	})

	t.Run("detached", func(t *testing.T) {
		socketPath := newHerdrSessionDir(t)
		// The session ran once, so the log is there; nobody holds it open.
		if err := os.WriteFile(herdrClientLogPath(socketPath), []byte("detached\n"), 0o600); err != nil {
			t.Fatalf("seed client log: %v", err)
		}
		pids, probed := herdrClientPIDs(socketPath)
		if !probed {
			t.Fatal("a detach is an answer, not a failure — reporting it as unknown would freeze the last host forever")
		}
		if len(pids) != 0 {
			t.Errorf("nobody holds the log open: %v", pids)
		}
	})

	t.Run("client log is not where we looked", func(t *testing.T) {
		// lsof exits 1 for this exactly as it does for a detached session, so
		// the stat guard is the only thing separating them. Left conflated,
		// herdr moving or renaming its client log is a permanent,
		// self-consistent "nobody is attached". A socket path addressing
		// another *real* session is NOT covered — that log exists, so the
		// stat passes; see herdrClientPIDs.
		if _, probed := herdrClientPIDs(newHerdrSessionDir(t)); probed {
			t.Error("a client log that does not exist is not evidence of a detach")
		}
	})

	t.Run("no socket path", func(t *testing.T) {
		if _, probed := herdrClientPIDs(""); probed {
			t.Error("no address means nothing was probed")
		}
	})
}

// TestHerdrClientLauncher_MemoizedNonAnswerIsStillANonAnswer REPLACES
// TestHerdrClientLauncher_DoesNotCacheANonAnswer, which locked #1485's rule
// that a non-answer must never be memoized. #1514 reversed that rule; this is
// the lock on what replaced it, and it is a strictly stronger claim than the
// one it retires.
//
// The retired lock said "do not cache". This one says "cache, and the cached
// thing must not be mistaken for an answer" — which is what actually protects
// #1485's invariant. #1485's defect was never the caching; it was a probe that
// did not run being read as a detach and clearing a resolved host. A cached
// nil with no probed bit beside it would re-enter that defect through the
// memo, and would do it silently, since every consumer reads the bool and not
// the nil.
//
// The third case is the vacuity guard: a cache that reported probed=false for
// everything would satisfy the first two and destroy the tri-state.
func TestHerdrClientLauncher_MemoizedNonAnswerIsStillANonAnswer(t *testing.T) {
	memoized := func(t *testing.T, socketPath string) herdrClientCacheEntry {
		t.Helper()
		herdrClientCacheMu.Lock()
		defer herdrClientCacheMu.Unlock()
		entry, ok := herdrClientCache[socketPath]
		if !ok {
			t.Fatal("nothing was memoized: every pane sharing this socket re-pays the probe (#1514)")
		}
		return entry
	}

	t.Run("probe could not run", func(t *testing.T) {
		socketPath := newHerdrSessionDir(t) // no client log: the probe cannot run
		t.Cleanup(func() { forgetHerdrClientMemo(socketPath) })

		if _, probed := herdrClientLauncher(socketPath); probed {
			t.Fatal("want a non-answer for a socket whose client log is absent")
		}
		if entry := memoized(t, socketPath); entry.probed {
			t.Error("the memo calls a probe that never ran an answer — #1485 re-entered through the cache")
		}
		if _, probed := herdrClientLauncher(socketPath); probed {
			t.Error("the second reader was handed a detach built from a probe that did not run")
		}
	})

	t.Run("candidate could not be read", func(t *testing.T) {
		// The branch #1492 widened, and the one that made the re-probe
		// expensive rather than merely repeated.
		socketPath, _ := unreadableClientSocket(t)

		if _, probed := herdrClientLauncher(socketPath); probed {
			t.Fatal("want a non-answer for a socket whose only candidate is unreadable")
		}
		if entry := memoized(t, socketPath); entry.probed {
			t.Error("an unreadable candidate was memoized as an answer: AdoptHostIdentity would clear a live host")
		}
		if _, probed := herdrClientLauncher(socketPath); probed {
			t.Error("the second pane was told its attached client is gone")
		}
	})

	t.Run("a hit returns what was memoized", func(t *testing.T) {
		// Without this the whole family is satisfiable by a cache that returns
		// the right tri-state and drops the launcher — measured: making
		// herdrClientCacheGet return a nil launcher leaves every other test in
		// the package green, while panes 2..N of a herdr server silently lose
		// their host. The socket has no client log, so a read that reached the
		// prober instead of the memo would answer probed=false and fail here.
		socketPath := newHerdrSessionDir(t)
		want := &session.Launcher{TermProgram: "iTerm.app", HostBundleID: "com.googlecode.iterm2"}
		herdrClientCachePut(socketPath, want, true)
		t.Cleanup(func() { forgetHerdrClientMemo(socketPath) })

		got, probed := herdrClientLauncher(socketPath)
		if !probed {
			t.Fatal("a memoized answer must read back as an answer")
		}
		if got != want {
			t.Errorf("got %+v, want the memoized launcher %+v", got, want)
		}
	})

	t.Run("detach is still an answer", func(t *testing.T) {
		socketPath := newHerdrSessionDir(t)
		if err := os.WriteFile(herdrClientLogPath(socketPath), []byte("detached\n"), 0o600); err != nil {
			t.Fatalf("seed client log: %v", err)
		}
		t.Cleanup(func() { forgetHerdrClientMemo(socketPath) })

		if _, probed := herdrClientLauncher(socketPath); !probed {
			t.Fatal("a detach is an answer")
		}
		if entry := memoized(t, socketPath); !entry.probed {
			t.Error("a real detach was memoized as a non-answer: the stored host can now never be cleared")
		}
	})
}

// TestReadLauncherEnv_Herdr_UnprobableClientReportsHostUnknown is #1485 at the
// port boundary: the reader has to carry the distinction all the way out, or
// the sweep that consumes it (refreshHerdrHosts) cannot act on it. The pane's
// own identity is unaffected either way — only the second return value moves.
func TestReadLauncherEnv_Herdr_UnprobableClientReportsHostUnknown(t *testing.T) {
	socketPath := newHerdrSessionDir(t) // no client log
	// This test reads the socket three times and the last read re-memoizes, so
	// it is the one place in the family that would otherwise leave residue in
	// the package-global memo.
	t.Cleanup(func() {
		forgetHerdrClientMemo(socketPath)
	})
	agentPID := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"HERDR_PANE_ID=w1:p1",
		"HERDR_SOCKET_PATH=" + socketPath,
	})

	l, hostKnown := ReadLauncherEnv(agentPID)
	if hostKnown {
		t.Error("the client probe could not run, so the host is unknown — not absent")
	}
	if l == nil || l.HerdrPaneID != "w1:p1" {
		t.Fatalf("the pane's own address must survive an unresolved host: %+v", l)
	}

	// The same pane once the log exists and nobody holds it open: the
	// emptiness is now an answer — but not yet a visible one. Since #1514 the
	// non-answer above is memoized, so the recovery is deferred until that
	// entry expires. This assertion is the price of that reversal, pinned
	// rather than left to be rediscovered: a cached unknown clears nothing and
	// is inert at every consumer, and deferring a recovery is the ONLY
	// behaviour it changes. The deferral is bounded by a SWEEP rather than by
	// the TTL — nothing reads the memo between sweeps — which herdrClientLauncher's
	// doc works out against SweepDeadPIDs' 5s/15s cadence.
	if err := os.WriteFile(herdrClientLogPath(socketPath), []byte("detached\n"), 0o600); err != nil {
		t.Fatalf("seed client log: %v", err)
	}
	if _, hostKnown := ReadLauncherEnv(agentPID); hostKnown {
		t.Error("the memoized non-answer was bypassed: every pane on this socket is re-paying the probe (#1514)")
	}

	// Dropping the entry stands in for the TTL elapsing — waiting out five real
	// seconds would buy nothing but a slower suite. Once it is gone the detach
	// is visible, which is what bounds the deferral at one TTL.
	forgetHerdrClientMemo(socketPath)
	if _, hostKnown := ReadLauncherEnv(agentPID); !hostKnown {
		t.Error("a detached session's host is known to be absent")
	}
}

// --- client-candidate resolution (#1492) ------------------------------------

// exitedPID is deadPIDForScannerTest (scanner_test.go, same package and same
// test binary) with the recycle policy the #1492 family needs. Every identity
// read of a reaped PID fails the way a herdr client that exits between the lsof
// scan and the identity read does — and, more to the point, it takes the *same*
// `if err != nil` branch inside resolveHostFromAncestry /
// resolveHostBundleIDFromAncestry that a `ps` which blows its 2s ceiling on a
// loaded machine takes. The timeout is the cause the issue is about; a reaped
// PID is the one cause of that class a test can arrange deterministically.
//
// The policy difference is the reason this wrapper exists rather than a second
// spawn-and-reap: its caller skips on a recycled PID, which for these tests
// would let the whole family pass vacuously. macOS allocates PIDs ascending and
// wraps at 99999, so a reuse inside this window needs tens of thousands of
// intervening spawns — rare enough to assert away, not rare enough to hide.
func exitedPID(t *testing.T) int {
	t.Helper()
	pid := deadPIDForScannerTest(t)
	if IsAlive(pid) {
		t.Fatalf("pid %d is alive after being reaped — PID reuse would make every assertion below vacuous", pid)
	}
	return pid
}

// TestResolveHostFromAncestry_UnreadableProcessIsNotAMiss covers the primitive
// #1492 turns on. Both rows return ("", 0) — the walk found no supported host
// either way — and the third value is the only thing that separates them:
// launchd's chain ends honestly at PID 1, while a reaped PID's first
// readProcInfo fails, which is the same branch a `ps` that blows its 2s ceiling
// takes.
func TestResolveHostFromAncestry_UnreadableProcessIsNotAMiss(t *testing.T) {
	if term, hostPID, complete := resolveHostFromAncestry(1); term != "" || hostPID != 0 || !complete {
		t.Errorf("launchd: got (%q, %d, %v); want a completed walk that found nothing", term, hostPID, complete)
	}
	if term, hostPID, complete := resolveHostFromAncestry(exitedPID(t)); term != "" || hostPID != 0 || complete {
		t.Errorf("reaped pid: got (%q, %d, %v); want an ABORTED walk — an unreadable process is not a miss", term, hostPID, complete)
	}
}

// TestResolveHostBundleIDFromAncestry_UnreadableProcessIsNotAMiss is the same
// distinction for the generic top-level-.app walk, and pins the split of what
// was one condition: `if err != nil || ppid <= 1`. "pid's parent is init" is a
// verdict (launchd, row 1); "pid could not be read" is not (row 2). Merging
// them is #1492 in miniature.
func TestResolveHostBundleIDFromAncestry_UnreadableProcessIsNotAMiss(t *testing.T) {
	if bid, hostPID, complete := resolveHostBundleIDFromAncestry(1); bid != "" || hostPID != 0 || !complete {
		t.Errorf("launchd: got (%q, %d, %v); want a completed walk that found nothing", bid, hostPID, complete)
	}
	if bid, hostPID, complete := resolveHostBundleIDFromAncestry(exitedPID(t)); bid != "" || hostPID != 0 || complete {
		t.Errorf("reaped pid: got (%q, %d, %v); want an ABORTED walk", bid, hostPID, complete)
	}
}

// TestIsKnownInteractiveHost_AbortedWalkAdmits is #1513's defect test, and it
// is the two tests above carried up to the caller that acts on their verdict.
// Both rows resolve no host at all; the completeness bit is the only thing
// separating them.
//
// A reaped PID's first readProcInfo fails, which is the branch a `ps` that
// blows its 2s ceiling under load takes. Before #1513 that returned false, and
// because this function gates session ADMISSION (#784) the result was that a
// legitimate agent session was silently declined — not, as in #1492, that a
// click target degraded. An unreadable probe is no evidence either way, so it
// fails OPEN: the direction core/pkg/cliversion already takes for a CLI version
// it cannot read, and the direction this very function's linux/other stubs
// already take by returning true unconditionally.
func TestIsKnownInteractiveHost_AbortedWalkAdmits(t *testing.T) {
	if !IsKnownInteractiveHost(exitedPID(t)) {
		t.Error("a walk that could not be completed is not evidence of a non-interactive host — it must not reject the session")
	}
}

// walk builds a fixed-verdict ancestryWalk, and aborted/finished name its third
// value at the call site — every test of this seam turns on which of the two a
// walk returned, and a bare `true` there reads as "found a host".
func walk(host string, complete bool) ancestryWalk {
	return func(int) (string, int, bool) { return host, 0, complete }
}

const (
	aborted  = false
	finished = true
)

// TestIsKnownInteractiveHostVia_AbortedFirstWalkDoesNotDeferToTheSecond pins
// the ORDER between the two walks, which neither the pure decision nor any
// arrangement of live processes can reach: driving the two walks to DIFFERENT
// verdicts on purpose needs them injected, because in a live chain a `ps` that
// fails for one walk fails for the other.
//
// It exists because the `&& complete` clause looks like an optimization and is
// not. The two allow-lists are asymmetric — walk 1 knows 26 curated terminals
// and IDEs by app name, walk 2 knows whatever is in knownEmbeddedHostBundleIDs
// (today: md.obsidian alone) — so walk 2 can confirm an embedded host and can
// never rule out a curated one. Row 1 is the case that costs: walk 1 aborted
// and walk 2 completed on an iTerm ancestor, and iTerm's BUNDLE id is not in
// walk 2's list because walk 1 is what recognizes iTerm. Deferring to walk 2
// there rejects a legitimate iTerm session — #1513 arriving through the
// second walk.
func TestIsKnownInteractiveHostVia_AbortedFirstWalkDoesNotDeferToTheSecond(t *testing.T) {
	tests := []struct {
		name            string
		term, bundle    ancestryWalk
		wantInteractive bool
	}{
		{
			"walk 1 aborted, walk 2 saw iTerm — whose bundle only walk 1 can vouch for",
			walk("", aborted), walk("com.googlecode.iterm2", finished), true,
		},
		{
			"walk 1 aborted, walk 2 saw CodexBar — a re-probe could only reject, and that is evidence we declined to trust",
			walk("", aborted), walk("com.steipete.codexbar", finished), true,
		},
		{
			"walk 1 finished and missed, walk 2 saw CodexBar — #784, and the vacuity guard",
			walk("", finished), walk("com.steipete.codexbar", finished), false,
		},
		{
			"walk 1 finished and missed, walk 2 saw Obsidian — the one host walk 2 does vouch for",
			walk("", finished), walk("md.obsidian", finished), true,
		},
		{
			"walk 1 matched, so walk 2 is never consulted",
			walk("iTerm.app", finished), walk("com.steipete.codexbar", finished), true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isKnownInteractiveHostVia(4242, tc.term, tc.bundle); got != tc.wantInteractive {
				t.Errorf("isKnownInteractiveHostVia = %v, want %v", got, tc.wantInteractive)
			}
		})
	}
}

// TestIsKnownInteractiveHost_ReadVerdictStillExcludes is the #784 LOCK, and it
// is what stops the fix above from being spelled `return true`. A walk that RAN
// and found no curated terminal and no allow-listed embedded host — the shape
// CodexBar's background `agy` process presents — is a real answer, and must
// still exclude.
//
// The two rows leave the walk by different exits, so neither can stand in for
// the other: launchd never enters the loop at all (`cur > 1` is false
// immediately), while a reparented orphan enters it and leaves through the
// `ppid <= 1` verdict — the arm every reparented and every tmux-hosted
// candidate takes.
func TestIsKnownInteractiveHost_ReadVerdictStillExcludes(t *testing.T) {
	if IsKnownInteractiveHost(1) {
		t.Error("launchd is readable and is not an interactive host — a completed walk that found nothing must still exclude (#784)")
	}
	if orphan := orphanPID(t); IsKnownInteractiveHost(orphan) {
		t.Errorf("orphan %d was read and has no host ancestor — a completed in-loop walk must still exclude (#784)", orphan)
	}
}

// envObserver is a ProcessObserver that answers EnvOf from a fixed map. It
// exists to reach one block of applyAncestryFallbacks that no live process can
// be arranged into: the kitty back-fill, which runs only when the env NAMES
// kitty and carries no KITTY_PID, and which is then the ONLY caller of the
// ancestry walk in that run.
type envObserver struct {
	fakeObserver
	env map[string]string
}

func (o envObserver) EnvOf(int) (map[string]string, error) { return o.env, nil }

// TestHostIdentity_KittyBackfillWalkCountsTowardCompleteness pins the one
// ancestry walk that applyAncestryFallbacks used to run and then ignore.
//
// A candidate whose env says TERM_PROGRAM=kitty with no KITTY_PID skips all
// three blocks above the back-fill (block 1 needs KITTY_WINDOW_ID, blocks 2 and
// 3 need an empty TermProgram), so the back-fill's walk is the only one that
// runs — and discarding its verdict reported an aborted walk as a complete one.
// Reaching it needs the env and the process to disagree, which only the osProc
// seam can arrange: a reaped PID whose env still reads as kitty's.
func TestHostIdentity_KittyBackfillWalkCountsTowardCompleteness(t *testing.T) {
	prev := osProc
	osProc = envObserver{env: map[string]string{"TERM_PROGRAM": "kitty"}}
	t.Cleanup(func() { osProc = prev })

	// Vacuity guard: with a LIVE pid the same block runs and completes, so a
	// hostIdentity hard-wired to "incomplete" would not satisfy this test.
	if l, complete := hostIdentity(os.Getpid()); !complete || l.TermProgram != "kitty" {
		t.Errorf("live pid: got (%+v, %v); want the back-fill to run and complete", l, complete)
	}

	l, complete := hostIdentity(exitedPID(t))
	if l.TermProgram != "kitty" {
		t.Fatalf("the env still names the host, so it must survive: %+v", l)
	}
	if complete {
		t.Error("the back-fill's walk aborted and it was the only walk in this run — that is not a complete read")
	}
}

// orphanPID returns the PID of a live process whose parent has exited, so it
// has been reparented to launchd. Its ancestry walk therefore enters the loop
// and leaves it through the `ppid <= 1` verdict — the branch PID 1 itself never
// reaches, because the loop's `cur > 1` guard skips it entirely.
//
// It is also the shape hostIdentity's own comment leans on for tmux: a tmux
// server daemonizes and is reparented to PID 1, so a pane's walk terminates
// there having found nothing. That premise is a comment everywhere else in this
// package; here it is an assertion.
func orphanPID(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", "sleep 30 >/dev/null 2>&1 & echo $!").Output()
	if err != nil {
		t.Fatalf("spawn orphan: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse orphan pid %q: %v", out, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// The `sh` has exited (Output waits for it); wait for launchd to actually
	// re-parent before asserting on the chain.
	for i := 0; i < 100; i++ {
		if ppid, _, err := readProcInfo(pid); err == nil && ppid <= 1 {
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("orphan %d was never reparented to launchd", pid)
	return 0
}

// TestResolveHostFromAncestry_ChainEndingAtInitIsAVerdict covers the arm PID 1
// cannot reach. Both of the other tests' "complete" rows use launchd, whose
// walk never enters the loop at all — so the in-loop verdict, which is the one
// every reparented client (and every tmux-hosted candidate) leaves through, was
// asserted by nothing. A walk that ends at init found no host and says so;
// calling that an abort would mean a genuinely detached session's stale host
// could never be cleared, which is the #1348 misroute by a third route.
func TestResolveHostFromAncestry_ChainEndingAtInitIsAVerdict(t *testing.T) {
	orphan := orphanPID(t)

	if term, hostPID, complete := resolveHostFromAncestry(orphan); term != "" || hostPID != 0 || !complete {
		t.Errorf("orphan: got (%q, %d, %v); want a completed in-loop walk that found nothing", term, hostPID, complete)
	}
	if _, hostKnown := resolveClientHostIdentity([]int{orphan}); !hostKnown {
		t.Error("a reparented candidate WAS read and has no window — that is an answer")
	}
}

// TestResolveClientHostIdentity_TruncatedCandidatesArePoisonToo: the cap is a
// decision not to look, and answering "every attached client was read" over it
// is the same conflation the rest of this file removes, arriving through the
// cap instead of through a timeout. launchd repeated is a readable, genuinely
// hostless candidate, so nothing but the truncation can move the verdict.
func TestResolveClientHostIdentity_TruncatedCandidatesArePoisonToo(t *testing.T) {
	full := make([]int, maxClientCandidates)
	for i := range full {
		full[i] = 1
	}
	if _, hostKnown := resolveClientHostIdentity(full); !hostKnown {
		t.Fatalf("exactly maxClientCandidates readable candidates is a complete look: %d", len(full))
	}
	if _, hostKnown := resolveClientHostIdentity(append(full, 1)); hostKnown {
		t.Error("one candidate past the cap was never probed, so 'no client has a window' is unsupported")
	}
}

// TestHostIdentity_CarriesTheAncestryVerdict is the link between the two
// primitives above and the loop below: hostIdentity is where a caller reads
// them, and #1501's tmux client path is queued to read it the same way.
func TestHostIdentity_CarriesTheAncestryVerdict(t *testing.T) {
	if l, complete := hostIdentity(1); !complete || l == nil {
		t.Errorf("launchd is readable and hostless: got (%+v, %v)", l, complete)
	}
	if l, complete := hostIdentity(exitedPID(t)); complete {
		t.Errorf("a reaped pid cannot be read, so its empty identity is not evidence: got (%+v, %v)", l, complete)
	}
}

// TestResolveClientHostIdentity_UnreadableCandidateIsNotDetached is #1492.
//
// The loop's `continue` meant two things at once: "this candidate genuinely has
// no local GUI window" (an SSH client — reporting one anyway is the #1348
// misroute) and "this candidate's identity could not be READ". Answering the
// second with (nil, true) is an authoritative "detached", and AdoptHostIdentity
// acts on it by clearing TermProgram / HostBundleID / ITermSessionID / the kitty
// selectors from a session whose client is attached the whole time.
//
// A candidate that cannot be read must therefore come back as the third state —
// "I could not look" — exactly as herdrClientPIDs already reports it one layer
// down (#1485).
func TestResolveClientHostIdentity_UnreadableCandidateIsNotDetached(t *testing.T) {
	dead := exitedPID(t)

	host, hostKnown := resolveClientHostIdentity([]int{dead})

	if host != nil {
		t.Errorf("an unreadable candidate must not produce a host: %+v", host)
	}
	if hostKnown {
		t.Error("the candidate's identity could not be read, so the host is unknown — not absent (#1492)")
	}
}

// TestResolveClientHostIdentity_HostlessCandidateStaysDetached is the lock the
// fix must not break, and it passes on main by construction: #1348 removed the
// misroute where a session with no local window was reported as living in the
// terminal the user had left. launchd stands in for that candidate — its
// ancestry walk terminates immediately and honestly (PID 1 has no parent), so
// "no local window" here is evidence, and the answer stays an answer.
func TestResolveClientHostIdentity_HostlessCandidateStaysDetached(t *testing.T) {
	host, hostKnown := resolveClientHostIdentity([]int{1})

	if host != nil {
		t.Errorf("launchd has no window; reporting one is the #1348 misroute: %+v", host)
	}
	if !hostKnown {
		t.Error("the candidate WAS read and genuinely has no local window — that is an answer, not a failed probe")
	}
}

// TestResolveClientHostIdentity_OneUnreadableCandidatePoisonsTheAnswer covers
// the mixed list. "No attached client has a local window" is a claim about
// every candidate, so a single one that could not be read is enough to make it
// unsupported — the readable ones cannot vouch for it.
func TestResolveClientHostIdentity_OneUnreadableCandidatePoisonsTheAnswer(t *testing.T) {
	dead := exitedPID(t)

	// launchd first, so a loop that only remembered the FIRST candidate's
	// verdict would answer "detached" here.
	if _, hostKnown := resolveClientHostIdentity([]int{1, dead}); hostKnown {
		t.Error("one unreadable candidate makes 'no client has a window' unsupported (#1492)")
	}
	// launchd last, so a loop that only remembered the LAST verdict would
	// answer "detached" here. Between them the two arms pin both spellings of
	// "the accumulator was dropped".
	if _, hostKnown := resolveClientHostIdentity([]int{dead, 1}); hostKnown {
		t.Error("order must not change the verdict")
	}
}

// TestResolveClientHostIdentity_ResolvableCandidateStillWins is the vacuity
// guard for the three above: a loop that answered "unknown" for everything
// would satisfy the #1492 assertions while resolving no session at all.
func TestResolveClientHostIdentity_ResolvableCandidateStillWins(t *testing.T) {
	client := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=iTerm.app",
		"ITERM_SESSION_ID=w0t0p0-CLIENT",
	})

	host, hostKnown := resolveClientHostIdentity([]int{client})

	if !hostKnown {
		t.Fatal("a candidate whose own env names its host is readable by definition")
	}
	if host == nil || host.TermProgram != "iTerm.app" || host.ITermSessionID != "w0t0p0-CLIENT" {
		t.Fatalf("want the candidate's own host identity, got %+v", host)
	}

	// The asymmetry the doc claims: a found host is evidence regardless of what
	// the rest of the list did, so an unreadable candidate ahead of it must not
	// poison a POSITIVE answer — only a negative one.
	host, hostKnown = resolveClientHostIdentity([]int{exitedPID(t), client})
	if !hostKnown || host == nil || host.TermProgram != "iTerm.app" {
		t.Errorf("a resolving candidate wins outright past an unreadable one: got (%+v, %v)", host, hostKnown)
	}
}

// --- non-answer memoization (#1514) -----------------------------------------

// forgetHerdrClientMemo drops socketPath's entry from the process-global memo.
// Since #1514 every socket a test touches leaves one behind — non-answers are
// memoized too — so this is the single spelling of the locking protocol rather
// than a copy per test. One caller uses it mid-test, standing in for the TTL
// elapsing; the rest wire it through t.Cleanup.
func forgetHerdrClientMemo(socketPath string) {
	herdrClientCacheMu.Lock()
	defer herdrClientCacheMu.Unlock()
	delete(herdrClientCache, socketPath)
}

// countingLsof makes the herdr client log at logPath answer as an lsof table,
// records one line per invocation, and returns a func reporting how many times
// the probe ran. It is how the cost claims below are asserted as a COUNT of
// probes rather than as a wall-clock time: the whole regression is "N panes pay
// N scans instead of 1", and a timing assertion for that is a flake generator
// on a loaded machine — which is, awkwardly, the exact machine state the defect
// needs.
//
// The shape is odd and the reason is macOS, not taste. The obvious stub — write
// a #!/bin/sh script somewhere, point lsofPath at it — spawns in ~6ms idle and
// **2.1s under load**, because the first exec of a newly written file is
// evaluated for code signing; a copied /bin/cat was outright `signal: killed`
// on every one of 12 attempts. herdrClientPIDs gives lsof a 2s ceiling, so that
// stub does not merely run slowly, it times out and the probe reports "could
// not run" — the exact state these tests are trying to tell apart from the one
// under test. Measured worst-of-N under a concurrent `go test ./core/... -race`:
// /usr/bin/true 3.6ms, written script 2.14s, script-as-data below 6.1ms.
//
// So nothing new is ever EXEC'd: lsofPath becomes /bin/sh — a warm, signed
// system binary — and the script it runs is DATA. herdrClientPIDs passes the
// client-log path as lsof's only argument, so the client log is necessarily
// where that script has to live. Production never reads the log's bytes (it
// stats the path and hands it to lsofPath), so the substitution is invisible to
// the code under test.
//
// lsofPath is a package var (process_darwin.go) rather than a seam invented for
// this test. Swapping it under t.Cleanup is safe because this test is serial and
// Go defers every t.Parallel test until the serial ones have finished, so the
// swap can never overlap one. (Some tests in this package DO call t.Parallel
// now — none touch lsofPath, and one that did would need this fixture reworked
// rather than the comment amended again.)
func countingLsof(t *testing.T, logPath, table string) func() int {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "calls")
	script := "echo x >> '" + counter + "'\ncat <<'TABLE'\n" + table + "\nTABLE\n"
	if err := os.WriteFile(logPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write client log: %v", err)
	}
	saved := lsofPath
	lsofPath = "/bin/sh"
	t.Cleanup(func() { lsofPath = saved })
	return func() int {
		b, err := os.ReadFile(counter)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatalf("read lsof counter: %v", err)
		}
		return strings.Count(string(b), "\n")
	}
}

// unreadableClientSocket builds a herdr session whose client log exists and
// whose only attached "client" is a reaped PID — so the lsof probe answers,
// every candidate is unreadable, and resolveClientHostIdentity returns the
// (nil, false) non-answer #1492 introduced. Returns the socket path and the
// scan counter.
func unreadableClientSocket(t *testing.T) (string, func() int) {
	t.Helper()
	socketPath := newHerdrSessionDir(t)
	logPath := herdrClientLogPath(socketPath)
	table := "COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF   NODE NAME\n" +
		"herdr     " + strconv.Itoa(exitedPID(t)) + " ingo    3w   REG   1,18      372 136170 " + logPath
	scans := countingLsof(t, logPath, table)
	t.Cleanup(func() {
		forgetHerdrClientMemo(socketPath)
	})
	return socketPath, scans
}

// TestHerdrClientLauncher_UnreadableCandidateIsProbedOncePerSocket is #1514.
// Every pane of one herdr server shares a socket, and the sweep resolves them
// one after another, so the memo is the only thing standing between "one probe
// per socket" and "one probe per pane". Before #1492 an unreadable candidate
// answered (nil, true) and was memoized; after it, it answers (nil, false) and
// — under #1485's rule — was not, so all eight panes re-paid the scan plus four
// candidate probes, each of which is two ancestry walks on a 2s ceiling.
//
// Eight is the count herdrClientLauncher's own doc uses.
func TestHerdrClientLauncher_UnreadableCandidateIsProbedOncePerSocket(t *testing.T) {
	socketPath, scans := unreadableClientSocket(t)

	const panes = 8
	for i := 0; i < panes; i++ {
		if _, probed := herdrClientLauncher(socketPath); probed {
			t.Fatalf("pane %d: an unreadable candidate is not an answer", i)
		}
	}
	if got := scans(); got != 1 {
		t.Errorf("%d panes of one herdr server cost %d lsof scans, want 1 — each also drags %d candidate probes behind it",
			panes, got, maxClientCandidates)
	}
}

// TestSortedDistinctPIDs pins both halves of what herdrClientPIDs hands to
// resolveClientHostIdentity. The ordering half is #1350's open question 3; the
// distinctness half is what makes maxClientCandidates count clients rather than
// file descriptors, since parseLsofFDs emits one entry per FD row by design.
//
// The dedup is latent rather than a live fix — herdr 0.8.0 gives one write
// handle per client — so the rows below are constructed, and the "two handles"
// case is the shape that would otherwise consume two of the four candidate
// slots for one client.
func TestSortedDistinctPIDs(t *testing.T) {
	cases := map[string]struct{ in, want []int }{
		"newest first":            {[]int{26932, 26941, 26940}, []int{26941, 26940, 26932}},
		"two handles, one client": {[]int{26932, 26932}, []int{26932}},
		"repeats collapse":        {[]int{7, 9, 7, 9, 9}, []int{9, 7}},
		"empty":                   {nil, nil},
	}
	for name, tc := range cases {
		if got := sortedDistinctPIDs(slices.Clone(tc.in)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

// TestSortedDistinctPIDs_DuplicateDoesNotConsumeACandidateSlot is the reason
// the dedup is in this PR rather than filed for later: a duplicated PID is a
// way to reach maxClientCandidates' truncation that has nothing to do with how
// many clients are attached, and that truncation is one of the two triggers of
// #1514's permanent non-answer.
func TestSortedDistinctPIDs_DuplicateDoesNotConsumeACandidateSlot(t *testing.T) {
	// Three clients, one of them holding two write handles: five FD rows.
	rows := []int{100, 100, 200, 300, 300}
	got := sortedDistinctPIDs(slices.Clone(rows))
	if len(got) > maxClientCandidates {
		t.Fatalf("%d FD rows for 3 clients still exceed the %d-candidate cap: %v",
			len(rows), maxClientCandidates, got)
	}
	if len(got) != 3 {
		t.Errorf("got %v, want one entry per client", got)
	}
}

// TestHerdrClientPIDs_DedupesFDRowsOfOneClient is the end-to-end half of the
// dedup, and it is the one that matters: TestSortedDistinctPIDs calls the
// helper directly, so nothing there pins that herdrClientPIDs — the helper's
// only production caller — actually routes through it. Measured: unwiring the
// call while keeping the sort leaves the whole package green.
//
// TestHerdrClientPIDs_MultipleClientsAreOrderedNewestFirst cannot cover this,
// because real herdr clients hold one write handle each — which is exactly the
// case the dedup is not about.
func TestHerdrClientPIDs_DedupesFDRowsOfOneClient(t *testing.T) {
	socketPath := newHerdrSessionDir(t)
	logPath := herdrClientLogPath(socketPath)
	// One client on two write handles, plus a second client. lsof emits a row
	// per FD, so this is three rows for two clients.
	table := "COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF   NODE NAME\n" +
		"herdr     26932 ingo    3w   REG   1,18      372 136170 " + logPath + "\n" +
		"herdr     26932 ingo    4w   REG   1,18      372 136170 " + logPath + "\n" +
		"herdr     26940 ingo    5u   REG   1,18      372 136170 " + logPath
	countingLsof(t, logPath, table)

	pids, probed := herdrClientPIDs(socketPath)
	if !probed {
		t.Fatal("the stub exits 0, so the probe ran")
	}
	if want := []int{26940, 26932}; !slices.Equal(pids, want) {
		t.Fatalf("got %v, want one entry per client, newest attach first %v — the cap counts candidates, so a duplicate consumes a slot", pids, want)
	}
}

// TestHerdrClientCachePut_NonAnswerDoesNotDisplaceALiveAnswer locks the one
// hazard memoizing non-answers introduces. Before #1514 a non-answer was never
// written, so it could not overwrite anything; now two goroutines can miss the
// memo for one socket and probe concurrently (refreshHerdrHosts reads outside
// assignMu on the sweep goroutine, PID discovery reaches captureLauncher on its
// own), and the non-answer is systematically the slower of the two to produce.
// Last-writer-wins would let the one carrying no information erase a resolved
// host AND restamp the entry fresh.
//
// The second case is the vacuity guard: a put that simply refused every
// overwrite would satisfy the first and freeze the memo forever.
func TestHerdrClientCachePut_NonAnswerDoesNotDisplaceALiveAnswer(t *testing.T) {

	t.Run("non-answer loses to a live answer", func(t *testing.T) {
		socketPath := newHerdrSessionDir(t)
		want := &session.Launcher{TermProgram: "iTerm.app"}
		t.Cleanup(func() { forgetHerdrClientMemo(socketPath) })

		herdrClientCachePut(socketPath, want, true)
		herdrClientCachePut(socketPath, nil, false) // the slow loser lands second

		got, probed := herdrClientLauncher(socketPath)
		if !probed || got != want {
			t.Errorf("got (%+v, %v), want the resolved host to survive — a probe that determined nothing erased one that did", got, probed)
		}
	})

	t.Run("answer still wins over a non-answer", func(t *testing.T) {
		socketPath := newHerdrSessionDir(t)
		want := &session.Launcher{TermProgram: "iTerm.app"}
		t.Cleanup(func() { forgetHerdrClientMemo(socketPath) })

		herdrClientCachePut(socketPath, nil, false)
		herdrClientCachePut(socketPath, want, true)

		got, probed := herdrClientLauncher(socketPath)
		if !probed || got != want {
			t.Errorf("got (%+v, %v), want the answer to replace the non-answer — re-probing is pointless otherwise", got, probed)
		}
	})
}

// TestHerdrClientCache_ExpiresANonAnswer is the load-bearing half of #1514's
// argument, and it was missing: deleting herdrClientCacheLive's expiry check
// entirely left every other test in the package green (measured).
//
// The case for reversing #1485 is that a memoized non-answer costs only a
// DEFERRED recovery. That sentence is true only because the entry expires. An
// unknown that never expired would pin every pane of a herdr server to "host
// unknown" for the life of the daemon — strictly worse than the per-pane
// re-probe #1514 removes, and arrived at through the fix for it.
//
// Backdating the stamp stands in for five real seconds; asserting the SCAN
// COUNT rather than the return value is what makes it discriminating, since a
// non-answer reads identically whether it was re-probed or served stale.
func TestHerdrClientCache_ExpiresANonAnswer(t *testing.T) {
	socketPath, scans := unreadableClientSocket(t)

	if _, probed := herdrClientLauncher(socketPath); probed {
		t.Fatal("want a non-answer for a socket whose only candidate is unreadable")
	}
	if got := scans(); got != 1 {
		t.Fatalf("first read cost %d scans, want 1", got)
	}

	herdrClientCacheMu.Lock()
	entry := herdrClientCache[socketPath]
	entry.at = time.Now().Add(-herdrClientCacheTTL - time.Second)
	herdrClientCache[socketPath] = entry
	herdrClientCacheMu.Unlock()

	if _, probed := herdrClientLauncher(socketPath); probed {
		t.Fatal("the re-probe finds the same unreadable candidate")
	}
	if got := scans(); got != 2 {
		t.Errorf("an expired non-answer was served from the memo (%d scans, want 2): a cached unknown that never lapses pins every pane on this socket forever", got)
	}
}

// --- plutil non-answers in the bundle-id walk (#1524) ------------------------

// stalledBundleIDCmd is a bundleIDCmd whose child never answers: `sleep` runs
// until bundleIDVia's own ceiling SIGKILLs it. It is the closest a test can get
// to the condition #1524 describes — a plutil that blows its 2s ceiling on a
// loaded machine — and it reaches it through the REAL exec, the REAL ceiling
// and the REAL error classification rather than a fabricated error value.
//
// /bin/sleep deliberately, not a script this test writes: the first exec of a
// newly written file is evaluated for code signing (measured at 2.14s worst-of
// -12 under load), which is itself past the ceiling — a test that planted its
// own binary could pass while proving nothing about plutil.
func stalledBundleIDCmd(ctx context.Context, _ string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sleep", "30")
}

// plistWithoutBundleID writes a real, well-formed Info.plist that simply has no
// CFBundleIdentifier key, and returns the ".app" path wrapping it. plutil exits
// NON-ZERO on it — the same exit status as for a file that does not exist
// (measured) — which is why bundleIDVia cannot key on the exit status alone.
func plistWithoutBundleID(t *testing.T) string {
	t.Helper()
	appPath := filepath.Join(t.TempDir(), "NoID.app")
	if err := os.MkdirAll(filepath.Join(appPath, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>CFBundleName</key><string>NoID</string></dict></plist>
`
	if err := os.WriteFile(filepath.Join(appPath, "Contents", "Info.plist"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return appPath
}

// TestBundleIDVia_AnsweredMissIsNotAnUnanswerableProbe is the primitive #1524
// turns on, and it is the whole reason bundleIDForAppPath grew an error return:
// before it, every one of these rows collapsed to "".
//
// Rows 1-3 are LOCKS on the pre-#1524 behaviour — an answer, however
// unhelpful, must keep reporting a nil error so the walk carries on past it.
// Row 3 is the one that stops the fix being spelled "any error aborts": a real
// app bundle whose plist genuinely carries no CFBundleIdentifier exits 1
// exactly like a missing file, and aborting the walk there would fail the #784
// admission gate OPEN for any unusual bundle in the chain.
//
// Rows 4-5 are the new behaviour: a child that never ran to a normal exit
// answered nothing, and says so.
func TestBundleIDVia_AnsweredMissIsNotAnUnanswerableProbe(t *testing.T) {
	t.Parallel() // one row spends the real 2s ceiling; overlap it with the siblings that do too
	missingBinary := func(ctx context.Context, plist string) *exec.Cmd {
		return exec.CommandContext(ctx, "/usr/bin/no-such-plutil-1524", plist)
	}
	// via adapts a bundleIDCmd into the bundleIDProbe shape bundleIDForAppPath
	// already has, so every row names the function it exercises instead of
	// encoding it as an absent value.
	via := func(c bundleIDCmd) bundleIDProbe {
		return func(appPath string) (string, error) { return bundleIDVia(appPath, c) }
	}
	tests := []struct {
		name    string
		appPath string
		probe   bundleIDProbe
		wantID  string
		wantErr bool
		wantWhy string
	}{
		{
			// bundleIDForAppPath, not via(plutilBundleIDCmd): this row runs the
			// PRODUCTION entry point, so the wiring from it down to plutil is
			// pinned rather than bypassed — reaching plutil through bundleIDVia
			// here would leave a rewiring of bundleIDForAppPath itself green
			// (found in review of #1524).
			"a real bundle answers with its id — the vacuity guard, through the production entry point",
			"/System/Library/CoreServices/Finder.app", bundleIDForAppPath,
			"com.apple.finder", false,
			"if this row ever fails the others prove nothing: they would all be passing on a probe that never works",
		},
		{
			"LOCK: a bundle that is not there is an ANSWER",
			filepath.Join(t.TempDir(), "Gone.app"), via(plutilBundleIDCmd),
			"", false,
			"plutil ran and said the file does not exist — a verdict, and the walk must carry on past it",
		},
		{
			"LOCK: a real plist with no CFBundleIdentifier is an ANSWER",
			plistWithoutBundleID(t), via(plutilBundleIDCmd),
			"", false,
			"same exit status as row 2; keying on the exit status would abort here and widen #784",
		},
		{
			"a child killed by the ceiling answered NOTHING",
			"/System/Library/CoreServices/Finder.app", via(stalledBundleIDCmd),
			"", true,
			"this is #1524: indistinguishable from row 2/3 before the error return existed",
		},
		{
			"a child that never started answered NOTHING",
			"/System/Library/CoreServices/Finder.app", via(missingBinary),
			"", true,
			"a fork that fails under the same load the ceiling fires under is equally no evidence",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := tc.probe(tc.appPath)
			if id != tc.wantID {
				t.Errorf("id = %q, want %q — %s", id, tc.wantID, tc.wantWhy)
			}
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("err = %v, want error:%v — %s", err, tc.wantErr, tc.wantWhy)
			}
		})
	}
}

// TestBundleIDVia_CeilingActuallyFires is the vacuity guard on the row above
// that costs the most to get wrong. A ceiling that fired BEFORE the child was
// started would classify identically (the error is a context error, not an
// ExitError, so it is still "no answer") while exercising a different branch —
// and the branch it would skip is the one #1524 is actually about, where the
// error IS an *exec.ExitError and errors.Is(err, context.DeadlineExceeded) is
// FALSE.
func TestBundleIDVia_CeilingActuallyFires(t *testing.T) {
	t.Parallel() // spends the real 2s ceiling and shares no state
	start := time.Now()
	if _, err := bundleIDVia("/System/Library/CoreServices/Finder.app", stalledBundleIDCmd); err == nil {
		t.Fatal("a child that never answers must report an error")
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("bundleIDVia returned after %v — too fast to have started the child and killed it; "+
			"the row above is passing on the pre-start branch, not on the mid-run kill #1524 is about", elapsed)
	}
}

// obsidianChain is the #1524 scenario as a synthetic ancestry: an antigravity
// CLI inside an Obsidian community terminal plugin. The two intermediate links
// are what make it the real shape rather than a one-hop fixture — the shell and
// the Electron renderer helper both have to be walked PAST (topLevelAppPath
// rejects the helper for being nested in Contents/Frameworks) before the walk
// reaches Obsidian.app and makes its one plutil call.
func obsidianChain() procInfoProbe {
	links := map[int]struct {
		ppid int
		cmd  string
	}{
		100: {200, "/opt/homebrew/bin/agy"},
		200: {300, "/bin/zsh"},
		300: {400, "/Applications/Obsidian.app/Contents/Frameworks/Obsidian Helper (Renderer).app/Contents/MacOS/Obsidian Helper (Renderer)"},
		400: {1, "/Applications/Obsidian.app/Contents/MacOS/Obsidian"},
	}
	return func(pid int) (int, string, error) {
		l, ok := links[pid]
		if !ok {
			return 0, "", fmt.Errorf("no process info for pid %d", pid)
		}
		return l.ppid, l.cmd, nil
	}
}

// answering returns a bundleIDProbe that answers id for every app path, and
// unanswerable returns one that never answers. The pair is the whole #1524
// distinction in two lines.
func answering(id string) bundleIDProbe {
	return func(string) (string, error) { return id, nil }
}

func unanswerable() bundleIDProbe {
	return func(string) (string, error) { return "", fmt.Errorf("plutil: signal: killed") }
}

// TestResolveHostBundleIDVia_UnanswerableBundleProbeIsNotAMiss is #1524's
// defect test at the walk. Every row walks the SAME four-link Obsidian chain to
// the same ancestor and differs only in what the bundle-id probe does there, so
// the probe's answer is the only variable.
//
// Row 3 is the load-bearing one and row 2 is what stops it being spelled
// "return false": an app the probe ANSWERED about but could not name is a
// completed miss (#784 keeps rejecting it), while an app it never answered
// about at all is no evidence and must abort the walk.
func TestResolveHostBundleIDVia_UnanswerableBundleProbeIsNotAMiss(t *testing.T) {
	// verdict is the walk's whole return, compared as one value: the three
	// fields are a single answer, and reading them apart invites a test that
	// checks two of the three.
	type verdict struct {
		bundleID string
		hostPID  int
		complete bool
	}
	tests := []struct {
		name  string
		probe bundleIDProbe
		want  verdict
	}{
		{
			"the probe answers with Obsidian's id — the vacuity guard",
			answering("md.obsidian"), verdict{"md.obsidian", 400, true},
		},
		{
			"LOCK: the probe answers that this app has no id it can name — a completed miss",
			answering(""), verdict{"", 0, true},
		},
		{
			"the probe never answered — no evidence, so the walk did NOT complete",
			unanswerable(), verdict{"", 0, false},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, hostPID, complete := resolveHostBundleIDVia(100, obsidianChain(), tc.probe)
			if got := (verdict{id, hostPID, complete}); got != tc.want {
				t.Errorf("resolveHostBundleIDVia = %+v; want %+v", got, tc.want)
			}
		})
	}
}

// TestIsKnownInteractiveHost_PlutilNonAnswerAdmits carries the walk's verdict up
// to the gate that acts on it — the admission decision itself, which is where
// #1524 costs a user a session.
//
// Walk 1 completes and finds nothing, which is the true answer for this chain:
// Obsidian is not in termProgramByAppName (that is exactly why
// knownEmbeddedHostBundleIDs exists). So walk 2 runs, and its verdict decides.
//
// Row 3 is the #784 LOCK in the same shape as the defect: an app walk 2
// answered about and which is not allow-listed must still be declined. Without
// it "fail open on a non-answer" and "fail open always" look identical here.
func TestIsKnownInteractiveHost_PlutilNonAnswerAdmits(t *testing.T) {
	walk1Finished := walk("", finished)
	walk2 := func(probe bundleIDProbe) ancestryWalk {
		return func(pid int) (string, int, bool) {
			return resolveHostBundleIDVia(pid, obsidianChain(), probe)
		}
	}
	tests := []struct {
		name  string
		probe bundleIDProbe
		want  bool
		why   string
	}{
		{
			"the probe never answered about Obsidian.app",
			unanswerable(), true,
			"#1524: a probe that could not be asked is not evidence of a non-interactive host, and the rejection is cached in hostGateRejected forever",
		},
		{
			"the probe answered md.obsidian — the vacuity guard",
			answering("md.obsidian"), true,
			"the allow-listed embedded host must be admitted on a real answer too, or row 1 proves only that everything is admitted",
		},
		{
			"LOCK: the probe answered com.steipete.codexbar",
			answering("com.steipete.codexbar"), false,
			"#784: a walk that RAN and found a non-allow-listed app is a real verdict and must still exclude",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isKnownInteractiveHostVia(100, walk1Finished, walk2(tc.probe)); got != tc.want {
				t.Errorf("isKnownInteractiveHostVia = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestIsKnownInteractiveHost_PlutilCeilingAdmitsEndToEnd is the same admission
// decision with nothing faked below the process table: the real bundleIDVia,
// the real 2s ceiling, the real exec and the real error classification, driven
// only by a child that does not answer. It is the row that proves the four
// layers agree — a fabricated probe error cannot show that bundleIDVia
// classifies a REAL kill the way resolveHostBundleIDVia expects.
func TestIsKnownInteractiveHost_PlutilCeilingAdmitsEndToEnd(t *testing.T) {
	t.Parallel() // spends the real 2s ceiling and shares no state
	stalled := func(appPath string) (string, error) {
		return bundleIDVia(appPath, stalledBundleIDCmd)
	}
	walk1Finished := walk("", finished)
	walk2 := func(pid int) (string, int, bool) {
		return resolveHostBundleIDVia(pid, obsidianChain(), stalled)
	}
	if !isKnownInteractiveHostVia(100, walk1Finished, walk2) {
		t.Error("a plutil that blew its own ceiling declined a legitimate Obsidian-hosted session (#1524)")
	}
}
