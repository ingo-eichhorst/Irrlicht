package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// fakeBroadcaster is a minimal PushBroadcaster backed by a single buffered
// channel, so a test controls exactly which messages the forwarder sees.
type fakeBroadcaster struct {
	ch chan outbound.PushMessage
}

func newFakeBroadcaster() *fakeBroadcaster {
	return &fakeBroadcaster{ch: make(chan outbound.PushMessage, 16)}
}

func (f *fakeBroadcaster) Subscribe() chan outbound.PushMessage  { return f.ch }
func (f *fakeBroadcaster) Unsubscribe(chan outbound.PushMessage) {}
func (f *fakeBroadcaster) Broadcast(msg outbound.PushMessage)    { f.ch <- msg }

// testRelay is a minimal WebSocket server standing in for irrlichtrelay: it
// upgrades each connection and forwards every received frame onto frames.
type testRelay struct {
	url    string
	frames chan []byte
	conns  chan *websocket.Conn
}

func newTestRelay(t *testing.T) *testRelay {
	t.Helper()
	tr := &testRelay{
		frames: make(chan []byte, 64),
		conns:  make(chan *websocket.Conn, 8),
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		tr.conns <- c
		// Mirror the real relay: acknowledge the daemon's hello so the forwarder
		// declares the link up and proceeds to send its snapshot. (The forwarder
		// now waits for this reply before sending the snapshot.)
		_ = c.WriteJSON(HelloAck{Type: MsgHelloAck, AcceptedVersion: ProtocolVersion})
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			tr.frames <- data
		}
	}))
	t.Cleanup(srv.Close)
	tr.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return tr
}

func (tr *testRelay) next(t *testing.T) []byte {
	t.Helper()
	select {
	case f := <-tr.frames:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a relay frame")
		return nil
	}
}

func mustUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}

func TestForwarderHelloSnapshotAndPush(t *testing.T) {
	tr := newTestRelay(t)
	bc := newFakeBroadcaster()
	snap := func() ([]*session.SessionState, []AgentInfo) {
		return []*session.SessionState{{SessionID: "s1", State: "working"}},
			[]AgentInfo{{Name: "claude-code", DisplayName: "Claude Code"}}
	}
	f := NewForwarder(tr.url, Identity{DaemonID: "d-123", DaemonLabel: "laptop"}, "", bc, snap, nil, nil, nil)
	go f.Run(t.Context())

	var hello Hello
	mustUnmarshal(t, tr.next(t), &hello)
	if hello.Type != MsgHello || hello.Role != RoleDaemon || hello.DaemonID != "d-123" || hello.ProtocolVersion != ProtocolVersion {
		t.Fatalf("unexpected hello: %+v", hello)
	}

	var ds DaemonSnapshot
	mustUnmarshal(t, tr.next(t), &ds)
	if ds.Type != MsgDaemonSnapshot || len(ds.Sessions) != 1 || ds.Sessions[0].SessionID != "s1" || len(ds.Agents) != 1 {
		t.Fatalf("unexpected daemon_snapshot: %+v", ds)
	}

	bc.Broadcast(outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: &session.SessionState{SessionID: "s1", State: "ready"}})
	var p Push
	mustUnmarshal(t, tr.next(t), &p)
	if p.Type != MsgPush || p.Source != "d-123" || p.TS == 0 {
		t.Fatalf("unexpected push envelope: %+v", p)
	}
	if p.Msg.Type != outbound.PushTypeUpdated || p.Msg.Session == nil || p.Msg.Session.SessionID != "s1" {
		t.Fatalf("push did not carry the unchanged PushMessage: %+v", p.Msg)
	}
}

func TestForwarderFiltersFocusRequested(t *testing.T) {
	tr := newTestRelay(t)
	bc := newFakeBroadcaster()
	f := NewForwarder(tr.url, Identity{DaemonID: "d1"}, "", bc, nil, nil, nil, nil)
	go f.Run(t.Context())

	tr.next(t) // hello
	tr.next(t) // daemon_snapshot

	// focus_requested is host-local and must be dropped; the following
	// update must still arrive, proving the filter skips rather than stalls.
	bc.Broadcast(outbound.PushMessage{Type: outbound.PushTypeFocusRequested, Session: &session.SessionState{SessionID: "s1"}})
	bc.Broadcast(outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: &session.SessionState{SessionID: "s2"}})

	var p Push
	mustUnmarshal(t, tr.next(t), &p)
	if p.Msg.Type != outbound.PushTypeUpdated || p.Msg.Session.SessionID != "s2" {
		t.Fatalf("expected focus_requested filtered, next push was %+v", p.Msg)
	}
}

