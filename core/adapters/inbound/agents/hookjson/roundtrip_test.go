package hookjson

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsoncSettings is the fixture issue #1371 is about: a realistic hand-maintained
// agent config that a naive read-modify-write destroys three different ways at
// once. Every property below is deliberate and is asserted on somewhere in this
// file.
//
//   - Comments, both line and block. gemini-cli's ~/.gemini/settings.json is
//     JSONC: it reads the file through stripJsonComments and writes it back
//     through the comment-json package specifically so these survive.
//   - A deliberately NON-alphabetical key order ("theme" first, "autoAccept"
//     last). json.MarshalIndent over a map sorts keys, so a writer that
//     re-serializes the whole document reorders a file the user hand-organized.
//   - An `&&` inside a command string, plus `>` and `|`. encoding/json rewrites
//     `<`, `>` and `&` into their six-character backslash-u escapes unless
//     SetEscapeHTML(false) is set, so `npm run build && npm run list-tools`
//     comes back mangled. assertNoHTMLEscapes below spells those escapes out.
//
// It is a const rather than a testdata file so the exact bytes being asserted on
// are visible next to the assertion.
const jsoncSettings = `// ~/.gemini/settings.json — hand-maintained. Comments are legal in this file
// and the CLI preserves them, so anything else that rewrites it must too.
{
  /* Appearance first, on purpose: it is what gets changed most often. */
  "theme": "GitHub",
  "selectedAuthType": "oauth-personal",

  // Sandboxing is off because the build script shells out to docker.
  "sandbox": false,

  // The && below is load-bearing: it must not come back HTML-escaped.
  "toolDiscoveryCommand": "npm run build && npm run list-tools",
  "toolCallCommand": "node ./tools/call.js 2>/dev/null || true",

  "excludeTools": ["ShellTool(rm -rf)"],
  "contextFileName": "GEMINI.md",

  // Deliberately last, and deliberately not in alphabetical order.
  "autoAccept": false
}
`

// seedRawFile writes raw bytes as the settings file, bypassing the Go-map
// seeders the rest of this package's tests use. A map seeder cannot express a
// comment or a key order, which is exactly what these tests are about.
func seedRawFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	return path
}

func readRawFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestRoundTrip_JSONCSettingsSurviveInstallThenUninstall is the deliverable of
// issue #1371: grant, then revoke, and the user gets their file back exactly as
// they wrote it. Byte-identical is the assertion because every weaker one —
// "still parses", "keys still present" — passes on a file whose comments have
// been deleted and whose keys have been reshuffled.
func TestRoundTrip_JSONCSettingsSurviveInstallThenUninstall(t *testing.T) {
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			path := seedRawFile(t, t.TempDir(), "settings.json", jsoncSettings)
			cfg := build(path)

			installed, err := EnsureInstalled(cfg)
			if err != nil {
				t.Fatalf("EnsureInstalled: %v", err)
			}
			if !installed {
				t.Fatal("EnsureInstalled reported no change; expected a fresh install")
			}

			removed, err := Uninstall(cfg)
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if !removed {
				t.Fatal("Uninstall reported no change; expected our hooks to be removed")
			}

			if got := readRawFile(t, path); got != jsoncSettings {
				t.Errorf("install+uninstall was not lossless\n--- want (original) ---\n%s\n--- got ---\n%s", jsoncSettings, got)
			}
		})
	}
}

// TestEnsureInstalled_PreservesCommentsOrderAndAmpersands pins the intermediate
// state, so a writer that destroys the file on install and happens to restore it
// on uninstall cannot pass the round-trip test above by cancelling its own
// damage out.
func TestEnsureInstalled_PreservesCommentsOrderAndAmpersands(t *testing.T) {
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			path := seedRawFile(t, t.TempDir(), "settings.json", jsoncSettings)

			if _, err := EnsureInstalled(build(path)); err != nil {
				t.Fatalf("EnsureInstalled: %v", err)
			}
			got := readRawFile(t, path)

			for _, comment := range []string{
				"// ~/.gemini/settings.json — hand-maintained.",
				"/* Appearance first, on purpose: it is what gets changed most often. */",
				"// Sandboxing is off because the build script shells out to docker.",
				"// Deliberately last, and deliberately not in alphabetical order.",
			} {
				if !strings.Contains(got, comment) {
					t.Errorf("comment deleted by install: %q\n--- got ---\n%s", comment, got)
				}
			}

			if !strings.Contains(got, `"npm run build && npm run list-tools"`) {
				t.Errorf("`&&` was HTML-escaped or otherwise mangled\n--- got ---\n%s", got)
			}
			assertNoHTMLEscapes(t, got)

			assertKeyOrder(t, got, []string{
				"theme", "selectedAuthType", "sandbox", "toolDiscoveryCommand",
				"toolCallCommand", "excludeTools", "contextFileName", "autoAccept",
			})
		})
	}
}

