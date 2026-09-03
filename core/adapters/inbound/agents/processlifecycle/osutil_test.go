package processlifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// TestHelperProcess is a sleeper used by the subprocess env-capture tests.
// It runs only when GO_WANT_LAUNCHER_HELPER=1 is set, and exits after a
// short sleep. We self-exec the test binary (which is unsigned on darwin,
// unlike /bin/sleep) so `sysctl kern.procargs2` can read its env — macOS
// strips env from sysctl responses for Apple-signed binaries.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_LAUNCHER_HELPER") != "1" {
		return
	}
	// GO_WANT_LAUNCHER_HELPER_HOLD makes the sleeper stand in for an attached
	// herdr client: it holds the named file open for writing, which is the
	// signal herdrClientPIDs matches on. Held for longer than the plain
	// sleeper because the herdr tests poll for the open fd to become visible
	// to lsof; every caller kills it via t.Cleanup, so the longer sleep costs
	// nothing.
	if hold := os.Getenv("GO_WANT_LAUNCHER_HELPER_HOLD"); hold != "" {
		f, err := os.OpenFile(hold, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(1)
		}
		defer f.Close()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	time.Sleep(3 * time.Second)
	os.Exit(0)
}

func TestParseProcargs2(t *testing.T) {
	// Build a synthetic KERN_PROCARGS2 buffer:
	//   argc (int32 LE) | exec_path\0 | argv[0]\0 argv[1]\0 | envp[0]\0 envp[1]\0 envp[2]\0
	makeBuf := func(argc int32, execPath string, argv []string, envp []string) []byte {
		var b []byte
		b = append(b, byte(argc), byte(argc>>8), byte(argc>>16), byte(argc>>24))
		b = append(b, []byte(execPath)...)
		b = append(b, 0)
		// real kernel adds alignment \0 padding after exec path — we omit it; the parser handles either.
		for _, a := range argv {
			b = append(b, []byte(a)...)
			b = append(b, 0)
		}
		for _, e := range envp {
			b = append(b, []byte(e)...)
			b = append(b, 0)
		}
		return b
	}

	tests := []struct {
		name string
		buf  []byte
		want map[string]string
	}{
		{
			name: "empty buffer",
			buf:  nil,
			want: map[string]string{},
		},
		{
			name: "whitelisted only, everything else dropped",
			buf: makeBuf(2, "/usr/local/bin/claude",
				[]string{"/usr/local/bin/claude", "--mode"},
				[]string{
					"HOME=/Users/alice",
					"PATH=/usr/bin",
					"TERM_PROGRAM=iTerm.app",
					"ITERM_SESSION_ID=w0t0p0",
					"SHELL=/bin/zsh",
				}),
			want: map[string]string{
				"TERM_PROGRAM":     "iTerm.app",
				"ITERM_SESSION_ID": "w0t0p0",
			},
		},
		{
			name: "tmux pair",
			buf: makeBuf(1, "/bin/claude",
				[]string{"/bin/claude"},
				[]string{
					"TMUX=/private/tmp/tmux-501/default,1234,0",
					"TMUX_PANE=%17",
				}),
			want: map[string]string{
				"TMUX":      "/private/tmp/tmux-501/default,1234,0",
				"TMUX_PANE": "%17",
			},
		},
		{
			name: "vscode fields",
			buf: makeBuf(1, "/bin/claude",
				[]string{"/bin/claude"},
				[]string{
					"TERM_PROGRAM=vscode",
					"VSCODE_PID=9876",
					"VSCODE_INJECTION=1",
				}),
			want: map[string]string{
				"TERM_PROGRAM": "vscode",
				"VSCODE_PID":   "9876",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProcargs2(tc.buf)
			if len(got) != len(tc.want) {
				t.Fatalf("parseProcargs2: want %v, got %v", tc.want, got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: want %q, got %q", k, v, got[k])
				}
			}
		})
	}
}

