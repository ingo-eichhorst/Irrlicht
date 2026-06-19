package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
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

	// rejectLogInterval throttles over-cap rejection logging so a connection
	// flood can't itself become a log flood.
	rejectLogInterval = time.Second
)

// limits bounds resource use on an exposed listener: maxMsgBytes caps a single
// inbound WebSocket frame (memory-exhaustion guard), maxConns the total live
// connections, and maxConnsPerIP the live connections from one remote IP. A
// non-positive value disables that cap.
type limits struct {
	maxMsgBytes   int64
	maxConns      int
	maxConnsPerIP int
}

// defaultLimits are the built-in caps used when no flag/env overrides them.
func defaultLimits() limits {
	return limits{maxMsgBytes: 1 << 20, maxConns: 256, maxConnsPerIP: 32}
}

// maxDaemonMsgBytes caps a frame on the trusted daemon path, which the strict
// maxMsgBytes (a client-frame guard) must not constrain: a daemon's
// daemon_snapshot is one JSON frame whose size scales with its live session
// count, so the small client cap would put a busy daemon into an unrecoverable
// "snapshot too big → close → reconnect → resend" loop. This bound is large
// enough for any realistic snapshot yet still finite, since the v0 daemon hello
// is unauthenticated — an attacker that sends a daemon hello must not unlock an
// unbounded read.
const maxDaemonMsgBytes = 32 << 20

// hub is the relay's in-memory core: it tracks connected daemons, caches the
// latest session state per (workspace, daemon_id, session_id) and the per-
// daemon adapter registry, and fans daemon push frames out to the clients in
// the matching workspace. Single node, no auth, no persistence — everything
// here is lost on restart and rebuilt from each daemon's reconnect
// daemon_snapshot.
type hub struct {
	mu      sync.Mutex
	clients map[*clientConn]struct{}
	// workspaces partitions all cached state by tenant. A connection's
	// workspace is server-derived from its bearer token (never client-supplied),
	// so a daemon in one workspace can neither read nor overwrite another's slot
	// even if it claims a colliding daemon_id. "" is the default workspace used
	// by no-auth relays and by tokens issued without an explicit workspace.
	workspaces map[string]*workspaceState
	upgrader   websocket.Upgrader

	// auth is nil when the relay runs with --auth off (trusted-LAN default):
	// every hello is accepted. When set, both daemon and client hellos must
	// carry a valid bearer token, and a token revoked mid-session closes the
	// peer with relay.CloseRevoked on its next frame.
	auth *authStore

	limits        limits
	totalConns    int            // live connections across all peers
	ipConns       map[string]int // remote IP → live connections
	lastRejectLog time.Time      // throttle for over-cap rejection logs
}

// workspaceState holds one tenant's view: the daemons connected under that
// workspace's tokens, their cached sessions, and their adapter registries. It
// mirrors the hub's pre-multi-tenant per-daemon maps, one set per workspace.
type workspaceState struct {
	daemons  map[string]*daemonState                     // daemon_id → liveness
	sessions map[string]map[string]*session.SessionState // daemon_id → session_id → state
	agents   map[string][]relay.AgentInfo                // daemon_id → adapter registry
}

func newWorkspaceState() *workspaceState {
	return &workspaceState{
		daemons:  make(map[string]*daemonState),
		sessions: make(map[string]map[string]*session.SessionState),
		agents:   make(map[string][]relay.AgentInfo),
	}
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
	conn      *websocket.Conn
	send      chan []byte
	done      chan struct{}
	tokenID   string // bearer-token id (empty on a no-auth relay); watched for revoke
	workspace string // tenant scope from the bearer token (server-derived)
}

// newHub builds a no-auth, all-origins hub with the given connection limits
// (the loopback default and the shape the tests use).
func newHub(lim limits) *hub { return newHubWithAuth(nil, nil, lim) }

// newHubWithAuth builds a hub with optional bearer-token auth, an optional
// browser-Origin allowlist, and connection limits. A nil auth accepts every
// hello; an empty allowlist accepts every Origin (loopback-safe, unchanged
// from v0).
func newHubWithAuth(auth *authStore, allowedOrigins []string, lim limits) *hub {
	return &hub{
		clients:    make(map[*clientConn]struct{}),
		workspaces: make(map[string]*workspaceState),
		auth:       auth,
		limits:     lim,
		ipConns:    make(map[string]int),
		upgrader:   websocket.Upgrader{CheckOrigin: originChecker(allowedOrigins)},
	}
}

