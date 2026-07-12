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
	return newTestServerWithLimits(t, defaultLimits())
}

func newTestServerWithLimits(t *testing.T, lim limits) (wsURL, baseURL string) {
	t.Helper()
	h := newHub(lim)
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

// connectClient dials wsURL and sends a client hello (token may be "" when
// auth is off), failing the test if the write errors.
func connectClient(t *testing.T, wsURL, token string) *websocket.Conn {
	t.Helper()
	c := dial(t, wsURL)
	if err := c.WriteJSON(relay.Hello{Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion, Role: relay.RoleClient, Token: token}); err != nil {
		t.Fatal(err)
	}
	return c
}

// connectDaemon dials wsURL and sends a daemon hello, returning the
// connection and the parsed hello_ack for the caller to assert on.
func connectDaemon(t *testing.T, wsURL, daemonID, label string) (*websocket.Conn, relay.HelloAck) {
	t.Helper()
	d := dial(t, wsURL)
	if err := d.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
		Role: relay.RoleDaemon, DaemonID: daemonID, DaemonLabel: label,
	}); err != nil {
		t.Fatal(err)
	}
	var ack relay.HelloAck
	if err := d.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	return d, ack
}

// daemonSession bundles the auth token and the single session that
// connectDaemonWithSession seeds via daemon_snapshot after the hello
// handshake.
type daemonSession struct {
	token string
	sid   string
	proj  string
}

// connectDaemonWithSession dials wsURL, sends a daemon hello (optionally
// bearer token-authenticated), and pushes a one-session daemon_snapshot — the
// connect-and-seed pattern used to test workspace isolation between two
// same-daemon-id tenants.
func connectDaemonWithSession(t *testing.T, wsURL string, ds daemonSession) *websocket.Conn {
	t.Helper()
	d := dial(t, wsURL)
	if err := d.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
		Role: relay.RoleDaemon, DaemonID: "d1", DaemonLabel: "host", Token: ds.token,
	}); err != nil {
		t.Fatal(err)
	}
	var ack relay.HelloAck
	if err := d.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteJSON(relay.DaemonSnapshot{
		Type:     relay.MsgDaemonSnapshot,
		Sessions: []*session.SessionState{{SessionID: ds.sid, State: "working", ProjectName: ds.proj}},
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

// waitForDaemonDisconnect reads frames from c until daemonID's disconnect
// status arrives, failing the test if that never happens within 2s. Every
// frame is also passed to onFrame first (when non-nil), so callers can assert
// on other traffic — e.g. no unexpected session_deleted — while waiting.
func waitForDaemonDisconnect(t *testing.T, c *websocket.Conn, daemonID string, onFrame func(data []byte)) {
	t.Helper()
	sawDisconnect := false
	deadline := time.Now().Add(2 * time.Second)
	for !sawDisconnect && time.Now().Before(deadline) {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("reading after daemon close: %v", err)
		}
		if onFrame != nil {
			onFrame(data)
		}
		if relay.FrameType(data) == relay.MsgDaemonStatus {
			var ds relay.DaemonStatus
			mustJSON(t, data, &ds)
			if ds.DaemonID == daemonID && ds.Status == relay.StatusDisconnected {
				sawDisconnect = true
			}
		}
	}
	if !sawDisconnect {
		t.Fatal("never observed the daemon disconnect status")
	}
}

// rejectSessionDeleted returns a waitForDaemonDisconnect onFrame callback
// that fails the test if a session_deleted push for sid arrives — used to
// prove disconnect fades rows rather than deleting them (#540).
func rejectSessionDeleted(t *testing.T, sid string) func(data []byte) {
	return func(data []byte) {
		if relay.FrameType(data) != relay.MsgPush {
			return
		}
		var p relay.Push
		mustJSON(t, data, &p)
		if p.Msg.Type == outbound.PushTypeDeleted && p.Msg.Session != nil && p.Msg.Session.SessionID == sid {
			t.Fatal("unexpected session_deleted on disconnect — rows must fade, not delete (#540)")
		}
	}
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

// httpGetAuth issues a bearer-authenticated GET, asserting a 200, and returns
// the body. Used to read a workspace-scoped slice of /api/v1/sessions.
func httpGetAuth(t *testing.T, url, token string) []byte {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	return body
}

// assertNeverSeesSession reads frames from c until within elapses and fails if
// a push for sid arrives — the core cross-tenant leakage assertion. A read
// deadline (or close) ends the wait cleanly with no leak observed.
func assertNeverSeesSession(t *testing.T, c *websocket.Conn, sid string, within time.Duration) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(within))
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return // deadline or close: nothing leaked
		}
		if relay.FrameType(data) != relay.MsgPush {
			continue
		}
		var p relay.Push
		if json.Unmarshal(data, &p) == nil && p.Msg.Session != nil && p.Msg.Session.SessionID == sid {
			t.Fatalf("workspace leak: client saw session %q from another tenant", sid)
		}
	}
}

