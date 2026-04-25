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

	// Pre-populate with an existing hook.
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "echo custom-hook",
						},
					},
				},
			},
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

	// Our hooks should be added.
	for _, event := range installedHookEvents {
		if _, ok := hooksMap[event]; !ok {
			t.Errorf("missing hook for %s", event)
		}
	}

	// Existing hook should be preserved.
	preToolUse, ok := hooksMap["PreToolUse"].([]interface{})
	if !ok || len(preToolUse) == 0 {
		t.Error("existing PreToolUse hook was removed")
	}
}

func TestEnsureHooksInstalled_UpgradesStaleCommand(t *testing.T) {
	home := withTempHome(t)
	path := filepath.Join(home, ".claude", "settings.json")

	// Stale command: same sentinel, but missing the trailing `|| true`.
	const staleCommand = "curl -fsS --max-time 1 -X POST --data-binary @- http://localhost:7837/api/v1/hooks/claudecode"
	if staleCommand == installedHookCommand {
		t.Fatal("stale command must differ from canonical command for this test to be meaningful")
	}

	staleGroup := map[string]interface{}{
		"matcher": hookMatcher,
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": staleCommand,
			},
		},
	}
	existing := map[string]interface{}{
		"hooks": map[string]interface{}{
			HookPostToolUse: []interface{}{staleGroup},
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
		t.Fatal("expected modified=true when upgrading stale command")
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
	innerHooks := group["hooks"].([]interface{})
	cmd := innerHooks[0].(map[string]interface{})["command"].(string)
	if cmd != installedHookCommand {
		t.Fatalf("expected upgraded command, got %q", cmd)
	}

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
	foreignGroup := map[string]interface{}{
		"matcher": "Bash",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": "echo foreign",
			},
		},
	}
	arr := hooksMap["PermissionRequest"].([]interface{})
	hooksMap["PermissionRequest"] = append(arr, foreignGroup)
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
