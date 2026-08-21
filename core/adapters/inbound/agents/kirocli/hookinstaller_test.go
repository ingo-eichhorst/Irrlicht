// hookinstaller_test.go covers the writing half: the install-type permission
// gate (#797), the install/verify/uninstall round trip against a fake
// kiro-cli, and this adapter's own extra obligations beyond every sibling
// hook installer's — materializing an agent config and switching
// chat.defaultAgent, then restoring it on uninstall (issue #1716's audit).
package kirocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
	"irrlicht/core/pkg/hookbeacon"
)

// hooksPermission returns the real declared hooks permission.
func hooksPermission(t *testing.T) (apply, remove func() error) {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			return p.Apply, p.Remove
		}
	}
	t.Fatal("no hooks permission declared")
	return nil, nil
}

// hooksFileHasOurEntries reports whether the irrlicht agent config currently
// holds a flat entry for every installed event.
func hooksFileHasOurEntries(t *testing.T) bool {
	t.Helper()
	path, err := irrlichtAgentConfigPath()
	if err != nil {
		t.Fatalf("irrlichtAgentConfigPath: %v", err)
	}
	status, err := verifyFlatHooksInstalled(kiroHookConfig(path))
	if err != nil {
		return false
	}
	return status.Intact()
}

// TestHooksPermission_IsGated wires the install-type flavour of the #797
// contract: nothing is written while the permission is pending, granting
// writes the entries, and denying removes them again.
func TestHooksPermission_IsGated(t *testing.T) {
	kiroInstallerHome(t)
	apply, remove := hooksPermission(t)

	state := permission.StatePending
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: PermissionKeyHooks,
		// transcripts is the adapter's only other declared permission and is
		// observe-kind, so this arm is inert here and repeats the revoked
		// arm exactly — the same situation copilot's and gemini-cli's
		// equivalent tests document.
		OtherKeys: []string{PermissionKeyTranscripts},
		SetState:  contracttesting.OnlyKey(PermissionKeyHooks, func(s permission.State) { state = s }),
		Exercise: func() {
			switch state {
			case permission.StateGranted:
				if err := apply(); err != nil {
					t.Fatalf("apply: %v", err)
				}
			case permission.StateDenied:
				if err := remove(); err != nil {
					t.Fatalf("remove: %v", err)
				}
			}
		},
		Observe: func() bool { return hooksFileHasOurEntries(t) },
	})
}

// TestInstallVerifyUninstall_RoundTrip pins the three-way agreement between
// what the installer writes, what Verify reports, and what Uninstall
// removes — including the two steps no sibling adapter's install performs:
// materializing the agent config via the (fake) kiro-cli, and pointing
// chat.defaultAgent at it.
func TestInstallVerifyUninstall_RoundTrip(t *testing.T) {
	home := kiroInstallerHome(t)

	status, err := VerifyHooksInstalled()
	if err != nil {
		t.Fatalf("verify before install: %v", err)
	}
	if status.Intact() {
		t.Error("Verify reports intact before anything was installed")
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !modified {
		t.Error("first install reported no modification")
	}

	configPath := filepath.Join(home, "agents", "irrlicht.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("install did not materialize %s: %v", configPath, err)
	}
	settingsPath := filepath.Join(home, "settings", "cli.json")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings/cli.json: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		t.Fatalf("settings/cli.json is not valid JSON: %v", err)
	}
	if settings[defaultAgentSettingsKey] != irrlichtAgentName {
		t.Errorf("chat.defaultAgent = %v, want %q", settings[defaultAgentSettingsKey], irrlichtAgentName)
	}

	if status, err = VerifyHooksInstalled(); err != nil || !status.Intact() {
		t.Errorf("Verify after install: intact=%v err=%v damage=%s", status.Intact(), err, status.Damage())
	}

	// Idempotent: a second install changes nothing.
	if modified, err = EnsureHooksInstalled(); err != nil || modified {
		t.Errorf("second install modified=%v err=%v, want false/nil", modified, err)
	}

	if modified, err = UninstallHooks(); err != nil || !modified {
		t.Errorf("uninstall modified=%v err=%v, want true/nil", modified, err)
	}
	if hooksFileHasOurEntries(t) {
		t.Error("entries survive uninstall")
	}
}

// TestVerifyDoesNotCreateTheConfigFile is the read-only half of #1372.
func TestVerifyDoesNotCreateTheConfigFile(t *testing.T) {
	home := kiroInstallerHome(t)

	if _, err := VerifyHooksInstalled(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "agents")); !os.IsNotExist(err) {
		t.Errorf("Verify created the agents directory; it must be read-only")
	}
}

// TestUninstallLeavesForeignEntriesAlone pins that a hook entry the user
// wrote into the irrlicht agent config by hand is preserved — the same
// property copilot's and gemini-cli's equivalent tests pin for their own
// files.
func TestUninstallLeavesForeignEntriesAlone(t *testing.T) {
	kiroInstallerHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	path, err := irrlichtAgentConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]interface{})
	hooks["agentSpawn"] = []interface{}{map[string]interface{}{"command": "echo mine"}}
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("agent config gone after uninstall, user entry lost: %v", err)
	}
	if !strings.Contains(string(data), "echo mine") {
		t.Error("uninstall removed a hook entry irrlicht did not install")
	}
}

