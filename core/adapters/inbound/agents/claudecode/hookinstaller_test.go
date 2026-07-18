package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// withTempHome overrides $HOME for the duration of the test so that
// claudeSettingsPath() resolves to a temp directory.
func withTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

func readJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// legacyMatcher is the pre-#307 PostToolUse matcher, used to seed test
// fixtures that simulate an existing irrlicht install from before the
// AskUserQuestion / ExitPlanMode expansion.
const legacyMatcher = "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch|mcp__.*"

// legacyCurlHookCommand is the pre-#1161 curl command form of our hook, used to
// seed fixtures that simulate an existing install from before native http
// delivery, so the migration path can be exercised.
const legacyCurlHookCommand = "curl -fsS --max-time 1 -X POST --data-binary @- http://localhost:7837/api/v1/hooks/claudecode || true"

// makeHookGroup builds a settings.json hook matcher group with the given
// matcher and a legacy `type: command` inner hook. Used by tests to seed
// pre-#1161 (curl) and foreign fixtures.
func makeHookGroup(matcher, command string) map[string]interface{} {
	return map[string]interface{}{
		"matcher": matcher,
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": command,
			},
		},
	}
}

// ourHookGroup builds a settings.json group in our current native-http form
// (matcher key omitted when empty), mirroring addOurHook. Used to seed fixtures
// that simulate an already-migrated install.
func ourHookGroup(matcher string) map[string]interface{} {
	group := map[string]interface{}{"hooks": []interface{}{ourHookEntry()}}
	if matcher != "" {
		group["matcher"] = matcher
	}
	return group
}

// assertHTTPEntry fails unless the group's single inner hook is our canonical
// native-http entry: type "http", our endpoint url, and no leftover command key.
func assertHTTPEntry(t *testing.T, group map[string]interface{}) {
	t.Helper()
	inner, ok := group["hooks"].([]interface{})
	if !ok || len(inner) != 1 {
		t.Fatalf("expected exactly 1 inner hook, got %v", group["hooks"])
	}
	hook := inner[0].(map[string]interface{})
	if _, hasCmd := hook["command"]; hasCmd {
		t.Errorf("http entry must not carry a legacy command key: %v", hook)
	}
	if ty, _ := hook["type"].(string); ty != "http" {
		t.Errorf("type = %q, want %q", ty, "http")
	}
	if u, _ := hook["url"].(string); u != hookEndpointURL {
		t.Errorf("url = %q, want %q", u, hookEndpointURL)
	}
}

func TestEnsureHooksInstalled_CreatesFileIfAbsent(t *testing.T) {
	home := withTempHome(t)
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true for fresh install")
	}

	path := filepath.Join(home, ".claude", "settings.json")
	settings := readJSON(t, path)

	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("missing hooks map")
	}

	for _, event := range installedHookEvents {
		arr, ok := hooksMap[event].([]interface{})
		if !ok || len(arr) == 0 {
			t.Errorf("missing hook array for %s", event)
		}
	}
}

func TestEnsureHooksInstalled_Idempotent(t *testing.T) {
	withTempHome(t)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Fatal("expected modified=false on second install (idempotent)")
	}
}

func TestEnsureHooksInstalled_PreservesExistingHooks(t *testing.T) {
	home := withTempHome(t)
	path := filepath.Join(home, ".claude", "settings.json")

	// Pre-populate with an existing foreign hook on PreToolUse.
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{makeHookGroup("Bash", "echo custom-hook")},
		},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, path)
	hooksMap := settings["hooks"].(map[string]interface{})

	// Our hooks should be added for all managed events.
	for _, event := range installedHookEvents {
		if _, ok := hooksMap[event]; !ok {
			t.Errorf("missing hook for %s", event)
		}
	}

	// Foreign PreToolUse group should be preserved (alongside our new one).
	preToolUse, ok := hooksMap["PreToolUse"].([]interface{})
	if !ok || len(preToolUse) < 2 {
		t.Errorf("expected at least 2 PreToolUse groups (foreign + ours), got %d", len(preToolUse))
	}
}

