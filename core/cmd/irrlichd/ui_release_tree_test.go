package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The served-side half of #1900. The released 0.6.2 daemon answered 404 for
// every one of the dashboard's ES-module imports, because tools/build-release.sh
// staged three files out of thirteen. registerUIRoutes was never at fault — it
// registers one blanket http.FileServer — but "the daemon serves what the
// release stages" was nobody's assertion, so the gap between the two stayed
// invisible until a user reported a blank page.
//
// #1847 fixed the packaging and guards the copy list statically
// (platforms/web/release-files.test.js). What no test asserted afterwards is
// the end of that chain: that the routes, pointed at a tree the real staging
// rule produced, actually answer 200 for every asset a browser will fetch.
//
// The two sides are derived from DIFFERENT sources on purpose. A test that
// asked the staged directory what to request could never reproduce the defect:
// whatever shipped would be exactly what it looked for, and three files would
// pass as readily as eighteen.
//
//   - What is REQUESTED comes from platforms/web/shippedFiles.testutil.js's
//     deriveShippedSet() — the transitive walk of index.html plus the import
//     graph, i.e. what the browser will actually fetch.
//   - What is SERVED comes from executing tools/build-release.sh's own
//     WEB_FILES array and copy_web_files helper — what a release will
//     actually contain.
//
// Both are the shipping implementations, not copies of them. A copy here would
// prove something about this file and nothing about the release.
//
// It runs in-process against httptest rather than booting a daemon: the
// packaging rule is graded by release-files.test.js, and what is left to prove
// here is only that the routes serve the staged directory.
func TestRegisterUIRoutesServesTheStagedReleaseTree(t *testing.T) {
	repoRoot := releaseRepoRoot(t)

	staged := stageReleaseWebTree(t, repoRoot, t.TempDir())
	reachable := reachableWebAssets(t, repoRoot)

	srv := serveStagedTree(t, staged)
	defer srv.Close()

	for _, missing := range missingFromServer(t, srv.URL, reachable) {
		t.Errorf("GET /%s = %s — this is exactly the 404 every released dashboard served for its ES modules (#1900)",
			missing.name, missing.why)
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

// TestServedTreeCheckGoesRedOnTheShippedRegression is the committed mutation
// fixture for the test above. The guard it protects has no "before the fix" to
// run red — #1847 already landed — so instead of describing the red path in a
// PR body nothing re-runs, this stages the exact tree that shipped blank in
// v0.6.0–v0.6.2 (index.html, irrlicht.css, irrlicht.js and nothing else) and
// requires missingFromServer to name every module that is gone.
//
// If this ever passes with an empty result, the assertion above has stopped
// being able to see the defect it exists for.
func TestServedTreeCheckGoesRedOnTheShippedRegression(t *testing.T) {
	repoRoot := releaseRepoRoot(t)
	reachable := reachableWebAssets(t, repoRoot)

	// The three-file staging rule, verbatim, as v0.6.2 shipped it.
	shippedBlank := []string{"index.html", "irrlicht.css", "irrlicht.js"}
	want := assetsExcept(reachable, shippedBlank)
	if len(want) == 0 {
		t.Fatal("cannot run: the closure holds nothing beyond the three entry files, so the regression has nothing to remove")
	}

	srv := serveStagedTree(t, stageNamedFiles(t, repoRoot, shippedBlank))
	defer srv.Close()

	got := assetNames(missingFromServer(t, srv.URL, reachable))
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the three-file staging must be reported as broken.\n got: %v\nwant: %v", got, want)
	}
}

// serveStagedTree points registerUIRoutes at staged and returns the running
// test server. The caller closes it.
func serveStagedTree(t *testing.T, staged string) *httptest.Server {
	t.Helper()
	t.Setenv(envUIDir, staged)
	mux := http.NewServeMux()
	registerUIRoutes(mux, e2eLog{})
	return httptest.NewServer(mux)
}

// stageNamedFiles copies exactly names out of platforms/web into a fresh temp
// directory — the deliberately-wrong staging the fixture above drives.
func stageNamedFiles(t *testing.T, repoRoot string, names []string) string {
	t.Helper()
	staged := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatalf("cannot run: %v", err)
	}
	for _, name := range names {
		src := filepath.Join(repoRoot, "platforms", "web", name)
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("cannot run: %s is missing (%v) — the regression cannot be staged", src, err)
		}
		if err := os.WriteFile(filepath.Join(staged, name), body, 0o644); err != nil {
			t.Fatalf("cannot run: %v", err)
		}
	}
	return staged
}

