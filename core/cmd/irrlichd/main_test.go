package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"irrlicht/core/adapters/inbound/agents/aider"
	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/pi"
	"irrlicht/core/adapters/outbound/filesystem"
	wshub "irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/capacity"
)

// testAgents mirrors main.go's agents slice. Tests register the real
// production Agent declarations so the gate test catches any future
// adapter that ships without branding fields filled in.
func testAgents() []agent.Agent {
	return []agent.Agent{
		claudecode.Agent(),
		codex.Agent(),
		pi.Agent(),
		aider.Agent(),
		opencode.Agent(),
	}
}

func newTestStack(t *testing.T) (*httptest.Server, *filesystem.SessionRepository) {
	t.Helper()

	repo := filesystem.NewWithDir(t.TempDir())
	push := services.NewPushService()
	orchMonitor := services.NewOrchestratorMonitor(nil, push, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(repo, orchMonitor, nil, nil))
	mux.HandleFunc("GET /api/v1/agents", handleGetAgents(testAgents()))
	mux.HandleFunc("GET /api/v1/version", handleGetVersion("test"))
	mux.HandleFunc("GET /state", handleGetState(repo))
	hub := wshub.NewHub(push, nil)
	mux.HandleFunc("GET /api/v1/sessions/stream", hub.ServeWS)

	uiDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(uiDir, "index.html"), []byte("<h1>ok</h1>"), 0644); err != nil {
		t.Fatalf("write stub index.html: %v", err)
	}
	mux.Handle("/", http.FileServer(http.Dir(uiDir)))

	return httptest.NewServer(mux), repo
}

// seedSession saves a test session to the filesystem repo.
func seedSession(t *testing.T, repo *filesystem.SessionRepository, id, state string) {
	t.Helper()
	s := &session.SessionState{
		SessionID: id,
		State:     state,
		UpdatedAt: time.Now().Unix(),
	}
	if err := repo.Save(s); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

// TestGate_GetSessions verifies that GET /api/v1/sessions returns seeded sessions.
func TestGate_GetSessions(t *testing.T) {
	srv, repo := newTestStack(t)
	defer srv.Close()

	seedSession(t, repo, "gate-1", session.StateReady)

	resp, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status: got %d, want 200", resp.StatusCode)
	}

	var payload sessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	groups := payload.Groups
	if len(groups) == 0 {
		t.Fatal("expected at least one group")
	}
	found := false
	for _, g := range groups {
		for _, a := range g.Agents {
			if a.SessionID == "gate-1" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("gate-1 session not found in GET /api/v1/sessions")
	}
}

// TestGate_GetAgents verifies that GET /api/v1/agents returns every registered
// adapter with non-empty branding. Catches the original foot-gun (#260): a
// new adapter that forgets to fill DisplayName / IconSVGLight / IconSVGDark
// would make this test fail before reaching review.
// agentListEntry mirrors one entry of GET /api/v1/agents' response body.
type agentListEntry struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	IconSVGLight string `json:"icon_svg_light"`
	IconSVGDark  string `json:"icon_svg_dark"`
}

// assertAgentEntryValid checks that e is one of the expected adapters and
// carries non-empty display metadata, marking its name seen in wantNames.
func assertAgentEntryValid(t *testing.T, e agentListEntry, wantNames map[string]bool) {
	t.Helper()
	if _, expected := wantNames[e.Name]; !expected {
		t.Errorf("unexpected adapter %q in response", e.Name)
		return
	}
	wantNames[e.Name] = true
	if e.DisplayName == "" {
		t.Errorf("adapter %q: display_name must not be empty", e.Name)
	}
	if e.IconSVGLight == "" {
		t.Errorf("adapter %q: icon_svg_light must not be empty", e.Name)
	}
	if e.IconSVGDark == "" {
		t.Errorf("adapter %q: icon_svg_dark must not be empty", e.Name)
	}
}

