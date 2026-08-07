package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
// (Reader → herdr pane read) are both driven against a REAL server here rather
// than only through the unit-level command builders.
//
// Fully hermetic: the server is addressed by $HERDR_SOCKET_PATH, which
// relocates its listen socket wholesale, so the test never touches the
// developer's own ~/.config/herdr sessions. Skips when herdr is unavailable.

func herdrOK(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("herdr not installed; skipping herdr backchannel e2e")
	}
}

// herdrRun invokes the herdr CLI against the test's own server.
func herdrRun(t *testing.T, socket string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "herdr", args...)
	cmd.Env = append(os.Environ(), "HERDR_SOCKET_PATH="+socket)
	return cmd.CombinedOutput()
}

// startHerdrCatPane boots a private herdr server, opens a workspace, and runs
// `cat` in its root pane — the same deterministic echo "agent" the tmux e2e
// uses, so a successful injection shows up verbatim in the pane.
func startHerdrCatPane(t *testing.T) (paneID, socket string) {
	t.Helper()

	// Deliberately not t.TempDir(): on macOS that lives under /var/folders/…
	// and a unix socket path over 104 bytes is silently truncated, which
	// surfaces as an unreachable server rather than an error.
	dir, err := os.MkdirTemp("/tmp", "irr-hrd-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	socket = dir + "/h.sock"
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	srv := exec.Command("herdr", "server")
	srv.Env = append(os.Environ(), "HERDR_SOCKET_PATH="+socket)
	if err := srv.Start(); err != nil {
		t.Skipf("cannot start herdr server (sandboxed?): %v", err)
	}
	t.Cleanup(func() {
		_, _ = herdrRun(t, socket, "server", "stop")
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
	})

	// The server publishes its socket asynchronously; poll until it answers.
	ready := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := herdrRun(t, socket, "pane", "list"); err == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Skip("herdr server did not become reachable; skipping")
	}

	if out, err := herdrRun(t, socket, "workspace", "create", "--cwd", dir); err != nil {
		t.Fatalf("workspace create: %v: %s", err, out)
	}

	out, err := herdrRun(t, socket, "pane", "list")
	if err != nil {
		t.Fatalf("pane list: %v: %s", err, out)
	}
	var listed struct {
		Result struct {
			Panes []struct {
				PaneID string `json:"pane_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("pane list json: %v: %s", err, out)
	}
	if len(listed.Result.Panes) == 0 {
		t.Fatalf("no panes after workspace create: %s", out)
	}
	paneID = listed.Result.Panes[0].PaneID

	if out, err := herdrRun(t, socket, "pane", "run", paneID, "cat"); err != nil {
		t.Fatalf("pane run cat: %v: %s", err, out)
	}
	return paneID, socket
}

// assertHerdrPaneContains polls the pane's rendered screen until want appears.
func assertHerdrPaneContains(t *testing.T, socket, paneID, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		out, _ := herdrRun(t, socket, "pane", "read", paneID, "--source", "visible", "--format", "text")
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
