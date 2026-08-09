package claudecode

import (
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/internal/contracttesting"
)

// TestHooksPermission_DeclaresVersionGate is the issue #1365 defect test for
// this adapter. Claude Code installed seven hook entries into the user's
// ~/.claude/settings.json at ANY version — no floor, no check, nothing to
// declare — which is what the issue is about. Against that code this fails at
// the "declares no minimum CLI version" fatal.
func TestHooksPermission_DeclaresVersionGate(t *testing.T) {
	contracttesting.AssertHookVersionGate(t, contracttesting.HookVersionGate{
		Agent:         Agent(),
		PermissionKey: PermissionKeyHooks,
		Installed:     installedHookEvents,
		Since:         hookEventSince,
	})
}

func hooksVersionGate(t *testing.T) *agent.VersionGate {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			if p.Hooks == nil || p.Hooks.Version == nil {
				t.Fatal("hooks permission declares no version gate")
			}
			return p.Hooks.Version
		}
	}
	t.Fatal("no hooks permission")
	return nil
}

// TestHooksGate_RefusesTooOldClaudeCode pins the boundary in real terms. 2.1.121
// is the version one patch below the floor, and the reason it is refused is not
// tidiness: below 2.1.122 a hooks entry Claude Code cannot parse invalidates the
// user's whole settings.json (upstream changelog 2.1.122, "Fixed a malformed
// hooks entry in settings.json no longer invalidating the entire file"), and
// below 2.1.101 an unrecognized event name does the same.
func TestHooksGate_RefusesTooOldClaudeCode(t *testing.T) {
	gate := hooksVersionGate(t)

	allowed, why := gate.Permits("2.1.121 (Claude Code)")
	if allowed {
		t.Fatalf("gate permits Claude Code 2.1.121, below the %s floor", minCLIVersion)
	}
	if !strings.Contains(why, minCLIVersion) {
		t.Errorf("refusal %q never names the required version", why)
	}
}

// TestHooksGate_AllowsCurrentClaudeCode is a LOCK against the floor being set
// so high that ordinary users stop getting hooks. The banner form is the one
// `claude --version` actually prints, so this also pins that the parser copes
// with the suffix.
func TestHooksGate_AllowsCurrentClaudeCode(t *testing.T) {
	gate := hooksVersionGate(t)
	for _, v := range []string{minCLIVersion, "2.1.122 (Claude Code)", "2.1.226 (Claude Code)", "3.0.0"} {
		if allowed, why := gate.Permits(v); !allowed {
			t.Errorf("gate refuses Claude Code %q: %s", v, why)
		}
	}
}

// TestHooksGate_ProbesTheBinary pins the declared probe. Claude Code has no
// cheap Observed source (unlike Codex's single session header), so the probe is
// the only way this gate ever learns a version — an empty Probe would make the
// floor decorative.
func TestHooksGate_ProbesTheBinary(t *testing.T) {
	gate := hooksVersionGate(t)
	if len(gate.Probe) == 0 {
		t.Fatal("no probe declared, and no Observed source either — the gate can never refuse")
	}
	if gate.Probe[0] != "claude" {
		t.Errorf("probe runs %q, want the claude binary", gate.Probe[0])
	}
}
