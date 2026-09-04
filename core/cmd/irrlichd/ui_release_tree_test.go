package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRegisterUIRoutesServesTheStagedReleaseTree is the served-side half of
// #1900, and the direct inversion of the evidence in that issue: on the
// released 0.6.2 daemon every one of the dashboard's ES-module imports
// returned 404, because tools/build-release.sh staged three files out of
// thirteen. registerUIRoutes was never at fault — it registers one blanket
// http.FileServer — but "the daemon serves what the release stages" was
// nobody's assertion, so the gap between the two was invisible until a user
// reported a blank page.
//
// The two sides are deliberately derived from DIFFERENT sources, because a
// test that asked the staged directory what to request could never reproduce
// the defect — whatever shipped would be exactly what it looked for, and three
// files would pass as readily as thirteen:
//
//   - what is REQUESTED comes from tools/web-release-assets-guard.sh's walk of
//     the import graph rooted at platforms/web/index.html — what the browser
//     will actually fetch;
//   - what is SERVED comes from executing the same tools/lib/stage-web.sh that
//     tools/build-release.sh sources — what a release will actually contain.
//
// Both are the shipping implementations, not copies of them: a copy here would
// prove something about this file and nothing about the release.
//
// It runs in-process against httptest rather than booting a daemon — the
// packaging rule is graded by the shell guard, and what is left to prove here
// is only that the routes serve the staged directory.
//
// Seen red before the fix existed, and reproducible on demand: restoring
// origin/main's three-file staging rule makes it report 404 for all ten
// modules — the same ten the issue measured against the released daemon.
//
//	tools/mutate.sh tools/lib/stage-web.sh \
//	  '    for f in "$src"/*.html "$src"/*.css "$src"/*.js; do' \
//	  '    for f in "$src"/index.html "$src"/irrlicht.css "$src"/irrlicht.js; do' \
//	  bash -c 'cd core && go test ./cmd/irrlichd/ -run TestRegisterUIRoutesServesTheStagedReleaseTree -count=1'
//
// The equivalent property is re-run on every push by
// tools/lib/web-release-assets-guard-mutations_test.sh, which drops a module
// from the staging rule and requires the shell guard to go red; that fixture
// lives in the `tools` gate rather than here so the cheap phase-1 gates do not
// have to build Go.
func TestRegisterUIRoutesServesTheStagedReleaseTree(t *testing.T) {
	repoRoot := repoRootFromWorkingDir(t)
	stageLib := filepath.Join(repoRoot, "tools", "lib", "stage-web.sh")
	guard := filepath.Join(repoRoot, "tools", "web-release-assets-guard.sh")
	webSrc := filepath.Join(repoRoot, "platforms", "web")
	for _, p := range []string{stageLib, guard, filepath.Join(webSrc, "index.html")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("cannot run: %s is missing (%v) — the release tooling this test executes is gone", p, err)
		}
	}

	staged := stageReleaseWebTree(t, stageLib, webSrc)
	reachable := reachableWebAssets(t, guard, webSrc)

	t.Setenv(envUIDir, staged)
	mux := http.NewServeMux()
	registerUIRoutes(mux, e2eLog{})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, name := range reachable {
		status, _, size := httpGetAsset(t, srv.URL+"/"+name)
		switch {
		case status != http.StatusOK:
			t.Errorf("GET /%s = %d, want 200 — this is exactly the 404 every released dashboard served for its ES modules (#1900)", name, status)
		case size == 0:
			t.Errorf("GET /%s returned 200 with an empty body", name)
		}
	}

	// The two Content-Types registerUIRoutes exists to pin: a stripped Linux
	// image has no mime.types, and Go's sniffing returns text/plain for CSS,
	// which a browser then refuses to apply.
	for name, wantPrefix := range map[string]string{
		"irrlicht.css": "text/css",
		"irrlicht.js":  "application/javascript",
	} {
		if _, got, _ := httpGetAsset(t, srv.URL+"/"+name); !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("GET /%s Content-Type = %q, want prefix %q", name, got, wantPrefix)
		}
	}
}

// stageReleaseWebTree stages webSrc into a fresh temp dir by executing the
// release's own tools/lib/stage-web.sh, and returns that directory. It also
// asserts the staged tree is flat: a directory in there is how node_modules
// ships by accident.
func stageReleaseWebTree(t *testing.T, stageLib, webSrc string) string {
	t.Helper()
	staged := filepath.Join(t.TempDir(), "web")
	cmd := exec.Command("bash", "-c",
		`set -euo pipefail; . "$1"; stage_web "$2" "$3"`, "bash", stageLib, webSrc, staged)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cannot run: stage_web refused to stage %s: %v\n%s", webSrc, err, out)
	}
	entries, err := os.ReadDir(staged)
	if err != nil {
		t.Fatalf("cannot run: staged tree unreadable: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("the staged release tree contains a directory (%s) — it must be flat", e.Name())
		}
	}
	return staged
}

// reachableWebAssets returns every asset the browser will fetch, by running the
// packaging guard's own transitive walk of the import graph rooted at
// webSrc/index.html.
func reachableWebAssets(t *testing.T, guard, webSrc string) []string {
	t.Helper()
	out, err := exec.Command("bash", "-c",
		`set -uo pipefail; . "$1"; web_assets_closure "$2"`, "bash", guard, webSrc).Output()
	if err != nil {
		t.Fatalf("cannot run: the import-graph walk refused on %s: %v", webSrc, err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	// Inability to look must not read as success: an empty or near-empty
	// closure would satisfy every 200 assertion by having nothing to assert.
	// The dashboard has been over ten files since #820.
	if len(names) < 10 {
		t.Fatalf("cannot run: the import-graph walk found only %d asset(s) (%v) — it did not read the dashboard", len(names), names)
	}
	return names
}

// httpGetAsset fetches url and reports its status, Content-Type and body size.
// A transport error is reported and returns a zero status, which every caller
// treats as a failure.
func httpGetAsset(t *testing.T, url string) (status int, contentType string, size int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Errorf("GET %s: %v", url, err)
		return 0, "", 0
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("GET %s: reading body: %v", url, err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), len(body)
}

// repoRootFromWorkingDir walks up from the test's working directory to the
// enclosing repo root (the directory holding tools/lib/stage-web.sh). It fails
// the test rather than returning a guess, so a relocated package surfaces as
// "cannot run" instead of as a silent skip.
func repoRootFromWorkingDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("cannot run: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "tools", "lib", "stage-web.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("cannot run: no repo root with tools/lib/stage-web.sh above the working directory")
		}
		dir = parent
	}
}