func TestForwarderReconnects(t *testing.T) {
	tr := newTestRelay(t)
	bc := newFakeBroadcaster()
	f := NewForwarder(tr.url, Identity{DaemonID: "d1"}, "", bc, nil, nil, nil, nil)
	f.minBackoff = 10 * time.Millisecond
	f.maxBackoff = 20 * time.Millisecond
	go f.Run(t.Context())

	c1 := <-tr.conns
	tr.next(t) // hello
	tr.next(t) // daemon_snapshot
	c1.Close() // simulate the relay dropping the daemon

	select {
	case <-tr.conns:
	case <-time.After(2 * time.Second):
		t.Fatal("forwarder did not reconnect after the relay dropped")
	}
	var hello Hello
	mustUnmarshal(t, tr.next(t), &hello)
	if hello.Type != MsgHello {
		t.Fatalf("expected a fresh hello on reconnect, got %+v", hello)
	}
}

func TestLoadOrCreateIdentityPersists(t *testing.T) {
	dir := t.TempDir()
	id1, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id1.DaemonID == "" || id1.DaemonLabel == "" {
		t.Fatalf("incomplete identity: %+v", id1)
	}
	id2, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id2.DaemonID != id1.DaemonID {
		t.Fatalf("daemon_id not stable across loads: %q vs %q", id1.DaemonID, id2.DaemonID)
	}
}

func TestFrameType(t *testing.T) {
	if got := FrameType([]byte(`{"type":"push","source":"x"}`)); got != "push" {
		t.Fatalf("FrameType = %q, want push", got)
	}
	if got := FrameType([]byte(`not json`)); got != "" {
		t.Fatalf("FrameType on garbage = %q, want empty", got)
	}
}