// TestEnsureHooksInstalled_MigratesCurlToHTTP verifies that a pre-#1161 install
// whose hook is the legacy curl `command` is migrated in place to native
// `type: http` delivery — the command key dropped, the http url set, no group
// appended — and that the matcher is reconciled at the same time (#1161).
func TestEnsureHooksInstalled_MigratesCurlToHTTP(t *testing.T) {
	home := withTempHome(t)
	path := filepath.Join(home, ".claude", "settings.json")

	// Legacy install: a single PostToolUse group with the pre-#307 narrow
	// matcher and the curl command. EnsureHooksInstalled should upgrade both the
	// delivery (curl → http) and the matcher in place — no group append.
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			HookPostToolUse: []interface{}{makeHookGroup(legacyMatcher, legacyCurlHookCommand)},
		},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true when migrating curl → http")
	}

	settings := readJSON(t, path)
	hooksMap := settings["hooks"].(map[string]interface{})

	postArr, ok := hooksMap[HookPostToolUse].([]interface{})
	if !ok {
		t.Fatalf("missing %s array after upgrade", HookPostToolUse)
	}
	if len(postArr) != 1 {
		t.Fatalf("expected 1 %s matcher group (in-place upgrade, no append), got %d", HookPostToolUse, len(postArr))
	}

	group := postArr[0].(map[string]interface{})
	if m, _ := group["matcher"].(string); m != hookMatcher {
		t.Errorf("expected matcher upgraded to %q, got %q", hookMatcher, m)
	}
	assertHTTPEntry(t, group)

	// Second call must be idempotent.
	modified, err = EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Fatal("expected modified=false on second install after upgrade (idempotent)")
	}
}

func TestUninstallHooks_RemovesOurHooks(t *testing.T) {
	home := withTempHome(t)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}

	modified, err := UninstallHooks()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true for uninstall")
	}

	path := filepath.Join(home, ".claude", "settings.json")
	settings := readJSON(t, path)
	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		// hooks key removed entirely — acceptable
		return
	}

	for _, event := range installedHookEvents {
		if _, ok := hooksMap[event]; ok {
			t.Errorf("hook for %s should have been removed", event)
		}
	}
}

func TestUninstallHooks_PreservesOtherHooks(t *testing.T) {
	home := withTempHome(t)
	path := filepath.Join(home, ".claude", "settings.json")

	// Install ours, then add a foreign hook to the same event.
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, path)
	hooksMap := settings["hooks"].(map[string]interface{})

	// Add a foreign matcher group to PermissionRequest.
	arr := hooksMap["PermissionRequest"].([]interface{})
	hooksMap["PermissionRequest"] = append(arr, makeHookGroup("Bash", "echo foreign"))
	data, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Uninstall should remove ours but keep the foreign one.
	if _, err := UninstallHooks(); err != nil {
		t.Fatal(err)
	}

	settings = readJSON(t, path)
	hooksMap = settings["hooks"].(map[string]interface{})

	permArr, ok := hooksMap["PermissionRequest"].([]interface{})
	if !ok || len(permArr) != 1 {
		t.Fatalf("expected 1 remaining PermissionRequest group, got %d", len(permArr))
	}
}

