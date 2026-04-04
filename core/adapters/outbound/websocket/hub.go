package websocket

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/ports/outbound"
)

const (
	pingInterval = 30 * time.Second
	pongTimeout  = 45 * time.Second
	writeTimeout = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Hub manages WebSocket connections and fans out session state updates.
type Hub struct {
	push outbound.PushBroadcaster
}

// NewHub creates a Hub backed by the provided PushBroadcaster.
func NewHub(push outbound.PushBroadcaster) *Hub {
	return &Hub{push: push}
}

// ServeWS upgrades the HTTP connection to WebSocket and streams typed session
// state update messages until the client disconnects.
// Register on GET /api/v1/sessions/stream.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
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