func TestParseProcargs2Argv(t *testing.T) {
	// Same KERN_PROCARGS2 layout as TestParseProcargs2:
	//   argc (int32 LE) | exec_path\0 | argv...\0 | envp...\0
	makeBuf := func(argc int32, execPath string, argv []string, envp []string) []byte {
		var b []byte
		b = append(b, byte(argc), byte(argc>>8), byte(argc>>16), byte(argc>>24))
		b = append(b, []byte(execPath)...)
		b = append(b, 0)
		for _, a := range argv {
			b = append(b, []byte(a)...)
			b = append(b, 0)
		}
		for _, e := range envp {
			b = append(b, []byte(e)...)
			b = append(b, 0)
		}
		return b
	}

	tests := []struct {
		name string
		buf  []byte
		want []string
	}{
		{name: "empty buffer", buf: nil, want: nil},
		{
			name: "argv extracted, env ignored",
			buf: makeBuf(3, "/usr/local/bin/claude",
				[]string{"claude", "--resume", "abc-123"},
				[]string{"HOME=/Users/alice", "PATH=/usr/bin"}),
			want: []string{"claude", "--resume", "abc-123"},
		},
		{
			name: "cc-daemon argv with no env",
			buf: makeBuf(3, "/Applications/ClaudeCode.app/Contents/MacOS/claude",
				[]string{"claude", "daemon", "run"}, nil),
			want: []string{"claude", "daemon", "run"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseProcargs2Argv(tc.buf)
			if len(got) != len(tc.want) {
				t.Fatalf("parseProcargs2Argv: want %v, got %v", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("argv[%d]: want %q, got %q", i, tc.want[i], got[i])
				}
			}
		})
	}
}

func TestReadLauncherEnv_InvalidPID(t *testing.T) {
	if l, _ := ReadLauncherEnv(0); l != nil {
		t.Errorf("pid 0: want nil, got %+v", l)
	}
	if l, _ := ReadLauncherEnv(-1); l != nil {
		t.Errorf("pid -1: want nil, got %+v", l)
	}
}

// spawnSleeperWithEnv starts a short-lived test-binary subprocess (via
// TestHelperProcess) with the given env and returns its PID. We self-exec
// the test binary rather than running /bin/sleep because on macOS sysctl
// kern.procargs2 strips env from Apple-signed binaries; the Go-built test
// binary is unsigned so env is readable.
func spawnSleeperWithEnv(t *testing.T, env []string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
	cmd.Env = append([]string{"GO_WANT_LAUNCHER_HELPER=1"}, env...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	// Give the kernel a beat to publish the exec'd env to sysctl / /proc.
	time.Sleep(50 * time.Millisecond)
	return cmd.Process.Pid
}

// TestReadLauncherEnv_Subprocess exercises the real ps / /proc path against
// a child process with a known controlled env.
func TestReadLauncherEnv_Subprocess(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("no env reader for %s", runtime.GOOS)
	}
	pid := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=iTerm.app",
		"ITERM_SESSION_ID=w0t0p0-test",
	})
	l, _ := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	if l.TermProgram != "iTerm.app" {
		t.Errorf("TermProgram: want iTerm.app, got %q", l.TermProgram)
	}
	if l.ITermSessionID != "w0t0p0-test" {
		t.Errorf("ITermSessionID: want w0t0p0-test, got %q", l.ITermSessionID)
	}
}

func TestReadLauncherEnv_Subprocess_VSCodePIDImpliesTermProgram(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	pid := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"VSCODE_PID=4242",
	})
	l, _ := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher when VSCODE_PID present")
	}
	if l.VSCodePID != 4242 {
		t.Errorf("VSCodePID: want 4242, got %d", l.VSCodePID)
	}
	if l.TermProgram != "vscode" {
		t.Errorf("TermProgram: expected implicit 'vscode', got %q", l.TermProgram)
	}
}

func TestReadLauncherEnv_Subprocess_NoRelevantEnv(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	pid := spawnSleeperWithEnv(t, []string{"PATH=/usr/bin:/bin"})
	// With the ancestry fallback, an empty relevant env no longer guarantees
	// nil — if the test process's host terminal/IDE is on the recognized
	// list, we'll populate TermProgram from ppid walk. The invariant that
	// still holds: env-derived fields must be empty (we only set PATH).
	l, _ := ReadLauncherEnv(pid)
	if l == nil {
		return // legitimate on unknown hosts
	}
	if l.ITermSessionID != "" || l.TermSessionID != "" || l.TmuxPane != "" || l.VSCodePID != 0 {
		t.Errorf("expected only ancestry-derived TermProgram, got %+v", l)
	}
}

func TestReadLauncherEnv_Subprocess_Tmux(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	pid := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=tmux",
		"TMUX=/private/tmp/tmux-501/default,1234,0",
		"TMUX_PANE=%17",
	})
	l, _ := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	assertPaneAddressFollowsProvenance(t, l, "%17", "/private/tmp/tmux-501/default")
}

