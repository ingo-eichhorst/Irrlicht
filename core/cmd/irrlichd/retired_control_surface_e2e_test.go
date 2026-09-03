package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"irrlicht/core/domain/session"
)

// This file is the removal proof for issue #1875: the daemon no longer offers
// remote session control. It is deliberately the ONE place under core/ that
// still spells the retired route paths — a 404 assertion that cannot name the
// path it probes proves nothing, so the epitaph names the feature even though
// the feature is gone. Nothing else here refers to it.
//
// Every assertion below was seen RED against the pre-removal tree: the routes
// answered 403/200, GET /api/v1/agents carried a "presets" key on every entry,
// and the session payload type carried a "controllable" JSON tag.

// retiredRoutes are the five routes issue #1875 deleted. The methods are the
// ones the daemon used to register, so a hit here means the handler is still
// wired — not merely that some other verb fell through.
var retiredRoutes = []struct {
	method string
	path   string
}{
	{http.MethodGet, "/api/v1/activation/backchannel"},
	{http.MethodGet, "/api/v1/activation/relay-control"},
	{http.MethodPost, "/api/v1/sessions/sess-1/input"},
	{http.MethodPost, "/api/v1/sessions/sess-1/interrupt"},
	{http.MethodGet, "/api/v1/backchannel/rules"},
}

// muxNotFoundBody is what net/http emits when NO pattern matched (both
// ServeMux's fallback and http.FileServer route through http.NotFound). It is
// the load-bearing half of the assertion: the retired input/interrupt handlers
// could themselves answer 404 ("session not found"), so a bare status check
// cannot tell "the route is gone" from "the route is there and the session
// isn't". Only the default body distinguishes them.
const muxNotFoundBody = "404 page not found"

// TestRetiredControlSurface boots one real irrlichd and asserts the whole
// removed surface at once: the five retired routes are unrouted, and
// GET /api/v1/agents no longer carries a presets key.
func TestRetiredControlSurface(t *testing.T) {
	bin := buildIrrlichd(t)
	// shortTempDir (uninstall_hooks_live_daemon_test.go) rather than
	// t.TempDir(): the daemon's unix socket lives under the state dir, and
	// t.TempDir() embeds this test's name, which pushes sun_path past its
	// 104-byte cap and kills the daemon at startup.
	//
	// IRRLICHT_UI_DIR is not decoration. Without a resolvable dashboard the
	// daemon installs a "/" handler that answers every unrouted path with 503
	// ("Dashboard UI not found"), so a retired route would come back 503 and
	// this test could never observe the 404 it is about. An installed daemon
	// always has a UI dir, so pinning one is what makes the fixture resemble
	// production rather than a stripped test rig.
	d := bootSmokeDaemonIn(t, bin, shortTempDir(t), shortTempDir(t),
		"IRRLICHT_DEMO_MODE=1", "IRRLICHT_UI_DIR="+stubUIDir(t))
	defer d.shutdown(t)

	// Guard the guard: a daemon that is not serving 404s everything, which
	// would make every assertion below pass for the wrong reason. Prove the
	// API surface is live before concluding anything from an absence.
	agents := getJSONArray(t, "http://"+d.addr+"/api/v1/agents")

	t.Run("retired routes are unrouted", func(t *testing.T) {
		for _, rt := range retiredRoutes {
			assertUnrouted(t, "http://"+d.addr, rt.method, rt.path)
		}
	})

	// The key is dropped outright rather than served empty (#1875), so this
	// reads the raw wire objects instead of a typed struct: only a generic
	// decode can tell "absent" from "present and empty", which is the claim.
	t.Run("agents carry no presets key", func(t *testing.T) {
		for _, e := range agents {
			if _, ok := e["presets"]; ok {
				t.Errorf("agent %v still carries a presets key: %v", e["name"], e["presets"])
			}
		}
	})
}

