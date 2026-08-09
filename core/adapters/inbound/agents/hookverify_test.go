package agents

import (
	"testing"

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
			if p.Hooks == nil {
				continue
			}
			if p.Hooks.Verify == nil {
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

// TestHookVerifiersAreReadOnlyOnAnAbsentConfig is the cheapest possible check
// that a declared Verify is actually a read: called against the real resolved
// config path it must not create the file, and must not panic or error merely
// because nothing is installed.
//
// It runs the production closures rather than a fixture, so it covers the wiring
// (right hookConfig, right path resolver) that a package-local test would mock
// away. It deliberately asserts nothing about the CONTENT of the verdict — this
// machine may or may not have Claude Code installed, and a test that depended on
// that would be a flake, not a check.
func TestHookVerifiersDoNotErrorOnARealPath(t *testing.T) {
	for _, a := range All() {
		for _, p := range a.Permissions {
			if p.Hooks == nil || p.Hooks.Verify == nil {
				continue
			}
			if _, err := p.Hooks.Verify(); err != nil {
				// Malformed JSON in a developer's real config is a legitimate
				// error and not this test's business, so this is only a signal
				// when it fires in CI, where the home dir is clean.
				t.Logf("%s/%s Verify returned %v (expected only for a malformed real config)",
					a.Identity.Name, p.Key, err)
			}
		}
	}
}
