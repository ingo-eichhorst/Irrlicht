package hookjson

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The two delivery shapes this package must serve without force-merging them:
// an http-delivered entry (Claude Code's native `type: http`) and a
// command-delivered one (Codex's curl). Every behavioral test below runs
// against both, so a change that only holds for one shape fails here.
const (
	httpSentinel = "localhost:7837/api/v1/hooks/httpagent"
	httpURL      = "http://" + httpSentinel
	cmdSentinel  = "localhost:7837/api/v1/hooks/cmdagent"
	cmdCommand   = "curl -fsS -X POST --data-binary @- http://" + cmdSentinel + " || true"

	eventWithMatcher = "PermissionRequest"
	eventNoMatcher   = "Stop"
	matcher          = "Bash|Write"
)

// testConfig holds everything the two shapes share, so httpConfig and cmdConfig
// below differ by exactly what a real adapter differs by — sentinel, entry
// builder, canonical predicate — and nothing else.
func testConfig(path, sentinel string, entry func() map[string]interface{}, isCanonical func(map[string]interface{}) bool) Config {
	return Config{
		Path:        path,
		Sentinel:    sentinel,
		Events:      []string{eventWithMatcher, eventNoMatcher},
		MatcherFor:  matcherForTestEvent,
		Entry:       entry,
		IsCanonical: isCanonical,
		WriteFile:   writeTestFile,
	}
}

// httpConfig models a claudecode-shaped adapter: native `type: http` delivery,
// and a canonical check that rejects any leftover legacy `command` key.
func httpConfig(path string) Config {
	return testConfig(path, httpSentinel,
		func() map[string]interface{} {
			return map[string]interface{}{"type": "http", "url": httpURL, "timeout": 5}
		},
		func(hook map[string]interface{}) bool {
			if _, hasCmd := hook["command"]; hasCmd {
				return false
			}
			t, _ := hook["type"].(string)
			u, _ := hook["url"].(string)
			return t == "http" && u == httpURL
		})
}

// cmdConfig models a codex-shaped adapter: a `type: command` curl, canonical
// when the command matches ours exactly.
func cmdConfig(path string) Config {
	return testConfig(path, cmdSentinel,
		func() map[string]interface{} {
			return map[string]interface{}{"type": "command", "command": cmdCommand, "timeout": 5}
		},
		func(hook map[string]interface{}) bool {
			t, _ := hook["type"].(string)
			c, _ := hook["command"].(string)
			return t == "command" && c == cmdCommand
		})
}

// matcherForTestEvent mirrors the real adapters' rule: every event but the
// turn-end one carries a matcher.
func matcherForTestEvent(event string) string {
	if event == eventNoMatcher {
		return ""
	}
	return matcher
}

// configs returns both delivery shapes for a table-driven subtest.
func configs() map[string]func(string) Config {
	return map[string]func(string) Config{"http": httpConfig, "command": cmdConfig}
}

// --- helpers ---

// writeTestFile stands in for the adapters' atomic writers, which stay on their
// side of Config precisely because they differ.
func writeTestFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// seedSettings writes a pre-existing settings file for a test to merge onto.
func seedSettings(t *testing.T, path string, settings map[string]interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
}

// readHooks returns the "hooks" sub-map of the settings file at path.
func readHooks(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	m, _ := settings["hooks"].(map[string]interface{})
	return m
}

// groupsFor returns the matcher-group array installed for an event.
func groupsFor(t *testing.T, hooks map[string]interface{}, event string) []interface{} {
	t.Helper()
	arr, ok := hooks[event].([]interface{})
	if !ok {
		t.Fatalf("event %s: no group array, got %#v", event, hooks[event])
	}
	return arr
}

// firstGroup returns the single matcher group installed for an event.
func firstGroup(t *testing.T, path, event string) map[string]interface{} {
	t.Helper()
	return groupsFor(t, readHooks(t, path), event)[0].(map[string]interface{})
}

// mustEnsure runs EnsureInstalled and asserts whether it reported a change.
func mustEnsure(t *testing.T, cfg Config, label string, wantModified bool) {
	t.Helper()
	modified, err := EnsureInstalled(cfg)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if modified != wantModified {
		t.Errorf("%s: modified=%v, want %v", label, modified, wantModified)
	}
}

