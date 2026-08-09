package services

import (
	"strings"
	"sync"
	"testing"

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
// here: it is the entire user-visible output of a refusal today, and the thing
// #1362's consent-effect surfacing (PR #1379) will lift into the wizard.
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
	return &PermissionService{log: log}, log
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
