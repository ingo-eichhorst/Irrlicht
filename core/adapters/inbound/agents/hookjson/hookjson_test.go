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

func httpConfig(path string) Config {
	return Config{
		Path:       path,
		Sentinel:   httpSentinel,
		Events:     []string{eventWithMatcher, eventNoMatcher},
		MatcherFor: matcherForTestEvent,
		Entry: func() map[string]interface{} {
			return map[string]interface{}{"type": "http", "url": httpURL, "timeout": 5}
		},
		IsCanonical: func(hook map[string]interface{}) bool {
			if _, hasCmd := hook["command"]; hasCmd {
				return false
			}
			t, _ := hook["type"].(string)
			u, _ := hook["url"].(string)
			return t == "http" && u == httpURL
		},
		WriteFile: writeTestFile,
	}
}

func cmdConfig(path string) Config {
	return Config{
		Path:       path,
		Sentinel:   cmdSentinel,
		Events:     []string{eventWithMatcher, eventNoMatcher},
		MatcherFor: matcherForTestEvent,
		Entry: func() map[string]interface{} {
			return map[string]interface{}{"type": "command", "command": cmdCommand, "timeout": 5}
		},
		IsCanonical: func(hook map[string]interface{}) bool {
			t, _ := hook["type"].(string)
			c, _ := hook["command"].(string)
			return t == "command" && c == cmdCommand
		},
		WriteFile: writeTestFile,
	}
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

// writeTestFile stands in for the adapters' atomic writers, which stay on their
// side of Config precisely because they differ.
func writeTestFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
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

func TestEnsureInstalled_CreatesIdempotentlyAndUninstalls(t *testing.T) {
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			// A nested dir the writer must create.
			path := filepath.Join(t.TempDir(), "nested", "settings.json")
			cfg := build(path)

			modified, err := EnsureInstalled(cfg)
			if err != nil {
				t.Fatalf("first install: %v", err)
			}
			if !modified {
				t.Fatal("first install: modified=false, want true")
			}

			hooks := readHooks(t, path)
			for _, event := range cfg.Events {
				if !HasOurHook(hooks, event, cfg.Sentinel) {
					t.Errorf("event %s: no entry installed", event)
				}
			}

			if modified, err = EnsureInstalled(cfg); err != nil {
				t.Fatalf("second install: %v", err)
			} else if modified {
				t.Error("second install: modified=true, want false (not idempotent)")
			}

			if modified, err = Uninstall(cfg); err != nil {
				t.Fatalf("uninstall: %v", err)
			} else if !modified {
				t.Error("uninstall: modified=false, want true")
			}
			hooks = readHooks(t, path)
			for _, event := range cfg.Events {
				if HasOurHook(hooks, event, cfg.Sentinel) {
					t.Errorf("event %s: entry survived uninstall", event)
				}
				if _, present := hooks[event]; present {
					t.Errorf("event %s: key kept after its last group was removed", event)
				}
			}

			if modified, err = Uninstall(cfg); err != nil {
				t.Fatalf("second uninstall: %v", err)
			} else if modified {
				t.Error("second uninstall: modified=true, want false")
			}
		})
	}
}

// TestEnsureInstalled_MatcherKeyOmittedWhenEmpty pins the shape a turn-end hook
// requires in both Claude Code and Codex: no matcher key at all, not an empty
// string. Both upstreams reject a Stop hook that carries one.
func TestEnsureInstalled_MatcherKeyOmittedWhenEmpty(t *testing.T) {
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if _, err := EnsureInstalled(build(path)); err != nil {
				t.Fatalf("install: %v", err)
			}
			hooks := readHooks(t, path)

			group := groupsFor(t, hooks, eventNoMatcher)[0].(map[string]interface{})
			if _, has := group["matcher"]; has {
				t.Errorf("%s group carries a matcher key: %#v", eventNoMatcher, group)
			}

			group = groupsFor(t, hooks, eventWithMatcher)[0].(map[string]interface{})
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
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := map[string]interface{}{
		"model": "user-choice",
		"hooks": map[string]interface{}{
			eventWithMatcher: []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{"type": "command", "command": "echo mine"},
					},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := httpConfig(path)
	if _, err := EnsureInstalled(cfg); err != nil {
		t.Fatalf("install: %v", err)
	}

	var settings map[string]interface{}
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if settings["model"] != "user-choice" {
		t.Errorf("unrelated top-level key lost: %#v", settings["model"])
	}

	hooks := readHooks(t, path)
	groups := groupsFor(t, hooks, eventWithMatcher)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2 (the user's plus ours)", len(groups))
	}
	userGroup := groups[0].(map[string]interface{})
	if m, _ := userGroup["matcher"].(string); m != "Bash" {
		t.Errorf("user group matcher rewritten to %q; only sentinel-bearing groups may be touched", m)
	}

	// Uninstall must give the file back exactly as seeded.
	if _, err := Uninstall(cfg); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	groups = groupsFor(t, readHooks(t, path), eventWithMatcher)
	if len(groups) != 1 {
		t.Fatalf("after uninstall: got %d groups, want 1 (the user's)", len(groups))
	}
}

// TestEnsureInstalled_UpgradesStaleEntryInPlace covers the migration path both
// adapters depend on — Claude Code's curl→http switch (#1161) and a Codex
// curl-flag change: an entry carrying our sentinel but no longer canonical is
// rewritten where it sits, never appended alongside.
func TestEnsureInstalled_UpgradesStaleEntryInPlace(t *testing.T) {
	for name, build := range configs() {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			cfg := build(path)

			// A legacy entry: our sentinel inside an outdated command wrapper.
			stale := map[string]interface{}{
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
			}
			data, _ := json.MarshalIndent(stale, "", "  ")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}

			if modified, err := EnsureInstalled(cfg); err != nil {
				t.Fatalf("install: %v", err)
			} else if !modified {
				t.Fatal("modified=false, want true (a stale entry needs rewriting)")
			}

			groups := groupsFor(t, readHooks(t, path), eventWithMatcher)
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

			// And the upgrade converges: a re-run changes nothing.
			if modified, err := EnsureInstalled(cfg); err != nil {
				t.Fatalf("re-install: %v", err)
			} else if modified {
				t.Error("re-install after upgrade: modified=true, want false")
			}
		})
	}
}