// assertUnrouted issues one request and asserts nothing is serving that path.
//
// Both halves are load-bearing. The status must be 404, and the BODY must be
// net/http's own not-found text: the retired input/interrupt handlers could
// themselves answer 404 ("session not found"), so a status check alone cannot
// tell "the route is gone" from "the route is there and the session isn't".
func assertUnrouted(t *testing.T, base, method, path string) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(`{"data":"x"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	switch {
	case resp.StatusCode != http.StatusNotFound:
		t.Errorf("%s %s: got %d, want 404 — the route is still registered (body: %s)",
			method, path, resp.StatusCode, bytes.TrimSpace(body))
	case !strings.Contains(string(body), muxNotFoundBody):
		t.Errorf("%s %s: the 404 came from a handler, not from an unrouted path (body: %s)",
			method, path, bytes.TrimSpace(body))
	}
}

// stubUIDir is a directory holding a minimal index.html, enough for the
// daemon's UI resolver to accept it and serve "/" from a FileServer.
func stubUIDir(t *testing.T) string {
	t.Helper()
	dir := shortTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>ok</h1>"), 0o644); err != nil {
		t.Fatalf("write stub index.html: %v", err)
	}
	return dir
}

// getJSONArray fetches url, requires 200 and a non-empty JSON array, and
// returns the decoded objects. The non-empty requirement is the guard: an
// empty list has no keys to be missing, so a key-absence assertion over one
// would pass without inspecting anything.
func getJSONArray(t *testing.T, url string) []map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: got %d, want 200 — the daemon is not serving its API, so an absence below would prove nothing", url, resp.StatusCode)
	}
	var entries []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	if len(entries) == 0 {
		t.Fatalf("GET %s returned no entries — nothing was inspected", url)
	}
	return entries
}

// TestSessionPayloadType_CarriesNoControllableTag asserts the session tree has
// no "controllable" field on the wire.
//
// It walks the JSON tags of the response type rather than a rendered payload
// on purpose. `controllable` was `omitempty`, so a daemon with the capability
// switched off omitted it anyway — an assertion over live JSON would have been
// green before the removal and proved nothing. The type graph cannot be
// switched off. Naming no field, it compiles on both sides of the change,
// which is what let it be run red first.
func TestSessionPayloadType_CarriesNoControllableTag(t *testing.T) {
	const retiredTag = "controllable"

	seen := map[reflect.Type]bool{}
	found := findJSONTag(reflect.TypeOf(sessionsResponse{}), "sessionsResponse", retiredTag, seen)

	// Guard the guard: a walk that reached nothing reports no tag for the
	// same reason a walk over a cleaned tree does. Pin a field that must
	// still be there, so "absent" and "never looked" cannot coincide.
	if !seen[reflect.TypeOf(session.Agent{})] {
		t.Fatal("the type walk never reached session.Agent — an absent tag proves nothing")
	}
	if len(found) != 0 {
		t.Errorf("session payload still carries a %q JSON tag at: %v", retiredTag, found)
	}
}

// findJSONTag walks ty's struct graph and returns the field paths whose JSON
// name is tag. seen doubles as a recursion guard (the group tree is recursive)
// and as this test's own reachability witness.
func findJSONTag(ty reflect.Type, path, tag string, seen map[reflect.Type]bool) []string {
	for ty.Kind() == reflect.Ptr || ty.Kind() == reflect.Slice ||
		ty.Kind() == reflect.Array || ty.Kind() == reflect.Map {
		ty = ty.Elem()
	}
	if ty.Kind() != reflect.Struct || seen[ty] {
		return nil
	}
	seen[ty] = true
	var found []string
	for i := 0; i < ty.NumField(); i++ {
		f := ty.Field(i)
		fieldPath := path + "." + f.Name
		if jsonName(f) == tag {
			found = append(found, fieldPath)
		}
		found = append(found, findJSONTag(f.Type, fieldPath, tag, seen)...)
	}
	return found
}

// jsonName is the key f serializes under: its json tag's name, or the Go field
// name when the tag names none.
func jsonName(f reflect.StructField) string {
	if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" {
		return name
	}
	return f.Name
}
