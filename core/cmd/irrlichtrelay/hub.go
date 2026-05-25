package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/adapters/outbound/relay"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

const (
	helloTimeout = 10 * time.Second
	pingInterval = 30 * time.Second
	pongTimeout  = 45 * time.Second
	writeTimeout = 10 * time.Second
)

// hub is the relay's in-memory core: it tracks connected daemons, caches the
// latest session state per (daemon_id, session_id) and the per-daemon adapter
// registry, and fans daemon push frames out to all connected clients. Single
// node, no auth, no persistence — everything here is lost on restart and
// rebuilt from each daemon's reconnect daemon_snapshot.
type hub struct {
	mu       sync.Mutex
	clients  map[*clientConn]struct{}
	daemons  map[string]*daemonState                     // daemon_id → liveness
	sessions map[string]map[string]*session.SessionState // daemon_id → session_id → state
	agents   map[string][]relay.AgentInfo                // daemon_id → adapter registry
	upgrader websocket.Upgrader
}

// daemonState tracks one daemon's connection liveness. conns counts live
// connections so a flapping reconnect (new connects before old read-error
// surfaces) doesn't prematurely mark the daemon disconnected.
type daemonState struct {
	label string
	since int64
	conns int
}

// clientConn is a connected client. send buffers outbound frames; done is
// closed by the read pump when the socket drops so the write pump exits
// promptly instead of waiting for the next ping tick.
type clientConn struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
}

func newHub() *hub {
	return &hub{
		clients:  make(map[*clientConn]struct{}),
		daemons:  make(map[string]*daemonState),
		sessions: make(map[string]map[string]*session.SessionState),
		agents:   make(map[string][]relay.AgentInfo),
		// Permissive origin policy: v0 is localhost-only with no auth, and the
		// dashboard served from a different port than the WS endpoint is
		// cross-origin. Tighten alongside auth in a later phase.
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
}

// ServeWS upgrades the connection, reads the opening hello, and dispatches to
// the daemon or client path by role. A peer that doesn't announce a daemon
// hello is treated as a client (lenient), so a stock dashboard still streams.
func (h *hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(helloTimeout))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{}) // clear; per-path deadlines take over
	if err != nil {
		return
	}
	var hello relay.Hello
	_ = json.Unmarshal(data, &hello)
	if hello.Type == relay.MsgHello && hello.Role == relay.RoleDaemon {
		h.serveDaemon(conn, hello)
		return
	}
	h.serveClient(conn)
}

// --- daemon side ---

func (h *hub) serveDaemon(conn *websocket.Conn, hello relay.Hello) {
	if hello.DaemonID == "" {
		return // untracked, undedupable — refuse
	}
	id, label := hello.DaemonID, hello.DaemonLabel

	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteJSON(relay.HelloAck{Type: relay.MsgHelloAck, AcceptedVersion: relay.ProtocolVersion}); err != nil {
		return
	}

	h.daemonConnected(id, label)
	defer h.daemonDisconnected(id)

	// Ping the daemon so an idle link stays alive (and dead ones are detected
	// at pongTimeout). Sole writer after the hello_ack above, satisfying
	// gorilla's one-concurrent-writer rule.
	done := make(chan struct{})
	defer close(done)
	go h.pingLoop(conn, done)

	conn.SetReadDeadline(time.Now().Add(pongTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		h.handleDaemonFrame(id, data)
	}
}

func (h *hub) handleDaemonFrame(daemonID string, data []byte) {
	switch relay.FrameType(data) {
	case relay.MsgDaemonSnapshot:
		var ds relay.DaemonSnapshot
		if json.Unmarshal(data, &ds) == nil {
			h.applyDaemonSnapshot(daemonID, ds)
		}
	case relay.MsgPush:
		var p relay.Push
		if json.Unmarshal(data, &p) == nil {
			h.applyPush(daemonID, p)
		}
	}
}

// applyDaemonSnapshot replaces the daemon's cached sessions with the snapshot
// and reconciles clients: a session_updated for each session present and a
// session_deleted for each that vanished since the prior snapshot. This lets a
// client that connected before the daemon (or before a reconnect) converge
// live, without depending on an HTTP re-poll.
func (h *hub) applyDaemonSnapshot(daemonID string, ds relay.DaemonSnapshot) {
	newMap := make(map[string]*session.SessionState, len(ds.Sessions))
	for _, s := range ds.Sessions {
		if s != nil && s.SessionID != "" {
			newMap[s.SessionID] = s
		}
	}

	h.mu.Lock()
	old := h.sessions[daemonID]
	h.sessions[daemonID] = newMap
	h.agents[daemonID] = ds.Agents
	h.mu.Unlock()

	for _, s := range newMap {
		h.fanoutPush(daemonID, outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: s})
	}
	for sid, s := range old {
		if _, ok := newMap[sid]; !ok {
			h.fanoutPush(daemonID, outbound.PushMessage{Type: outbound.PushTypeDeleted, Session: s})
		}
	}
}

