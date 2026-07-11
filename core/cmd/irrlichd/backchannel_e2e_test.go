package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/adapters/outbound/control"
	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// This is the backchannel-control e2e (issue #724): it drives the REAL control
// stack against a REAL tmux pane, both locally (InputService → Controller →
// tmux send-keys) and remotely (a stand-in relay pushes a control frame to a
// real Forwarder, which dispatches into the same InputService). tmux is the
// one terminal environment automatable headlessly; kitty/iTerm2/Terminal.app
// share the same InputService/Controller seam, verified by unit tests and the
// onboarding scenario assessments. Skips when tmux is unavailable.

// --- minimal stubs (the daemon's real types are exercised; only the repo,
// consent, push, and logger are stubbed so the test is hermetic) ---

type e2eRepo struct{ state *session.SessionState }

func (r *e2eRepo) Load(id string) (*session.SessionState, error) {
	if r.state != nil && r.state.SessionID == id {
		return r.state, nil
	}
	return nil, services.ErrSessionNotFound
}
func (r *e2eRepo) Save(*session.SessionState) error { return nil }
func (r *e2eRepo) Delete(string) error              { return nil }
func (r *e2eRepo) ListAll() ([]*session.SessionState, error) {
	return []*session.SessionState{r.state}, nil
}

type allowConsent struct{}

func (allowConsent) Granted(string, string) bool { return true }

type e2ePush struct{}

func (e2ePush) Broadcast(outbound.PushMessage)        {}
func (e2ePush) Subscribe() chan outbound.PushMessage  { return make(chan outbound.PushMessage, 1) }
func (e2ePush) Unsubscribe(chan outbound.PushMessage) {}

type e2eLog struct{}

func (e2eLog) LogInfo(_, _, _ string)                                  {}
func (e2eLog) LogError(_, _, _ string)                                 {}
func (e2eLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (e2eLog) Close() error                                            { return nil }

func tmuxOK(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed; skipping backchannel e2e")
	}
}

// startCatPane launches `cat` (a deterministic echo "agent") in a private tmux
// server and returns its pane id + socket. cat echoes each submitted line, so a
// successful injection shows up verbatim in the pane.
func startCatPane(t *testing.T) (paneID, socket string) {
	t.Helper()
	socket = t.TempDir() + "/tmux.sock"
	sess := "bc-e2e"
	run := func(args ...string) ([]byte, error) {
		full := append([]string{"-S", socket}, args...)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "tmux", full...).CombinedOutput()
	}
	if out, err := run("new-session", "-d", "-s", sess, "cat"); err != nil {
		t.Skipf("cannot start tmux session (sandboxed?): %v: %s", err, out)
	}
	t.Cleanup(func() { _, _ = run("kill-server") })
	out, err := run("display-message", "-t", sess, "-p", "#{pane_id}")
	if err != nil {
		t.Fatalf("display-message: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out)), socket
}

// assertPaneContains polls capture-pane until want appears or the deadline hits.
func assertPaneContains(t *testing.T, socket, paneID, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, _ := exec.CommandContext(ctx, "tmux", "-S", socket, "capture-pane", "-t", paneID, "-p").CombinedOutput()
		cancel()
		if strings.Contains(string(out), want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("pane never showed %q", want)
}

func newE2EStack(paneID, socket string) (*services.InputService, *control.Controller) {
	repo := &e2eRepo{state: &session.SessionState{
		SessionID: "e2e",
		Adapter:   "claude-code",
		State:     session.StateWorking,
		Launcher:  &session.Launcher{TmuxPane: paneID, TmuxSocket: socket},
	}}
	ctrl := control.NewController(repo, e2ePush{}, e2eLog{})
	in := services.NewInputService(repo, ctrl, allowConsent{}, func() bool { return true }, e2eLog{})
	return in, ctrl
}

// TestBackchannelE2E_Local drives a real tmux pane through the local stack.
func TestBackchannelE2E_Local(t *testing.T) {
	tmuxOK(t)
	paneID, socket := startCatPane(t)
	in, _ := newE2EStack(paneID, socket)

	if !in.Controllable("e2e") {
		t.Fatal("tmux-hosted session should be controllable")
	}
	if err := in.SendInput("e2e", []byte("LOCAL_OK\r")); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	assertPaneContains(t, socket, paneID, "LOCAL_OK")

	// Interrupt kills cat → the session is no longer controllable end-to-end,
	// proving the interrupt byte reached the pane.
	if err := in.Interrupt("e2e"); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}

// TestBackchannelE2E_Remote pushes a control frame from a stand-in relay to a
// real Forwarder, which dispatches it into the same InputService → tmux.
func TestBackchannelE2E_Remote(t *testing.T) {
	tmuxOK(t)
	paneID, socket := startCatPane(t)
	in, _ := newE2EStack(paneID, socket)

	// Stand-in relay: accept the daemon hello, ack it, then push one control
	// frame — exactly what the real hub's routeControl would deliver.
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var hello relay.Hello
		if err := c.ReadJSON(&hello); err != nil {
			return
		}
		_ = c.WriteJSON(relay.HelloAck{Type: relay.MsgHelloAck, AcceptedVersion: relay.ProtocolVersion})
		// daemon sends its snapshot next; drain it, then deliver control.
		_, _, _ = c.ReadMessage()
		_ = c.WriteJSON(relay.Control{
			Type: relay.MsgControl, TargetDaemon: hello.DaemonID, SessionID: "e2e",
			Action: relay.ControlActionInput, Data: "REMOTE_OK\r",
		})
		time.Sleep(2 * time.Second) // keep the socket open while the daemon dispatches
	}))
	defer srv.Close()

	// Real forwarder with remote control enabled, dispatching into InputService.
	fwd := relay.NewForwarder(
		"ws"+strings.TrimPrefix(srv.URL, "http"),
		relay.Identity{DaemonID: "d-e2e", DaemonLabel: "e2e"},
		relay.ForwarderDeps{
			Push:           e2ePush{},
			Snapshot:       func() ([]*session.SessionState, []relay.AgentInfo) { return nil, nil },
			Control:        in,                          // ControlHandler
			ControlEnabled: func() bool { return true }, // relay-control toggle ON
			Logger:         e2eLog{},
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fwd.Run(ctx)

	assertPaneContains(t, socket, paneID, "REMOTE_OK")
}
