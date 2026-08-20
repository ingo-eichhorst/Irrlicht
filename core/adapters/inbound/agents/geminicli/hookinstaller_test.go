// hookinstaller_test.go covers the writing half: the install-type permission
// gate (#797), and the install/verify/uninstall round trip against Gemini
// CLI's own shared settings.json.
//
// The shared-file shape is the point of several tests here that copilot's
// equivalent file does not need: ~/.gemini/settings.json is Gemini CLI's ONE
// general settings file (theme, auth, everything), not a file irrlicht owns
// outright the way copilot's hooks/irrlicht.json is — so uninstall must never
// delete it, and any key this adapter did not write must survive untouched.
// That is also the property that makes gemini-cli's documented sync-by-
// omission settings writer survivable at all: Verify (wired below and in
// hookversion_test.go's gate) is what lets services.HookEntryVerifier notice
// and repair an install Gemini CLI's own settings UI silently dropped.
package geminicli

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// hooksFileHasOurEntries reports whether the settings file currently holds
// an irrlicht entry for every installed event.
//
// Uses hookjson's OWN predicates rather than a hand-rolled walk, so the test
// agrees with the installer by construction — the same reasoning copilot's
// equivalent helper documents.
func hooksFileHasOurEntries(t *testing.T) bool {
	t.Helper()
	path, err := geminiSettingsPath()
	if err != nil {
		t.Fatalf("geminiSettingsPath: %v", err)
	}
	settings, err := hookjson.ReadSettings(path)
	if err != nil {
		return false
	}
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}
	sentinel := hookbeacon.Sentinel(AdapterName)
	for _, ev := range installedHookEvents {
		if !hookjson.HasOurHook(hooks, ev, sentinel) {
			return false
		}
	}
	return true
}

// TestHooksPermission_IsGated wires the install-type flavour of the #797
// contract: nothing is written while the permission is pending, granting
// writes the entries, and denying removes them again.
func TestHooksPermission_IsGated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	apply, remove := hooksPermission(t)

	state := permission.StatePending
	contracttesting.AssertPermissionGated(t, contracttesting.PermissionGate{
		Key: PermissionKeyHooks,
		// transcripts is the adapter's only other declared permission and is
		// observe-kind, so it has no closure to drive: the key-isolation arm
		// is INERT here and repeats the revoked arm exactly, the same
		// situation copilot's equivalent test documents. Install-type
		// wirings hold their own permission's closures, so a wrong key is
		// not representable — the arm is load-bearing at the live receiver
		// (hooks_test.go), not here.
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
// removes.
func TestInstallVerifyUninstall_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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

// TestVerifyDoesNotCreateTheConfigFile is the read-only half of #1372.
func TestVerifyDoesNotCreateTheConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := VerifyHooksInstalled(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Errorf("Verify created the .gemini directory; it must be read-only")
	}
}

// TestUninstallPreservesUnrelatedSettingsKeys is the shared-file property
// this whole issue turns on: ~/.gemini/settings.json is Gemini CLI's general
// settings file, so install and uninstall must touch only the "hooks" key.
// A destructive rewrite here would be irrlicht doing to the user's file
// exactly what #1355 Phase C found Gemini CLI's OWN writer capable of.
func TestUninstallPreservesUnrelatedSettingsKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := geminiSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"security":{"auth":{"selectedType":"oauth-personal"}},"someUnknownTopLevelKey":"keep-me"}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("settings file gone after uninstall: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings file is not valid JSON after install+uninstall: %v", err)
	}
	sec, ok := settings["security"].(map[string]interface{})
	if !ok {
		t.Fatal("security key lost after install+uninstall")
	}
	auth, ok := sec["auth"].(map[string]interface{})
	if !ok || auth["selectedType"] != "oauth-personal" {
		t.Errorf("security.auth.selectedType = %v, want preserved \"oauth-personal\"", auth["selectedType"])
	}
	if settings["someUnknownTopLevelKey"] != "keep-me" {
		t.Errorf("someUnknownTopLevelKey = %v, want preserved \"keep-me\" — irrlicht must "+
			"never touch a key it did not write", settings["someUnknownTopLevelKey"])
	}
}

// TestUninstallDoesNotDeleteTheSettingsFile pins the shared-file counterpart
// of copilot's "gives the directory back": unlike copilot's dedicated
// hooks/irrlicht.json (which IS irrlicht's own file and is removed when
// empty), ~/.gemini/settings.json is never irrlicht's to delete, even when
// our entries were the only content it ever held.
func TestUninstallDoesNotDeleteTheSettingsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if _, err := EnsureHooksInstalled(); err != nil {
		t.Fatalf("install: %v", err)
	}
	path, err := geminiSettingsPath()
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
		t.Errorf("uninstall removed %s; this file is the user's general Gemini CLI settings, "+
			"never irrlicht's to delete: %v", path, err)
	}
}