// TestEnsureHooksInstalled_InstallsPreToolUseAndExpandedMatcher verifies that
// a fresh install writes the narrow PreToolUse group and uses the expanded
// matcher (including AskUserQuestion|ExitPlanMode) for the clearing events.
// (Issue #307.)
func TestEnsureHooksInstalled_InstallsPreToolUseAndExpandedMatcher(t *testing.T) {
	home := withTempHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, filepath.Join(home, ".claude", "settings.json"))
	hooksMap := settings["hooks"].(map[string]interface{})

	// PreToolUse: one group with the narrow matcher.
	pre, ok := hooksMap[HookPreToolUse].([]interface{})
	if !ok || len(pre) != 1 {
		t.Fatalf("expected 1 PreToolUse group, got %d", len(pre))
	}
	preGroup := pre[0].(map[string]interface{})
	if m, _ := preGroup["matcher"].(string); m != hookMatcherPreToolUse {
		t.Errorf("PreToolUse matcher = %q, want %q", m, hookMatcherPreToolUse)
	}

	// PostToolUse: one group, matcher includes AskUserQuestion|ExitPlanMode.
	post, ok := hooksMap[HookPostToolUse].([]interface{})
	if !ok || len(post) != 1 {
		t.Fatalf("expected 1 PostToolUse group, got %d", len(post))
	}
	postGroup := post[0].(map[string]interface{})
	if m, _ := postGroup["matcher"].(string); m != hookMatcher {
		t.Errorf("PostToolUse matcher = %q, want %q", m, hookMatcher)
	}

	// PreCompact: one group whose matcher is the compaction trigger "manual"
	// (not a tool-name regex), so the hook fires only for a user /compact (#657).
	preCompact, ok := hooksMap[HookPreCompact].([]interface{})
	if !ok || len(preCompact) != 1 {
		t.Fatalf("expected 1 PreCompact group, got %d", len(preCompact))
	}
	preCompactGroup := preCompact[0].(map[string]interface{})
	if m, _ := preCompactGroup["matcher"].(string); m != hookMatcherPreCompact {
		t.Errorf("PreCompact matcher = %q, want %q", m, hookMatcherPreCompact)
	}

	// Stop: one group with NO matcher key (Claude Code rejects a Stop matcher)
	// and native http delivery (#1161).
	stop, ok := hooksMap[HookStop].([]interface{})
	if !ok || len(stop) != 1 {
		t.Fatalf("expected 1 Stop group, got %d", len(stop))
	}
	stopGroup := stop[0].(map[string]interface{})
	if _, has := stopGroup["matcher"]; has {
		t.Errorf("Stop group must not carry a matcher key, got %v", stopGroup["matcher"])
	}
	assertHTTPEntry(t, stopGroup)
}

// TestEnsureHooksInstalled_AddsStopToExistingInstall verifies the idempotent
// installer adds the newly-listed Stop event to a settings.json that already
// carries the five prior events (in native-http form), without duplicating them
// — the upgrade path for existing installs (#1161).
func TestEnsureHooksInstalled_AddsStopToExistingInstall(t *testing.T) {
	home := withTempHome(t)

	existing := map[string]interface{}{"hooks": map[string]interface{}{}}
	hooks := existing["hooks"].(map[string]interface{})
	for _, ev := range []string{HookPermissionRequest, HookPreToolUse, HookPostToolUse, HookPostToolUseFailure, HookPreCompact} {
		hooks[ev] = []interface{}{ourHookGroup(matcherForEvent(ev))}
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true (Stop was missing)")
	}

	settings := readJSON(t, path)
	hooksMap := settings["hooks"].(map[string]interface{})
	stop, ok := hooksMap[HookStop].([]interface{})
	if !ok || len(stop) != 1 {
		t.Fatalf("expected exactly 1 Stop group after upgrade, got %d", len(stop))
	}
	stopGroup := stop[0].(map[string]interface{})
	if _, has := stopGroup["matcher"]; has {
		t.Errorf("Stop group must not carry a matcher key, got %v", stopGroup["matcher"])
	}
	assertHTTPEntry(t, stopGroup)

	// Re-running must be a no-op now that all six events are present.
	modified, err = EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Errorf("expected modified=false on second run (already installed)")
	}
}

