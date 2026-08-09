package processlifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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
	if l := ReadLauncherEnv(0); l != nil {
		t.Errorf("pid 0: want nil, got %+v", l)
	}
	if l := ReadLauncherEnv(-1); l != nil {
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
	l := ReadLauncherEnv(pid)
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
	l := ReadLauncherEnv(pid)
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
	l := ReadLauncherEnv(pid)
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
	l := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher")
	}
	if l.TmuxSocket != "/private/tmp/tmux-501/default" {
		t.Errorf("TmuxSocket: got %q", l.TmuxSocket)
	}
	if l.TmuxPane != "%17" {
		t.Errorf("TmuxPane: got %q", l.TmuxPane)
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
	l := ReadLauncherEnv(pid)
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
	l := ReadLauncherEnv(pid)
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
	ancestry := resolveTermProgramFromAncestry(pid)
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
	l := ReadLauncherEnv(pid)
	if l == nil {
		t.Fatal("expected non-nil launcher when TERMINAL_EMULATOR=JetBrains-JediTerm")
	}
	if l.TermProgram != "jetbrains" {
		t.Errorf("TermProgram: expected implicit 'jetbrains', got %q", l.TermProgram)
	}
}

// --- herdr click-to-focus (#1350) -------------------------------------------

// newHerdrSessionDir returns a stand-in for a herdr session directory: the
// socket path the pane's $HERDR_SOCKET_PATH points at, and the client log
// that sits beside it. Mirrors the real layout, which is identical for the
// default session (<config>/herdr.sock) and a named one
// (<config>/sessions/<name>/herdr.sock) — verified against herdr 0.8.0.
func newHerdrSessionDir(t *testing.T) (socketPath, clientLog string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "herdr.sock"), filepath.Join(dir, "herdr-client.log")
}

// spawnHerdrClient starts a sleeper that stands in for an attached herdr
// client: it holds clientLog open for writing and carries the host terminal's
// identity in its own env. Waits until the open fd is actually visible to the
// discovery helper so the caller isn't racing the child's OpenFile.
func spawnHerdrClient(t *testing.T, socketPath, clientLog string, env ...string) int {
	t.Helper()
	pid := spawnSleeperWithEnv(t, append([]string{
		"PATH=/usr/bin:/bin",
		"GO_WANT_LAUNCHER_HELPER_HOLD=" + clientLog,
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
	t.Fatalf("client pid %d never became visible as a writer of %s", pid, clientLog)
	return 0
}

// TestReadLauncherEnv_Herdr_ResolvesHostFromAttachedClient pins the defect in
// #1350: a herdr pane reached the macOS app with no term_program and no
// host_bundle_id, so resolveActivator returned nil and a click did nothing.
// The window belongs to the attached *client*, so the host identity has to be
// read from that process — one indirection past the pane's own env, which
// describes the (detached, reparented) server and is deliberately suppressed.
func TestReadLauncherEnv_Herdr_ResolvesHostFromAttachedClient(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("herdr client resolution is darwin-only (lsof + ancestry)")
	}
	socketPath, clientLog := newHerdrSessionDir(t)
	spawnHerdrClient(t, socketPath, clientLog,
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

// TestReadLauncherEnv_Herdr_DetachedSessionResolvesNoHost is the honest-failure
// half: a herdr session with no attached client has no window anywhere, so the
// launcher must stay herdr-only rather than guess. Verified live against herdr
// 0.8.0 — a detached session's client log has no writer at all.
func TestReadLauncherEnv_Herdr_DetachedSessionResolvesNoHost(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip()
	}
	socketPath, clientLog := newHerdrSessionDir(t)
	// The log exists (the session ran once) but nobody holds it open.
	if err := os.WriteFile(clientLog, []byte("detached\n"), 0o600); err != nil {
		t.Fatalf("seed client log: %v", err)
	}
	agentPID := spawnSleeperWithEnv(t, []string{
		"PATH=/usr/bin:/bin",
		"HERDR_PANE_ID=w1:p1",
		"HERDR_SOCKET_PATH=" + socketPath,
		"TERM_PROGRAM=kitty",
		"KITTY_WINDOW_ID=42",
	})
	l := ReadLauncherEnv(agentPID)
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

// TestHerdrClientPIDs_MultipleClientsAreOrderedNewestFirst covers open question
// 3 of #1350. herdr supports attaching from more than one place at once;
// verified live (two clients on one session → two writers on the client log).
// The newest attach is the window the user most recently chose, so the
// discovery helper reports descending PID and the resolver takes the first
// candidate that yields a host.
func TestHerdrClientPIDs_MultipleClientsAreOrderedNewestFirst(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("herdr client resolution is darwin-only (lsof + ancestry)")
	}
	socketPath, clientLog := newHerdrSessionDir(t)
	first := spawnHerdrClient(t, socketPath, clientLog)
	second := spawnHerdrClient(t, socketPath, clientLog)

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

// TestParseHerdrClientWriters covers the FD-column rule: "holds the client log
// open" is not the predicate, "holds it open for writing" is. A read-only
// holder — a developer running `tail -f` on the log to debug herdr — is not a
// client, and adopting its terminal as the herdr host would be the #1348
// misroute with a new cause. The reader is deliberately given the higher PID
// here, because the caller sorts descending and would otherwise prefer it.
func TestParseHerdrClientWriters(t *testing.T) {
	const out = `COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF   NODE NAME
herdr     26932 ingo    3w   REG   1,18      372 136170 /cfg/herdr/herdr-client.log
tail      99999 ingo    3r   REG   1,18      372 136170 /cfg/herdr/herdr-client.log
herdr     26940 ingo    9u   REG   1,18      372 136170 /cfg/herdr/herdr-client.log
irrlichd    777 ingo    4w   REG   1,18      372 136170 /cfg/herdr/herdr-client.log`

	got := parseHerdrClientWriters(out, 777)
	want := map[int]bool{26932: true, 26940: true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want exactly the writers %v", got, want)
	}
	for _, pid := range got {
		if !want[pid] {
			t.Errorf("pid %d must not be reported: readers are not clients, and the daemon is not its own client", pid)
		}
	}
}

// TestParseHerdrClientWriters_NoHolders is the detached case as lsof reports
// it — header only, or nothing at all.
func TestParseHerdrClientWriters_NoHolders(t *testing.T) {
	for name, out := range map[string]string{
		"empty":       "",
		"header only": "COMMAND     PID USER   FD   TYPE DEVICE SIZE/OFF   NODE NAME",
	} {
		if got := parseHerdrClientWriters(out, 1); len(got) != 0 {
			t.Errorf("%s: want no clients, got %v", name, got)
		}
	}
}