// mustUninstall runs Uninstall and asserts whether it reported a change.
func mustUninstall(t *testing.T, cfg Config, label string, wantModified bool) {
	t.Helper()
	modified, err := Uninstall(cfg)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if modified != wantModified {
		t.Errorf("%s: modified=%v, want %v", label, modified, wantModified)
	}
}

// assertInstalled checks every one of cfg's events for the presence (or, with
// want=false, the complete absence) of our entry. Absence is the stricter
// check: the event key itself must be gone once its last group was removed.
func assertInstalled(t *testing.T, cfg Config, want bool) {
	t.Helper()
	hooks := readHooks(t, cfg.Path)
	for _, event := range cfg.Events {
		if got := HasOurHook(hooks, event, cfg.Sentinel); got != want {
			t.Errorf("event %s: installed=%v, want %v", event, got, want)
		}
		if _, present := hooks[event]; !want && present {
			t.Errorf("event %s: key kept after its last group was removed", event)
		}
	}
}

// --- tests ---

func TestEnsureInstalled_CreatesIdempotentlyAndUninstalls(t *testing.T) {
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			// A nested dir the writer must create.
			cfg := build(filepath.Join(t.TempDir(), "nested", "settings.json"))

			mustEnsure(t, cfg, "first install", true)
			assertInstalled(t, cfg, true)
			mustEnsure(t, cfg, "second install (idempotency)", false)

			mustUninstall(t, cfg, "uninstall", true)
			assertInstalled(t, cfg, false)
			mustUninstall(t, cfg, "second uninstall", false)
		})
	}
}

// TestEnsureInstalled_MatcherKeyOmittedWhenEmpty pins the shape a turn-end hook
// requires in both Claude Code and Codex: no matcher key at all, not an empty
// string. Both upstreams reject a Stop hook that carries one.
func TestEnsureInstalled_MatcherKeyOmittedWhenEmpty(t *testing.T) {
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			cfg := build(filepath.Join(t.TempDir(), "settings.json"))
			mustEnsure(t, cfg, "install", true)

			if group := firstGroup(t, cfg.Path, eventNoMatcher); group["matcher"] != nil {
				t.Errorf("%s group carries a matcher key: %#v", eventNoMatcher, group)
			}
			group := firstGroup(t, cfg.Path, eventWithMatcher)
			if m, _ := group["matcher"].(string); m != matcher {
				t.Errorf("%s matcher = %q, want %q", eventWithMatcher, m, matcher)
			}
		})
	}
}

// TestEnsureInstalled_PreservesUnrelatedContent is the safety property that
// matters most: these are user-authored config files, and a merge must leave
// every key and every foreign hook group exactly where it was.
func TestEnsureInstalled_PreservesUnrelatedContent(t *testing.T) {
	cfg := httpConfig(filepath.Join(t.TempDir(), "settings.json"))
	seedSettings(t, cfg.Path, map[string]interface{}{
		"model": "user-choice",
		"hooks": map[string]interface{}{
			eventWithMatcher: []interface{}{userAuthoredGroup()},
		},
	})

	mustEnsure(t, cfg, "install", true)

	var settings map[string]interface{}
	raw, _ := os.ReadFile(cfg.Path)
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if settings["model"] != "user-choice" {
		t.Errorf("unrelated top-level key lost: %#v", settings["model"])
	}

	groups := groupsFor(t, readHooks(t, cfg.Path), eventWithMatcher)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 (the user's plus ours)", len(groups))
	}
	if m, _ := groups[0].(map[string]interface{})["matcher"].(string); m != "Bash" {
		t.Errorf("user group matcher rewritten to %q; only sentinel-bearing groups may be touched", m)
	}

	// Uninstall must give the file back as seeded.
	mustUninstall(t, cfg, "uninstall", true)
	if groups := groupsFor(t, readHooks(t, cfg.Path), eventWithMatcher); len(groups) != 1 {
		t.Fatalf("after uninstall: got %d groups, want 1 (the user's)", len(groups))
	}
}