// TestEnsureHooksInstalled_AddsPreCompactToExistingInstall verifies the
// idempotent installer adds the newly-listed PreCompact event to a settings.json
// that already carries the original four events, without duplicating them — the
// upgrade path for existing installs (#657).
func TestEnsureHooksInstalled_AddsPreCompactToExistingInstall(t *testing.T) {
	home := withTempHome(t)

	// Seed settings.json with the original four managed hooks (no PreCompact).
	existing := map[string]interface{}{"hooks": map[string]interface{}{}}
	hooks := existing["hooks"].(map[string]interface{})
	for _, ev := range []string{HookPermissionRequest, HookPreToolUse, HookPostToolUse, HookPostToolUseFailure} {
		hooks[ev] = []interface{}{ourHookGroup(matcherForEvent(ev))}
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatalf("expected modified=true (PreCompact was missing)")
	}

	settings := readJSON(t, filepath.Join(home, ".claude", "settings.json"))
	hooksMap := settings["hooks"].(map[string]interface{})
	preCompact, ok := hooksMap[HookPreCompact].([]interface{})
	if !ok || len(preCompact) != 1 {
		t.Fatalf("expected exactly 1 PreCompact group after upgrade, got %d", len(preCompact))
	}

	// Re-running must be a no-op now that all five events are present.
	modified, err = EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Errorf("expected modified=false on second run (already installed)")
	}
}

// TestEnsureHooksInstalled_MigratesLegacyMatchers simulates a pre-#307
// install where PermissionRequest/PostToolUse/PostToolUseFailure each have
// a single managed group with the legacy narrow matcher. EnsureHooksInstalled
// must rewrite those matchers to the expanded form in place (no group
// append) and add the new PreToolUse group.
func TestEnsureHooksInstalled_MigratesLegacyMatchers(t *testing.T) {
	home := withTempHome(t)
	path := filepath.Join(home, ".claude", "settings.json")

	legacy := func() []interface{} {
		return []interface{}{makeHookGroup(legacyMatcher, legacyCurlHookCommand)}
	}
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			HookPermissionRequest:  legacy(),
			HookPostToolUse:        legacy(),
			HookPostToolUseFailure: legacy(),
		},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(existing)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true to migrate matchers and add PreToolUse")
	}

	settings := readJSON(t, path)
	hooksMap := settings["hooks"].(map[string]interface{})

	// Legacy groups upgraded in place; still exactly one group per event.
	for _, event := range []string{HookPermissionRequest, HookPostToolUse, HookPostToolUseFailure} {
		arr := hooksMap[event].([]interface{})
		if len(arr) != 1 {
			t.Errorf("%s: expected 1 group after migration (in-place rewrite), got %d", event, len(arr))
			continue
		}
		group := arr[0].(map[string]interface{})
		if m, _ := group["matcher"].(string); m != hookMatcher {
			t.Errorf("%s: matcher = %q, want %q (legacy not migrated)", event, m, hookMatcher)
		}
	}

	// PreToolUse is brand new.
	if pre, ok := hooksMap[HookPreToolUse].([]interface{}); !ok || len(pre) != 1 {
		t.Errorf("PreToolUse group not installed (groups=%d)", len(pre))
	}

	// Second install is idempotent.
	modified, err = EnsureHooksInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Fatal("expected modified=false on second install (idempotent)")
	}
}

// TestUninstallHooks_RemovesPreToolUseGroup ensures uninstall sweeps both
// the new PreToolUse group and the existing clearing-event groups.
func TestUninstallHooks_RemovesPreToolUseGroup(t *testing.T) {
	home := withTempHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatal(err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatal(err)
	}

	settings := readJSON(t, filepath.Join(home, ".claude", "settings.json"))
	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return // hooks key removed — acceptable
	}
	if _, ok := hooksMap[HookPreToolUse]; ok {
		t.Error("PreToolUse should have been removed")
	}
	if _, ok := hooksMap[HookPostToolUse]; ok {
		t.Error("PostToolUse should have been removed")
	}
}

func TestUninstallHooks_NoFileIsNoop(t *testing.T) {
	withTempHome(t)
	modified, err := UninstallHooks()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Fatal("expected modified=false when no settings file exists")
	}
}
