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

// hub manages WebSocket connections and fans out session state updates.
type hub struct {
	push     outbound.PushBroadcaster
	upgrader websocket.Upgrader
}

// NewHub creates a hub backed by the provided PushBroadcaster. The upgrader
// enforces a loopback-only origin policy.
func NewHub(push outbound.PushBroadcaster) *hub {
	return &hub{
		push:     push,
		upgrader: websocket.Upgrader{CheckOrigin: loopbackCheckOrigin},
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

	// Set initial read deadline; reset on each pong.
	conn.SetReadDeadline(time.Now().Add(pongTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})

	// Detect client disconnect via a read pump running concurrently.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Ping ticker to keep the connection alive.
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