// userAuthoredGroup is a hook group the daemon must never touch — it carries no
// sentinel of ours.
func userAuthoredGroup() map[string]interface{} {
	return map[string]interface{}{
		"matcher": "Bash",
		"hooks": []interface{}{
			map[string]interface{}{"type": "command", "command": "echo mine"},
		},
	}
}

// TestEnsureInstalled_UpgradesStaleEntryInPlace covers the migration path both
// adapters depend on — Claude Code's curl→http switch (#1161) and a Codex
// curl-flag change: an entry carrying our sentinel but no longer canonical is
// rewritten where it sits, never appended alongside.
func TestEnsureInstalled_UpgradesStaleEntryInPlace(t *testing.T) {
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			cfg := build(filepath.Join(t.TempDir(), "settings.json"))
			// A legacy entry: our sentinel inside an outdated command wrapper,
			// under a matcher we no longer install.
			seedSettings(t, cfg.Path, map[string]interface{}{
				"hooks": map[string]interface{}{
					eventWithMatcher: []interface{}{
						map[string]interface{}{
							"matcher": "OldMatcher",
							"hooks": []interface{}{
								map[string]interface{}{
									"type":    "command",
									"command": "curl --old-flags http://" + cfg.Sentinel,
								},
							},
						},
					},
				},
			})

			mustEnsure(t, cfg, "install over a stale entry", true)
			assertUpgradedInPlace(t, cfg)
			// The upgrade converges: a re-run changes nothing.
			mustEnsure(t, cfg, "re-install after upgrade", false)
		})
	}
}

// assertUpgradedInPlace checks that the seeded stale group was rewritten where
// it sat — same single group, matcher reconciled, entry now canonical.
func assertUpgradedInPlace(t *testing.T, cfg Config) {
	t.Helper()
	groups := groupsFor(t, readHooks(t, cfg.Path), eventWithMatcher)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1 — the stale entry was duplicated, not upgraded", len(groups))
	}
	group := groups[0].(map[string]interface{})
	if m, _ := group["matcher"].(string); m != matcher {
		t.Errorf("stale matcher = %q, want %q (not reconciled)", m, matcher)
	}
	entry := group["hooks"].([]interface{})[0].(map[string]interface{})
	if !cfg.IsCanonical(entry) {
		t.Errorf("entry not upgraded to canonical form: %#v", entry)
	}
}

// TestEnsureInstalled_StripsMatcherFromStaleTurnEndGroup is the other half of
// matcher reconciliation: an install predating the "Stop takes no matcher" rule
// must have the key removed, not merely overwritten.
func TestEnsureInstalled_StripsMatcherFromStaleTurnEndGroup(t *testing.T) {
	cfg := httpConfig(filepath.Join(t.TempDir(), "settings.json"))
	seedSettings(t, cfg.Path, map[string]interface{}{
		"hooks": map[string]interface{}{
			eventNoMatcher: []interface{}{
				map[string]interface{}{"matcher": "*", "hooks": []interface{}{cfg.Entry()}},
			},
		},
	})

	mustEnsure(t, cfg, "install over a stale turn-end matcher", true)

	if group := firstGroup(t, cfg.Path, eventNoMatcher); group["matcher"] != nil {
		t.Errorf("matcher key survived on the turn-end group: %#v", group)
	}
}