// assertPaneAddressFollowsProvenance asserts #1582's rule against a launcher
// read through the real sysctl / /proc path: the tmux address survives exactly
// when nothing claimed a host for the process, and is dropped otherwise.
//
// A biconditional rather than a fixed expectation, because the fixture the two
// callers use cannot model a pane faithfully on every platform.
// spawnSleeperWithEnv's helper is a CHILD of the test binary, so on darwin its
// ancestry walk climbs `go test` → the shell → the terminal and resolves a host
// (measured while #1582 was written: TermProgram=vscode) — which is precisely
// the descendant shape dropInheritedTmuxPane refuses to record an address for.
// On linux the ancestry helpers are stubs, nothing claims a host, and the
// address survives. Pinning either single outcome would pin the RIG rather than
// the rule, and would fail on one platform for a reason unrelated to the code.
//
// The fixture that IS faithful is spawnPaneSleeperWithEnv, which reparents the
// helper to init exactly as tmux's server is; the cases in
// tmuxclient_darwin_test.go use it and do assert the address outright.
func assertPaneAddressFollowsProvenance(t *testing.T, l *session.Launcher, pane, socket string) {
	t.Helper()
	if l.TermProgram == "" && l.HostBundleID == "" {
		if l.TmuxPane != pane || l.TmuxSocket != socket {
			t.Errorf("nothing claimed a host, so this is a pane and keeps its address: %+v", l)
		}
		return
	}
	if l.TmuxPane != "" || l.TmuxSocket != "" {
		t.Errorf("a host was claimed (%q/%q), so the pane address is inherited and must not be recorded (#1582): %+v",
			l.TermProgram, l.HostBundleID, l)
	}
}

// herdrPaneEnv is the env a process sees inside a herdr pane whose server was
// itself started from a tmux pane in a VS Code terminal that had once been a
// kitty window. Every non-HERDR_ var here is real, inherited, and wrong: herdr's
// server is long-lived and detached (PPID 1), so it hands its own launch-time
// environment to every pane it will ever spawn. Values match a live capture from
// herdr 0.8.0 (#1348).
var herdrPaneEnv = map[string]string{
	"HERDR_PANE_ID":     "w1:p1",
	"HERDR_SOCKET_PATH": "/Users/test/.config/herdr/sessions/probe/herdr.sock",
	"TERM_PROGRAM":      "tmux",
	"TMUX":              "/tmp/irrprobe-tmux,95058,0",
	"TMUX_PANE":         "%0",
	"KITTY_LISTEN_ON":   "unix:/tmp/fake-kitty",
	"KITTY_WINDOW_ID":   "42",
	"VSCODE_PID":        "99999",
}

// TestLauncherFromEnv_HerdrSuppressesInheritedIdentity pins the defect: without
// suppression, resolveBackend sees TmuxPane/%0 on a foreign socket and injects
// input into an unrelated pane in a different window — strictly worse than
// reporting the session uncontrollable.
func TestLauncherFromEnv_HerdrSuppressesInheritedIdentity(t *testing.T) {
	l := launcherFromEnv(herdrPaneEnv)
	if l.TermProgram != "" {
		t.Errorf("TermProgram: inherited value must not survive, got %q", l.TermProgram)
	}
	if l.TmuxPane != "" || l.TmuxSocket != "" {
		t.Errorf("tmux identity must not survive, got pane=%q socket=%q", l.TmuxPane, l.TmuxSocket)
	}
	if l.KittyListenOn != "" || l.KittyWindowID != "" {
		t.Errorf("kitty identity must not survive, got listen=%q window=%q", l.KittyListenOn, l.KittyWindowID)
	}
	if l.VSCodePID != 0 {
		t.Errorf("VSCodePID: inherited value must not survive, got %d", l.VSCodePID)
	}
}

// TestLauncherFromEnv_HerdrCapture covers the other half: the pane's own
// identity, which is the only thing in that env that actually describes it.
func TestLauncherFromEnv_HerdrCapture(t *testing.T) {
	l := launcherFromEnv(herdrPaneEnv)
	if l.HerdrPaneID != "w1:p1" {
		t.Errorf("HerdrPaneID: want w1:p1, got %q", l.HerdrPaneID)
	}
	want := "/Users/test/.config/herdr/sessions/probe/herdr.sock"
	if l.HerdrSocketPath != want {
		t.Errorf("HerdrSocketPath: want %q, got %q", want, l.HerdrSocketPath)
	}
	if l.IsEmpty() {
		t.Error("a herdr-only launcher must not be reported empty")
	}
}

