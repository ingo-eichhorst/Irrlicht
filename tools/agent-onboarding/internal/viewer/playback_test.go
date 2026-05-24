package viewer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureRoot constructs a tiny replaydata tree with one scenario.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	scenarioDir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "test")
	if err := os.MkdirAll(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	events := strings.Join([]string{
		`{"seq":1,"ts":"` + start.Format(time.RFC3339Nano) + `","kind":"transcript_new","session_id":"s1","adapter":"claudecode"}`,
		`{"seq":2,"ts":"` + start.Add(time.Second).Format(time.RFC3339Nano) + `","kind":"state_transition","session_id":"s1","new_state":"working"}`,
		`{"seq":3,"ts":"` + start.Add(2*time.Second).Format(time.RFC3339Nano) + `","kind":"state_transition","session_id":"s1","new_state":"ready"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(scenarioDir, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	// platforms/web/{index.html,irrlicht.css,irrlicht.js} — a realistic
	// dashboard tree: index.html references its siblings relatively (as
	// the real one does since #418), so the viewer must serve the assets
	// too or the iframe renders unstyled (#459). The "test dashboard"
	// marker is kept for TestPlayback_startEndToEnd's body assertion.
	dashboardDir := filepath.Join(root, "platforms", "web")
	os.MkdirAll(dashboardDir, 0o755)
	indexHTML := `<!DOCTYPE html>
<html>
<head>
<link rel="stylesheet" href="irrlicht.css">
</head>
<body>
test dashboard
<script type="module" src="irrlicht.js"></script>
</body>
</html>`
	os.WriteFile(filepath.Join(dashboardDir, "index.html"), []byte(indexHTML), 0o644)
	os.WriteFile(filepath.Join(dashboardDir, "irrlicht.css"), []byte(":root{--marker:css}"), 0o644)
	os.WriteFile(filepath.Join(dashboardDir, "irrlicht.js"), []byte("export const marker = 'js';"), 0o644)
	// An arbitrarily-named sibling: the viewer serves platforms/web/ as a
	// fallback, so any dashboard asset resolves — not just irrlicht.css/js.
	os.WriteFile(filepath.Join(dashboardDir, "vendor.css"), []byte(".vendor{--marker:vendor}"), 0o644)
	return root
}

func TestPlayback_startEndToEnd(t *testing.T) {
	root := fixtureRoot(t)
	s := &Server{RepoRoot: root}
	h := s.Handler()

	body, _ := json.Marshal(map[string]any{
		"agent": "claudecode", "subtree": "scenarios", "scenario": "test",
		"mode": "viewer-internal", "speed": 1000.0,
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/replay/start", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("start: status=%d body=%s", rr.Code, rr.Body)
	}
	var resp startResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.PlaybackID == "" || resp.DashboardURL != "/dashboard" || resp.Mode != "viewer-internal" {
		t.Errorf("bad start response: %+v", resp)
	}

	// Give the state machine a moment to apply at least one event.
	time.Sleep(50 * time.Millisecond)

	// /api/v1/sessions should report the session.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/sessions", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("sessions: status=%d body=%s", rr.Code, rr.Body)
	}
	// The viewer returns the daemon's /api/v1/sessions envelope:
	// {"groups": [{"name": "<project>", "agents": [{"session_id": "...", ...}]}]}
	if !strings.Contains(rr.Body.String(), `"session_id":"s1"`) {
		t.Errorf("expected session s1 in /api/v1/sessions: %s", rr.Body)
	}

	// /api/v1/agents should return the synthetic agents list.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/agents", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"claudecode"`) {
		t.Errorf("agents endpoint wrong: %d %s", rr.Code, rr.Body)
	}

	// /dashboard should serve the platforms/web/index.html copy.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/dashboard", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "test dashboard") {
		t.Errorf("dashboard route wrong: %d %s", rr.Code, rr.Body)
	}

	// Stop cleanly.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/replay/stop", nil))
	if rr.Code != http.StatusNoContent {
		t.Errorf("stop status: %d", rr.Code)
	}
}

// TestPlayback_servesDashboardAssets guards #459: the dashboard's
// index.html references its assets relatively, so the viewer must serve
// the platforms/web/ siblings or the recording preview renders unstyled
// (the iframe 404s the assets). The vendor.css case proves the fallback
// resolves any sibling, not just the irrlicht.css/js pair.
func TestPlayback_servesDashboardAssets(t *testing.T) {
	root := fixtureRoot(t)
	s := &Server{RepoRoot: root}
	h := s.Handler()

	cases := []struct {
		path        string
		wantBody    string
		wantCTParts string
	}{
		{"/irrlicht.css", "--marker:css", "text/css"},
		{"/irrlicht.js", "marker = 'js'", "javascript"},
		{"/vendor.css", "--marker:vendor", "text/css"},
	}
	for _, tc := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", tc.path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status=%d body=%s", tc.path, rr.Code, rr.Body)
			continue
		}
		if !strings.Contains(rr.Body.String(), tc.wantBody) {
			t.Errorf("%s: body %q missing %q", tc.path, rr.Body, tc.wantBody)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, tc.wantCTParts) {
			t.Errorf("%s: Content-Type=%q want substring %q", tc.path, ct, tc.wantCTParts)
		}
	}

	// The platforms/web/ fallback must not shadow the embedded viewer SPA:
	// GET / serves the embedded index.html (the Recording Viewer), never
	// the fixture's platforms/web/index.html "test dashboard" marker.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /: status=%d", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "Recording Viewer") || strings.Contains(body, "test dashboard") {
		t.Errorf("GET / should serve the embedded viewer SPA, not the dashboard fixture: %.120q", body)
	}
}

func TestPlayback_pauseResumeSeek(t *testing.T) {
	root := fixtureRoot(t)
	s := &Server{RepoRoot: root}
	h := s.Handler()

	body, _ := json.Marshal(map[string]any{
		"agent": "claudecode", "subtree": "scenarios", "scenario": "test",
		"mode": "viewer-internal", "speed": 1.0, // slow so we can pause mid-stream
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/replay/start", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("start: %d", rr.Code)
	}

	for _, route := range []string{"/api/replay/pause", "/api/replay/resume", "/api/replay/seek?offset_ms=1500", "/api/replay/speed?speed=5.0"} {
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("POST", route, nil))
		if rr.Code != http.StatusNoContent {
			t.Errorf("%s: status=%d body=%s", route, rr.Code, rr.Body)
		}
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/api/replay/status", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"active":true`) {
		t.Errorf("status wrong: %d %s", rr.Code, rr.Body)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/replay/stop", nil))
}

func TestPlayback_rejectsTraversalInAgent(t *testing.T) {
	root := fixtureRoot(t)
	s := &Server{RepoRoot: root}
	h := s.Handler()

	body, _ := json.Marshal(map[string]any{
		"agent": "../etc", "subtree": "scenarios", "scenario": "test",
		"mode": "viewer-internal", "speed": 1.0,
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/replay/start", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("traversal not blocked: status=%d body=%s", rr.Code, rr.Body)
	}
}
