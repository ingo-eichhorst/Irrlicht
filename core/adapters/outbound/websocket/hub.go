package websocket

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"

	"irrlicht/core/ports/outbound"
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

// ServeWS upgrades the HTTP connection to WebSocket and streams state updates
// until the client disconnects. Register on GET /api/v1/sessions/stream.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch := h.push.Subscribe()
	defer h.push.Unsubscribe(ch)

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

	for {
		select {
		case state, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(state)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
