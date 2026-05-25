package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	f := NewForwarder(tr.url, Identity{DaemonID: "d-123", DaemonLabel: "laptop"}, bc, snap, nil)
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
	f := NewForwarder(tr.url, Identity{DaemonID: "d1"}, bc, nil, nil)
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
	f := NewForwarder(tr.url, Identity{DaemonID: "d1"}, bc, nil, nil)
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

func TestShouldForward(t *testing.T) {
	if shouldForward(outbound.PushMessage{Type: outbound.PushTypeFocusRequested}) {
		t.Fatal("focus_requested must not be forwarded")
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