func TestGate_GetAgents(t *testing.T) {
	srv, _ := newTestStack(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/agents")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var entries []agentListEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}

	wantNames := map[string]bool{
		"claude-code": false,
		"codex":       false,
		"pi":          false,
		"aider":       false,
		"opencode":    false,
	}
	for _, e := range entries {
		assertAgentEntryValid(t, e, wantNames)
	}
	for name, seen := range wantNames {
		if !seen {
			t.Errorf("expected adapter %q in response, missing", name)
		}
	}
}

// TestGate_WebSocketConnect verifies that a WebSocket client can connect to the stream endpoint.
func TestGate_WebSocketConnect(t *testing.T) {
	srv, _ := newTestStack(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/sessions/stream"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()
}

// TestGate_WebSocketRejectsForeignOrigin verifies that the stream endpoint
// refuses cross-site WebSocket handshakes.
func TestGate_WebSocketRejectsForeignOrigin(t *testing.T) {
	srv, _ := newTestStack(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/sessions/stream"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Origin": []string{"https://evil.example.com"},
	})
	if err == nil {
		t.Fatal("expected handshake to fail for foreign origin")
	}
	if resp == nil {
		t.Fatalf("expected HTTP response on rejection, got nil (err=%v)", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestResolveBindAddr(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", defaultBindAddr},
		{"garbage", defaultBindAddr},
		{"127.0.0.1:7837", "127.0.0.1:7837"},
		{"0.0.0.0:7837", "0.0.0.0:7837"},
		{":7837", ":7837"},
	}
	for _, tt := range tests {
		if got := resolveBindAddr(tt.in); got != tt.want {
			t.Errorf("resolveBindAddr(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDataDir(t *testing.T) {
	// Default: derived from the passed-in home dir.
	t.Run("default", func(t *testing.T) {
		t.Setenv("IRRLICHT_HOME", "")
		want := filepath.Join("/home/alice", ".local", "share", "irrlicht")
		if got := dataDir("/home/alice"); got != want {
			t.Errorf("dataDir(home) = %q, want %q", got, want)
		}
	})

	// Default with empty home (lookup failed): /tmp fallback.
	t.Run("empty home", func(t *testing.T) {
		t.Setenv("IRRLICHT_HOME", "")
		if got := dataDir(""); got != "/tmp/irrlicht" {
			t.Errorf("dataDir(\"\") = %q, want /tmp/irrlicht", got)
		}
	})

	// IRRLICHT_HOME overrides the whole tree and ignores the home arg.
	t.Run("override", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("IRRLICHT_HOME", dir)
		if got := dataDir("/home/alice"); got != dir {
			t.Errorf("dataDir with IRRLICHT_HOME = %q, want %q", got, dir)
		}
		if got := dataDir(""); got != dir {
			t.Errorf("dataDir(\"\") with IRRLICHT_HOME = %q, want %q", got, dir)
		}
	})
}

func TestStateStoreDir(t *testing.T) {
	// Without IRRLICHT_HOME, returns "" so callers keep their production default.
	t.Run("unset keeps default", func(t *testing.T) {
		t.Setenv("IRRLICHT_HOME", "")
		for _, sub := range []string{"instances", "cost", "sessions"} {
			if got := stateStoreDir(sub); got != "" {
				t.Errorf("stateStoreDir(%q) = %q, want \"\" when IRRLICHT_HOME unset", sub, got)
			}
		}
	})

	// With IRRLICHT_HOME, every store nests beneath it for full isolation.
	t.Run("override nests stores", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("IRRLICHT_HOME", dir)
		for _, sub := range []string{"instances", "cost", "sessions"} {
			want := filepath.Join(dir, sub)
			if got := stateStoreDir(sub); got != want {
				t.Errorf("stateStoreDir(%q) = %q, want %q", sub, got, want)
			}
		}
	})
}

// TestGate_GetState verifies that GET /state returns the compact debug-state format.
func TestGate_GetState(t *testing.T) {
	srv, repo := newTestStack(t)
	defer srv.Close()

	seedSession(t, repo, "state-gate-1", session.StateWorking)

	resp, err := http.Get(srv.URL + "/state")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /state status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}

	var state struct {
		Sessions []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"sessions"`
		SessionCount int    `json:"sessionCount"`
		LastUpdated  string `json:"lastUpdated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.LastUpdated == "" {
		t.Error("lastUpdated must not be empty")
	}
	if state.SessionCount != len(state.Sessions) {
		t.Errorf("sessionCount %d != len(sessions) %d", state.SessionCount, len(state.Sessions))
	}
	found := false
	for _, s := range state.Sessions {
		if s.ID == "state-gate-1" {
			found = true
			if s.State == "" {
				t.Error("sessions[].state must not be empty")
			}
		}
	}
	if !found {
		t.Error("state-gate-1 not found in GET /state response")
	}
}

// TestHandleGetSessions_AttachesGroupCosts verifies that /api/v1/sessions
// embeds per-timeframe cost totals on non-orchestrator groups when a
// CostTracker is wired, and that it omits `costs` when the tracker is nil.
func TestHandleGetSessions_AttachesGroupCosts(t *testing.T) {
	repoDir := t.TempDir()
	costDir := filepath.Join(t.TempDir(), "cost")
	if err := os.MkdirAll(costDir, 0o700); err != nil {
		t.Fatalf("mkdir cost dir: %v", err)
	}

	// Seed a cost JSONL for project "proj-a" with a pre-window baseline
	// (10h ago, $1.00) and a current row (1h ago, $1.25). Trailing-window
	// deltas should be $0.25 for day / week / month / year.
	now := time.Now().Unix()
	writeCostRow(t, costDir, "proj-a", now-10*3600, "sess-1", 1.00)
	writeCostRow(t, costDir, "proj-a", now-1*3600, "sess-1", 1.25)

	repo := filesystem.NewWithDir(repoDir)
	tracker := filesystem.NewCostTrackerWithDir(costDir)

	// Seed a session whose project name matches the cost file so
	// BuildDashboard groups it under "proj-a".
	if err := repo.Save(&session.SessionState{
		SessionID:   "sess-1",
		State:       session.StateReady,
		ProjectName: "proj-a",
		FirstSeen:   now - 10*3600,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	push := services.NewPushService()
	orchMonitor := services.NewOrchestratorMonitor(nil, push, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(repo, orchMonitor, tracker, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var payload sessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}

	projA := findAgentGroup(payload.Groups, "proj-a")
	if projA == nil {
		t.Fatalf("proj-a group missing from response: %+v", payload.Groups)
	}
	if projA.Costs == nil {
		t.Fatalf("proj-a.Costs must not be nil")
	}
	assertPositiveCostForAllTimeframes(t, "proj-a", projA.Costs)
	// The pre-window baseline $1.00 → max $1.25 ⇒ delta $0.25 for every
	// window. Floating-point comparison tolerates tiny scanner artefacts.
	if got := projA.Costs["day"]; got < 0.24 || got > 0.26 {
		t.Errorf("proj-a.Costs[day]: want ≈0.25, got %v", got)
	}
}

// findAgentGroup returns the group named name, or nil if none matches.
func findAgentGroup(groups []*session.AgentGroup, name string) *session.AgentGroup {
	for _, g := range groups {
		if g.Name == name {
			return g
		}
	}
	return nil
}

// assertPositiveCostForAllTimeframes checks that costs carries a positive
// value for every trailing window ("day", "week", "month", "year").
func assertPositiveCostForAllTimeframes(t *testing.T, label string, costs map[string]float64) {
	t.Helper()
	for _, tf := range []string{"day", "week", "month", "year"} {
		if v, ok := costs[tf]; !ok || v <= 0 {
			t.Errorf("%s.Costs[%q]: want > 0, got %v (ok=%v)", label, tf, v, ok)
		}
	}
}

// TestHandleGetSessions_AttachesProviderCosts verifies that /api/v1/sessions
// surfaces the top-level provider_costs map (providerKey → timeframe → USD),
// keyed by the row's provider rather than its project.
func TestHandleGetSessions_AttachesProviderCosts(t *testing.T) {
	repoDir := t.TempDir()
	costDir := filepath.Join(t.TempDir(), "cost")
	if err := os.MkdirAll(costDir, 0o700); err != nil {
		t.Fatalf("mkdir cost dir: %v", err)
	}

	// Anthropic-stamped rows: $1.00 baseline (10h ago) → $1.25 now ⇒ $0.25
	// in every trailing window.
	now := time.Now().Unix()
	writeCostRowWithProvider(t, costDir, "proj-a", "anthropic", now-10*3600, "sess-1", 1.00)
	writeCostRowWithProvider(t, costDir, "proj-a", "anthropic", now-1*3600, "sess-1", 1.25)

	repo := filesystem.NewWithDir(repoDir)
	tracker := filesystem.NewCostTrackerWithDir(costDir)
	if err := repo.Save(&session.SessionState{
		SessionID:   "sess-1",
		State:       session.StateReady,
		ProjectName: "proj-a",
		FirstSeen:   now - 10*3600,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	push := services.NewPushService()
	orchMonitor := services.NewOrchestratorMonitor(nil, push, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(repo, orchMonitor, tracker, nil))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}

	var payload sessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	anthropic := payload.ProviderCosts["anthropic"]
	if anthropic == nil {
		t.Fatalf("provider_costs missing anthropic key: %+v", payload.ProviderCosts)
	}
	for _, tf := range []string{"day", "week", "month", "year"} {
		if v, ok := anthropic[tf]; !ok || v < 0.24 || v > 0.26 {
			t.Errorf("provider_costs[anthropic][%q]: want ≈0.25, got %v (ok=%v)", tf, v, ok)
		}
	}
}

// TestHandleGetSessions_OmitsCostsWhenTrackerNil keeps the no-tracker path
// honest: the response must parse cleanly and groups must not carry a
// costs field.
func TestHandleGetSessions_OmitsCostsWhenTrackerNil(t *testing.T) {
	srv, repo := newTestStack(t)
	defer srv.Close()
	seedSession(t, repo, "no-cost-1", session.StateReady)

	resp, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var payload sessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.ProviderCosts != nil {
		t.Errorf("provider_costs must be nil when tracker is nil, got %+v", payload.ProviderCosts)
	}
	for _, g := range payload.Groups {
		if g.Costs != nil {
			t.Errorf("group %q: Costs must be nil when tracker is nil, got %+v", g.Name, g.Costs)
		}
	}
}

// writeCostRow appends a raw JSONL row to <costDir>/<project>.jsonl matching
// the CostTracker's on-disk schema, with no provider set. Used to seed
// historical rows that RecordSnapshot (which stamps time.Now) cannot
// produce. Thin wrapper over writeCostRowWithProvider — CostTracker decodes
// a row's Provider field via json.Unmarshal, which treats an omitted key and
// an explicit empty string identically, so the two share one implementation.
func writeCostRow(t *testing.T, costDir, project string, ts int64, sessionID string, cost float64) {
	t.Helper()
	writeCostRowWithProvider(t, costDir, project, "", ts, sessionID, cost)
}

// writeCostRowWithProvider appends a raw JSONL row to <costDir>/<project>.jsonl
// matching the CostTracker's on-disk schema, for exercising the per-provider
// rollup surfaced as provider_costs. An empty provider is written as
// `"provider":""`, decoding the same as an omitted field (see writeCostRow).
func writeCostRowWithProvider(t *testing.T, costDir, project, provider string, ts int64, sessionID string, cost float64) {
	t.Helper()
	line := fmt.Sprintf(`{"ts":%d,"project":%q,"provider":%q,"session":%q,"cost":%g}`+"\n", ts, project, provider, sessionID, cost)
	path := filepath.Join(costDir, project+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestRunCapacityRefreshLoop_RetriesUntilSuccess verifies that a server
// failing the first few requests is retried with backoff until it recovers,
// after which the cache file is written.
func TestRunCapacityRefreshLoop_RetriesUntilSuccess(t *testing.T) {
	var hits atomic.Int32
	const failUntil = 3

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n <= failUntil {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"claude-sonnet-4-6": {
				"max_input_tokens": 200000,
				"max_output_tokens": 64000,
				"input_cost_per_token": 0.000003,
				"output_cost_per_token": 0.000015,
				"litellm_provider": "anthropic",
				"mode": "chat"
			}
		}`))
	}))
	defer srv.Close()

	capacity.SetLiteLLMURLForTest(t, srv.URL)
	t.Setenv("HOME", t.TempDir())

	logger := &capturingLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		runCapacityRefreshLoop(ctx, logger, 5*time.Millisecond, 50*time.Millisecond, time.Hour)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !logger.hasInfo() {
		time.Sleep(10 * time.Millisecond)
	}
	if !logger.hasInfo() {
		t.Fatalf("expected success log within 5s; errors=%d", logger.errorCount())
	}
	if got := logger.errorCount(); got < failUntil {
		t.Errorf("expected at least %d error logs before success, got %d", failUntil, got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit after context cancel")
	}
}

type capturingLogger struct {
	mu     sync.Mutex
	infos  []string
	errors []string
}

func (l *capturingLogger) LogInfo(eventType, sessionID, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, message)
}
func (l *capturingLogger) LogError(eventType, sessionID, errorMsg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, errorMsg)
}
func (l *capturingLogger) LogProcessingTime(string, string, int64, int, string) {}
func (l *capturingLogger) Close() error                                         { return nil }

func (l *capturingLogger) hasInfo() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.infos) > 0
}
func (l *capturingLogger) errorCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.errors)
}

// TestGate_UIServed verifies that GET / returns 200 with HTML content.
func TestGate_UIServed(t *testing.T) {
	srv, _ := newTestStack(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status: got %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type: got %q, want text/html", ct)
	}
}

// TestResolveUIDir covers each branch of the runtime UI lookup and the
// worktree-isolation guarantee of the dev walk-up.
// writeMarkerFile creates dir and writes a single marker file (name/content)
// inside it, shared by writeIndexUI (index.html) and markRepoRoot (.git) —
// otherwise identical mkdir+write+Fatalf boilerplate.
func writeMarkerFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// writeIndexUI creates dir/index.html and returns dir, standing in for an
// installed dashboard across TestResolveUIDir's subtests.
func writeIndexUI(t *testing.T, dir string) string {
	t.Helper()
	writeMarkerFile(t, dir, "index.html", "ok")
	return dir
}

// markRepoRoot writes a .git marker file in dir (mimics a worktree's .git file).
func markRepoRoot(t *testing.T, dir string) {
	t.Helper()
	writeMarkerFile(t, dir, ".git", "gitdir: x")
}

func testResolveUIDirEnvWinsWhenIndexPresent(t *testing.T) {
	envDir := writeIndexUI(t, filepath.Join(t.TempDir(), "env"))
	if got := resolveUIDirFor(envDir, "", ""); got != envDir {
		t.Errorf("got %q, want %q", got, envDir)
	}
}

func testResolveUIDirEnvWithoutIndexFallsThrough(t *testing.T) {
	emptyEnv := t.TempDir()
	homeDir := t.TempDir()
	webDir := writeIndexUI(t, filepath.Join(homeDir, ".local", "share", "irrlicht", "web"))
	if got := resolveUIDirFor(emptyEnv, "", homeDir); got != webDir {
		t.Errorf("got %q, want %q", got, webDir)
	}
}

func testResolveUIDirExeResources(t *testing.T) {
	bundle := t.TempDir()
	exe := filepath.Join(bundle, "MacOS", "irrlichd")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("mkdir MacOS: %v", err)
	}
	want := writeIndexUI(t, filepath.Join(bundle, "Resources", "web"))
	got := resolveUIDirFor("", exe, "")
	// filepath.Join collapses ../, but the lookup uses
	// <exe>/../Resources/web literally — compare via Clean.
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func testResolveUIDirHomeInstall(t *testing.T) {
	homeDir := t.TempDir()
	want := writeIndexUI(t, filepath.Join(homeDir, ".local", "share", "irrlicht", "web"))
	if got := resolveUIDirFor("", "", homeDir); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func testResolveUIDirDevWalkUpFindsRepoPlatformsWeb(t *testing.T) {
	repo := t.TempDir()
	markRepoRoot(t, repo)
	want := writeIndexUI(t, filepath.Join(repo, "platforms", "web"))
	exe := filepath.Join(repo, "core", "bin", "irrlichd")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if got := resolveUIDirFor("", exe, ""); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func testResolveUIDirWalkUpDoesNotEscapeIntoOuterRepo(t *testing.T) {
	// Outer "fake parent repo" with platforms/web/index.html — must NOT
	// be served when the binary lives inside an inner repo (worktree).
	outer := t.TempDir()
	markRepoRoot(t, outer)
	writeIndexUI(t, filepath.Join(outer, "platforms", "web"))

	inner := filepath.Join(outer, "worktrees", "wt-1")
	markRepoRoot(t, inner) // inner has .git but no platforms/web/
	exe := filepath.Join(inner, "core", "bin", "irrlichd")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if got := resolveUIDirFor("", exe, ""); got != "" {
		t.Errorf("expected isolation (empty), got %q", got)
	}
}

func testResolveUIDirNoSourceMatchesReturnsEmpty(t *testing.T) {
	// exe in a tree with no .git anywhere → walk-up exhausts → "".
	emptyHome := t.TempDir()
	exe := filepath.Join(t.TempDir(), "elsewhere", "irrlichd")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := resolveUIDirFor("", exe, emptyHome); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestResolveUIDir(t *testing.T) {
	t.Run("env wins when index present", testResolveUIDirEnvWinsWhenIndexPresent)
	t.Run("env without index falls through", testResolveUIDirEnvWithoutIndexFallsThrough)
	t.Run("exe Resources/web (production .app)", testResolveUIDirExeResources)
	t.Run("home .local/share/irrlicht/web (daemon-only install)", testResolveUIDirHomeInstall)
	t.Run("dev walk-up finds repo platforms/web", testResolveUIDirDevWalkUpFindsRepoPlatformsWeb)
	t.Run("walk-up does not escape inner repo into outer", testResolveUIDirWalkUpDoesNotEscapeIntoOuterRepo)
	t.Run("no source matches returns empty", testResolveUIDirNoSourceMatchesReturnsEmpty)
}
