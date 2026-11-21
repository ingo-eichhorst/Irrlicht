package main

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

// updateUsageGoldens regenerates testdata/herdr-usage/*/want*.json:
//
//	UPDATE_HERDR_USAGE_GOLDENS=1 go test ./core/cmd/irrlicht-ls/ -run TestHerdrUsage -count=1
var updateUsageGoldens = os.Getenv("UPDATE_HERDR_USAGE_GOLDENS") == "1"

var usageClock = func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }

// serveFixture serves a committed /api/v1/sessions body.
func serveFixture(t *testing.T, path string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %s: %v", path, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sessions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func usageEnv(url, pane, socket string) herdrUsageEnv {
	return herdrUsageEnv{
		paneID:     pane,
		socketPath: socket,
		url:        url,
		client:     &http.Client{Timeout: 2 * time.Second},
		now:        usageClock,
	}
}

func runUsage(env herdrUsageEnv) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = runHerdrUsage(env, &out, &errb)
	return code, out.String(), errb.String()
}

// TestHerdrUsage_Goldens are LOCKS on the formatter: committed
// /api/v1/sessions bodies in, committed schema-1 documents out. Every golden
// is also decoded by TestHerdrUsage_GoldensMatchFactorysSchema.
func TestHerdrUsage_Goldens(t *testing.T) {
	cases := []usageGoldenCase{
		// A complete Claude Code session: every token type, a table price, a
		// rate-limit snapshot whose throughput window is not a schema-1 window.
		{"complete", "complete", "w1:p1", "", "want.json"},
		// An unpriced model, a provider-cost pi session, an adapter that reads
		// no cache tokens, an undeclared adapter, and a session with nothing
		// counted yet — each a null with its reason, never a zero.
		{"partial", "partial", "w1:p1", "", "want.json"},
		// A Claude Code parent with two subagents: the parent's own counts,
		// the children listed apart. And an opencode parent whose adapter has
		// not established exclusion: its counts are withheld.
		{"subagents claude-code", "subagents", "w1:p1", "", "want-claude-code.json"},
		{"subagents unestablished", "subagents", "w2:p1", "", "want-unestablished.json"},
		// Two panes: only the requested one is answered — never the other,
		// whichever of them a person has focused.
		{"two panes p1", "two-panes", "w1:p1", "", "want-p1.json"},
		{"two panes p2", "two-panes", "w1:p2", "", "want-p2.json"},
		// A pane id that exists on two herdr servers, told apart by socket.
		{"two sockets a", "two-sockets", "w1:p1", "/tmp/herdr-a/herdr.sock", "want-a.json"},
		{"two sockets b", "two-sockets", "w1:p1", "/tmp/herdr-b//herdr.sock", "want-b.json"},
		// A parent whose subagents the daemon has partly removed (#2061): the
		// removed ones come from finished_subagents, a grandchild included;
		// the one still live is listed once, from its live session, never
		// from its stale record — which, like the record of a session the
		// daemon removed and then admitted again, carries an earlier
		// FirstSeen and so would sort first; a record of another root's
		// child is not listed.
		{"finished subagents", "finished-subagents", "w1:p1", "", "want.json"},
		// A pane with no session: a valid document with no sessions.
		{"empty pane", "two-panes", "w9:p9", "", "want-empty.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkUsageGolden(t, tc) })
	}
}

// usageGoldenCase is one TestHerdrUsage_Goldens case: the fixture directory
// under testdata/herdr-usage, the pane and socket asked for, and the golden
// in that directory the document must equal.
type usageGoldenCase struct {
	name, fixture, pane, socket, golden string
}

func checkUsageGolden(t *testing.T, tc usageGoldenCase) {
	dir := filepath.Join("testdata", "herdr-usage", tc.fixture)
	srv := serveFixture(t, filepath.Join(dir, "sessions.json"))
	code, stdout, stderr := runUsage(usageEnv(srv.URL+"/api/v1/sessions", tc.pane, tc.socket))
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q; want 0 and nothing", code, stderr)
	}
	goldenPath := filepath.Join(dir, tc.golden)
	want := readUsageGolden(t, goldenPath, []byte(stdout))
	if stdout != string(want) {
		t.Errorf("document differs from %s\n--- got ---\n%s\n--- want ---\n%s", goldenPath, stdout, want)
	}
	if strings.Contains(stdout, "prompt text") {
		t.Error("the document carries transcript text; it must carry usage fields only")
	}
}

// readUsageGolden reads a golden, first rewriting it from got when
// UPDATE_HERDR_USAGE_GOLDENS=1.
func readUsageGolden(t *testing.T, goldenPath string, got []byte) []byte {
	if updateUsageGoldens {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden %s: %v (regenerate with UPDATE_HERDR_USAGE_GOLDENS=1)", goldenPath, err)
	}
	return want
}