// TestLauncherFromEnv_HerdrSuppressionIsScoped guards the blast radius: the
// suppression must key off an actual herdr pane, never fire on a plain tmux
// session. This one is a lock — it passes before the change and must keep
// passing after.
func TestLauncherFromEnv_HerdrSuppressionIsScoped(t *testing.T) {
	l := launcherFromEnv(map[string]string{
		"TERM_PROGRAM": "tmux",
		"TMUX":         "/private/tmp/tmux-501/default,1234,0",
		"TMUX_PANE":    "%17",
	})
	if l.TmuxPane != "%17" || l.TmuxSocket != "/private/tmp/tmux-501/default" {
		t.Errorf("non-herdr tmux identity must be preserved, got %+v", l)
	}
}

// TestReadLauncherEnv_Subprocess_Herdr exercises the real sysctl / /proc read
// path end to end, mirroring the tmux subprocess test above.
func TestReadLauncherEnv_Subprocess_Herdr(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	pid := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=tmux",
		"TMUX=/tmp/irrprobe-tmux,95058,0",
		"TMUX_PANE=%0",
		"HERDR_PANE_ID=w1:p1",
		"HERDR_SOCKET_PATH=/tmp/herdr-probe.sock",
	})
	l, _ := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	if l.HerdrPaneID != "w1:p1" {
		t.Errorf("HerdrPaneID: got %q", l.HerdrPaneID)
	}
	if l.HerdrSocketPath != "/tmp/herdr-probe.sock" {
		t.Errorf("HerdrSocketPath: got %q", l.HerdrSocketPath)
	}
	if l.TmuxPane != "" || l.TmuxSocket != "" {
		t.Errorf("inherited tmux identity survived the real read path: %+v", l)
	}
	// The ancestry fallbacks must not put host identity back: the walk from a
	// herdr pane leads to the herdr server, not to the terminal the pane is
	// displayed in. (This test process is itself hosted by some terminal, so
	// without the skip TermProgram/HostBundleID would be populated here.)
	if l.TermProgram != "" || l.HostBundleID != "" {
		t.Errorf("ancestry identity must not be merged into a herdr launcher: %+v", l)
	}
	if l.KittyListenOn != "" || l.KittyWindowID != "" || l.KittyPID != 0 {
		t.Errorf("kitty backfill must not fire for a herdr pane: %+v", l)
	}
	// TTY is deliberately still captured — it describes this process's own pty.
	if l.TTY == "" {
		t.Log("TTY empty (subprocess may have no controlling terminal); not asserted")
	}
}

// tmuxPaneEnv is the env a process sees inside a tmux pane whose server was
// started from an iTerm2 window — iTerm session "w0t0p0:ALPHA-UUID" — that the
// user has since detached from. Values match a live capture on tmux 3.6a
// (#1486), and the shape is herdrPaneEnv's above for the same reason: tmux's
// server daemonizes at first use, is reparented to PID 1, outlives every
// client, and hands its own launch-time environment to every pane it will ever
// spawn.
//
// Two measured details drive the assertions below.
//
// First, TERM_PROGRAM is NOT a stale host name: tmux overwrites it with its own
// name ("tmux", alongside TERM_PROGRAM_VERSION=3.6a). That refutes #1486's
// stated hypothesis, but it is not a reprieve — "tmux" matches no entry in the
// macOS activator registry, and being non-empty it also suppresses the
// TermProgram=="" guards that would otherwise reach the ancestry fallbacks. So
// it is a value that can only mask, never resolve.
//
// Second, every *other* terminal-identity var IS handed through frozen. This
// was measured on a pane created minutes after the ALPHA terminal was gone,
// while a completely different client (Apple_Terminal, "w9t9p9:BRAVO-UUID")
// was the only one attached — so unlike #1405's herdr staleness, it is wrong
// from the very first click rather than only after a re-attach.
var tmuxPaneEnv = map[string]string{
	"TERM_PROGRAM":      "tmux",
	"TMUX":              "/private/tmp/tmux-501/default,69323,0",
	"TMUX_PANE":         "%4",
	"ITERM_SESSION_ID":  "w0t0p0:ALPHA-UUID",
	"TERM_SESSION_ID":   "ALPHA_TERMSESS",
	"KITTY_LISTEN_ON":   "unix:/tmp/kitty-ALPHA/sock",
	"KITTY_WINDOW_ID":   "77",
	"KITTY_PID":         "555",
	"VSCODE_PID":        "4242",
	"TERMINAL_EMULATOR": "JetBrains-JediTerm",
}