// wsLocked returns the workspace's state, creating it on first use. The caller
// must hold h.mu.
func (h *hub) wsLocked(workspace string) *workspaceState {
	ws := h.workspaces[workspace]
	if ws == nil {
		ws = newWorkspaceState()
		h.workspaces[workspace] = ws
	}
	return ws
}

// originChecker gates the WS upgrade by the request Origin. An empty allowlist
// keeps the permissive v0 behavior (localhost dev served cross-origin from a
// different port). A non-empty allowlist admits only listed hosts; requests
// without an Origin header (native daemons, curl — non-browser peers) are
// always allowed since browsers always send one and auth still gates them.
func originChecker(allowed []string) func(*http.Request) bool {
	if len(allowed) == 0 {
		return func(*http.Request) bool { return true }
	}
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[strings.ToLower(strings.TrimSpace(o))] = true
	}
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return set[strings.ToLower(u.Host)]
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

	// Cap a single inbound frame before any read so an oversized payload is
	// closed by gorilla (code 1009) instead of buffered unbounded. This strict
	// cap governs the pre-dispatch hello read and the client read pump; the
	// trusted daemon path raises it in serveDaemon (see maxDaemonMsgBytes).
	if h.limits.maxMsgBytes > 0 {
		conn.SetReadLimit(h.limits.maxMsgBytes)
	}

	// Enforce connection caps before the hello read. A WebSocket close frame
	// requires a completed handshake, so we upgrade first, then close over-cap
	// peers cleanly with code 1013 (try-again-later) and a diagnostic reason.
	ip := remoteIP(r)
	if reason, ok := h.acquire(ip); !ok {
		closeWith(conn, websocket.CloseTryAgainLater, reason)
		h.logReject(ip, reason)
		return
	}
	defer h.release(ip)

	conn.SetReadDeadline(time.Now().Add(helloTimeout))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{}) // clear; per-path deadlines take over
	if err != nil {
		return
	}
	var hello relay.Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		// Lenient: a peer with an unparseable opening frame is still served as
		// a client (a daemon always sends a well-formed hello), but log it so a
		// daemon misconfiguration isn't silently indistinguishable from a stock
		// dashboard.
		log.Printf("relay: opening frame is not a valid hello (%v); treating peer as a client", err)
	}

	// Bearer-token gate (both roles). On a no-auth relay h.auth is nil and the
	// token is ignored. Otherwise the hello.token must hash to a known token;
	// an empty/invalid token closes the socket with relay.CloseRevoked.
	tokenID := ""
	workspace := ""
	if h.auth != nil {
		id, ws, ok := h.auth.validate(hello.Token)
		if !ok {
			closeWith(conn, relay.CloseRevoked, "unauthorized")
			return
		}
		tokenID = id
		workspace = ws
	}

	if hello.Type == relay.MsgHello && hello.Role == relay.RoleDaemon {
		h.serveDaemon(conn, hello, tokenID, workspace)
		return
	}
	h.serveClient(conn, tokenID, workspace)
}

// closeWith sends a WebSocket close frame with the given code and reason, then
// lets the deferred conn.Close() tear down the socket. Best-effort.
func closeWith(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(writeTimeout),
	)
}

// revoked reports whether an authenticated connection's token has since been
// revoked. Always false on a no-auth relay or an unauthenticated (token-less)
// connection.
func (h *hub) revoked(tokenID string) bool {
	return h.auth != nil && tokenID != "" && !h.auth.valid(tokenID)
}

// closeIfRevoked closes conn with CloseRevoked and returns true when tokenID has
// been revoked, so each read/write loop can guard with a single line and the
// check+close+return triplet lives in one place.
func (h *hub) closeIfRevoked(conn *websocket.Conn, tokenID string) bool {
	if h.revoked(tokenID) {
		closeWith(conn, relay.CloseRevoked, "token revoked")
		return true
	}
	return false
}

