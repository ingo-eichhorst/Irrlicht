package websocket

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/adapters/outbound/httputil"
	"irrlicht/core/ports/outbound"
)

const (
	pingInterval = 30 * time.Second
	pongTimeout  = 45 * time.Second
	writeTimeout = 10 * time.Second
)

// loopbackCheckOrigin accepts WebSocket handshakes only from loopback origins
// (or requests with no Origin header, which native clients like URLSession do
// not send). It blocks cross-site WebSocket connections from arbitrary web
// pages. The RemoteAddr check is a second line of defence in case the daemon
// is bound to a non-loopback interface.
func loopbackCheckOrigin(r *http.Request) bool {
	if !httputil.IsLoopbackRequest(r) {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ConnectSnapshots returns the messages to deliver to a freshly-attached
// WebSocket client before the live stream takes over. Typically this is one
// history_snapshot per known session.
type ConnectSnapshots func() []outbound.PushMessage

// hub manages WebSocket connections and fans out session state updates.
type hub struct {
	push             outbound.PushBroadcaster
	connectSnapshots ConnectSnapshots
	upgrader         websocket.Upgrader
}

// NewHub creates a hub backed by the provided PushBroadcaster. The upgrader
// enforces a loopback-only origin policy. connectSnapshots, when non-nil, is
// invoked on each new connection to ship the per-session history snapshots
// (so freshly-attached clients see the full 60-bucket history without polling).
func NewHub(push outbound.PushBroadcaster, connectSnapshots ConnectSnapshots) *hub {
	return &hub{
		push:             push,
		connectSnapshots: connectSnapshots,
		upgrader:         websocket.Upgrader{CheckOrigin: loopbackCheckOrigin},
	}
}

// ServeWS upgrades the HTTP connection to WebSocket and streams typed session
// state update messages until the client disconnects.
// Register on GET /api/v1/sessions/stream.
func (h *hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch := h.push.Subscribe()
	defer h.push.Unsubscribe(ch)

	// Hydrate the new client with one history_snapshot per known session
	// before the live stream starts. Subscribe-then-snapshot order ensures
	// no tick or upgrade emitted between these two operations is lost; per-
	// session tick generations on snapshot/tick messages let the client
	// dedupe a tick that's already reflected in its snapshot.
	if !h.sendSnapshots(conn) {
		return
	}

	// Set initial read deadline; reset on each pong.
	conn.SetReadDeadline(time.Now().Add(pongTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})

	// Detect client disconnect via a read pump running concurrently.
	done := watchForDisconnect(conn)

	// Ping ticker to keep the connection alive.
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !writeJSONMessage(conn, msg) {
				return
			}
		case <-ticker.C:
			if !sendPing(conn) {
				return
			}
		case <-done:
			return
		}
	}
}

// sendSnapshots hydrates a freshly-attached client with one history_snapshot
// per known session (h.connectSnapshots may be nil, in which case there's
// nothing to send). It returns false if a write failed and the caller should
// abort the connection.
func (h *hub) sendSnapshots(conn *websocket.Conn) bool {
	if h.connectSnapshots == nil {
		return true
	}
	for _, snap := range h.connectSnapshots() {
		if !writeJSONMessage(conn, snap) {
			return false
		}
	}
	return true
}

// watchForDisconnect runs a read pump goroutine that detects a client
// disconnect (WebSocket clients otherwise send nothing but pong/close
// frames) and closes the returned channel when the connection drops.
func watchForDisconnect(conn *websocket.Conn) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	return done
}

// writeJSONMessage marshals v and writes it as a text message. A marshal
// failure is swallowed (the message is skipped, the connection stays open);
// a write failure means the connection is dead, so it returns false to tell
// the caller to abort.
func writeJSONMessage(conn *websocket.Conn, v any) bool {
	data, err := json.Marshal(v)
	if err != nil {
		return true
	}
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.TextMessage, data) == nil
}

// sendPing writes a ping frame, returning false if the write failed and the
// caller should abort the connection.
func sendPing(conn *websocket.Conn) bool {
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(websocket.PingMessage, nil) == nil
}
