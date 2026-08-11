package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
)

// readHooks reads the installed hooks.json under home and returns the "hooks"
// map (or nil if absent).
func readHooks(t *testing.T, home string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "hooks.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal hooks.json: %v", err)
	}
	m, _ := settings["hooks"].(map[string]interface{})
	return m
}

func eventHasSentinel(hooks map[string]interface{}, event string) bool {
	return hookjson.HasOurHook(hooks, event, hookSentinel)
}

func TestEnsureAndUninstallHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv(codexHomeEnvVar, home)

	// First install writes the file and reports modified.
	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	if !modified {
		t.Fatal("first install: got modified=false, want true")
	}

	hooks := readHooks(t, home)
	if hooks == nil {
		t.Fatal("hooks.json was not created")
	}
	for _, event := range installedHookEvents {
		if !eventHasSentinel(hooks, event) {
			t.Errorf("event %s: no irrlicht hook entry installed", event)
		}
	}

	// Second install is a no-op (idempotent).
	modified, err = EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("second EnsureHooksInstalled: %v", err)
	}
	if modified {
		t.Error("second install: got modified=true, want false (idempotent)")
	}

	// Uninstall removes and reports modified.
	modified, err = UninstallHooks()
	if err != nil {
		t.Fatalf("UninstallHooks: %v", err)
	}
	if !modified {
		t.Error("uninstall: got modified=false, want true")
	}
	hooks = readHooks(t, home)
	for _, event := range installedHookEvents {
		if eventHasSentinel(hooks, event) {
			t.Errorf("event %s: hook entry still present after uninstall", event)
		}
	}

	// Second uninstall is a no-op.
	modified, err = UninstallHooks()
	if err != nil {
		t.Fatalf("second UninstallHooks: %v", err)
	}
	if modified {
		t.Error("second uninstall: got modified=true, want false")
	}
}

func TestStopHookHasNoMatcher(t *testing.T) {
	home := t.TempDir()
	t.Setenv(codexHomeEnvVar, home)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}

	hooks := readHooks(t, home)
	arr, ok := hooks[HookStop].([]interface{})
	if !ok || len(arr) == 0 {
		t.Fatalf("Stop event not installed: %v", hooks[HookStop])
	}
	group := arr[0].(map[string]interface{})
	if _, hasMatcher := group["matcher"]; hasMatcher {
		t.Error("Stop hook group carries a matcher key; Codex Stop fires at every turn end and takes none")
	}

	// A non-Stop event does carry the wildcard matcher.
	permArr := hooks[HookPermissionRequest].([]interface{})
	permGroup := permArr[0].(map[string]interface{})
	if permGroup["matcher"] != hookMatcher {
		t.Errorf("PermissionRequest matcher: got %v, want %q", permGroup["matcher"], hookMatcher)
	}
}

func TestEnsurePreservesUnrelatedContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv(codexHomeEnvVar, home)

	// A pre-existing hooks.json with an unrelated top-level key and a
	// user-authored hook the daemon must not touch.
	existing := map[string]interface{}{
		"description": "user config",
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
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
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), data, 0o600); err != nil {
		t.Fatalf("seed hooks.json: %v", err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}

	raw, _ := os.ReadFile(filepath.Join(home, "hooks.json"))
	var settings map[string]interface{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if settings["description"] != "user config" {
		t.Errorf("top-level description not preserved: %v", settings["description"])
	}
	if !strings.Contains(string(raw), "echo mine") {
		t.Error("user-authored PreToolUse hook was clobbered")
	}
	// And our hooks are now present.
	hooks := readHooks(t, home)
	if !eventHasSentinel(hooks, HookStop) {
		t.Error("irrlicht Stop hook not installed alongside existing content")
	}
}

// TestHooksPermission_InstallGateContract exercises the install-type gate: the
// hooks permission's effect (entries on disk) must appear only while granted
// and be undone on revoke, driven through the real Apply/Remove closures.
func TestHooksPermission_InstallGateContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv(codexHomeEnvVar, home)

	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: PermissionKeyHooks,
		// transcripts is the adapter's other declared permission and is
		// observe-kind — it carries no Apply/Remove, so granting it must leave
		// hooks.json untouched while hooks itself is denied.
		OtherKeys: []string{PermissionKeyTranscripts},
		SetState: func(key string, state permission.State) {
			if key != PermissionKeyHooks {
				return // no install effect of its own to drive
			}
			var err error
			if state == permission.StateGranted {
				_, err = EnsureHooksInstalled()
			} else {
				_, err = UninstallHooks()
			}
			if err != nil {
				t.Fatalf("drive permission to %v: %v", state, err)
			}
		},
		Exercise: func() {},
		Observe: func() bool {
			return eventHasSentinel(readHooks(t, home), HookStop)
		},
	})
}