// acquire reserves a connection slot for ip, enforcing the total and per-IP
// caps. It returns (reason, false) without reserving when a cap is hit, so the
// caller closes the connection; otherwise (",", true) and the caller must
// release(ip) on exit.
func (h *hub) acquire(ip string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.limits.maxConns > 0 && h.totalConns >= h.limits.maxConns {
		return "relay at capacity", false
	}
	if h.limits.maxConnsPerIP > 0 && h.ipConns[ip] >= h.limits.maxConnsPerIP {
		return "per-IP connection limit reached", false
	}
	h.totalConns++
	h.ipConns[ip]++
	return "", true
}

// release frees the slot reserved by a matching acquire.
func (h *hub) release(ip string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.totalConns > 0 {
		h.totalConns--
	}
	if n := h.ipConns[ip]; n <= 1 {
		delete(h.ipConns, ip)
	} else {
		h.ipConns[ip] = n - 1
	}
}

// logReject logs an over-cap rejection at most once per rejectLogInterval so a
// connection flood doesn't become a log flood.
func (h *hub) logReject(ip, reason string) {
	now := time.Now()
	h.mu.Lock()
	throttled := now.Sub(h.lastRejectLog) < rejectLogInterval
	if !throttled {
		h.lastRejectLog = now
	}
	h.mu.Unlock()
	if !throttled {
		log.Printf("relay: rejected connection from %s: %s", ip, reason)
	}
}

// remoteIP extracts the host portion of the request's remote address. v1 trusts
// no proxy headers (X-Forwarded-For), so this is the direct peer address.
func remoteIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// --- daemon side ---

func (h *hub) serveDaemon(conn *websocket.Conn, hello relay.Hello, tokenID, workspace string) {
	if hello.DaemonID == "" {
		log.Printf("relay: refusing daemon hello with empty daemon_id")
		return // untracked, undedupable — refuse
	}
	id, label := hello.DaemonID, hello.DaemonLabel

	// Relax the strict client-frame cap: the daemon_snapshot read below scales
	// with the daemon's session count and must not be clamped by maxMsgBytes.
	conn.SetReadLimit(maxDaemonMsgBytes)

	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteJSON(relay.HelloAck{Type: relay.MsgHelloAck, AcceptedVersion: relay.ProtocolVersion}); err != nil {
		return
	}

	h.daemonConnected(workspace, id, label)
	defer h.daemonDisconnected(workspace, id)

	// Ping the daemon so an idle link stays alive (and dead ones are detected
	// at pongTimeout). Sole writer after the hello_ack above, satisfying
	// gorilla's one-concurrent-writer rule.
	done := make(chan struct{})
	defer close(done)
	go h.pingLoop(conn, done, tokenID)

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
		if h.closeIfRevoked(conn, tokenID) {
			return
		}
		h.handleDaemonFrame(workspace, id, data)
	}
}

func (h *hub) handleDaemonFrame(workspace, daemonID string, data []byte) {
	switch relay.FrameType(data) {
	case relay.MsgDaemonSnapshot:
		var ds relay.DaemonSnapshot
		if json.Unmarshal(data, &ds) == nil {
			h.applyDaemonSnapshot(workspace, daemonID, ds)
		}
	case relay.MsgPush:
		var p relay.Push
		if json.Unmarshal(data, &p) == nil {
			h.applyPush(workspace, daemonID, p)
		}
	}
}

// applyDaemonSnapshot replaces the daemon's cached sessions with the snapshot
// and reconciles clients: a session_updated for each session present and a
// session_deleted for each that vanished since the prior snapshot. This lets a
// client that connected before the daemon (or before a reconnect) converge
// live, without depending on an HTTP re-poll.
func (h *hub) applyDaemonSnapshot(workspace, daemonID string, ds relay.DaemonSnapshot) {
	newMap := make(map[string]*session.SessionState, len(ds.Sessions))
	for _, s := range ds.Sessions {
		if s != nil && s.SessionID != "" {
			newMap[s.SessionID] = s
		}
	}

	h.mu.Lock()
	ws := h.wsLocked(workspace)
	old := ws.sessions[daemonID]
	ws.sessions[daemonID] = newMap
	ws.agents[daemonID] = ds.Agents
	h.mu.Unlock()

	for _, s := range newMap {
		h.fanoutPush(workspace, daemonID, outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: s})
	}
	for sid, s := range old {
		if _, ok := newMap[sid]; !ok {
			h.fanoutPush(workspace, daemonID, outbound.PushMessage{Type: outbound.PushTypeDeleted, Session: s})
		}
	}
}

