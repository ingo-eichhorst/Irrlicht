package agents

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/cliversion"
)

// TestEveryHookInstallDeclaresAVersionFloor is the registry-wide tripwire for
// issue #1365, and the reason scope item 4 says "declaring a version string,
// not copying a function": it fails for a NEW adapter that installs hooks
// without declaring a floor, before that adapter has any test of its own.
//
// The per-adapter contract (contracttesting.AssertHookVersionGate) is stronger
// but has to be wired; this one cannot be forgotten, because it walks the same
// registry projection --uninstall-hooks and the recorder read. #1357 added that
// projection precisely so a third hook adapter is covered by existing rather
// than by remembering, and the version floor is the second obligation to ride
// it.
//
// Phase C of #1355 has already established the floors the next two adapters
// will declare: copilot >= 1.0.26, kiro-cli >= 1.29.8. Each joins by adding a
// Version block to its ManagedUserFile — no new function, nothing here to change.
//
// Scoped to HooksPermissionKey since #1383 widened the declaration from "the
// hook config" to "any shared user file this permission writes". The floor is
// an obligation of installing into an agent's OWN config format, where an older
// CLI can reject what irrlicht writes; a permission that patches a file
// irrlicht owns end-to-end (the CLAUDE.md blocks, the kitty block) depends on
// no upstream feature and would be failed by a blanket rule for no reason.
func TestEveryHookInstallDeclaresAVersionFloor(t *testing.T) {
	for _, a := range All() {
		for _, p := range a.Permissions {
			if p.Writes == nil || p.Key != agent.HooksPermissionKey {
				continue
			}
			if p.Writes.Version == nil || p.Writes.Version.Min == "" {
				t.Errorf("%s/%s installs hooks into the user's config but declares no "+
					"minimum CLI version (#1365) — an older CLI is written entries it may "+
					"reject or ignore, and the user sees a granted permission whose channel "+
					"never fires. Add Version: &agent.VersionGate{Min: \"x.y.z\", Probe: "+
					"[]string{\"<cli>\", \"--version\"}}",
					a.Identity.Name, p.Key)
				continue
			}
			if _, ok := cliversion.Parse(p.Writes.Version.Min); !ok {
				t.Errorf("%s/%s declares minimum version %q, which is not a "+
					"major.minor.patch triple; an unparseable floor permits every version",
					a.Identity.Name, p.Key, p.Writes.Version.Min)
			}
			if p.Writes.Version.Observed == nil && len(p.Writes.Version.Probe) == 0 {
				t.Errorf("%s/%s declares a floor but no version source (neither Observed "+
					"nor Probe), so the gate can never refuse anything",
					a.Identity.Name, p.Key)
			}
		}
	}
}
