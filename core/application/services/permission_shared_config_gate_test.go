package services

import (
	"errors"
	"path/filepath"
	"testing"

	// Importing an adapter from the services package is confined to this test
	// file and is the point of the composition test below: it is the only place
	// kiro-cli's real Also declaration and this package's real enforcement
	// meet. Test-only, so it does not affect the hexagonal import direction
	// core/architecture_test.go enforces (that walks non-test package
	// imports) — see permission_version_gate_test.go's identical import for
	// the precedent this follows.
	"irrlicht/core/adapters/inbound/agents/kirocli"
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
// the shape gastown/state has: an effect that declares no ManagedUserFile
// because it writes no shared file. Gating those
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

// writingPermissionWithAlso is writingPermission plus one or more Also files
// (#1718) — the shape mistral-vibe's hooks permission needs: one Apply closure
// writing hooks.toml (Path) and the enable_experimental_hooks flag in a
// separate config.toml (Also).
func writingPermissionWithAlso(path string, also []string, applied *bool) agent.Permission {
	p := writingPermission(path, applied)
	for _, a := range also {
		aPath := a
		p.Writes.Also = append(p.Writes.Also, func() (string, error) { return aPath, nil })
	}
	return p
}

// TestSharedConfigGate_RefusesWhenAlsoResolvesOutsideTheIsolatedHome is the
// #1718 widening's defect test: a permission whose Path is safely inside the
// isolated home but whose Also file is the user's REAL config must still be
// refused. Checking Path alone would let exactly this Apply run — writing
// hooks.toml into the recorder's sandbox while the SAME closure flips
// enable_experimental_hooks in the user's real ~/.vibe/config.toml, unprotected
// and unrestored, which is #1449's incident one file over.
func TestSharedConfigGate_RefusesWhenAlsoResolvesOutsideTheIsolatedHome(t *testing.T) {
	applied := false
	sandbox := "/tmp/irr-sandbox"
	safePath := filepath.Join(sandbox, ".vibe", "hooks.toml")
	realAlso := filepath.Join("/Users/someone", ".vibe", "config.toml")
	svc, log := sharedConfigService(config.PermissionModeGrantAll, sandbox, false)

	grant(svc, writingPermissionWithAlso(safePath, []string{realAlso}, &applied))

	if applied {
		t.Error("Apply ran although its Also file resolves outside the isolated home — " +
			"a grant-all daemon would write enable_experimental_hooks into the user's real config.toml")
	}
	if !log.errorMentioning(realAlso) {
		t.Errorf("the refusal did not name the Also file it refused; errors were %v", log.errors)
	}
}

// TestSharedConfigGate_AllowsWhenPathAndAlsoAreBothInsideTheIsolatedHome is the
// vacuity guard for the test above: a permission declaring Also is not refused
// merely for declaring it — only when one of its files actually lands outside
// the isolated home.
func TestSharedConfigGate_AllowsWhenPathAndAlsoAreBothInsideTheIsolatedHome(t *testing.T) {
	applied := false
	sandbox := "/tmp/irr-sandbox"
	svc, log := sharedConfigService(config.PermissionModeGrantAll, sandbox, false)

	grant(svc, writingPermissionWithAlso(
		filepath.Join(sandbox, ".vibe", "hooks.toml"),
		[]string{filepath.Join(sandbox, ".vibe", "config.toml")},
		&applied))

	if !applied {
		t.Errorf("Apply was refused although Path and Also both resolve inside the isolated home %s; errors were %v",
			sandbox, log.errors)
	}
}

// TestSharedConfigGate_UnresolvableAlsoFailsClosed mirrors
// TestSharedConfigGate_UnresolvablePathFailsClosed for the second file: an Also
// resolver that errors must refuse just as an unresolvable Path does, naming
// which entry (Also[0]) failed.
func TestSharedConfigGate_UnresolvableAlsoFailsClosed(t *testing.T) {
	applied := false
	sandbox := "/tmp/irr-sandbox"
	svc, log := sharedConfigService(config.PermissionModeGrantAll, sandbox, false)
	p := writingPermission(filepath.Join(sandbox, ".vibe", "hooks.toml"), &applied)
	p.Writes.Also = []func() (string, error){
		func() (string, error) { return "", errors.New("no vibe home") },
	}

	grant(svc, p)

	if applied {
		t.Error("Apply ran although its Also[0] resolver could not resolve a path")
	}
	if !log.errorMentioning("no vibe home") || !log.errorMentioning("Also[0]") {
		t.Errorf("the refusal did not carry the resolution failure and the failing entry's label; errors were %v", log.errors)
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

// TestSharedConfigGate_KiroCLIAlsoDeclarationReachesTheGuard is the
// composition test for kiro-cli's own Writes.Also declaration (issue #1716,
// via #1718's ManagedUserFile.Also widening): it extracts kiro-cli's REAL
// Also resolver from kirocli.Agent()'s own hooks permission — not a
// re-implementation of it — and proves the generic mechanism above
// (TestSharedConfigGate_RefusesWhenAlsoResolvesOutsideTheIsolatedHome)
// actually gets to evaluate it.
//
// It has to decouple Path from Also to observe anything at all. kiro-cli's
// real Path (agents/irrlicht.json) and its real Also entry
// (settings/cli.json) both derive from the SAME $KIRO_HOME root
// (hookinstaller.go's kiroHome()), so in production they can never diverge —
// see kirocli_alsofile_test.go's (core/cmd/irrlichd) own structural note on
// exactly this. sharedConfigRefusal checks Path first and returns
// immediately on its own refusal, so a test using kiro-cli's real Path
// resolver too would refuse on Path alone regardless of what Also carries —
// proving nothing about Also specifically. So this test stands a synthetic,
// always-safe Path in for kiro-cli's real one, and points $KIRO_HOME OUTSIDE
// the isolated home so the real kiroSettingsPath resolver (reached only via
// the extracted Also closure, never re-typed) resolves to the user's real
// settings/cli.json. That isolates exactly the question this test asks —
// does the DECLARED Also resolver reach the guard — independent of the
// co-location that would otherwise mask it.
func TestSharedConfigGate_KiroCLIAlsoDeclarationReachesTheGuard(t *testing.T) {
	sandbox := "/tmp/irr-sandbox"
	realKiroHome := filepath.Join("/Users/someone", ".kiro")
	t.Setenv("KIRO_HOME", realKiroHome)

	kirocliAlso := kirocliSettingsAlsoResolver(t)
	wantAlsoPath, err := kirocliAlso()
	if err != nil {
		t.Fatalf("kiro-cli's real Also resolver: %v", err)
	}
	if !filepath.IsAbs(wantAlsoPath) || filepath.Base(filepath.Dir(wantAlsoPath)) != "settings" {
		t.Fatalf("kiro-cli's real Also resolver returned %q, want an absolute .../settings/cli.json path — the fixture below is not testing what it claims to", wantAlsoPath)
	}

	// With Also declared (this test's synthetic permission carries kiro-cli's
	// REAL resolver): refused, naming the real settings/cli.json path.
	applied := false
	svc, log := sharedConfigService(config.PermissionModeGrantAll, sandbox, false)
	p := writingPermission(filepath.Join(sandbox, "agents", "irrlicht.json"), &applied)
	p.Writes.Also = []func() (string, error){kirocliAlso}
	grant(svc, p)
	if applied {
		t.Error("Apply ran although kiro-cli's real Also resolver resolves outside the isolated home — settings/cli.json would have been left rewritten and unrestored under grant-all")
	}
	if !log.errorMentioning(wantAlsoPath) {
		t.Errorf("the refusal did not name kiro-cli's real settings/cli.json path %q; errors were %v", wantAlsoPath, log.errors)
	}

	// With Also dropped (this permission carries NO Also at all — the exact
	// state agent.go would be in if the declaration were removed): the same
	// unsafe file is invisible to the guard, and the synthetic-safe Path
	// alone lets Apply run. This is the coordinator's requested mutation
	// expressed as a permanent lock rather than a transient hand-edit: "drop
	// Also and show the #1449 refusal stops seeing settings/cli.json."
	applied2 := false
	svc2, log2 := sharedConfigService(config.PermissionModeGrantAll, sandbox, false)
	p2 := writingPermission(filepath.Join(sandbox, "agents", "irrlicht.json"), &applied2)
	grant(svc2, p2)
	if !applied2 {
		t.Errorf("Apply was refused even with Also absent; errors were %v — expected the synthetic safe Path alone to pass, proving the guard now sees NOTHING about settings/cli.json", log2.errors)
	}
	if log2.errorMentioning(wantAlsoPath) {
		t.Errorf("the refusal named %q even though Also was not declared on this permission — it cannot have evaluated a resolver it was never given", wantAlsoPath)
	}
}

// kirocliSettingsAlsoResolver extracts kiro-cli's REAL settings/cli.json Also
// resolver off its own registration.
//
// It is picked out by what it RESOLVES TO, not by its index: kiro-cli declares
// more than one Also entry (the second is the prior-default sidecar,
// core/cmd/irrlichd's TestKiroCLIAlso_PriorDefaultSidecarIsAManagedFile), and
// an index would silently retarget the caller at a different file the day the
// declaration order moved.
func kirocliSettingsAlsoResolver(t *testing.T) func() (string, error) {
	t.Helper()
	for _, p := range kirocli.Agent().Permissions {
		if p.Key != kirocli.PermissionKeyHooks || p.Writes == nil {
			continue
		}
		for _, resolve := range p.Writes.Also {
			got, err := resolve()
			if err != nil {
				t.Fatalf("resolving one of kiro-cli's Also entries: %v", err)
			}
			if filepath.Base(got) == "cli.json" && filepath.Base(filepath.Dir(got)) == "settings" {
				return resolve
			}
		}
	}
	t.Fatal("kirocli.Agent() declares no hooks permission whose Writes.Also resolves a settings/cli.json")
	return nil
}