// applyPush updates the session cache for session_* frames (so /api/v1/sessions
// reflects live state) and fans every frame out to clients. History frames
// carry no Session and are forwarded but not cached — history re-hydration of
// late-joining clients is deferred to a later phase.
func (h *hub) applyPush(workspace, daemonID string, p relay.Push) {
	msg := p.Msg
	if msg.Session != nil && msg.Session.SessionID != "" {
		sid := msg.Session.SessionID
		h.mu.Lock()
		ws := h.wsLocked(workspace)
		m := ws.sessions[daemonID]
		if m == nil {
			m = make(map[string]*session.SessionState)
			ws.sessions[daemonID] = m
		}
		if msg.Type == outbound.PushTypeDeleted {
			delete(m, sid)
		} else {
			m[sid] = msg.Session
		}
		h.mu.Unlock()
	}
	h.fanoutPush(workspace, daemonID, msg)
}

func (h *hub) daemonConnected(workspace, id, label string) {
	now := time.Now().Unix()
	h.mu.Lock()
	ws := h.wsLocked(workspace)
	d := ws.daemons[id]
	if d == nil {
		d = &daemonState{label: label, since: now}
		ws.daemons[id] = d
	} else {
		d.label = label
		if d.conns == 0 {
			d.since = now
		}
	}
	d.conns++
	h.mu.Unlock()
	h.broadcastDaemonStatus(workspace, id, label, relay.StatusConnected, now)
}

func (h *hub) daemonDisconnected(workspace, id string) {
	now := time.Now().Unix()
	h.mu.Lock()
	ws := h.workspaces[workspace]
	if ws == nil {
		h.mu.Unlock()
		return
	}
	d := ws.daemons[id]
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
	delete(ws.daemons, id)
	delete(ws.sessions, id)
	delete(ws.agents, id)
	// Drop the tenant's container once its last daemon leaves so a churn of
	// short-lived workspaces doesn't grow the map unbounded; it is recreated
	// lazily on the next daemon hello. daemons is the authoritative lifecycle
	// key — every sessions/agents entry is keyed by a connected daemon's id and
	// deleted above — so an empty daemons map means the container is empty. A
	// client still connected to this workspace keeps receiving live frames via
	// fanout (which filters by the client's own workspace, independent of this
	// map).
	if len(ws.daemons) == 0 {
		delete(h.workspaces, workspace)
	}
	h.mu.Unlock()

	// Announce the disconnect and let clients decide how to present the now-
	// orphaned rows (#540 "fade, don't delete"): the relay no longer fans out a
	// session_deleted per session, so a flapping link doesn't yank rows out from
	// under a client. macOS fades them and restores on reconnect; the web
	// dashboard drops them. The cache is still evicted, so /api/v1/sessions and
	// any late-joining client reflect only live daemons, and a reconnect
	// reconciles via a fresh daemon_snapshot.
	h.broadcastDaemonStatus(workspace, id, label, relay.StatusDisconnected, now)
}

// --- client side ---

