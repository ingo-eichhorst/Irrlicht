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

func TestRelayDaemonDisconnectDeletesSessions(t *testing.T) {
	wsURL, baseURL := newTestServer(t)

	client := dial(t, wsURL)
	if err := client.WriteJSON(relay.Hello{Type: relay.MsgHello, ProtocolVersion: relay.ProtocolVersion, Role: relay.RoleClient}); err != nil {
		t.Fatal(err)
	}
	readUntil(t, client, relay.MsgSnapshot)

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

	// Drain the connect status + the snapshot-reconciliation push, and confirm
	// the session is cached.
	readUntil(t, client, relay.MsgDaemonStatus)
	readUntil(t, client, relay.MsgPush)
	if body := httpGet(t, baseURL+"/api/v1/sessions"); !bytes.Contains(body, []byte(`"session_id":"s1"`)) {
		t.Fatalf("expected s1 cached before disconnect, got %s", body)
	}

	// Daemon drops → the relay must fan out a session_deleted for its cached
	// session and a disconnected status, and evict the session from the cache.
	daemon.Close()
	sawDelete, sawDisconnect := false, false
	deadline := time.Now().Add(2 * time.Second)
	for (!sawDelete || !sawDisconnect) && time.Now().Before(deadline) {
		client.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("reading after daemon close: %v", err)
		}
		switch relay.FrameType(data) {
		case relay.MsgPush:
			var p relay.Push
			mustJSON(t, data, &p)
			if p.Msg.Type == outbound.PushTypeDeleted && p.Msg.Session != nil && p.Msg.Session.SessionID == "s1" {
				sawDelete = true
			}
		case relay.MsgDaemonStatus:
			var ds relay.DaemonStatus
			mustJSON(t, data, &ds)
			if ds.DaemonID == "d1" && ds.Status == relay.StatusDisconnected {
				sawDisconnect = true
			}
		}
	}
	if !sawDelete {
		t.Fatal("no session_deleted push for the disconnected daemon's session")
	}
	if !sawDisconnect {
		t.Fatal("no daemon disconnect status")
	}
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
		_, plaintext, err := issueToken(tokensPath, l)
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
	_, plaintext, err := issueToken(tokensPath, "http")
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
