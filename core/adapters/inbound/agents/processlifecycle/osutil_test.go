package processlifecycle

import (
	"os"
	"os/exec"
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
	if l := ReadLauncherEnv(pid); l != nil {
		t.Errorf("expected nil launcher, got %+v", l)
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
