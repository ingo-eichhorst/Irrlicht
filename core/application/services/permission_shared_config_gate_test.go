package services

import (
	"errors"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/permission"
)

// This file covers issue #1449: a grant-all daemon must not run an Apply that
// writes the user's real, shared agent config. The end-to-end half — a real
// irrlichd child in grant-all mode against a temp HOME, asserting a sentinel
// ~/.claude/settings.json comes back byte-identical — is
// TestGrantAllLeavesAgentConfigsAlone in core/cmd/irrlichd. The split is the one
// #1365's version gate draws between its declaration and its enforcement.
//
// It reuses gateLog / grant / newPermissionService from
// permission_version_gate_test.go: the two gates share one call site
// (runClosureEffect), so testing them through different harnesses would let the
// harness, rather than the service, decide what "refused" means.

// sharedConfigService builds a service in the given mode with the given
// isolated home. newPermissionService, not a struct literal — it owns every map
// allocation (#1400).
func sharedConfigService(mode, isolatedHome string, allow bool) (*PermissionService, *gateLog) {
	svc, log := newGateService()
	svc.mode = mode
	svc.isolatedHome = isolatedHome
	svc.allowSharedConfigWrites = allow
	return svc, log
}

// writingPermission is a modify permission whose Apply records that it ran and
// which declares the file it writes — the shape every hook installer, the
// CLAUDE.md instruction block and the kitty patch all have. No Version gate, so
// nothing but #1449's guard can refuse it.
func writingPermission(path string, applied *bool) agent.Permission {
	return agent.Permission{
		Key:    "hooks",
		Kind:   permission.KindModify,
		Title:  "Install status hooks",
		Apply:  func() error { *applied = true; return nil },
		Remove: func() error { *applied = false; return nil },
		Writes: &agent.ManagedUserFile{
			Path:      func() (string, error) { return path, nil },
			Uninstall: func() (bool, error) { return false, nil },
		},
	}
}

func TestSharedConfigGate_RefusesTheUsersRealConfigAndNamesIt(t *testing.T) {
	applied := false
	realConfig := filepath.Join("/Users/someone", ".claude", "settings.json")
	svc, log := sharedConfigService(config.PermissionModeGrantAll, "/tmp/irr-dev-home", false)

	grant(svc, writingPermission(realConfig, &applied))

	if applied {
		t.Error("Apply ran against the user's real ~/.claude/settings.json under grant-all — " +
			"this is the write that left three dead ports in the reporter's config")
	}
	if !log.errorMentioning(realConfig) {
		t.Errorf("the refusal did not name the file it refused; errors were %v — "+
			"the issue asks for an error naming the file", log.errors)
	}
	if !log.errorMentioning("IRRLICHT_ALLOW_SHARED_CONFIG_WRITES") {
		t.Errorf("the refusal did not name the override that lifts it; errors were %v — "+
			"a refusal with no way forward just gets worked around blindly", log.errors)
	}
}

// TestSharedConfigGate_AllowsAFileInsideTheIsolatedHome is the vacuity guard.
// A guard that refuses everything is indistinguishable from one that refuses
// the right things, and this is the arm that tells them apart: when $HOME (or a
// per-agent override like CODEX_HOME) points inside IRRLICHT_HOME, the managed
// file really is the daemon's own and there is nothing shared to protect.
func TestSharedConfigGate_AllowsAFileInsideTheIsolatedHome(t *testing.T) {
	sandbox := "/tmp/irr-sandbox"
	for _, path := range []string{
		filepath.Join(sandbox, ".claude", "settings.json"),
		filepath.Join(sandbox, "home", ".codex", "hooks.json"),
	} {
		applied := false
		svc, log := sharedConfigService(config.PermissionModeGrantAll, sandbox, false)

		grant(svc, writingPermission(path, &applied))

		if !applied {
			t.Errorf("Apply was refused for %s, which is inside the isolated home %s; errors were %v",
				path, sandbox, log.errors)
		}
	}
}

func TestSharedConfigGate_OverrideAllowsTheWrite(t *testing.T) {
	applied := false
	svc, log := sharedConfigService(config.PermissionModeGrantAll, "/tmp/irr-dev-home", true)

	grant(svc, writingPermission("/Users/someone/.claude/settings.json", &applied))

	if !applied {
		t.Errorf("Apply was refused although IRRLICHT_ALLOW_SHARED_CONFIG_WRITES is set; errors were %v — "+
			"the recording rig sets it precisely because it snapshots and restores these files itself", log.errors)
	}
}