// TestEnsureInstalled_PlainJSONKeepsOrderAndEscaping is the claudecode half of
// the issue: ~/.claude/settings.json carries no comments, so the parse never
// fails and the corruption is entirely silent — the file simply comes back
// alphabetized with its `&&` escaped.
func TestEnsureInstalled_PlainJSONKeepsOrderAndEscaping(t *testing.T) {
	const plain = `{
  "theme": "dark",
  "statusLine": {
    "type": "command",
    "command": "date && whoami >/dev/null 2>&1 || true"
  },
  "autoAccept": false
}
`
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			path := seedRawFile(t, t.TempDir(), "settings.json", plain)

			if _, err := EnsureInstalled(build(path)); err != nil {
				t.Fatalf("EnsureInstalled: %v", err)
			}
			got := readRawFile(t, path)

			if !strings.Contains(got, `"date && whoami >/dev/null 2>&1 || true"`) {
				t.Errorf("user command was HTML-escaped\n--- got ---\n%s", got)
			}
			assertNoHTMLEscapes(t, got)
			assertKeyOrder(t, got, []string{"theme", "statusLine", "autoAccept"})
		})
	}
}

// assertNoHTMLEscapes fails if the document carries any of the three escapes
// encoding/json emits unless SetEscapeHTML(false) is set. The needles are built
// by concatenation rather than written literally so that nothing along the way
// can quietly turn the six characters back into the one they denote — which
// would leave the assertion searching for `&` in a file that legitimately
// contains `&&`, and passing for the wrong reason.
func assertNoHTMLEscapes(t *testing.T, doc string) {
	t.Helper()
	for _, hex := range []string{"0026", "003c", "003e", "003C", "003E"} {
		needle := "\\u" + hex
		if strings.Contains(doc, needle) {
			t.Errorf("output contains the HTML escape %s\n--- got ---\n%s", needle, doc)
		}
	}
}

// assertKeyOrder checks that the given top-level-ish keys appear in the file in
// the order listed. It works on the raw text rather than a decoded map because a
// decoded map has no order to assert on — which is precisely how this class of
// bug stayed invisible to every existing test in this package.
func assertKeyOrder(t *testing.T, doc string, keys []string) {
	t.Helper()
	prev := -1
	prevKey := ""
	for _, k := range keys {
		at := strings.Index(doc, `"`+k+`"`)
		if at < 0 {
			t.Errorf("key %q missing from output\n--- got ---\n%s", k, doc)
			return
		}
		if at < prev {
			t.Errorf("key order not preserved: %q now precedes %q\n--- got ---\n%s", k, prevKey, doc)
			return
		}
		prev, prevKey = at, k
	}
}

// TestUninstall_EmptiedHooksObjectIsRemoved pins the one behavior change this
// work makes beyond the three lossy ones: an emptied "hooks" object goes away,
// so revoking consent on a config that had no hooks gives the file back rather
// than leaving `"hooks": {}` litter behind.
//
// The narrow case it does NOT cover is a user who wrote `"hooks": {"Stop": []}`
// themselves. Their "Stop" key is already deleted by removeOurHook on main —
// that `delete(hooksMap, event)` predates this change and is untouched by it —
// so all this adds is removal of the empty object that deletion leaves. The
// user content was lost before; only the wrapper is new.
func TestUninstall_EmptiedHooksObjectIsRemoved(t *testing.T) {
	const src = "{\n  \"theme\": \"dark\"\n}\n"
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			path := seedRawFile(t, t.TempDir(), "settings.json", src)
			cfg := build(path)
			mustEnsure(t, cfg, "install", true)
			mustUninstall(t, cfg, "uninstall", true)
			if got := readRawFile(t, path); got != src {
				t.Errorf("uninstall left something behind\n--- want ---\n%q\n--- got ---\n%q", src, got)
			}
		})
	}
}

// TestUninstall_UserHooksKeepTheObject is the other side of it: any surviving
// user-authored hook keeps "hooks" alive, so the cleanup can never take a
// populated object with it.
func TestUninstall_UserHooksKeepTheObject(t *testing.T) {
	const src = "{\n  \"hooks\": {\n    \"PreToolUse\": [\n      {\n        \"hooks\": [\n          { \"type\": \"command\", \"command\": \"echo mine && true\" }\n        ]\n      }\n    ]\n  }\n}\n"
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			path := seedRawFile(t, t.TempDir(), "settings.json", src)
			cfg := build(path)
			mustEnsure(t, cfg, "install", true)
			mustUninstall(t, cfg, "uninstall", true)
			if got := readRawFile(t, path); got != src {
				t.Errorf("the user's own hook was not restored exactly\n--- want ---\n%q\n--- got ---\n%q", src, got)
			}
		})
	}
}
