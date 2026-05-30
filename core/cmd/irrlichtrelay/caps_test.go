package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/domain/session"
)

// dialDaemonHolding dials wsURL, completes a daemon hello, and reads the
// hello_ack — so the caller knows the connection has passed the cap check and
// is holding its slot before opening a second connection.
func dialDaemonHolding(t *testing.T, wsURL, id string) *websocket.Conn {
	t.Helper()
	c := dial(t, wsURL)
	if err := c.WriteJSON(relay.Hello{
		Type:            relay.MsgHello,
		ProtocolVersion: relay.ProtocolVersion,
		Role:            relay.RoleDaemon,
		DaemonID:        id,
		DaemonLabel:     "test",
	}); err != nil {
		t.Fatalf("write daemon hello: %v", err)
	}
	readUntil(t, c, relay.MsgHelloAck)
	return c
}

// expectClose asserts the next read on c fails with a WebSocket close of the
// given code (the connection was rejected/closed by the server).
func expectClose(t *testing.T, c *websocket.Conn, code int) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, _, err := c.ReadMessage()
		if err == nil {
			continue // skip any frame delivered before the close
		}
		var ce *websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("expected close code %d, got non-close error: %v", code, err)
		}
		if ce.Code != code {
			t.Fatalf("expected close code %d, got %d (%q)", code, ce.Code, ce.Text)
		}
		return
	}
}

func TestRelayRejectsOverGlobalCap(t *testing.T) {
	// One total connection allowed; per-IP disabled so only the global cap can trip.
	wsURL, _ := newTestServerWithLimits(t, limits{maxConns: 1})

	held := dialDaemonHolding(t, wsURL, "d1")
	defer held.Close()

	// The second connection exceeds the total cap and is closed with 1013.
	over := dial(t, wsURL)
	expectClose(t, over, websocket.CloseTryAgainLater)
}

func TestRelayRejectsOverPerIPCap(t *testing.T) {
	// Total disabled; one connection per IP. httptest binds 127.0.0.1, so both
	// dials share an IP and the second trips the per-IP cap, not the global one.
	wsURL, _ := newTestServerWithLimits(t, limits{maxConnsPerIP: 1})

	held := dialDaemonHolding(t, wsURL, "d1")
	defer held.Close()

	over := dial(t, wsURL)
	expectClose(t, over, websocket.CloseTryAgainLater)
}

func TestRelayPerIPSlotReleasedOnDisconnect(t *testing.T) {
	// Closing a connection frees its per-IP slot so a later dial succeeds.
	wsURL, _ := newTestServerWithLimits(t, limits{maxConnsPerIP: 1})

	first := dialDaemonHolding(t, wsURL, "d1")
	first.Close()

	// Give the server's deferred release time to run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c := dial(t, wsURL)
		if err := c.WriteJSON(relay.Hello{
			Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
			Role: relay.RoleDaemon, DaemonID: "d2", DaemonLabel: "test",
		}); err != nil {
			c.Close()
			t.Fatalf("write daemon hello: %v", err)
		}
		c.SetReadDeadline(time.Now().Add(time.Second))
		_, data, err := c.ReadMessage()
		if err == nil && relay.FrameType(data) == relay.MsgHelloAck {
			c.Close()
			return // slot was released — success
		}
		c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("per-IP slot was not released after the holder disconnected")
}

func TestRelayAcceptsLargeDaemonSnapshot(t *testing.T) {
	// The strict client-frame cap must not clamp the trusted daemon path: a
	// daemon_snapshot larger than maxMsgBytes must still be ingested (else a busy
	// daemon loops snapshot-too-big → close → reconnect forever).
	wsURL, baseURL := newTestServerWithLimits(t, limits{maxMsgBytes: 4 << 10, maxConns: 8, maxConnsPerIP: 8})

	daemon := dial(t, wsURL)
	if err := daemon.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
		Role: relay.RoleDaemon, DaemonID: "d1", DaemonLabel: "busy",
	}); err != nil {
		t.Fatal(err)
	}
	readUntil(t, daemon, relay.MsgHelloAck)

	// Build a snapshot well past the 4 KiB client cap.
	sessions := make([]*session.SessionState, 0, 64)
	for i := range 64 {
		sessions = append(sessions, &session.SessionState{
			SessionID:   "s" + strconv.Itoa(i),
			State:       "working",
			ProjectName: strings.Repeat("p", 256),
		})
	}
	if err := daemon.WriteJSON(relay.DaemonSnapshot{Type: relay.MsgDaemonSnapshot, Sessions: sessions}); err != nil {
		t.Fatal(err)
	}

	// If the cap had clamped the daemon path, the read would have errored and
	// the cache stayed empty; instead the sessions must surface over HTTP.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if body := httpGet(t, baseURL+"/api/v1/sessions"); strings.Contains(string(body), `"session_id":"s63"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("large daemon snapshot was not ingested — client cap leaked onto the daemon path")
}

func TestRelayRejectsOversizedFrame(t *testing.T) {
	// A tiny message cap; an oversized inbound frame is closed with 1009.
	wsURL, _ := newTestServerWithLimits(t, limits{maxMsgBytes: 16})

	c := dial(t, wsURL)
	if err := c.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 1024))); err != nil {
		t.Fatalf("write oversized frame: %v", err)
	}
	expectClose(t, c, websocket.CloseMessageTooBig)
}