// TestLauncherFromEnv_TmuxSuppressesInheritedIdentity pins the defect. Each
// field here describes the terminal the tmux *server* was started in, not the
// one displaying the pane, and none of them is refreshed when the user
// re-attaches somewhere else.
func TestLauncherFromEnv_TmuxSuppressesInheritedIdentity(t *testing.T) {
	l := launcherFromEnv(tmuxPaneEnv)
	if l.TermProgram != "" {
		t.Errorf("TermProgram: tmux's own name is not a host, got %q", l.TermProgram)
	}
	if l.ITermSessionID != "" || l.TermSessionID != "" {
		t.Errorf("iTerm/Terminal.app session identity must not survive, got iterm=%q term=%q",
			l.ITermSessionID, l.TermSessionID)
	}
	if l.KittyListenOn != "" || l.KittyWindowID != "" || l.KittyPID != 0 {
		t.Errorf("kitty identity must not survive, got listen=%q window=%q pid=%d",
			l.KittyListenOn, l.KittyWindowID, l.KittyPID)
	}
	if l.VSCodePID != 0 {
		t.Errorf("VSCodePID: inherited value must not survive, got %d", l.VSCodePID)
	}
}

// TestLauncherFromEnv_TmuxCapture covers the other half: the pane's own
// address, which is the only thing in that env that actually describes it and
// which the macOS TmuxActivator selects on — and since #1593 it needs BOTH of
// them. Keeping them is what makes the suppression above a
// click-to-focus change and not a control regression.
func TestLauncherFromEnv_TmuxCapture(t *testing.T) {
	l := launcherFromEnv(tmuxPaneEnv)
	if l.TmuxPane != "%4" {
		t.Errorf("TmuxPane: want %%4, got %q", l.TmuxPane)
	}
	if l.TmuxSocket != "/private/tmp/tmux-501/default" {
		t.Errorf("TmuxSocket: got %q", l.TmuxSocket)
	}
	if l.IsEmpty() {
		t.Error("a tmux-only launcher must not be reported empty")
	}
}

// TestLauncherFromEnv_TmuxPaneSurvivesAClearedTmux is the reachability half of
// #1593, and a LOCK: it passes before that fix and after, because the shape it
// pins is an input to the router rather than a decision the router makes.
//
// $TMUX carries the socket and $TMUX_PANE the address, and they are separate
// variables. `unset TMUX` is the documented way to run tmux inside tmux, and
// some shell configs do it — which leaves a genuine pane reporting its own
// address with no server to address it against. #1582's drop does not close
// this: it clears the two fields TOGETHER, but only for an address the process
// inherited, and this process's address is its own (nothing else claimed a host
// for it), so the drop deliberately keeps it.
//
// So `{TmuxPane: "%4", TmuxSocket: ""}` reaches control.resolveBackend, which
// is why TestResolveBackend_TmuxPaneWithoutSocketIsNotAddressable is a defect
// test rather than a lock over an unreachable state.
func TestLauncherFromEnv_TmuxPaneSurvivesAClearedTmux(t *testing.T) {
	env := map[string]string{}
	for k, v := range tmuxPaneEnv {
		env[k] = v
	}
	delete(env, "TMUX")

	l := launcherFromEnv(env)
	if l.TmuxPane != "%4" {
		t.Errorf("TmuxPane: want %%4, got %q", l.TmuxPane)
	}
	if l.TmuxSocket != "" {
		t.Errorf("TmuxSocket: want empty with $TMUX cleared, got %q", l.TmuxSocket)
	}
	// And #1582's drop leaves it alone, because it is this process's own.
	dropInheritedTmuxPane(l)
	if l.TmuxPane != "%4" || l.TmuxSocket != "" {
		t.Errorf("after dropInheritedTmuxPane: pane=%q socket=%q, want %%4 and empty",
			l.TmuxPane, l.TmuxSocket)
	}
}

// TestLauncherFromEnv_TmuxSuppressionIsScoped guards the blast radius: the
// suppression must key off an actual tmux pane and leave an ordinary terminal
// session alone. A lock — it passes before the change and must keep passing
// after.
func TestLauncherFromEnv_TmuxSuppressionIsScoped(t *testing.T) {
	l := launcherFromEnv(map[string]string{
		"TERM_PROGRAM":     "iTerm.app",
		"ITERM_SESSION_ID": "w0t0p0:REAL-UUID",
		"KITTY_WINDOW_ID":  "3",
	})
	if l.TermProgram != "iTerm.app" || l.ITermSessionID != "w0t0p0:REAL-UUID" {
		t.Errorf("a non-tmux session must keep its own identity, got %+v", l)
	}
	if l.KittyWindowID != "3" {
		t.Errorf("KittyWindowID: want 3, got %q", l.KittyWindowID)
	}
}