// assetsExcept returns names minus drop, sorted.
func assetsExcept(names, drop []string) []string {
	skip := make(map[string]bool, len(drop))
	for _, d := range drop {
		skip[d] = true
	}
	var out []string
	for _, n := range names {
		if !skip[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// assetNames reduces a missing-asset report to its sorted names.
func assetNames(missing []missingAsset) []string {
	var out []string
	for _, m := range missing {
		out = append(out, m.name)
	}
	sort.Strings(out)
	return out
}

// missingAsset names one asset the server failed to hand back, and why.
type missingAsset struct {
	name string
	why  string
}

// missingFromServer requests every asset in want from base and reports the
// ones that did not come back as a non-empty 200. Shared by the assertion and
// its mutation fixture so both grade with the same code.
func missingFromServer(t *testing.T, base string, want []string) []missingAsset {
	t.Helper()
	var out []missingAsset
	for _, name := range want {
		// http.FileServer redirects /index.html to /, so the entry document is
		// requested the way a browser asks for it.
		url := base + "/" + name
		if name == "index.html" {
			url = base + "/"
		}
		status, _, size := httpGetAsset(t, url)
		switch {
		case status == 0:
			out = append(out, missingAsset{name, "no response (transport error)"})
		case status != http.StatusOK:
			out = append(out, missingAsset{name, strconv.Itoa(status) + " " + http.StatusText(status)})
		case size == 0:
			out = append(out, missingAsset{name, "200 with an empty body"})
		}
	}
	return out
}

// stageReleaseWebTree stages platforms/web into dir by executing the release's
// own WEB_FILES array and copy_web_files helper, lifted out of
// tools/build-release.sh. The script cannot be sourced whole — past this region
// it deletes .build and starts a full release build — so the region is cut out
// and evaluated. The cut is asserted, not assumed: a fragment that fails to
// define copy_web_files fails the test rather than staging nothing.
func stageReleaseWebTree(t *testing.T, repoRoot, tmp string) string {
	t.Helper()
	script := filepath.Join(repoRoot, "tools", "build-release.sh")
	source, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("cannot run: %s is unreadable (%v) — the release tooling this test executes is gone", script, err)
	}
	fragment, ok := cutWebStagingRegion(string(source))
	if !ok {
		t.Fatalf("cannot run: could not cut the WEB_FILES / copy_web_files region out of %s — the release script's shape changed and this test is no longer executing it", script)
	}

	staged := filepath.Join(tmp, "web")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatalf("cannot run: %v", err)
	}
	runReleaseStaging(t, repoRoot, fragment, staged)
	assertStagedTreeIsFlat(t, staged)
	return staged
}

// runReleaseStaging evaluates the cut fragment and calls copy_web_files. The
// fragment is asserted to define what it is supposed to define: a cut that
// silently produced nothing would stage an empty tree, which every 200
// assertion would then fail for the wrong reason.
func runReleaseStaging(t *testing.T, repoRoot, fragment, staged string) {
	t.Helper()
	const preamble = "set -euo pipefail\ncd \"$1\"\n"
	const epilogue = `
declare -F copy_web_files >/dev/null || { echo "the cut region did not define copy_web_files" >&2; exit 2; }
[ "${#WEB_FILES[@]}" -gt 0 ] || { echo "the cut region defined an empty WEB_FILES" >&2; exit 2; }
copy_web_files "$2"
`
	cmd := exec.Command("bash", "-c", preamble+fragment+epilogue, "bash", repoRoot, staged)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cannot run: the release staging refused: %v\n%s", err, out)
	}
}

