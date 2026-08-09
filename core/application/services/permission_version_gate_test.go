package services

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	// Importing an adapter from the services package is confined to this test
	// file and is the point of the composition test below: it is the only place
	// the real declaration and the real enforcement meet. Test-only, so it does
	// not affect the hexagonal import direction core/architecture_test.go
	// enforces (that walks non-test package imports).
	"irrlicht/core/adapters/inbound/agents/codex"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// This file covers the RUNTIME half of the issue #1365 obligation: that
// PermissionService actually consults a declared version floor before running
// an Apply closure. The static half — that each adapter declares a sane floor
// covering every event it installs — is contracttesting.AssertHookVersionGate,
// wired from the adapter packages. The split is the one AssertPermissionGated
// draws, and it exists because a declaration nobody reads is indistinguishable
// from no declaration at all.

// gateLog captures what the service reported. The log line is not incidental
// here: the same string is recorded as the permission's EffectError, which is
// what #1362's consent-effect surfacing renders in both wizards.
type gateLog struct {
	mu     sync.Mutex
	infos  []string
	errors []string
}

func (l *gateLog) LogInfo(_, _, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, msg)
}

func (l *gateLog) LogError(_, _, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}

func (l *gateLog) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (l *gateLog) Close() error                                            { return nil }

func (l *gateLog) errorMentioning(needles ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.errors {
		all := true
		for _, n := range needles {
			if !strings.Contains(e, n) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// gatedHooksPermission builds a hooks permission whose Apply records whether it
// ran, so a test can tell "refused" from "installed" the way the filesystem
// would. min is the declared floor; observed is what the machine reports.
func gatedHooksPermission(min, observed string, applied *bool) agent.Permission {
	return agent.Permission{
		Key:    "hooks",
		Kind:   permission.KindModify,
		Title:  "Install status hooks",
		Apply:  func() error { *applied = true; return nil },
		Remove: func() error { *applied = false; return nil },
		Hooks: &agent.HookInstall{
			ConfigPath: func() (string, error) { return "/tmp/irrlicht-1365-test.json", nil },
			Uninstall:  func() (bool, error) { return false, nil },
			Version: &agent.VersionGate{
				Min:      min,
				Observed: func() string { return observed },
			},
		},
	}
}

func newGateService() (*PermissionService, *gateLog) {
	log := &gateLog{}
	// effectErrs must exist: runClosureEffect records every outcome there
	// (#1362), so a bare struct panics on the first effect.
	return &PermissionService{log: log, effectErrs: map[string]string{}}, log
}

func grant(svc *PermissionService, p agent.Permission) {
	svc.runClosureEffect(pendingEffect{"test-agent", p, permission.StateGranted})
}

func TestHookVersionGate_RefusesBelowFloorAndSaysWhy(t *testing.T) {
	applied := false
	svc, log := newGateService()

	grant(svc, gatedHooksPermission("2.1.122", "2.1.121", &applied))

	if applied {
		t.Error("Apply ran although the installed CLI is below the declared floor — " +
			"refusing means nothing is written, not that a half-install is tidied up after")
	}
	if !log.errorMentioning("2.1.121", "2.1.122") {
		t.Errorf("no logged failure naming both the installed and the required version; "+
			"errors were %v — the refusal is the only thing the user ever sees", log.errors)
	}
}

func TestHookVersionGate_InstallsAtOrAboveFloor(t *testing.T) {
	for _, v := range []string{"2.1.122", "2.1.226", "3.0.0"} {
		applied := false
		svc, _ := newGateService()

		grant(svc, gatedHooksPermission("2.1.122", v, &applied))

		if !applied {
			t.Errorf("Apply did not run at CLI version %s, at or above the floor", v)
		}
	}
}

// TestHookVersionGate_UnknownVersionInstalls is a LOCK on the fail-open
// direction, not a defect test. A daemon started by launchd routinely cannot
// see the user's CLI at all — minimal PATH, binary in ~/.local/bin — and
// refusing there would produce the exact "granted, but the channel never fires"
// outcome #1365 is about, reached from the other side.
func TestHookVersionGate_UnknownVersionInstalls(t *testing.T) {
	for _, unreadable := range []string{"", "garbage", "2.1"} {
		applied := false
		svc, _ := newGateService()
		p := gatedHooksPermission("2.1.122", unreadable, &applied)
		p.Hooks.Version.Probe = nil // nothing left to learn the version from

		grant(svc, p)

		if !applied {
			t.Errorf("Apply was skipped on unreadable version %q — unknown is not old", unreadable)
		}
	}
}

// TestHookVersionGate_RevokeIsNeverGated pins that taking our entries back out
// works at any version. A user on an old CLI that somehow has entries installed
// must still be able to remove them; gating Remove would strand them.
func TestHookVersionGate_RevokeIsNeverGated(t *testing.T) {
	applied := true
	svc, _ := newGateService()
	p := gatedHooksPermission("2.1.122", "1.0.0", &applied)

	svc.runClosureEffect(pendingEffect{"test-agent", p, permission.StateDenied})

	if applied {
		t.Error("Remove was gated on the CLI version; uninstall must always be possible")
	}
}

// TestHookVersionGate_NoFloorDeclaredInstalls pins that the gate is opt-in, so
// every non-hook permission — and any hook adapter that has not declared a
// floor — behaves exactly as before.
func TestHookVersionGate_NoFloorDeclaredInstalls(t *testing.T) {
	applied := false
	svc, _ := newGateService()

	grant(svc, gatedHooksPermission("", "1.0.0", &applied))

	if !applied {
		t.Error("Apply was skipped for a permission declaring no floor")
	}

	applied = false
	svc2, _ := newGateService()
	svc2.runClosureEffect(pendingEffect{"test-agent", agent.Permission{
		Key:   "statusline",
		Kind:  permission.KindModify,
		Apply: func() error { applied = true; return nil },
	}, permission.StateGranted})

	if !applied {
		t.Error("Apply was skipped for a permission with no HookInstall at all")
	}
}

// TestHookVersionGate_ProbeFailureInstallsAndSaysSo pins both halves of the
// missing/hung/unparseable-probe case: the install proceeds (fail open), and
// the fact that the gate could not check is recorded rather than passing
// silently as "checked and approved".
func TestHookVersionGate_ProbeFailureInstallsAndSaysSo(t *testing.T) {
	applied := false
	svc, log := newGateService()
	p := gatedHooksPermission("2.1.122", "", &applied)
	p.Hooks.Version.Probe = []string{"irrlicht-no-such-binary-1365"}

	grant(svc, p)

	if !applied {
		t.Error("Apply was skipped because the version probe could not run; a binary the " +
			"daemon cannot see is not an old binary")
	}
	if len(log.infos) == 0 {
		t.Error("a probe that could not run left no trace; an unchecked gate must not be " +
			"indistinguishable from a passed one")
	}
}

// TestHookVersionGate_RealCodexDeclarationRefusesOldCLI is the composition
// test: the REAL adapter declaration driven through the REAL service, asserting
// nothing lands on disk. Every other test here mocks one side or the other —
// the adapter tests call gate.Permits directly, and the tests above use a
// synthetic permission — so without this, a wiring regression between the two
// (the service reading the wrong field, an adapter dropping its Version block)
// would leave every test green.
//
// It replaces the end-to-end coverage that codex's TestApplyCodexHooks_VersionGate
// had before #1365 moved the gate out of the adapter.
func TestHookVersionGate_RealCodexDeclarationRefusesOldCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	meta := `{"type":"session_meta","payload":{"id":"s1","cli_version":"0.100.0"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "s1.jsonl"), []byte(meta), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	var hooks agent.Permission
	for _, p := range codex.Agent().Permissions {
		if p.Hooks != nil {
			hooks = p
		}
	}
	if hooks.Key == "" {
		t.Fatal("codex declares no hooks permission")
	}
	if len(hooks.Hooks.Version.Probe) == 0 {
		t.Fatal("codex declares no probe; the line below would be hiding its absence")
	}
	// Pin the version to the passive source. Left alone, the stale-transcript
	// path would confirm against the real `codex` on the developer's machine
	// (shouldConfirmByProbe, and correctly so — it is newer than 0.100.0), which
	// makes the outcome depend on what happens to be installed. Everything else
	// here is the real declaration: floor, Observed, ConfigPath, Apply.
	hooks.Hooks.Version.Probe = nil

	svc, log := newGateService()
	svc.runClosureEffect(pendingEffect{"codex", hooks, permission.StateGranted})

	if _, err := os.Stat(filepath.Join(home, "hooks.json")); !os.IsNotExist(err) {
		t.Errorf("hooks.json was written under %s for a Codex reporting 0.100.0 — "+
			"the real declaration and the real service did not compose into a refusal", home)
	}
	if !log.errorMentioning("0.100.0") {
		t.Errorf("no logged reason naming the installed version; errors were %v", log.errors)
	}
}

// TestHookVersionGate_RefusalIsRecordedAsAnEffectError is the #1365 x #1362
// join. The gate deliberately produces an ordinary effect error rather than a
// separate "skipped" concept, so the refusal rides the mechanism #1379 landed:
// it is stored in effectErrs, which Snapshot exposes as EffectError and both
// wizards render as "granted but NOT applied, because <reason>".
//
// Without this the two features are only adjacent — the gate could refuse into
// the log while the wizard still showed a clean "granted", which is the exact
// state #1365 was filed about.
func TestHookVersionGate_RefusalIsRecordedAsAnEffectError(t *testing.T) {
	applied := false
	svc, _ := newGateService()
	p := gatedHooksPermission("2.1.122", "2.1.121", &applied)

	grant(svc, p)

	got := svc.effectErrs[effectKey("test-agent", "hooks")]
	if got == "" {
		t.Fatal("refusal was not recorded as an EffectError — the wizard would render a " +
			"clean \"granted\" for a permission that installed nothing (#1362/#1365)")
	}
	if !strings.Contains(got, "2.1.121") || !strings.Contains(got, "2.1.122") {
		t.Errorf("recorded EffectError %q does not name both versions", got)
	}
	// The recorded failure is also what makes the refusal's advice actionable:
	// planAnswerLocked re-runs the effect when a re-answer finds one.
	svc.mu.Lock()
	retries := svc.effectFailedLocked("test-agent", "hooks")
	svc.mu.Unlock()
	if !retries {
		t.Error("a recorded refusal does not mark the permission for retry, so " +
			"\"upgrade the CLI and grant again\" would not actually re-run the install")
	}
}

// TestHookVersionGate_SuccessClearsAnEarlierRefusal pins the other half: after
// the user upgrades and re-grants, the stale "too old" reason must not linger
// on a permission that is now installed.
func TestHookVersionGate_SuccessClearsAnEarlierRefusal(t *testing.T) {
	applied := false
	svc, _ := newGateService()

	grant(svc, gatedHooksPermission("2.1.122", "2.1.121", &applied)) // too old
	grant(svc, gatedHooksPermission("2.1.122", "2.1.226", &applied)) // upgraded

	if !applied {
		t.Fatal("install did not run after the CLI was upgraded")
	}
	if got := svc.effectErrs[effectKey("test-agent", "hooks")]; got != "" {
		t.Errorf("stale refusal %q survived a successful install", got)
	}
}
