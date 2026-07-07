package main

import (
	"encoding/json"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/relay"
)

// TestRouteControlWorkspaceIsolation drives the routing decision directly: a
// control frame reaches a daemon only from a client in the daemon's own
// token-derived workspace (issue #724 tenant boundary).
func TestRouteControlWorkspaceIsolation(t *testing.T) {
	h := newHub(defaultLimits())
	daemonSend := make(chan []byte, 4)

	h.mu.Lock()
	ws := h.wsLocked("team-a")
	ws.daemons["d1"] = &daemonState{label: "lap", send: daemonSend}
	h.mu.Unlock()

	frame, _ := json.Marshal(relay.Control{
		Type: relay.MsgControl, TargetDaemon: "d1", SessionID: "s1",
		Action: relay.ControlActionInput, Data: "hi",
	})

	// Same workspace → routed to the daemon's queue.
	h.routeControl(&clientConn{workspace: "team-a"}, frame)
	select {
	case got := <-daemonSend:
		var c relay.Control
		if json.Unmarshal(got, &c) != nil || c.SessionID != "s1" || c.Data != "hi" {
			t.Fatalf("daemon received a malformed control frame: %s", got)
		}
	default:
		t.Fatal("same-workspace control was not routed to the daemon")
	}

	// Different workspace → dropped (cross-tenant isolation).
	h.routeControl(&clientConn{workspace: "team-b"}, frame)
	select {
	case <-daemonSend:
		t.Fatal("cross-workspace control must NOT reach the daemon")
	default:
	}

	// Unknown target daemon → dropped.
	other, _ := json.Marshal(relay.Control{Type: relay.MsgControl, TargetDaemon: "nope", SessionID: "s1", Action: relay.ControlActionInput})
	h.routeControl(&clientConn{workspace: "team-a"}, other)
	select {
	case <-daemonSend:
		t.Fatal("control for an unknown daemon must NOT be routed")
	default:
	}
}

// TestRelayRoutesControlToDaemon is the end-to-end path: a connected client's
// control frame is delivered to the addressed daemon over the WebSocket.
func TestRelayRoutesControlToDaemon(t *testing.T) {
	wsURL, _ := newTestServer(t)

	// Connect the client FIRST so the daemon's registration reaches it as a
	// live daemon_status(connected) — a late-joining client would instead get
	// the daemon folded into its initial snapshot, with nothing to sync on.
	client := dial(t, wsURL)
	if err := client.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion, Role: relay.RoleClient,
	}); err != nil {
		t.Fatal(err)
	}
	// Wait for the client's initial snapshot before dialing the daemon: dial()
	// returning only means the TCP/HTTP handshake finished, not that the
	// server has processed the hello and registered the client yet. Without
	// this sync point the daemon's hello can race ahead and register+broadcast
	// daemon_status before the client is in h.clients, so fanout drops the
	// frame and the readUntil below times out (flaked in #913's CI run).
	readUntil(t, client, relay.MsgSnapshot)

	daemon := dial(t, wsURL)
	if err := daemon.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
		Role: relay.RoleDaemon, DaemonID: "d1", DaemonLabel: "lap",
	}); err != nil {
		t.Fatal(err)
	}
	var ack relay.HelloAck
	if err := daemon.ReadJSON(&ack); err != nil || ack.Type != relay.MsgHelloAck {
		t.Fatalf("daemon hello_ack: %v", err)
	}
	if err := daemon.WriteJSON(relay.DaemonSnapshot{Type: relay.MsgDaemonSnapshot}); err != nil {
		t.Fatal(err)
	}

	// Sync on the daemon_status(connected) the client receives once the daemon
	// is registered, so the control frame can't race ahead of registration.
	readUntil(t, client, relay.MsgDaemonStatus)

	if err := client.WriteJSON(relay.Control{
		Type: relay.MsgControl, TargetDaemon: "d1", SessionID: "s1",
		Action: relay.ControlActionInput, Data: "echo hi\r",
	}); err != nil {
		t.Fatal(err)
	}

	daemon.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, data, err := daemon.ReadMessage()
		if err != nil {
			t.Fatalf("daemon did not receive the control frame: %v", err)
		}
		if relay.FrameType(data) != relay.MsgControl {
			continue // skip pings/other frames
		}
		var c relay.Control
		if json.Unmarshal(data, &c) != nil || c.SessionID != "s1" || c.Data != "echo hi\r" {
			t.Fatalf("unexpected control frame at daemon: %s", data)
		}
		break
	}
}