// TestUninstallLeavesTheAgentConfigInPlace pins the deliberate difference from
// copilot's "gives the directory back": the irrlicht agent config is a
// complete, otherwise-usable kiro-cli agent (materialized from the user's own
// built-in default), not an empty file irrlicht invented — the user may since
// have started relying on it directly, so uninstall strips the hook entries
// and leaves the file (see UninstallHooks's own doc comment for the full
// argument).
func TestUninstallLeavesTheAgentConfigInPlace(t *testing.T) {
	kiroInstallerHome(t)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	path, err := irrlichtAgentConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("install did not create %s: %v", path, err)
	}

	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("uninstall removed %s; it must survive, inert, so a user who started relying "+
			"on it directly keeps it: %v", path, err)
	}
}

// TestUninstall_RestoresThePriorDefaultAgent pins the audit's own load-bearing
// requirement: uninstall must restore whatever chat.defaultAgent held before
// this adapter's first grant, not assume kiro_default — which would silently
// discard a user's own prior choice.
func TestUninstall_RestoresThePriorDefaultAgent(t *testing.T) {
	home := kiroInstallerHome(t)
	settingsPath := filepath.Join(home, "settings", "cli.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"chat.defaultAgent":"my-custom-agent","chat.enableTodoList":true}`
	if err := os.WriteFile(settingsPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings/cli.json gone after uninstall: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings/cli.json not valid JSON after uninstall: %v", err)
	}
	if settings[defaultAgentSettingsKey] != "my-custom-agent" {
		t.Errorf("chat.defaultAgent = %v after uninstall, want the restored prior value %q",
			settings[defaultAgentSettingsKey], "my-custom-agent")
	}
	if settings["chat.enableTodoList"] != true {
		t.Error("uninstall lost an unrelated settings key while restoring chat.defaultAgent")
	}
}

// TestUninstall_NoPriorDefaultRestoresTheBuiltIn pins the other branch: a
// user who had NO explicit chat.defaultAgent (running the built-in default)
// gets kiro-cli's own built-in name back, since kiro-cli has no "unset" verb.
func TestUninstall_NoPriorDefaultRestoresTheBuiltIn(t *testing.T) {
	kiroInstallerHome(t)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	settings, err := hookjson.ReadSettings(mustKiroSettingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if settings[defaultAgentSettingsKey] != kiroBuiltinDefaultAgent {
		t.Errorf("chat.defaultAgent = %v after uninstall, want the built-in %q",
			settings[defaultAgentSettingsKey], kiroBuiltinDefaultAgent)
	}
}

// TestUninstall_DoesNotClobberADefaultChangedSinceInstall pins the defensive
// half: if something changed chat.defaultAgent away from "irrlicht" between
// grant and revoke, uninstall must not stomp on that newer choice.
func TestUninstall_DoesNotClobberADefaultChangedSinceInstall(t *testing.T) {
	home := kiroInstallerHome(t)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	settingsPath := filepath.Join(home, "settings", "cli.json")
	if err := os.WriteFile(settingsPath, []byte(`{"chat.defaultAgent":"someone-elses-choice"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	settings, err := hookjson.ReadSettings(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if settings[defaultAgentSettingsKey] != "someone-elses-choice" {
		t.Errorf("chat.defaultAgent = %v after uninstall, want the untouched %q",
			settings[defaultAgentSettingsKey], "someone-elses-choice")
	}
}

func mustKiroSettingsPath(t *testing.T) string {
	t.Helper()
	path, err := kiroSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestForeignBinaryInstall_UpgradedInPlace is a direct, adapter-local
// exercise of the same beacon-drift scenario hookport_test.go's
// AssertHookEndpointFollowsBindAddr contract already runs — kept here as well
// because it is what the mutation evidence for #1716's write-up targets
// (kiroctl.go and the flat-merge reconciliation, rather than the shared
// contract's own machinery).
func TestForeignBinaryInstall_UpgradedInPlace(t *testing.T) {
	kiroInstallerHome(t)
	configPath, err := irrlichtAgentConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"name":"irrlicht","hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	staleCommand, err := hookbeacon.Command("/nonexistent-irrlicht-1716/bin/irrlichd", AdapterName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ensureFlatHooksInstalled(flatHookConfig{
		Path:        configPath,
		Sentinel:    hookbeacon.Sentinel(AdapterName),
		Events:      installedHookEvents,
		Entry:       func() map[string]interface{} { return flatBeaconEntry(staleCommand) },
		IsCanonical: hookEntryIsCanonical,
		WriteFile:   atomicWriteFile,
	}); err != nil {
		t.Fatalf("seed stale install: %v", err)
	}

	modified, err := EnsureHooksInstalled()
	if err != nil {
		t.Fatalf("EnsureHooksInstalled: %v", err)
	}
	if !modified {
		t.Fatal("expected modified=true repointing a stale binary path — an unrepaired install " +
			"reads healthy while delivering nothing")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]interface{})
	for _, event := range installedHookEvents {
		arr, ok := hooks[event].([]interface{})
		if !ok || len(arr) != 1 {
			t.Fatalf("event %s: expected exactly 1 entry (upgraded in place, not duplicated), got %v", event, hooks[event])
		}
	}
}