// assertStagedTreeIsFlat rejects an empty staged tree and any directory in it —
// node_modules ships by accident exactly that way.
func assertStagedTreeIsFlat(t *testing.T, staged string) {
	t.Helper()
	entries, err := os.ReadDir(staged)
	if err != nil {
		t.Fatalf("cannot run: staged tree unreadable: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("cannot run: the release staging produced an empty tree")
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("the staged release tree contains a directory (%s) — it must be flat", e.Name())
		}
	}
}

// cutWebStagingRegion returns the WEB_FILES array plus the copy_web_files
// helper, from the array's opening line through the helper's closing brace at
// column zero. Reports false when either end is absent.
func cutWebStagingRegion(source string) (string, bool) {
	lines := strings.Split(source, "\n")
	start := lineIndex(lines, func(l string) bool { return strings.HasPrefix(l, "WEB_FILES=(") })
	if start < 0 {
		return "", false
	}
	rest := lines[start:]
	helper := lineIndex(rest, func(l string) bool { return strings.HasPrefix(l, "copy_web_files()") })
	if helper < 0 {
		return "", false
	}
	// The helper's body ends at the first closing brace in column zero.
	end := lineIndex(rest[helper:], func(l string) bool { return l == "}" })
	if end < 0 {
		return "", false
	}
	return strings.Join(rest[:helper+end+1], "\n"), true
}

// lineIndex returns the first index in lines whose value satisfies match, or -1.
func lineIndex(lines []string, match func(string) bool) int {
	for i, l := range lines {
		if match(l) {
			return i
		}
	}
	return -1
}

// reachableWebAssets returns every asset the browser will fetch, by running
// platforms/web/shippedFiles.testutil.js's own transitive walk of the import
// graph rooted at index.html. That module is the derivation
// release-files.test.js grades the copy list against, so the two halves of this
// test never share a source.
func reachableWebAssets(t *testing.T, repoRoot string) []string {
	t.Helper()
	webDir := filepath.Join(repoRoot, "platforms", "web")
	cmd := exec.Command("node", "--input-type=module", "-e", `
import { deriveShippedSet } from './shippedFiles.testutil.js';
process.stdout.write(JSON.stringify([...deriveShippedSet().files].sort()));
`)
	cmd.Dir = webDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// The derivation's whole design is that every refusal explains itself,
		// so its explanation is reported rather than reduced to "exit status 1".
		t.Fatalf("cannot run: the import-graph walk refused on %s: %v\n%s", webDir, err, stderr.String())
	}

	var names []string
	if err := json.Unmarshal(out, &names); err != nil {
		t.Fatalf("cannot run: the import-graph walk returned unparseable output (%v): %s", err, out)
	}

	// Inability to look must not read as success: an empty or near-empty
	// closure would satisfy every 200 assertion by having nothing to assert.
	// The floor is the defect itself rather than a number picked here —
	// v0.6.0–v0.6.2 shipped exactly index.html + irrlicht.css + irrlicht.js,
	// so a closure that small is the bug wearing the test's clothes.
	const shippedBroken = 3
	if len(names) <= shippedBroken {
		t.Fatalf("cannot run: the import-graph walk found only %d asset(s) (%v) — no more than the %d that shipped blank in #1900, so it did not read the dashboard", len(names), names, shippedBroken)
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

// releaseRepoRoot returns the repo root. This file's own location is fixed at
// core/cmd/irrlichd/, so the root is three directories up — the repo's
// established test idiom — rather than a search that could silently land
// somewhere else. It fails the test rather than returning a guess, so a
// relocated package surfaces as "cannot run" instead of as a silent skip.
func releaseRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot run: runtime.Caller could not locate this test file")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	if err != nil {
		t.Fatalf("cannot run: %v", err)
	}
	for _, p := range []string{
		filepath.Join(root, "tools", "build-release.sh"),
		filepath.Join(root, "platforms", "web", "shippedFiles.testutil.js"),
		filepath.Join(root, "platforms", "web", "index.html"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("cannot run: %s is missing (%v) — the release tooling this test executes is gone", p, err)
		}
	}
	return root
}