// TestLauncherFromEnv_TmuxSuppressionSparesADescendantsOwnHost is the other
// half of the blast-radius guard, and the one that is easy to get wrong.
// $TMUX_PANE is not self-describing: it is inherited by *every* descendant of a
// pane, including a GUI terminal or IDE launched from inside one (`code .`,
// `kitty`). Such a process carries the stale pane address AND a correct,
// self-reported host identity — so keying the suppression on $TMUX_PANE alone
// throws away a host that click-to-focus could actually have used, turning
// "raises the right window" into a silent no-op.
//
// The discriminator is tmux's own marker: tmux stamps TERM_PROGRAM=tmux onto
// the panes it spawns, and a descendant that reports a different host reported
// it itself. A descendant that reports no host of its own (kitty sets no
// TERM_PROGRAM, upstream #4793) is still suppressed here, and is recovered by
// the ancestry fallbacks instead — which is why hostIdentity must NOT skip
// them for tmux. TestReadLauncherEnv_Subprocess_TmuxSkipsAncestry pins the
// genuine-pane end of that.
func TestLauncherFromEnv_TmuxSuppressionSparesADescendantsOwnHost(t *testing.T) {
	l := launcherFromEnv(map[string]string{
		"TERM_PROGRAM": "vscode",
		"VSCODE_PID":   "4242",
		// Inherited from the pane VS Code was launched from — stale, but not a
		// reason to discard VS Code's own identity below.
		"TMUX":      "/private/tmp/tmux-501/default,69323,0",
		"TMUX_PANE": "%4",
	})
	if l.TermProgram != "vscode" {
		t.Errorf("a descendant's own TERM_PROGRAM must survive, got %q", l.TermProgram)
	}
	if l.VSCodePID != 4242 {
		t.Errorf("VSCodePID: want 4242, got %d", l.VSCodePID)
	}
	// #1582: keeping that identity is only half the answer. The pane address
	// beside it belongs to the pane VS Code was launched FROM, and
	// dropInheritedTmuxPane is what stops it being stored — see the table
	// below, and TestReadLauncherEnv_Tmux_InheritedPaneKeepsItsOwnHost for the
	// same case through the real read path.
}

// TestDropInheritedTmuxPane is #1582's rule at the one place it is decided: a
// $TMUX_PANE is recorded only when it is the reading process's own, which is
// exactly when nothing else has claimed a host for that process.
//
// The end-to-end proof against a live process is
// TestReadLauncherEnv_Tmux_InheritedPaneKeepsItsOwnHost (darwin, and the case
// seen red before the fix). This table is the cross-platform half, and it is
// the only coverage of two rows that fixture cannot express: the descendant
// whose host came from the ANCESTRY walk rather than from its own env, and the
// blast radius — the drop may move those two fields and nothing else.
func TestDropInheritedTmuxPane(t *testing.T) {
	const (
		pane   = "%17"
		socket = "/private/tmp/tmux-501/default"
	)
	cases := []struct {
		name string
		in   session.Launcher
		keep bool
	}{
		// Nothing claimed a host: launcherFromEnv suppressed the inherited
		// identity and the ancestry walk terminated at the reparented tmux
		// server having found nothing. That is a genuine pane, and dropping its
		// address would cost it click-to-focus.
		{"genuine pane", session.Launcher{TmuxPane: pane, TmuxSocket: socket}, true},
		// The same pane with $TMUX cleared (`unset TMUX`, the documented way to
		// nest tmux). Still this process's own address, so still kept — which
		// is what leaves a socket-less pane reaching control.resolveBackend and
		// makes #1593 a live defect rather than a state #1582 closed off.
		{"genuine pane whose $TMUX was cleared", session.Launcher{TmuxPane: pane}, true},
		// The shape the issue was filed about: `open -a iTerm` from a pane.
		{"descendant reporting its own TERM_PROGRAM",
			session.Launcher{TermProgram: "iTerm.app", ITermSessionID: "w0t0p0-OWN", TmuxPane: pane, TmuxSocket: socket}, false},
		// `code .` from a pane. VSCODE_PID implies the host, so this is the
		// same row through launcherFromEnv's inference rather than $TERM_PROGRAM.
		{"descendant reporting a vscode host",
			session.Launcher{TermProgram: "vscode", VSCodePID: 4242, TmuxPane: pane, TmuxSocket: socket}, false},
		// The row an env-only check cannot see, and the reason this runs after
		// the ancestry walk: kitty sets no $TERM_PROGRAM (upstream kitty#4793),
		// so a kitty window launched from a pane inherits tmux's own marker and
		// is indistinguishable from a pane until the walk finds kitty.app.
		{"descendant whose host came from the ancestry walk",
			session.Launcher{HostBundleID: "net.kovidgoyal.kitty", TmuxPane: pane, TmuxSocket: socket}, false},
		// Same descendant once the kitty back-fill has run. Dropping the pane
		// is what lets control.resolveBackend reach the kitty branch, which is
		// the backend that actually addresses this session's window.
		{"descendant with its own kitty backend",
			session.Launcher{TermProgram: "kitty", KittyListenOn: "unix:/tmp/kitty/sock", KittyWindowID: "42", TmuxPane: pane, TmuxSocket: socket}, false},
		// Vacuity guards: neither row has an address to decide about, and a
		// drop that fired on them would be clearing fields it was never given.
		{"no pane address at all", session.Launcher{TermProgram: "iTerm.app", ITermSessionID: "w0t0p0-OWN"}, true},
		{"herdr pane", session.Launcher{HerdrPaneID: "w1:p1", HerdrSocketPath: "/tmp/h.sock"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in
			dropInheritedTmuxPane(&got)
			wantPane, wantSocket := "", ""
			if c.keep {
				wantPane, wantSocket = c.in.TmuxPane, c.in.TmuxSocket
			}
			if got.TmuxPane != wantPane || got.TmuxSocket != wantSocket {
				t.Errorf("pane=%q socket=%q, want pane=%q socket=%q", got.TmuxPane, got.TmuxSocket, wantPane, wantSocket)
			}
			// The address is the whole of the decision: a drop that also took
			// the host with it would turn a descendant into a session nothing
			// can focus, which is the outcome #1499 exists to prevent.
			gotRest, wantRest := got, c.in
			gotRest.TmuxPane, gotRest.TmuxSocket = "", ""
			wantRest.TmuxPane, wantRest.TmuxSocket = "", ""
			if gotRest != wantRest {
				t.Errorf("only the pane address may change: got %+v, want %+v", gotRest, wantRest)
			}
		})
	}
}