// TestSharedConfigGate_AskModeIsNeverGated is a LOCK, not a defect test: it
// passes on main by construction. Ask mode is the #570 consent contract — a
// human answered this exact permission in the wizard — and gating it would
// break every production install, which is the opposite failure.
func TestSharedConfigGate_AskModeIsNeverGated(t *testing.T) {
	applied := false
	svc, log := sharedConfigService(config.PermissionModeAsk, "", false)

	grant(svc, writingPermission("/Users/someone/.claude/settings.json", &applied))

	if !applied {
		t.Errorf("Apply was refused in ask mode; errors were %v — the user granted this in the wizard", log.errors)
	}
}

// TestSharedConfigGate_RevokeIsNeverGated is a LOCK. Taking our entries back
// out of a file is always safe and always wanted; a daemon that refused to
// revoke would strand exactly the user this issue is about, with our stale
// entries still in their config and no way to remove them.
func TestSharedConfigGate_RevokeIsNeverGated(t *testing.T) {
	applied := true
	svc, log := sharedConfigService(config.PermissionModeGrantAll, "/tmp/irr-dev-home", false)
	p := writingPermission("/Users/someone/.claude/settings.json", &applied)

	svc.runClosureEffect(pendingEffect{"test-agent", p, permission.StateDenied})

	if applied {
		t.Errorf("Remove was refused under grant-all; errors were %v", log.errors)
	}
}

// TestSharedConfigGate_UnresolvablePathFailsClosed pins the direction, which is
// the opposite of #1365's. An unknown CLI version fails OPEN because a daemon
// under launchd routinely cannot see the user's binaries and refusing there
// would break working installs. An unresolvable managed PATH fails CLOSED
// because we cannot name the file, and a write we cannot name is one we cannot
// claim is safe.
func TestSharedConfigGate_UnresolvablePathFailsClosed(t *testing.T) {
	applied := false
	svc, log := sharedConfigService(config.PermissionModeGrantAll, "/tmp/irr-sandbox", false)
	p := writingPermission("", &applied)
	p.Writes.Path = func() (string, error) { return "", errors.New("no home directory") }

	grant(svc, p)

	if applied {
		t.Error("Apply ran although the managed file path could not be resolved")
	}
	if !log.errorMentioning("no home directory") {
		t.Errorf("the refusal did not carry the resolution failure; errors were %v", log.errors)
	}
}

// TestSharedConfigGate_PermissionWritingNothingIsNeverGated is a LOCK covering
// the shape agent.ControlPermission and gastown/state have: an effect that
// declares no ManagedUserFile because it writes no shared file. Gating those
// would disable orchestration and watcher startup in every grant-all recording.
func TestSharedConfigGate_PermissionWritingNothingIsNeverGated(t *testing.T) {
	applied := false
	svc, log := sharedConfigService(config.PermissionModeGrantAll, "", false)
	p := writingPermission("", &applied)
	p.Writes = nil

	grant(svc, p)

	if !applied {
		t.Errorf("Apply was refused for a permission declaring no managed user file; errors were %v", log.errors)
	}
}

// TestSharedConfigGate_RelativeRootRefuses pins the one comparison detail that
// could silently invert the guard: pathInsideRoot only ever answers true for a
// pair of absolute paths, so a root that arrives relative (or empty) refuses
// rather than matching by accident.
func TestSharedConfigGate_RelativeRootRefuses(t *testing.T) {
	cases := []struct {
		root, path string
		want       bool
	}{
		{"/tmp/sandbox", "/tmp/sandbox/.claude/settings.json", true},
		{"/tmp/sandbox", "/tmp/sandbox", true},
		{"/tmp/sandbox", "/tmp/sandbox2/.claude/settings.json", false},
		{"/tmp/sandbox", "/Users/someone/.claude/settings.json", false},
		{"/tmp/sandbox", "/tmp/sandbox/../other/x", false},
		{"sandbox", "/tmp/sandbox/.claude/settings.json", false},
		{"", "/tmp/sandbox/.claude/settings.json", false},
		{"/tmp/sandbox", "relative/path.json", false},
	}
	for _, c := range cases {
		if got := pathInsideRoot(c.root, c.path); got != c.want {
			t.Errorf("pathInsideRoot(%q, %q) = %v, want %v", c.root, c.path, got, c.want)
		}
	}
}