func TestRelayRoundTrip(t *testing.T) {
	wsURL, baseURL := newTestServer(t)

	// Client connects first — no daemons yet, so the snapshot is empty.
	client := connectClient(t, wsURL, "")
	var snap relay.Snapshot
	mustJSON(t, readUntil(t, client, relay.MsgSnapshot), &snap)
	if len(snap.Daemons) != 0 {
		t.Fatalf("expected no daemons initially, got %+v", snap.Daemons)
	}

	// Daemon connects, handshakes, and reconciles one session.
	daemon, ack := connectDaemon(t, wsURL, "d1", "laptop")
	if ack.Type != relay.MsgHelloAck || ack.AcceptedVersion != relay.ProtocolVersion {
		t.Fatalf("hello_ack: ack=%+v", ack)
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
	waitForDaemonDisconnect(t, client, "d1", nil)
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

func TestRelayDaemonDisconnectKeepsRowsClientSide(t *testing.T) {
	wsURL, baseURL := newTestServer(t)

	client := connectClient(t, wsURL, "")
	readUntil(t, client, relay.MsgSnapshot)

	daemon, _ := connectDaemon(t, wsURL, "d1", "laptop")
	if err := daemon.WriteJSON(relay.DaemonSnapshot{
		Type:     relay.MsgDaemonSnapshot,
		Sessions: []*session.SessionState{{SessionID: "s1", State: "working", ProjectName: "proj"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Drain the connect status + the snapshot-reconciliation push, and confirm
	// the session is cached.
	readUntil(t, client, relay.MsgDaemonStatus)
	readUntil(t, client, relay.MsgPush)
	if body := httpGet(t, baseURL+"/api/v1/sessions"); !bytes.Contains(body, []byte(`"session_id":"s1"`)) {
		t.Fatalf("expected s1 cached before disconnect, got %s", body)
	}

	// Daemon drops → the relay announces the disconnect but must NOT fan out a
	// session_deleted (#540 "fade, don't delete"): clients keep the rows and
	// decide how to present them. The cache is still evicted.
	daemon.Close()
	waitForDaemonDisconnect(t, client, "d1", rejectSessionDeleted(t, "s1"))
	if body := httpGet(t, baseURL+"/api/v1/sessions"); bytes.Contains(body, []byte(`"session_id":"s1"`)) {
		t.Fatalf("s1 should be evicted from the cache after daemon disconnect, got %s", body)
	}
}

// newTestServerWithAuth starts a relay with bearer-token auth backed by a
// tokens file seeded with the given labels, returning the ws URL and the
// plaintext token for each label.
func newTestServerWithAuth(t *testing.T, labels ...string) (wsURL string, tokens map[string]string, tokensPath string, store *authStore) {
	t.Helper()
	tokensPath = filepath.Join(t.TempDir(), "tokens.json")
	tokens = make(map[string]string, len(labels))
	for _, l := range labels {
		_, plaintext, err := issueToken(tokensPath, l, "")
		if err != nil {
			t.Fatalf("seed token %q: %v", l, err)
		}
		tokens[l] = plaintext
	}
	store, err := newAuthStore(tokensPath)
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}
	h := newHubWithAuth(store, nil, defaultLimits())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions/stream", h.ServeWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/sessions/stream", tokens, tokensPath, store
}

// expectClose4401 reads from c expecting the relay to close with CloseRevoked.
func expectClose4401(t *testing.T, c *websocket.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, _, err := c.ReadMessage()
		if err == nil {
			continue // skip any buffered data frames until the close arrives
		}
		if ce, ok := err.(*websocket.CloseError); ok {
			if ce.Code != relay.CloseRevoked {
				t.Fatalf("close code = %d, want %d", ce.Code, relay.CloseRevoked)
			}
			return
		}
		t.Fatalf("expected a 4401 close, got: %v", err)
	}
}

func TestAuthDaemonAcceptedWithValidToken(t *testing.T) {
	wsURL, tokens, _, _ := newTestServerWithAuth(t, "daemon")
	c := dial(t, wsURL)
	if err := c.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
		Role: relay.RoleDaemon, DaemonID: "d1", DaemonLabel: "laptop", Token: tokens["daemon"],
	}); err != nil {
		t.Fatal(err)
	}
	var ack relay.HelloAck
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := c.ReadJSON(&ack); err != nil || ack.Type != relay.MsgHelloAck {
		t.Fatalf("expected hello_ack for a valid token: err=%v ack=%+v", err, ack)
	}
}

func TestAuthRejectsMissingAndBadToken(t *testing.T) {
	wsURL, tokens, _, _ := newTestServerWithAuth(t, "good")

	// Daemon with no token.
	d := dial(t, wsURL)
	if err := d.WriteJSON(relay.Hello{Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion, Role: relay.RoleDaemon, DaemonID: "d1"}); err != nil {
		t.Fatal(err)
	}
	expectClose4401(t, d)

	// Client with a wrong token.
	c := dial(t, wsURL)
	if err := c.WriteJSON(relay.Hello{Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion, Role: relay.RoleClient, Token: tokens["good"] + "x"}); err != nil {
		t.Fatal(err)
	}
	expectClose4401(t, c)
}

func TestAuthRevokeClosesLiveDaemon(t *testing.T) {
	wsURL, tokens, tokensPath, store := newTestServerWithAuth(t, "daemon")
	recs, _ := loadTokens(tokensPath)
	id := recs[0].ID

	d := dial(t, wsURL)
	if err := d.WriteJSON(relay.Hello{
		Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion,
		Role: relay.RoleDaemon, DaemonID: "d1", DaemonLabel: "laptop", Token: tokens["daemon"],
	}); err != nil {
		t.Fatal(err)
	}
	var ack relay.HelloAck
	if err := d.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}

	// Revoke the token out-of-band and reload the live store as the poll loop
	// would. The daemon's next frame must then be closed with 4401.
	if ok, err := revokeToken(tokensPath, id); err != nil || !ok {
		t.Fatalf("revoke: ok=%v err=%v", ok, err)
	}
	if err := store.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	// Send a frame to trigger the read loop's revocation check.
	if err := d.WriteJSON(relay.DaemonSnapshot{Type: relay.MsgDaemonSnapshot}); err != nil {
		t.Fatal(err)
	}
	expectClose4401(t, d)
}

// TestWorkspaceIsolation proves a connection only ever sees its own tenant's
// sessions — over WS replay, over a live push, and over the HTTP read API —
// even when a daemon in another workspace claims a colliding daemon_id.
func TestWorkspaceIsolation(t *testing.T) {
	tokensPath := filepath.Join(t.TempDir(), "tokens.json")
	_, daemonA := mustIssueToken(t, tokensPath, "daemon-a", "ws-a")
	_, clientA := mustIssueToken(t, tokensPath, "client-a", "ws-a")
	_, daemonB := mustIssueToken(t, tokensPath, "daemon-b", "ws-b")
	_, clientB := mustIssueToken(t, tokensPath, "client-b", "ws-b")

	store, err := newAuthStore(tokensPath)
	if err != nil {
		t.Fatalf("newAuthStore: %v", err)
	}
	h := newHubWithAuth(store, nil, defaultLimits())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions/stream", h.ServeWS)
	mux.HandleFunc("GET /api/v1/sessions", requireToken(store, handleSessions(h)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/sessions/stream"

	// Both daemons claim the SAME daemon_id "d1" but authenticate into different
	// workspaces, so a colliding id must not let one tenant read or overwrite
	// the other's slot.
	connectDaemonWithSession(t, wsURL, daemonSession{token: daemonA, sid: "sa", proj: "proj-a"})
	dB := connectDaemonWithSession(t, wsURL, daemonSession{token: daemonB, sid: "sb", proj: "proj-b"})
	// Let both snapshots ingest before the clients connect (the replay path).
	time.Sleep(50 * time.Millisecond)

	// Client A replays only ws-a's session, never ws-b's.
	ca := connectClient(t, wsURL, clientA)
	var p relay.Push
	mustJSON(t, readUntil(t, ca, relay.MsgPush), &p)
	if p.Msg.Session == nil || p.Msg.Session.SessionID != "sa" {
		t.Fatalf("client A replay = %+v; want session sa", p.Msg.Session)
	}

	cb := connectClient(t, wsURL, clientB)
	mustJSON(t, readUntil(t, cb, relay.MsgPush), &p)
	if p.Msg.Session == nil || p.Msg.Session.SessionID != "sb" {
		t.Fatalf("client B replay = %+v; want session sb", p.Msg.Session)
	}

	// HTTP reads are scoped by the bearer token's workspace, both directions.
	bodyA := httpGetAuth(t, srv.URL+"/api/v1/sessions", clientA)
	if !bytes.Contains(bodyA, []byte(`"session_id":"sa"`)) {
		t.Fatalf("client A HTTP missing sa: %s", bodyA)
	}
	if bytes.Contains(bodyA, []byte(`"session_id":"sb"`)) {
		t.Fatalf("client A HTTP leaked ws-b's sb: %s", bodyA)
	}
	bodyB := httpGetAuth(t, srv.URL+"/api/v1/sessions", clientB)
	if !bytes.Contains(bodyB, []byte(`"session_id":"sb"`)) {
		t.Fatalf("client B HTTP missing sb: %s", bodyB)
	}
	if bytes.Contains(bodyB, []byte(`"session_id":"sa"`)) {
		t.Fatalf("client B HTTP leaked ws-a's sa: %s", bodyB)
	}

	// A live delta in ws-b reaches client B but never client A.
	if err := dB.WriteJSON(relay.Push{
		Type: relay.MsgPush,
		Msg:  outbound.PushMessage{Type: outbound.PushTypeUpdated, Session: &session.SessionState{SessionID: "sb2", State: "ready"}},
	}); err != nil {
		t.Fatal(err)
	}
	mustJSON(t, readUntil(t, cb, relay.MsgPush), &p)
	if p.Msg.Session == nil || p.Msg.Session.SessionID != "sb2" {
		t.Fatalf("client B live delta = %+v; want sb2", p.Msg.Session)
	}
	assertNeverSeesSession(t, ca, "sb2", 300*time.Millisecond)
}

func TestOriginChecker(t *testing.T) {
	// Empty allowlist: every origin (and none) is allowed.
	allow := originChecker(nil)
	if !allow(reqWithOrigin("https://evil.example")) || !allow(reqWithOrigin("")) {
		t.Fatal("empty allowlist should allow all origins")
	}

	// Non-empty allowlist: only listed hosts; non-browser (no Origin) allowed.
	allow = originChecker([]string{"app.irrlicht.dev", "localhost:7839"})
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://app.irrlicht.dev", true},
		{"http://localhost:7839", true},
		{"https://evil.example", false},
		{"", true}, // native daemon / curl: no Origin header
		{"://malformed", false},
	}
	for _, c := range cases {
		if got := allow(reqWithOrigin(c.origin)); got != c.want {
			t.Errorf("origin %q: got %v, want %v", c.origin, got, c.want)
		}
	}
}

func reqWithOrigin(origin string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/sessions/stream", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestRequireTokenGatesHTTP(t *testing.T) {
	tokensPath := filepath.Join(t.TempDir(), "tokens.json")
	_, plaintext, err := issueToken(tokensPath, "http", "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := newAuthStore(tokensPath)
	if err != nil {
		t.Fatal(err)
	}
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	// Auth off: pass-through, no token needed.
	rec := httptest.NewRecorder()
	requireToken(nil, ok)(rec, httptest.NewRequest("GET", "/api/v1/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("auth off should pass through, got %d", rec.Code)
	}

	gated := requireToken(store, ok)

	// No token → 401.
	rec = httptest.NewRecorder()
	gated(rec, httptest.NewRequest("GET", "/api/v1/sessions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token should be 401, got %d", rec.Code)
	}

	// Valid token via query param → 200.
	rec = httptest.NewRecorder()
	gated(rec, httptest.NewRequest("GET", "/api/v1/sessions?token="+plaintext, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("valid ?token should be 200, got %d", rec.Code)
	}

	// Valid token via Authorization header → 200.
	rec = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	r.Header.Set("Authorization", "Bearer "+plaintext)
	gated(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid Bearer header should be 200, got %d", rec.Code)
	}

	// Wrong token → 401.
	rec = httptest.NewRecorder()
	gated(rec, httptest.NewRequest("GET", "/api/v1/sessions?token=nope", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token should be 401, got %d", rec.Code)
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