// TestReadLauncherEnv_Subprocess_TmuxSuppressesInheritedIdentity is the tmux
// twin of TestReadLauncherEnv_Subprocess_Herdr: it exercises the real sysctl /
// proc read path rather than launcherFromEnv in isolation.
//
// It deliberately does NOT assert TermProgram == "", the way the herdr twin
// does. The herdr twin can, because hostIdentity skips the ancestry fallbacks
// for a herdr pane; for tmux they run on purpose (see hostIdentity). In
// production that walk is inert — a genuine pane's chain reaches the tmux
// server, which is reparented to PID 1, and terminates there. Here it is not:
// the subprocess is a child of the test binary, which really is hosted by a
// terminal, so ancestry legitimately repopulates TermProgram. That is the same
// recovery path a kitty window launched from a pane depends on, so asserting
// its absence would pin the bug rather than the fix.
//
// What must hold either way is that nothing inherited from the tmux server's
// launch environment survives, and that tmux's own marker is never kept as if
// it were a host.
func TestReadLauncherEnv_Subprocess_TmuxSuppressesInheritedIdentity(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	pid := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=tmux",
		"TMUX=/private/tmp/tmux-501/default,69323,0",
		"TMUX_PANE=%4",
		"ITERM_SESSION_ID=w0t0p0:ALPHA-UUID",
		"TERM_SESSION_ID=ALPHA_TERMSESS",
	})
	l, _ := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	assertPaneAddressFollowsProvenance(t, l, "%4", "/private/tmp/tmux-501/default")
	if l.ITermSessionID != "" || l.TermSessionID != "" {
		t.Errorf("inherited terminal identity survived the real read path: %+v", l)
	}
	if l.TermProgram == "tmux" {
		t.Errorf("tmux's own marker must never be kept as a host: %+v", l)
	}
}

