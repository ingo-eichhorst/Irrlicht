package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/control"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// This is the herdr counterpart of the tmux backchannel e2e (#1348). herdr is
// the second terminal environment that automates headlessly — a Rust agent
// multiplexer whose server owns each pane's pty — so the write path
// (InputService → Controller → herdr pane send-text) and the read path
// (Reader → herdr pane read) are both driven against a REAL server here
// rather than only through the unit-level command builders.
//
// Isolation matters more here than for the tmux e2e, because herdr persists
// its layout. A default-session server restores every workspace the developer
// has ever opened and writes ~/.config/herdr/session.json back on exit, so a
// test using it would spawn a shell per saved workspace and leave a workspace
// pointing at a deleted temp dir behind on every run. $HERDR_SOCKET_PATH does
// not fix that: it relocates the socket, not the state.
//
// So the test boots a *named* session, whose state herdr confines to
// ~/.config/herdr/sessions/<name>/, and tears it down with herdr's own
// `session delete`. Teardown deliberately removes no filesystem path itself:
// the only directory it could compute is the session dir, and for a
// default-session server that same expression is the developer's entire herdr
// configuration. TestHerdrE2ELeavesNoGlobalState is the standing check.
//
// Skips when herdr is unavailable.

func herdrOK(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("herdr not installed; skipping herdr backchannel e2e")
	}
}

// herdrCLI invokes the herdr CLI, optionally against a specific server socket.
func herdrCLI(t *testing.T, socket string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "herdr", args...)
	if socket != "" {
		cmd.Env = append(os.Environ(), "HERDR_SOCKET_PATH="+socket)
	}
	return cmd.CombinedOutput()
}

// herdrSession invokes the CLI against this test's named session.
func herdrSession(t *testing.T, name string, args ...string) ([]byte, error) {
	t.Helper()
	return herdrCLI(t, "", append([]string{"--session", name}, args...)...)
}

// herdrSocketOf returns the running server's socket path as reported by the
// server itself, so the test never hardcodes herdr's state-dir layout.
func herdrSocketOf(t *testing.T, name string) string {
	t.Helper()
	out, err := herdrSession(t, name, "status", "server")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "socket:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// startHerdrCatPane boots a private named herdr server, opens a workspace, and
// runs `cat` in its root pane — the same deterministic echo "agent" the tmux
// e2e uses, so a successful injection shows up verbatim in the pane.
func startHerdrCatPane(t *testing.T) (paneID, socket string) {
	t.Helper()

	// Unique per test *and* per process: two tests in one run must not share a
	// session name, and neither must two concurrent runs.
	name := fmt.Sprintf("irr-e2e-%d-%s", os.Getpid(),
		strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))

	cwd, err := os.MkdirTemp("/tmp", "irr-hrd-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cwd) }) // created here, so safe to remove

	srv := exec.Command("herdr", "--session", name, "server")
	if err := srv.Start(); err != nil {
		t.Skipf("cannot start herdr server (sandboxed?): %v", err)
	}
	t.Cleanup(func() {
		_, _ = herdrSession(t, name, "server", "stop")
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
		// herdr owns its state dir; let it do the deleting.
		_, _ = herdrCLI(t, "", "session", "delete", name)
	})

	// The server publishes its socket asynchronously; poll until it answers.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if socket = herdrSocketOf(t, name); socket != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if socket == "" {
		t.Skip("herdr server did not become reachable; skipping")
	}

	// Take the pane id from the create response, not from `pane list`: the
	// latter is ordered across the whole server, so its head is not
	// necessarily the workspace just created.
	out, err := herdrSession(t, name, "workspace", "create", "--cwd", cwd)
	if err != nil {
		t.Fatalf("workspace create: %v: %s", err, out)
	}
	var created struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		t.Fatalf("workspace create json: %v: %s", err, out)
	}
	paneID = created.Result.RootPane.PaneID
	if paneID == "" {
		t.Fatalf("no root pane in create response: %s", out)
	}

	if out, err := herdrSession(t, name, "pane", "run", paneID, "cat"); err != nil {
		t.Fatalf("pane run cat: %v: %s", err, out)
	}
	return paneID, socket
}