// applyPush updates the session cache for session_* frames (so /api/v1/sessions
// reflects live state) and fans every frame out to clients. History frames
// carry no Session and are forwarded but not cached — history re-hydration of
// late-joining clients is deferred to a later phase.
func (h *hub) applyPush(daemonID string, p relay.Push) {
	msg := p.Msg
	if msg.Session != nil && msg.Session.SessionID != "" {
		sid := msg.Session.SessionID
		h.mu.Lock()
		m := h.sessions[daemonID]
		if m == nil {
			m = make(map[string]*session.SessionState)
			h.sessions[daemonID] = m
		}
		if msg.Type == outbound.PushTypeDeleted {
			delete(m, sid)
		} else {
			m[sid] = msg.Session
		}
		h.mu.Unlock()
	}
	h.fanoutPush(daemonID, msg)
}

func (h *hub) daemonConnected(id, label string) {
	now := time.Now().Unix()
	h.mu.Lock()
	d := h.daemons[id]
	if d == nil {
		d = &daemonState{label: label, since: now}
		h.daemons[id] = d
	} else {
		d.label = label
		if d.conns == 0 {
			d.since = now
		}
	}
	d.conns++
	h.mu.Unlock()
	h.broadcastDaemonStatus(id, label, relay.StatusConnected, now)
}

func (h *hub) daemonDisconnected(id string) {
	now := time.Now().Unix()
	h.mu.Lock()
	d := h.daemons[id]
	if d == nil {
		h.mu.Unlock()
		return
	}
	d.conns--
	label := d.label
	if d.conns > 0 {
		h.mu.Unlock()
		return // another live connection for this daemon remains
	}
	sessions := h.sessions[id]
	delete(h.daemons, id)
	delete(h.sessions, id)
	delete(h.agents, id)
	h.mu.Unlock()

	// v0 deletes the daemon's rows on disconnect ("fade don't delete" is a
	// deferred enhancement), then announces the disconnect for the tooltip.
	for _, s := range sessions {
		h.fanoutPush(id, outbound.PushMessage{Type: outbound.PushTypeDeleted, Session: s})
	}
	h.broadcastDaemonStatus(id, label, relay.StatusDisconnected, now)
}

// --- client side ---

func (h *hub) serveClient(conn *websocket.Conn) {
	cc := &clientConn{conn: conn, send: make(chan []byte, 64), done: make(chan struct{})}

	// Register before snapshotting so no daemon_status fired between the two
	// is missed: any daemon present at snapshot time is in the snapshot, any
	// connecting after registration arrives as a (possibly duplicate, but
	// idempotent) daemon_status.
	h.mu.Lock()
	h.clients[cc] = struct{}{}
	h.mu.Unlock()
	defer h.removeClient(cc)

	if data, err := json.Marshal(h.clientSnapshot()); err == nil {
		cc.send <- data // buffered; never blocks on the first frame
	}
	// Replay cached session state so a client that connected after a daemon
	// hydrates its list over the WebSocket alone — no cross-origin HTTP needed
	// for a remote relay source. History is not replayed (deferred); bars fill
	// in from live ticks.
	h.replaySessions(cc)

	go h.clientReadPump(cc)
	h.clientWritePump(cc)
}