func (h *hub) serveClient(conn *websocket.Conn, tokenID, workspace string) {
	cc := &clientConn{conn: conn, send: make(chan []byte, 64), done: make(chan struct{}), tokenID: tokenID, workspace: workspace}

	// Register before snapshotting so no daemon_status fired between the two
	// is missed: any daemon present at snapshot time is in the snapshot, any
	// connecting after registration arrives as a (possibly duplicate, but
	// idempotent) daemon_status.
	h.mu.Lock()
	h.clients[cc] = struct{}{}
	h.mu.Unlock()
	defer h.removeClient(cc)

	if data, err := json.Marshal(h.clientSnapshot(workspace)); err == nil {
		// Non-blocking, like every other send: the write pump that drains
		// cc.send only starts below, so a blocking send to a buffer already
		// filled by concurrent fan-out (between registration and here) would
		// hang the connection forever. Worst case the tooltip seeds from the
		// next daemon_status instead of this snapshot.
		select {
		case cc.send <- data:
		default:
		}
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
	if ws := h.workspaces[cc.workspace]; ws != nil {
		for did, m := range ws.sessions {
			for _, s := range m {
				items = append(items, item{did, s})
			}
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
			// A client mostly receives, so its read pump rarely runs; check for a
			// mid-session revoke here (and on the ping tick) so a revoked client is
			// closed within one fan-out frame or ping interval.
			if h.closeIfRevoked(cc.conn, cc.tokenID) {
				return
			}
			cc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := cc.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		case <-ticker.C:
			if h.closeIfRevoked(cc.conn, cc.tokenID) {
				return
			}
			cc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := cc.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// --- fan-out + snapshots ---

func (h *hub) fanoutPush(workspace, daemonID string, msg outbound.PushMessage) {
	data, err := json.Marshal(relay.Push{Type: relay.MsgPush, Source: daemonID, TS: time.Now().Unix(), Msg: msg})
	if err != nil {
		return
	}
	h.fanout(workspace, data)
}

func (h *hub) broadcastDaemonStatus(workspace, id, label, status string, since int64) {
	data, err := json.Marshal(relay.DaemonStatus{
		Type: relay.MsgDaemonStatus, DaemonID: id, DaemonLabel: label, Status: status, Since: since,
	})
	if err != nil {
		return
	}
	h.fanout(workspace, data)
}

// fanout delivers one frame to every client in workspace, dropping it for any
// client whose buffer is full (mirrors the daemon push service's slow-consumer
// policy). Scoping by workspace is the isolation boundary: a daemon's frames
// never reach a client outside its tenant.
func (h *hub) fanout(workspace string, data []byte) {
	h.mu.Lock()
	targets := make([]*clientConn, 0, len(h.clients))
	for cc := range h.clients {
		if cc.workspace == workspace {
			targets = append(targets, cc)
		}
	}
	h.mu.Unlock()
	for _, cc := range targets {
		select {
		case cc.send <- data:
		default: // slow client — drop
		}
	}
}

func (h *hub) clientSnapshot(workspace string) relay.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	daemons := make([]relay.DaemonInfo, 0)
	if ws := h.workspaces[workspace]; ws != nil {
		for id, d := range ws.daemons {
			daemons = append(daemons, relay.DaemonInfo{DaemonID: id, DaemonLabel: d.label, Status: relay.StatusConnected})
		}
	}
	return relay.Snapshot{Type: relay.MsgSnapshot, Daemons: daemons}
}

// buildSessions flattens one workspace's per-daemon caches into a list for the
// HTTP /api/v1/sessions handler. The workspace is the caller's token scope, so
// the response carries only that tenant's sessions.
func (h *hub) buildSessions(workspace string) []*session.SessionState {
	h.mu.Lock()
	defer h.mu.Unlock()
	var all []*session.SessionState
	if ws := h.workspaces[workspace]; ws != nil {
		for _, m := range ws.sessions {
			for _, s := range m {
				all = append(all, s)
			}
		}
	}
	return all
}

// buildAgents returns the union of one workspace's daemon adapter registries,
// deduped by adapter name (frontends key off Name). Always non-nil so the JSON
// is [].
func (h *hub) buildAgents(workspace string) []relay.AgentInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := make(map[string]bool)
	out := []relay.AgentInfo{}
	if ws := h.workspaces[workspace]; ws != nil {
		for _, infos := range ws.agents {
			for _, a := range infos {
				if seen[a.Name] {
					continue
				}
				seen[a.Name] = true
				out = append(out, a)
			}
		}
	}
	return out
}

func (h *hub) pingLoop(conn *websocket.Conn, done <-chan struct{}, tokenID string) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			// An idle daemon sends no frames, so the read loop's per-frame revoke
			// check never fires. Re-check here each tick (WriteControl is
			// concurrent-safe with these pings) so a revoked but quiet daemon is
			// closed within one ping interval rather than lingering authenticated.
			if h.closeIfRevoked(conn, tokenID) {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