// TestEnsureInstalled_StripsMatcherFromStaleTurnEndGroup is the other half of
// matcher reconciliation: an install predating the "Stop takes no matcher" rule
// must have the key removed, not merely overwritten.
func TestEnsureInstalled_StripsMatcherFromStaleTurnEndGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	cfg := httpConfig(path)

	stale := map[string]interface{}{
		"hooks": map[string]interface{}{
			eventNoMatcher: []interface{}{
				map[string]interface{}{
					"matcher": "*",
					"hooks":   []interface{}{cfg.Entry()},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(stale, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if modified, err := EnsureInstalled(cfg); err != nil {
		t.Fatalf("install: %v", err)
	} else if !modified {
		t.Fatal("modified=false, want true (the stale matcher key needs stripping)")
	}

	group := groupsFor(t, readHooks(t, path), eventNoMatcher)[0].(map[string]interface{})
	if _, has := group["matcher"]; has {
		t.Errorf("matcher key survived on the turn-end group: %#v", group)
	}
}

// TestEnsureInstalled_ReplacesNonObjectHooksValue: these files are
// hand-editable, so "hooks" can be anything. A non-object value is replaced
// rather than crashing the install.
func TestEnsureInstalled_ReplacesNonObjectHooksValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks": "not-an-object", "keep": 1}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := httpConfig(path)
	if _, err := EnsureInstalled(cfg); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !HasOurHook(readHooks(t, path), eventNoMatcher, cfg.Sentinel) {
		t.Error("install did not recover from a non-object hooks value")
	}
}

// TestUninstall_NoHooksKey: nothing to remove is not an error, and must not
// rewrite the file.
func TestUninstall_NoHooksKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"model": "x"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg := httpConfig(path)
	cfg.WriteFile = func(string, []byte) error {
		t.Fatal("uninstall wrote the file despite finding nothing of ours")
		return nil
	}
	if modified, err := Uninstall(cfg); err != nil {
		t.Fatalf("uninstall: %v", err)
	} else if modified {
		t.Error("modified=true, want false")
	}
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

func TestReadSettings(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing file is empty, not an error", func(t *testing.T) {
		got, err := ReadSettings(filepath.Join(dir, "absent.json"))
		if err != nil {
			t.Fatalf("ReadSettings: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %#v, want an empty map", got)
		}
	})

	// A settings file the user (or a crashed writer) left blank must not block
	// the install — there is simply nothing to merge onto yet.
	t.Run("blank file is empty, not an error", func(t *testing.T) {
		path := filepath.Join(dir, "blank.json")
		if err := os.WriteFile(path, []byte("  \n\t "), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		got, err := ReadSettings(path)
		if err != nil {
			t.Fatalf("ReadSettings: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %#v, want an empty map", got)
		}
	})

	// Malformed is different from blank: overwriting it would destroy content
	// the user meant to keep, so it must surface as an error.
	t.Run("malformed JSON errors", func(t *testing.T) {
		path := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(path, []byte(`{"hooks":`), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := ReadSettings(path); err == nil {
			t.Error("ReadSettings on malformed JSON: got nil error, want a decode error")
		}
	})
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