// TestHerdrUsage_SamePaneOnTwoServersIsRefused: with no socket to tell them
// apart, a pane id seen on two herdr servers is an error, not a guess.
func TestHerdrUsage_SamePaneOnTwoServersIsRefused(t *testing.T) {
	srv := serveFixture(t, filepath.Join("testdata", "herdr-usage", "two-sockets", "sessions.json"))
	code, stdout, stderr := runUsage(usageEnv(srv.URL+"/api/v1/sessions", "w1:p1", ""))
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "ambiguous pane w1:p1") || strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr %q, want one line naming the ambiguous pane", stderr)
	}
}

// TestHerdrUsage_FailuresAreOneStderrLine: every way the mode can fail exits
// non-zero with exactly one stderr line and nothing on stdout — the shape
// Factory turns into its "unknown" reason.
func TestHerdrUsage_FailuresAreOneStderrLine(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL + "/api/v1/sessions"
	gone.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	t.Cleanup(broken.Close)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom\nsecond line", http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	t.Cleanup(slow.Close)

	cases := []struct {
		name     string
		env      herdrUsageEnv
		wantCode int
		wantErr  string
	}{
		{"daemon unreachable", usageEnv(goneURL, "w1:p1", ""), 1, "daemon unreachable at " + goneURL},
		{"unreadable answer", usageEnv(broken.URL, "w1:p1", ""), 1, "unreadable answer from daemon"},
		{"daemon error", usageEnv(failing.URL, "w1:p1", ""), 1, "answered 500"},
		{"no pane id", usageEnv(broken.URL, "", ""), 2, "HERDR_PANE_ID is not set"},
	}
	timeout := usageEnv(slow.URL, "w1:p1", "")
	timeout.client = &http.Client{Timeout: 50 * time.Millisecond}
	cases = append(cases, struct {
		name     string
		env      herdrUsageEnv
		wantCode int
		wantErr  string
	}{"daemon too slow", timeout, 1, "daemon unreachable"})
	withWarning := usageEnv(goneURL, "w1:p1", "")
	withWarning.warning = "IRRLICHT_BIND_ADDR is malformed"
	cases = append(cases, struct {
		name     string
		env      herdrUsageEnv
		wantCode int
		wantErr  string
	}{"warning folded into the line", withWarning, 1, "(IRRLICHT_BIND_ADDR is malformed)"})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runUsage(tc.env)
			if code != tc.wantCode {
				t.Errorf("exit %d, want %d", code, tc.wantCode)
			}
			if stdout != "" {
				t.Errorf("stdout %q, want nothing", stdout)
			}
			if strings.Count(stderr, "\n") != 1 || !strings.HasSuffix(stderr, "\n") {
				t.Errorf("stderr %q, want exactly one line", stderr)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr %q, want it to contain %q", stderr, tc.wantErr)
			}
		})
	}
}

// TestHerdrUsage_PanicIsContained: a panic inside the mode becomes an exit
// code and one stderr line, never a crash with a trace.
func TestHerdrUsage_PanicIsContained(t *testing.T) {
	env := usageEnv("http://127.0.0.1:1/api/v1/sessions", "w1:p1", "")
	env.client = nil // fetchSessions dereferences it
	code, stdout, stderr := runUsage(env)
	contained := code == 1 && stdout == "" && strings.Contains(stderr, "internal error") && strings.Count(stderr, "\n") == 1
	if !contained {
		t.Errorf("exit %d, stdout %q, stderr %q; want 1, nothing, one internal-error line", code, stdout, stderr)
	}
}

// TestHerdrUsage_NoWriteToDisk: the mode reads one URL and writes only its
// two streams. Run with HOME pointed at an empty directory, which must stay
// empty.
func TestHerdrUsage_NoWriteToDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv := serveFixture(t, filepath.Join("testdata", "herdr-usage", "complete", "sessions.json"))
	if code, _, stderr := runUsage(usageEnv(srv.URL+"/api/v1/sessions", "w1:p1", "")); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the mode wrote under HOME: %v", entries)
	}
}

// TestHerdrUsage_DocumentIsValidJSONWithSchemaOne is a cheap guard on the
// envelope every golden shares.
func TestHerdrUsage_DocumentIsValidJSONWithSchemaOne(t *testing.T) {
	srv := serveFixture(t, filepath.Join("testdata", "herdr-usage", "two-panes", "sessions.json"))
	_, stdout, _ := runUsage(usageEnv(srv.URL+"/api/v1/sessions", "w1:p2", ""))
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	envelope := doc["schema"] == float64(1) && doc["pane_id"] == "w1:p2" && doc["sampled_at"] == "2026-11-21T12:00:00Z"
	if !envelope {
		t.Errorf("envelope = schema %v, pane_id %v, sampled_at %v", doc["schema"], doc["pane_id"], doc["sampled_at"])
	}
}