// assertHerdrPaneContains polls the pane's rendered screen until want appears.
// Addressed by socket rather than --session, so it goes through the same
// addressing the production builders use.
func assertHerdrPaneContains(t *testing.T, socket, paneID, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		out, _ := herdrCLI(t, socket, "pane", "read", paneID, "--source", "visible", "--format", "text")
		if strings.Contains(string(out), want) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("herdr pane never showed %q", want)
}

func newHerdrE2EStack(paneID, socket string) (*services.InputService, *control.Reader) {
	repo := &e2eRepo{state: &session.SessionState{
		SessionID: "hrd",
		Adapter:   "claude-code",
		State:     session.StateWorking,
		Launcher:  &session.Launcher{HerdrPaneID: paneID, HerdrSocketPath: socket},
	}}
	ctrl := control.NewController(repo, e2ePush{}, e2eLog{})
	in := services.NewInputService(repo, ctrl, allowConsent{}, func() bool { return true }, e2eLog{})
	return in, control.NewReader(repo, e2eLog{})
}

// TestBackchannelHerdrE2E drives a real herdr pane through the local stack and
// reads it back. Before #1348 this session resolved to no backend at all:
// Controllable was false and CaptureScreen returned ErrNotReadable.
func TestBackchannelHerdrE2E(t *testing.T) {
	herdrOK(t)
	paneID, socket := startHerdrCatPane(t)
	in, reader := newHerdrE2EStack(paneID, socket)

	if !in.Controllable("hrd") {
		t.Fatal("herdr-hosted session should be controllable")
	}

	// Write path: SendCommand owns the submit sequence, so the CR that makes
	// cat echo the line is the production code's, not the test's.
	if err := in.SendCommand("hrd", "HERDR_OK"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	assertHerdrPaneContains(t, socket, paneID, "HERDR_OK")

	// Read path: the daemon's own Reader must see the same screen.
	screen, err := reader.CaptureScreen("hrd")
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}
	if !strings.Contains(string(screen), "HERDR_OK") {
		t.Errorf("CaptureScreen missed the injected line; got %q", screen)
	}

	if err := in.Interrupt("hrd"); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}

// TestHerdrE2ELeavesNoGlobalState pins the isolation claim above: this suite
// must not mutate the developer's shared herdr layout. It is a lock, and it
// earns its place — an earlier revision of this file drove the *default*
// session, which both rewrote session.json and (via a teardown that removed
// the socket's parent directory) deleted ~/.config/herdr outright.
func TestHerdrE2ELeavesNoGlobalState(t *testing.T) {
	herdrOK(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	configDir := filepath.Join(home, ".config", "herdr")
	global := filepath.Join(configDir, "session.json")
	before, err := os.ReadFile(global)
	if err != nil {
		t.Skip("no global herdr state to protect")
	}
	// A default-session server of the developer's own rewrites this file
	// whenever they move a pane, which would read as our mutation.
	status, _ := herdrCLI(t, "", "status", "server")
	if strings.Contains(string(status), "status: running") {
		t.Skip("developer's default herdr session is running; cannot attribute state changes")
	}

	paneID, socket := startHerdrCatPane(t)
	in, _ := newHerdrE2EStack(paneID, socket)
	if err := in.SendCommand("hrd", "STATE_CHECK"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	assertHerdrPaneContains(t, socket, paneID, "STATE_CHECK")

	after, err := os.ReadFile(global)
	if err != nil {
		t.Fatalf("global herdr state is gone after the run: %v", err)
	}
	if string(before) != string(after) {
		t.Error("the herdr e2e mutated the developer's global herdr state")
	}
	if _, err := os.Stat(configDir); err != nil {
		t.Errorf("the herdr e2e removed the developer's herdr config dir: %v", err)
	}
}
