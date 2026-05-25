package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

func newTestServer(t *testing.T) (wsURL, baseURL string) {
	t.Helper()
	h := newHub()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions/stream", h.ServeWS)
	mux.HandleFunc("GET /api/v1/sessions", handleSessions(h))
	mux.HandleFunc("GET /api/v1/agents", handleAgents(h))
	mux.HandleFunc("GET /api/v1/version", handleVersion("test"))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/sessions/stream", srv.URL
}

func dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// readUntil reads frames until one of the wanted type arrives, so a test is
// robust to interleaved control/other frames.
func readUntil(t *testing.T, c *websocket.Conn, typ string) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for %q frame: %v", typ, err)
		}
		if relay.FrameType(data) == typ {
			return data
		}
	}
}

func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}

func httpGet(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body
}

func TestRelayRoundTrip(t *testing.T) {
	wsURL, baseURL := newTestServer(t)

	// Client connects first — no daemons yet, so the snapshot is empty.
	client := dial(t, wsURL)
	if err := client.WriteJSON(relay.Hello{Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion, Role: relay.RoleClient}); err != nil {
		t.Fatal(err)
	}
	var snap relay.Snapshot
	mustJSON(t, readUntil(t, client, relay.MsgSnapshot), &snap)
	if len(snap.Daemons) != 0 {
		t.Fatalf("expected no daemons initially, got %+v", snap.Daemons)
	}

	// Daemon connects, handshakes, and reconciles one session.
	daemon := dial(t, wsURL)
	if err := daemon.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
		Role: relay.RoleDaemon, DaemonID: "d1", DaemonLabel: "laptop",
	}); err != nil {
		t.Fatal(err)
	}
	var ack relay.HelloAck
	if err := daemon.ReadJSON(&ack); err != nil || ack.Type != relay.MsgHelloAck || ack.AcceptedVersion != relay.ProtocolVersion {
		t.Fatalf("hello_ack: err=%v ack=%+v", err, ack)
	}
	if err := daemon.WriteJSON(relay.DaemonSnapshot{
		Type:     relay.MsgDaemonSnapshot,
		Sessions: []*session.SessionState{{SessionID: "s1", State: "working", ProjectName: "proj"}},
		Agents:   []relay.AgentInfo{{Name: "claude-code", DisplayName: "Claude Code"}},
	}); err != nil {
		t.Fatal(err)
	}

	// The pre-existing client sees the daemon appear and its session arrive live.
	var ds relay.DaemonStatus
	mustJSON(t, readUntil(t, client, relay.MsgDaemonStatus), &ds)
	if ds.DaemonID != "d1" || ds.DaemonLabel != "laptop" || ds.Status != relay.StatusConnected {
		t.Fatalf("unexpected daemon_status: %+v", ds)
	}
	var p relay.Push
	mustJSON(t, readUntil(t, client, relay.MsgPush), &p)
	if p.Source != "d1" || p.Msg.Session == nil || p.Msg.Session.SessionID != "s1" {
		t.Fatalf("snapshot reconciliation push wrong: %+v", p)
	}

	// HTTP mirrors the cache: the session and the agent union.
	if body := httpGet(t, baseURL+"/api/v1/sessions"); !bytes.Contains(body, []byte(`"session_id":"s1"`)) {
		t.Fatalf("/api/v1/sessions missing s1: %s", body)
	}
	if body := httpGet(t, baseURL+"/api/v1/agents"); !bytes.Contains(body, []byte(`"name":"claude-code"`)) {
		t.Fatalf("/api/v1/agents missing claude-code: %s", body)
	}

	// A live delta forwards through, source-stamped.
	if err := daemon.WriteJSON(relay.Push{
		Type: relay.MsgPush, Source: "ignored-by-relay",
		Msg: outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: &session.SessionState{SessionID: "s2", State: "ready"}},
	}); err != nil {
		t.Fatal(err)
	}
	mustJSON(t, readUntil(t, client, relay.MsgPush), &p)
	if p.Source != "d1" || p.Msg.Session == nil || p.Msg.Session.SessionID != "s2" {
		t.Fatalf("live delta push wrong (source must be stamped d1): %+v", p)
	}

	// Daemon drops → the relay deletes its rows and announces disconnect.
	daemon.Close()
	sawDisconnect := false
	deadline := time.Now().Add(2 * time.Second)
	for !sawDisconnect && time.Now().Before(deadline) {
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("reading after daemon close: %v", err)
		}
		if relay.FrameType(data) == relay.MsgDaemonStatus {
			var d relay.DaemonStatus
			mustJSON(t, data, &d)
			if d.DaemonID == "d1" && d.Status == relay.StatusDisconnected {
				sawDisconnect = true
			}
		}
	}
	if !sawDisconnect {
		t.Fatal("never observed the daemon disconnect status")
	}
}

func TestRelayReplaysCacheToLateClient(t *testing.T) {
	wsURL, _ := newTestServer(t)

	// Daemon connects and caches a session first.
	daemon := dial(t, wsURL)
	if err := daemon.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
		Role: relay.RoleDaemon, DaemonID: "d1", DaemonLabel: "laptop",
	}); err != nil {
		t.Fatal(err)
	}
	var ack relay.HelloAck
	if err := daemon.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	if err := daemon.WriteJSON(relay.DaemonSnapshot{
		Type:     relay.MsgDaemonSnapshot,
		Sessions: []*session.SessionState{{SessionID: "s1", State: "working", ProjectName: "proj"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Let the relay ingest the snapshot before the client connects.
	time.Sleep(50 * time.Millisecond)

	// A client connecting now must still receive s1 over the WS via replay.
	client := dial(t, wsURL)
	if err := client.WriteJSON(relay.Hello{Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion, Role: relay.RoleClient}); err != nil {
		t.Fatal(err)
	}
	var p relay.Push
	mustJSON(t, readUntil(t, client, relay.MsgPush), &p)
	if p.Source != "d1" || p.Msg.Session == nil || p.Msg.Session.SessionID != "s1" {
		t.Fatalf("expected replayed s1 push, got %+v", p)
	}
}

func TestResolveUIDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveUIDirFor(dir, "", ""); got != dir {
		t.Fatalf("resolveUIDirFor env = %q, want %q", got, dir)
	}
	if got := resolveUIDirFor("/nonexistent", "", ""); got != "" {
		t.Fatalf("resolveUIDirFor miss = %q, want empty", got)
	}
}