// replaySessions enqueues the relay's cached sessions to one client as
// source-stamped session_updated pushes. Best-effort: frames beyond the send
// buffer are dropped (the same-origin HTTP cache and the next delta cover the
// gap), mirroring the slow-consumer policy.
func (h *hub) replaySessions(cc *clientConn) {
	type item struct {
		daemonID string
		state    *session.SessionState
	}
	h.mu.Lock()
	items := make([]item, 0)
	for did, m := range h.sessions {
		for _, s := range m {
			items = append(items, item{did, s})
		}
	}
	h.mu.Unlock()

	for _, it := range items {
		data, err := json.Marshal(relay.Push{
			Type: relay.MsgPush, Source: it.daemonID, TS: time.Now().Unix(),
			Msg: outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: it.state},
		})
		if err != nil {
			continue
		}
		select {
		case cc.send <- data:
		default:
		}
	}
}

func (h *hub) removeClient(cc *clientConn) {
	h.mu.Lock()
	delete(h.clients, cc)
	h.mu.Unlock()
}

func (h *hub) clientReadPump(cc *clientConn) {
	defer close(cc.done)
	cc.conn.SetReadDeadline(time.Now().Add(pongTimeout))
	cc.conn.SetPongHandler(func(string) error {
		cc.conn.SetReadDeadline(time.Now().Add(pongTimeout))
		return nil
	})
	for {
		if _, _, err := cc.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *hub) clientWritePump(cc *clientConn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-cc.done:
			return
		case data := <-cc.send:
			cc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := cc.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			cc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := cc.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// --- fan-out + snapshots ---

func (h *hub) fanoutPush(daemonID string, msg outbound.PushMessage) {
	data, err := json.Marshal(relay.Push{Type: relay.MsgPush, Source: daemonID, TS: time.Now().Unix(), Msg: msg})
	if err != nil {
		return
	}
	h.fanout(data)
}

func (h *hub) broadcastDaemonStatus(id, label, status string, since int64) {
	data, err := json.Marshal(relay.DaemonStatus{
		Type: relay.MsgDaemonStatus, DaemonID: id, DaemonLabel: label, Status: status, Since: since,
	})
	if err != nil {
		return
	}
	h.fanout(data)
}

// fanout delivers one frame to every client, dropping it for any client whose
// buffer is full (mirrors the daemon push service's slow-consumer policy).
func (h *hub) fanout(data []byte) {
	h.mu.Lock()
	targets := make([]*clientConn, 0, len(h.clients))
	for cc := range h.clients {
		targets = append(targets, cc)
	}
	h.mu.Unlock()
	for _, cc := range targets {
		select {
		case cc.send <- data:
		default: // slow client — drop
		}
	}
}

func (h *hub) clientSnapshot() relay.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	daemons := make([]relay.DaemonInfo, 0, len(h.daemons))
	for id, d := range h.daemons {
		daemons = append(daemons, relay.DaemonInfo{DaemonID: id, DaemonLabel: d.label, Status: relay.StatusConnected})
	}
	return relay.Snapshot{Type: relay.MsgSnapshot, Daemons: daemons}
}

// buildSessions flattens the per-daemon caches into one list for the HTTP
// /api/v1/sessions handler.
func (h *hub) buildSessions() []*session.SessionState {
	h.mu.Lock()
	defer h.mu.Unlock()
	var all []*session.SessionState
	for _, m := range h.sessions {
		for _, s := range m {
			all = append(all, s)
		}
	}
	return all
}

// buildAgents returns the union of every daemon's adapter registry, deduped by
// adapter name (frontends key off Name). Always non-nil so the JSON is [].
func (h *hub) buildAgents() []relay.AgentInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := make(map[string]bool)
	out := []relay.AgentInfo{}
	for _, infos := range h.agents {
		for _, a := range infos {
			if seen[a.Name] {
				continue
			}
			seen[a.Name] = true
			out = append(out, a)
		}
	}
	return out
}

func (h *hub) pingLoop(conn *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
