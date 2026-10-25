package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
)

// matchesArgv answers what Agent().Process.Match would for argv: a
// CommandPattern is matched against the space-joined command line, as
// processlifecycle's Linux observer and macOS `pgrep -f` match it; an
// ExactName against the executable's base name, which is what `pgrep -x`
// compares.
func matchesArgv(t *testing.T, argv []string) bool {
	t.Helper()
	switch m := Agent().Process.Match.(type) {
	case agent.CommandPattern:
		return m.Regex.MatchString(strings.Join(argv, " "))
	case agent.ExactName:
		return filepath.Base(argv[0]) == m.Name
	default:
		t.Fatalf("unexpected matcher %T", m)
		return false
	}
}

// TestProcessMatch_ClientArgvShapes is #2106's red-first matcher table. A
// client is a matched process the service predicate does not claim. Measured
// shapes (#2104 live verification): the 2.x TUI is comm `opencode` through
// the Homebrew/shim binary and `opencode2` through the bin of the same name;
// the npm package's binary is `opencode.exe`, which its bin/opencode and
// bin/opencode2 both symlink to (`ls -la ~/.local/opencode-v2/bin` on the
// dev host); the service is `<exe> serve --service`.
func TestProcessMatch_ClientArgvShapes(t *testing.T) {
	sessionHost := Agent().Process.SessionHostArgv
	for _, c := range []struct {
		argv   []string
		client bool
	}{
		{[]string{"opencode"}, true},
		{[]string{"/opt/homebrew/bin/opencode"}, true},
		{[]string{"/x/opencode2", "--prompt", "Reply with PONG."}, true},
		{[]string{"/x/node_modules/@opencode/cli/bin/opencode.exe"}, true},
		{[]string{"/x/opencode", "run", "hello"}, true},
		{[]string{"/x/opencode", "serve"}, true}, // 1.x's headless server is unchanged

		{[]string{"/x/opencode", "serve", "--service"}, false},
		{[]string{"/x/opencode.exe", "serve", "--service"}, false},
		{[]string{"opencode-ai"}, false},
		{[]string{"/x/opencode-helper"}, false},
		{[]string{"/x/opencode2x"}, false},
		{[]string{"/x/irrlichd", "--watch", "/Users/x/.local/share/opencode/opencode.db"}, false},
		{[]string{"vim", "opencode"}, false},
		// An opencode path as an ARGUMENT is not an opencode process — seen
		// live as the tui-detach.py driver and its shell wrapper (#2106).
		{[]string{"python3", "/x/tui-detach.py", "/x/shim/opencode"}, false},
		{[]string{"sh", "-c", "sleep 20; echo /x/shim/opencode"}, false},
	} {
		got := matchesArgv(t, c.argv) && (sessionHost == nil || !sessionHost(c.argv))
		if got != c.client {
			t.Errorf("client(%q) = %t, want %t", c.argv, got, c.client)
		}
	}
}

// TestDiscoverPID_FindsAnOpencode2ClientOnThisHost runs the real process
// observer — `pgrep -f` on macOS, /proc on Linux — against a live process
// whose argv[0] is `…/opencode2`: the system sleep behind a symlink of that
// name (a symlink, so macOS code signing still holds; junie's fakeJunieProcess
// does the same). A table of strings cannot catch a pattern the host's matcher
// reads differently from Go's regexp: macOS pgrep's ERE has no `\s` —
// `pgrep -lf '(^|/)zsh(\s|$)'` printed nothing on this host while
// `pgrep -lf '(^|/)zsh( |$)'` listed three zsh processes.
func TestDiscoverPID_FindsAnOpencode2ClientOnThisHost(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "opencode2")
	if err := os.Symlink("/bin/sleep", bin); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	cmd := exec.Command(bin, "60")
	cmd.Dir = project
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	want := cmd.Process.Pid

	// Poll to a deadline: on Linux /proc/<pid>/cmdline reads back empty until
	// execve completes (see junie's fakeJunieProcess).
	start := time.Now()
	for {
		got, err := DiscoverPID(project, "", nil)
		if err == nil && got == want {
			return
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("DiscoverPID(%s) = (%d, %v) after %s, want the opencode2 process %d",
				project, got, err, elapsed, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