// TestEnsureInstalled_ReplacesNonObjectHooksValue: these files are
// hand-editable, so "hooks" can hold anything. A non-object value is replaced
// rather than crashing the install.
func TestEnsureInstalled_ReplacesNonObjectHooksValue(t *testing.T) {
	cfg := httpConfig(filepath.Join(t.TempDir(), "settings.json"))
	if err := os.WriteFile(cfg.Path, []byte(`{"hooks": "not-an-object", "keep": 1}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mustEnsure(t, cfg, "install over a non-object hooks value", true)
	assertInstalled(t, cfg, true)
}

// TestUninstall_NoHooksKey: nothing to remove is not an error, and must not
// rewrite the file.
func TestUninstall_NoHooksKey(t *testing.T) {
	cfg := httpConfig(filepath.Join(t.TempDir(), "settings.json"))
	if err := os.WriteFile(cfg.Path, []byte(`{"model": "x"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg.WriteFile = func(string, []byte) error {
		t.Error("uninstall wrote the file despite finding nothing of ours")
		return nil
	}

	mustUninstall(t, cfg, "uninstall with no hooks key", false)
}

// TestSentinelMatchesEitherDeliveryField pins the one predicate the two
// adapters share rather than parameterize: an entry is ours when the sentinel
// appears in `command` (a shell hook) OR `url` (native http delivery). Checking
// both is what lets a single predicate serve an adapter mid-migration.
func TestSentinelMatchesEitherDeliveryField(t *testing.T) {
	cases := []struct {
		name string
		hook map[string]interface{}
		want bool
	}{
		{"command carries it", map[string]interface{}{"command": "curl " + httpURL}, true},
		{"url carries it", map[string]interface{}{"url": httpURL}, true},
		{"foreign command", map[string]interface{}{"command": "echo hi"}, false},
		{"foreign url", map[string]interface{}{"url": "http://example.test/hook"}, false},
		{"non-string field", map[string]interface{}{"command": 42}, false},
		{"no delivery field", map[string]interface{}{"type": "http"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := entryIsSentinel(tc.hook, httpSentinel); got != tc.want {
				t.Errorf("entryIsSentinel = %v, want %v", got, tc.want)
			}
		})
	}
}

// assertReadsEmpty asserts ReadSettings yields a usable empty map — never a nil
// one, which every later write would panic on.
func assertReadsEmpty(t *testing.T, path string) {
	t.Helper()
	got, err := ReadSettings(path)
	if err != nil {
		t.Fatalf("ReadSettings(%s): %v", path, err)
	}
	if got == nil {
		t.Fatalf("ReadSettings(%s): got a nil map; a later write would panic on it", path)
	}
	if len(got) != 0 {
		t.Errorf("ReadSettings(%s) = %#v, want an empty map", path, got)
	}
}

// seedRaw writes a literal file body (bypassing JSON marshalling) and returns
// its path, for the shapes a settings file can degenerate into.
func seedRaw(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return path
}

func TestReadSettings_MissingFileIsEmpty(t *testing.T) {
	assertReadsEmpty(t, filepath.Join(t.TempDir(), "absent.json"))
}

// A settings file the user (or a crashed writer) left blank must not block the
// install — there is simply nothing to merge onto yet.
func TestReadSettings_BlankFileIsEmpty(t *testing.T) {
	assertReadsEmpty(t, seedRaw(t, "blank.json", "  \n\t "))
}

// TestReadSettings_NullDocumentIsEmpty: a bare `null` decodes to a NIL map with
// no error, and every later write would panic on it. Regression guard.
func TestReadSettings_NullDocumentIsEmpty(t *testing.T) {
	path := seedRaw(t, "null.json", "null\n")
	assertReadsEmpty(t, path)

	// The whole install must survive it, not just the read.
	cfg := httpConfig(path)
	mustEnsure(t, cfg, "install over a null document", true)
	assertInstalled(t, cfg, true)
}

// Malformed is different from blank: overwriting it would destroy content the
// user meant to keep, so it must surface as an error.
func TestReadSettings_MalformedJSONErrors(t *testing.T) {
	if _, err := ReadSettings(seedRaw(t, "bad.json", `{"hooks":`)); err == nil {
		t.Error("ReadSettings on malformed JSON: got nil error, want a decode error")
	}
}

// TestWriteSettings_ShapeAndDelegation pins the on-disk shape (2-space indent,
// trailing newline — these are hand-edited files) and that persistence is
// delegated to the caller's writer rather than done here.
func TestWriteSettings_ShapeAndDelegation(t *testing.T) {
	var gotPath string
	var gotData []byte
	err := WriteSettings("/somewhere/settings.json", map[string]interface{}{"a": "b"},
		func(path string, data []byte) error {
			gotPath, gotData = path, data
			return nil
		})
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	if gotPath != "/somewhere/settings.json" {
		t.Errorf("path = %q, want the one passed in", gotPath)
	}
	if want := "{\n  \"a\": \"b\"\n}\n"; string(gotData) != want {
		t.Errorf("data = %q, want %q", gotData, want)
	}
}
