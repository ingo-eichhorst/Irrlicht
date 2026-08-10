// hookinstaller_test.go covers the writing half: the install-type permission
// gate (#797), and the install/verify/uninstall round trip against Copilot's
// own hooks directory.
package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/permission"
	"irrlicht/core/internal/contracttesting"
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

// hooksFileHasOurEntries reports whether the hooks file currently holds an
// irrlicht entry for every installed event.
func hooksFileHasOurEntries(t *testing.T) bool {
	t.Helper()
	path, err := copilotHooksPath()
	if err != nil {
		t.Fatalf("copilotHooksPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}
	for _, ev := range installedHookEvents {
		if !hooksJSONHasEvent(hooks, ev) {
			return false
		}
	}
	return true
}

func hooksJSONHasEvent(hooks map[string]interface{}, event string) bool {
	groups, ok := hooks[event].([]interface{})
	if !ok {
		return false
	}
	for _, g := range groups {
		group, ok := g.(map[string]interface{})
		if !ok {
			continue
		}
		inner, ok := group["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, h := range inner {
			entry, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if url, _ := entry["url"].(string); url != "" &&
				filepath.Base(url) == filepath.Base(HookEndpointPath) {
				return true
			}
		}
	}
	return false
}

// TestHooksPermission_IsGated wires the install-type flavour of the #797
// contract: nothing is written while the permission is pending, granting
// writes the entries, and denying removes them again.
func TestHooksPermission_IsGated(t *testing.T) {
	t.Setenv(copilotHomeEnvVar, t.TempDir())
	apply, remove := hooksPermission(t)

	state := permission.StatePending
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		SetState: func(s permission.State) { state = s },
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
// what the installer writes, what Verify reports, and what Uninstall removes.
func TestInstallVerifyUninstall_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv(copilotHomeEnvVar, home)

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

// TestVerifyDoesNotCreateTheConfigFile is the read-only half of #1372, checked
// here as well as by the registry tripwire because this adapter's config path
// is a file inside a directory that may not exist either.
func TestVerifyDoesNotCreateTheConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(copilotHomeEnvVar, home)

	if _, err := VerifyHooksInstalled(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "hooks")); !os.IsNotExist(err) {
		t.Errorf("Verify created the hooks directory; it must be read-only")
	}
}

// TestUninstallLeavesForeignEntriesAlone pins that a hook the user wrote in
// our own file is preserved. Copilot unions every *.json in the directory, so
// a user could legitimately add one to irrlicht.json by hand.
func TestUninstallLeavesForeignEntriesAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv(copilotHomeEnvVar, home)
	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}

	path, err := copilotHooksPath()
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
	hooks["Stop"] = append(hooks["Stop"].([]interface{}), map[string]interface{}{
		"hooks": []interface{}{map[string]interface{}{"type": "http", "url": "https://example.invalid/mine"}},
	})
	out, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("file gone after uninstall, user entry lost: %v", err)
	}
	if !strings.Contains(string(data), "example.invalid/mine") {
		t.Error("uninstall removed a hook entry irrlicht did not install")
	}
}

// TestUninstallGivesTheDirectoryBack pins the consent-hygiene half of
// uninstall. This hook file is one irrlicht created and nothing else writes,
// so leaving an empty `{}` behind after a revoke is the same failure #1371
// fixed one level up: the user does not get their directory back.
func TestUninstallGivesTheDirectoryBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv(copilotHomeEnvVar, home)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	path, err := copilotHooksPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("install did not create %s: %v", path, err)
	}

	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		data, _ := os.ReadFile(path)
		t.Errorf("uninstall left %s behind (content %q) — irrlicht created this file "+
			"and nothing else writes it, so revoking consent must remove it", path, string(data))
	}
}