func TestNormalizeRelayURL(t *testing.T) {
	cases := map[string]string{
		"ws://localhost:7839":                        "ws://localhost:7839/api/v1/sessions/stream",
		"localhost:7839":                             "ws://localhost:7839/api/v1/sessions/stream",
		"http://relay.example:7839":                  "ws://relay.example:7839/api/v1/sessions/stream",
		"https://relay.example/":                     "wss://relay.example/api/v1/sessions/stream",
		"ws://localhost:7839/api/v1/sessions/stream": "ws://localhost:7839/api/v1/sessions/stream",
		"": "",
	}
	for in, want := range cases {
		if got := normalizeRelayURL(in); got != want {
			t.Errorf("normalizeRelayURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateDialURLAccepts(t *testing.T) {
	for _, in := range []string{
		"ws://localhost:7839/api/v1/sessions/stream",
		"wss://relay.example/api/v1/sessions/stream",
		normalizeRelayURL("relay.example:7839"),
	} {
		if err := validateDialURL(in); err != nil {
			t.Errorf("validateDialURL(%q) = %v, want nil", in, err)
		}
	}
}

func TestValidateDialURLRejects(t *testing.T) {
	for _, in := range []string{
		"",
		"not a url\x7f",
		"http://relay.example/stream",  // normalizeRelayURL always rewrites to ws/wss; a bare http(s) here means something bypassed it
		"ws:///api/v1/sessions/stream", // no host
		"ws://user:pass@relay.example/api/v1/sessions/stream",
	} {
		if err := validateDialURL(in); err == nil {
			t.Errorf("validateDialURL(%q) = nil, want a rejection error", in)
		}
	}
}

func TestShouldForward(t *testing.T) {
	if shouldForward(outbound.PushMessage{Type: outbound.PushTypeFocusRequested}) {
		t.Fatal("focus_requested must not be forwarded")
	}
	if shouldForward(outbound.PushMessage{Type: outbound.PushTypePermissionsUpdated}) {
		t.Fatal("permissions_updated is host-local consent state and must not be forwarded")
	}
	for _, ty := range []string{
		outbound.PushTypeCreated, outbound.PushTypeUpdated, outbound.PushTypeDeleted,
		outbound.PushTypeHistoryTick, outbound.PushTypeHistorySnapshot,
	} {
		if !shouldForward(outbound.PushMessage{Type: ty}) {
			t.Fatalf("%s should be forwarded", ty)
		}
	}
}

// TestForwarderSendsToken verifies a configured bearer token is carried in the
// daemon hello so an auth-enabled relay can authenticate it.
func TestForwarderSendsToken(t *testing.T) {
	tr := newTestRelay(t)
	bc := newFakeBroadcaster()
	snap := func() ([]*session.SessionState, []AgentInfo) { return nil, nil }
	f := NewForwarder(tr.url, Identity{DaemonID: "d1"}, "s3cr3t-token", bc, snap, nil, nil, nil)
	go f.Run(t.Context())

	var hello Hello
	mustUnmarshal(t, tr.next(t), &hello)
	if hello.Type != MsgHello || hello.Role != RoleDaemon {
		t.Fatalf("first frame is not a daemon hello: %+v", hello)
	}
	if hello.Token != "s3cr3t-token" {
		t.Fatalf("hello.Token = %q, want the configured token", hello.Token)
	}
}

// waitForState polls f.Status() until it reaches want, failing the test if it
// doesn't within a generous deadline.
func waitForState(t *testing.T, f *Forwarder, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.Status().State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	s := f.Status()
	t.Fatalf("forwarder did not reach state %q (last state %q, lastError %q)", want, s.State, s.LastError)
}

// TestForwarderStatusConnected verifies the forwarder reports PublishConnected
// once the relay has acked its hello and it has sent its snapshot, with the
// identity carried through to Status().
func TestForwarderStatusConnected(t *testing.T) {
	tr := newTestRelay(t)
	bc := newFakeBroadcaster()
	snap := func() ([]*session.SessionState, []AgentInfo) { return nil, nil }
	f := NewForwarder(tr.url, Identity{DaemonID: "d1", DaemonLabel: "lap"}, "", bc, snap, nil, nil, nil)
	go f.Run(t.Context())

	tr.next(t) // hello
	tr.next(t) // daemon_snapshot — only sent after the ack was read

	waitForState(t, f, PublishConnected)
	if s := f.Status(); s.DaemonID != "d1" || s.DaemonLabel != "lap" || s.URL == "" || s.LastError != "" {
		t.Fatalf("unexpected status: %+v", s)
	}
}

// TestForwarderStatusAuthFailed verifies a relay that rejects the token with a
// CloseRevoked (4401) close moves the forwarder to PublishAuthFailed — the
// distinct state that lets the app show a "bad token" indicator rather than a
// generic reconnecting one.
func TestForwarderStatusAuthFailed(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Reject like an auth-enabled relay handed a bad/revoked token.
		_ = c.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(CloseRevoked, "unauthorized"),
			time.Now().Add(time.Second),
		)
		// Complete the close handshake so the forwarder reliably reads the close
		// frame (and its 4401 code) before the TCP teardown.
		_, _, _ = c.ReadMessage()
		_ = c.Close()
	}))
	defer srv.Close()

	bc := newFakeBroadcaster()
	f := NewForwarder("ws"+strings.TrimPrefix(srv.URL, "http"), Identity{DaemonID: "d1"}, "bad-token", bc, nil, nil, nil, nil)
	f.minBackoff = 10 * time.Millisecond
	f.maxBackoff = 20 * time.Millisecond
	go f.Run(t.Context())

	waitForState(t, f, PublishAuthFailed)
}

// TestLoadDaemonToken covers env-var precedence and the tokens.json fallback.
func TestLoadDaemonToken(t *testing.T) {
	dir := t.TempDir()
	if got := LoadDaemonToken(dir); got != "" {
		t.Fatalf("no env, no file: got %q want empty", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "relay-token.json"), []byte(`{"token":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadDaemonToken(dir); got != "from-file" {
		t.Fatalf("file fallback: got %q want from-file", got)
	}

	t.Setenv("IRRLICHT_RELAY_TOKEN", "from-env")
	if got := LoadDaemonToken(dir); got != "from-env" {
		t.Fatalf("env precedence: got %q want from-env", got)
	}
}
