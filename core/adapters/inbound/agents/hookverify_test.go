package agents

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// TestEveryHookInstallDeclaresAVerifier is the registry-wide tripwire for issue
// #1372, and the third obligation to ride the projection #1357 added — after
// #1365's version floor and #1357's own ConfigPath/Uninstall pair.
//
// It fails for a NEW adapter that installs hooks into a user config without
// declaring how to check they are still there, before that adapter has a test
// of its own. That matters more here than for most declarations, because the
// omission is invisible in every other way: an adapter with no Verify is simply
// skipped by the re-verification loop, so its entries can be deleted by the
// agent's own settings UI and every surface — the permission state, the
// diagnostics bundle, the wizard — goes on reading green. A missing
// declaration produces no error, no log line and no empty row. It produces
// nothing at all, which is the failure mode #1372 exists to end.
//
// Declaring it is one line: Verify: VerifyHooksInstalled, next to Uninstall.
func TestEveryHookInstallDeclaresAVerifier(t *testing.T) {
	for _, a := range All() {
		for _, p := range a.Permissions {
			// Scoped to HooksPermissionKey since #1383 widened the declaration
			// from "the hook config" to every managed user file — the same
			// scoping TestEveryHookInstallDeclaresAVersionFloor carries.
			// Without it this would demand a hook verifier from claudecode's
			// statusline and instruction blocks, which install no hooks.
			if p.Writes == nil || p.Key != agent.HooksPermissionKey {
				continue
			}
			if p.Writes.Verify == nil {
				t.Errorf("%s/%s installs hooks into a user config but declares no Verify "+
					"(#1372), so the re-verification loop skips it silently: the agent's own "+
					"settings UI can delete our entries and the permission still reads granted "+
					"with no error anywhere. Add Verify: VerifyHooksInstalled next to Uninstall.",
					a.Identity.Name, p.Key)
				continue
			}
			// A hooks permission must be modify-kind. The re-verification loop
			// runs its repair through PermissionService's granted-effect path,
			// which is the same #570 gate the original install passed; an
			// observe-kind hooks permission would be a write behind a read
			// consent, and there would be nothing in the loop to catch it.
			if p.Kind != permission.KindModify {
				t.Errorf("%s/%s declares a hook install but is kind %q, not %q — the #1372 "+
					"repair writes to the user's config and must sit behind modify consent",
					a.Identity.Name, p.Key, p.Kind, permission.KindModify)
			}
			// Apply is what the repair re-runs. A hooks permission without one
			// could be verified and found damaged forever with no way to fix it.
			if p.Apply == nil {
				t.Errorf("%s/%s declares a hook install and a Verify but no Apply, so a "+
					"damaged install can be detected and never repaired (#1372)",
					a.Identity.Name, p.Key)
			}
		}
	}
}

// TestHookVerifiersDoNotCreateTheConfigFile runs each adapter's PRODUCTION
// Verify closure against an empty HOME and asserts it left no file behind.
//
// It exercises the real wiring — the adapter's own path resolver and hookConfig
// — which a package-local test would mock away, and it is the only mechanical
// check that a declared Verify is genuinely a read. "Read-only" is the whole
// reason the repair is a separate step behind the consent gate (#1372/#570); a
// verifier that quietly created or rewrote the file would move a write outside
// that gate while every consent test still passed.
//
// HOME (and CODEX_HOME) point at a temp dir, so this asserts on a known-empty
// tree rather than on the developer's real config, which may legitimately hold
// anything at all.
func TestHookVerifiersDoNotCreateTheConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on windows
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	checked := 0
	for _, a := range All() {
		for _, p := range a.Permissions {
			if p.Writes == nil || p.Writes.Verify == nil || p.Writes.Path == nil {
				continue
			}
			path, err := p.Writes.Path()
			if err != nil {
				t.Errorf("%s/%s: ConfigPath failed under a temp HOME: %v", a.Identity.Name, p.Key, err)
				continue
			}
			if _, err := os.Stat(path); err == nil {
				t.Fatalf("%s/%s: %s already exists under a fresh temp HOME — the fixture is "+
					"not empty, so the assertion below would prove nothing",
					a.Identity.Name, p.Key, path)
			}

			status, err := p.Writes.Verify()
			if err != nil {
				t.Errorf("%s/%s: Verify errored on an absent config: %v — an install that is "+
					"simply not there is the ordinary answer, not a fault",
					a.Identity.Name, p.Key, err)
			}
			if status.Intact() {
				t.Errorf("%s/%s: Verify reports an intact install with no config file at all "+
					"(%+v); nothing is installed, so the loop would never repair it",
					a.Identity.Name, p.Key, status)
			}
			if _, err := os.Stat(path); err == nil {
				t.Errorf("%s/%s: Verify CREATED %s. It must be a pure read — the repair is a "+
					"separate step that runs behind the #570 consent gate, and a verifier that "+
					"writes moves a write outside that gate entirely",
					a.Identity.Name, p.Key, path)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no hook adapter was exercised; this test passed without checking anything")
	}
}