// TestReadLauncherEnv_Subprocess_KittyOverridesInheritedTermProgram covers
// the case where kitty was launched from a process whose env contained
// TERM_PROGRAM=vscode (e.g. a VS Code integrated terminal). kitty itself
// sets KITTY_WINDOW_ID but not TERM_PROGRAM, so the inherited value leaks
// through and would mis-route the focus click to VS Code. The override
// only fires when kitty actually appears in the process ancestry — this
// guards against KITTY_WINDOW_ID leaking the other direction (e.g. a kitty
// shell spawning VS Code).
//
// Test logic depends on whether kitty.app is an ancestor of `go test`:
//   - if yes: override fires, TermProgram normalized to "kitty"
//   - if no:  override skipped, TermProgram stays "vscode"
func TestReadLauncherEnv_Subprocess_KittyOverridesInheritedTermProgram(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	pid := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERM_PROGRAM=vscode",
		"KITTY_WINDOW_ID=42",
		"KITTY_PID=4242",
	})
	l, _ := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher when KITTY_WINDOW_ID present")
	}
	if l.KittyWindowID != "42" {
		t.Errorf("KittyWindowID: want 42, got %q", l.KittyWindowID)
	}
	if l.KittyPID != 4242 {
		t.Errorf("KittyPID: want 4242, got %d", l.KittyPID)
	}
	// Resolve ancestry of the spawned subprocess to decide which branch we're in.
	ancestry := resolveTermProgramFromAncestry(noAggregateBudget(), pid)
	if ancestry == "kitty" {
		if l.TermProgram != "kitty" {
			t.Errorf("kitty in ancestry: expected TermProgram override to 'kitty', got %q", l.TermProgram)
		}
	} else {
		if l.TermProgram != "vscode" {
			t.Errorf("no kitty ancestry: expected TermProgram to stay 'vscode', got %q", l.TermProgram)
		}
	}
}

func TestReadLauncherEnv_Subprocess_JetBrainsImpliesTermProgram(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	pid := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"TERMINAL_EMULATOR=JetBrains-JediTerm",
	})
	l, _ := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher when TERMINAL_EMULATOR=JetBrains-JediTerm")
	}
	if l.TermProgram != "jetbrains" {
		t.Errorf("TermProgram: expected implicit 'jetbrains', got %q", l.TermProgram)
	}
}

// --- herdr click-to-focus (#1350) -------------------------------------------

// newHerdrSessionDir returns a stand-in for a herdr session directory: the
// socket path the pane's $HERDR_SOCKET_PATH points at. The client log that
// identifies an attached client sits beside it and is derived, never spelled
// out again, so the layout has one definition (herdrClientLogPath). Mirrors the
// real thing, which is identical for the default session
// (<config>/herdr.sock) and a named one
// (<config>/sessions/<name>/herdr.sock) — verified against herdr 0.8.0.
func newHerdrSessionDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "herdr.sock")
}

// TestReadLauncherEnv_Herdr_DetachedSessionResolvesNoHost is the honest-failure
// half of #1350: a herdr session with no attached client has no window
// anywhere, so the launcher must stay herdr-only rather than guess. Verified
// live against herdr 0.8.0 — a detached session's client log has no writer.
func TestReadLauncherEnv_Herdr_DetachedSessionResolvesNoHost(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	socketPath := newHerdrSessionDir(t)
	// The log exists (the session ran once) but nobody holds it open.
	if err := os.WriteFile(herdrClientLogPath(socketPath), []byte("detached\n"), 0o600); err != nil {
		t.Fatalf("seed client log: %v", err)
	}
	agentPID := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"HERDR_PANE_ID=w1:p1",
		"HERDR_SOCKET_PATH=" + socketPath,
		"TERM_PROGRAM=kitty",
		"KITTY_WINDOW_ID=42",
	})
	l, _ := ReadLauncherEnv(agentPID)
	if l == nil {
		t.Fatal("expected non-nil launcher (the herdr address is still identity)")
	}
	if l.TermProgram != "" || l.HostBundleID != "" {
		t.Errorf("no client is attached, so no host may be reported: %+v", l)
	}
	if l.KittyWindowID != "" || l.KittyListenOn != "" {
		t.Errorf("inherited kitty identity must stay suppressed: %+v", l)
	}
	if l.HerdrPaneID != "w1:p1" {
		t.Errorf("HerdrPaneID: got %q", l.HerdrPaneID)
	}
}

// TestHerdrClientLogPath pins the addressing derivation: the client log sits
// beside the socket, so the socket path the daemon already captures is a
// complete key — no $HERDR_SESSION capture and no argv parsing needed
// (open question 1 of #1350).
func TestHerdrClientLogPath(t *testing.T) {
	cases := map[string]string{
		"/Users/t/.config/herdr/herdr.sock":                "/Users/t/.config/herdr/herdr-client.log",
		"/Users/t/.config/herdr/sessions/probe/herdr.sock": "/Users/t/.config/herdr/sessions/probe/herdr-client.log",
		"": "",
	}
	for socket, want := range cases {
		if got := herdrClientLogPath(socket); got != want {
			t.Errorf("herdrClientLogPath(%q) = %q, want %q", socket, got, want)
		}
	}
}
