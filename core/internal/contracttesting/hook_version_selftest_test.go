// hook_version_selftest_test.go is the committed mutation evidence for
// AssertHookVersionGate (#1365), and it is the one family where most of it had
// to be RE-DERIVED rather than transcribed: PR #1393 carries red-first defect
// tests against the two live adapters but no per-obligation mutation table, so
// four of the five arms had no recorded evidence that they can fail (#1479).
//
// Writing them found something. Two of this family's five obligations do not
// grade the ADAPTER at all — they grade agent.VersionGate.Permits, which is
// domain code every wiring shares — so no wrong wiring can make them fail:
//
//   - floor_refuses_an_older_cli was reachable only through Min, and for every
//     parseable Min above 0.0.0 the domain answers correctly. At Min = "0.0.0"
//     it answered nothing at all: predecessors() is empty, the loop that proves
//     the gate blocks never ran, and the arm passed having asserted only that
//     the gate permits its own floor. That is now a reported failure
//     (assertFloorRefusesOlder), and TestVersionArm_FloorRefusesAnOlderCLI is
//     the mutation for it.
//   - unknown_version_fails_open is a LOCK by its own doc comment: it pins the
//     direction chosen in cliversion.AtLeast and passes by construction. Its
//     mutation is in the domain, not here — PR #1393's review made AtLeast fail
//     CLOSED and the arm went red in both adapters — and AGENTS.md's Testing
//     section is explicit that a lock's green is not evidence. It is named in
//     TestVersionArm_UnknownVersionFailsOpen_IsALock rather than given a
//     wiring mutation that does not exist.
package contracttesting

import (
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

const selfTestVersionAgent = "selftest-version"

// selfTestSince is the provenance map a correct adapter declares: every
// installed event, at or below the floor.
func selfTestSince() map[string]string {
	return map[string]string{
		"SessionStart": "2.1.0",
		"PostToolUse":  "2.1.0",
		"Notification": selfTestFloor,
	}
}

// correctVersionGate is a wiring that satisfies all five obligations. Each
// mutation below overrides exactly one thing.
func correctVersionGate() HookVersionGate {
	return versionGateWith(&agent.VersionGate{
		Min:   selfTestFloor,
		Probe: []string{selfTestCLIName, "--version"},
	})
}

func versionGateWith(gate *agent.VersionGate) HookVersionGate {
	return HookVersionGate{
		Agent: agent.Agent{
			Identity: agent.Identity{Name: selfTestVersionAgent},
			Permissions: []agent.Permission{{
				Key:    agent.HooksPermissionKey,
				Kind:   permission.KindModify,
				Apply:  func() error { return nil },
				Writes: &agent.ManagedUserFile{Version: gate},
			}},
		},
		PermissionKey: agent.HooksPermissionKey,
		Installed:     selfTestInstalledEvents,
		Since:         selfTestSince(),
	}
}

// TestVersionArms_PassACorrectDeclaration is the family's vacuity guard: an arm
// that reported unconditionally would satisfy every mutation below.
func TestVersionArms_PassACorrectDeclaration(t *testing.T) {
	g := correctVersionGate()
	gate, reason := versionGateUnderTest(g)
	if reason != "" {
		t.Fatalf("a well-formed wiring was rejected: %s", reason)
	}

	for name, arm := range map[string]func(armT){
		"floor_is_parseable": func(at armT) { assertFloorParses(at, gate.Min) },
		"floor_covers_every_installed_event": func(at armT) {
			assertFloorCoversEveryEvent(at, gate.Min, g.Installed, g.Since)
		},
		"floor_refuses_an_older_cli": func(at armT) { assertFloorRefusesOlder(at, gate) },
		"unknown_version_fails_open": func(at armT) { assertUnknownFailsOpen(at, gate) },
		"version_source_is_declared": func(at armT) {
			assertVersionSourceDeclared(at, g.PermissionKey, gate)
		},
	} {
		t.Run(name, func(t *testing.T) {
			mustBeSilent(t, observe(t, arm), "a correct version-gate declaration")
		})
	}
}

// TestVersionArm_FloorIsParseable: a floor the comparison cannot read fails
// open on every comparison, so the declaration looks like a gate while gating
// nothing — the failure mode the whole family is about.
func TestVersionArm_FloorIsParseable(t *testing.T) {
	for name, min := range map[string]string{
		"two_field_version": "2.1",
		"not_a_version":     "latest",
	} {
		t.Run(name, func(t *testing.T) {
			rec := observe(t, func(at armT) { assertFloorParses(at, min) })
			mustReport(t, rec, "is not a major.minor.patch triple",
				"a declared floor of "+min+", which cliversion cannot parse")
		})
	}
}

// TestVersionArm_FloorCoversEveryInstalledEvent covers the four ways the floor
// and the provenance map can disagree. The third is #1365's own shape: an event
// written into the user's config at a version the CLI does not know it at.
func TestVersionArm_FloorCoversEveryInstalledEvent(t *testing.T) {
	cases := map[string]struct {
		since map[string]string
		want  string
		what  string
	}{
		"an_installed_event_with_no_provenance": {
			since: map[string]string{"SessionStart": "2.1.0", "PostToolUse": "2.1.0"},
			want:  "no Since entry for installed event",
			what:  "an installed event nobody stated a version for — the position claudecode's seven-event install was in",
		},
		"provenance_for_an_event_never_installed": {
			since: withSince("PreCompact", "2.1.0"),
			want:  "which is not installed",
			what:  "a stale Since entry naming an event the installer dropped",
		},
		"floor_below_an_installed_events_own_version": {
			since: withSince("Notification", "9.9.9"),
			want:  "is below",
			what:  "a floor lower than the version one installed event is known at (#1365)",
		},
		"unparseable_provenance": {
			since: withSince("Notification", "two point one"),
			want:  "is not a major.minor.patch triple",
			what:  "a Since entry the comparison cannot read",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := observe(t, func(at armT) {
				assertFloorCoversEveryEvent(at, selfTestFloor, selfTestInstalledEvents, c.since)
			})
			mustReport(t, rec, c.want, c.what)
		})
	}

	// An empty Installed list is a wiring error, not a satisfied obligation:
	// without this the arm would pass for an adapter that forgot to pass its
	// event slice, which is the "nothing to check reads as clean" shape.
	t.Run("no_installed_events_at_all", func(t *testing.T) {
		rec := observe(t, func(at armT) {
			assertFloorCoversEveryEvent(at, selfTestFloor, nil, selfTestSince())
		})
		mustReport(t, rec, "Installed is empty", "a wiring that passed no event list")
	})
}

// withSince returns the correct provenance map with one entry overridden or
// added, so each mutation differs from the correct fixture in exactly one key.
func withSince(event, version string) map[string]string {
	since := selfTestSince()
	since[event] = version
	return since
}

// TestVersionArm_FloorRefusesAnOlderCLI is the arm's only reachable mutation,
// and it exists because writing this file found the arm vacuous at a zero
// floor: predecessors("0.0.0") is empty, so the loop proving the gate blocks
// never ran and the arm passed having asserted nothing. Every other parseable
// Min grades agent.VersionGate.Permits — domain code no adapter can get wrong —
// which is why there is one case here and not four.
func TestVersionArm_FloorRefusesAnOlderCLI(t *testing.T) {
	rec := observe(t, func(at armT) {
		assertFloorRefusesOlder(at, &agent.VersionGate{Min: "0.0.0", Probe: []string{"x", "--version"}})
	})
	mustReport(t, rec, "has no version below it",
		"a floor of 0.0.0 — nothing is below it, so the obligation exercised nothing and passed")
}

// TestVersionArm_UnknownVersionFailsOpen_IsALock records what this arm is,
// since a green here would otherwise read as mutation evidence it is not.
//
// The arm pins a direction chosen in cliversion.AtLeast: an unknown or
// unparseable version PERMITS the install, because the daemon runs under
// launchd with a minimal PATH and routinely cannot probe the user's CLI. No
// wiring can make it fail — Permits is domain code shared by every adapter —
// so its mutation lives in the domain, and PR #1393's review ran it: making
// AtLeast fail CLOSED reddened this arm in both adapters. This test asserts
// only that the direction still holds, and says out loud that that is all it
// asserts.
func TestVersionArm_UnknownVersionFailsOpen_IsALock(t *testing.T) {
	gate := &agent.VersionGate{Min: selfTestFloor, Probe: []string{selfTestCLIName, "--version"}}
	for _, unknown := range []string{"", "not a version", "2.1"} {
		if allowed, why := gate.Permits(unknown); !allowed {
			t.Fatalf("unknown version %q was refused (%s) — the fail-open direction has been "+
				"reversed, which disables hooks for every user whose CLI the daemon cannot probe",
				unknown, why)
		}
	}
}

// TestVersionArm_VersionSourceIsDeclared: a floor with no way to learn the
// installed version can never refuse anything. claudecode's was probe-only, and
// under launchd's minimal PATH the probe always missed — protection that was
// not there.
func TestVersionArm_VersionSourceIsDeclared(t *testing.T) {
	rec := observe(t, func(at armT) {
		assertVersionSourceDeclared(at, agent.HooksPermissionKey, &agent.VersionGate{Min: selfTestFloor})
	})
	mustReport(t, rec, "reads as protection that isn't there",
		"a declared floor with neither Observed nor Probe")
}

// TestVersionGateUnderTest covers the preconditions, which are the three ways a
// hooks permission arrives with no floor to grade at all. #1365 was filed
// because claudecode was in the third state and wrote seven entries at any
// version.
func TestVersionGateUnderTest(t *testing.T) {
	if _, reason := versionGateUnderTest(correctVersionGate()); reason != "" {
		t.Fatalf("a well-formed wiring was rejected: %s", reason)
	}

	for name, mutate := range map[string]func(*HookVersionGate){
		"permission_key_names_nothing": func(g *HookVersionGate) { g.PermissionKey = "no-such-key" },
		"no_managed_user_file": func(g *HookVersionGate) {
			g.Agent.Permissions[0].Writes = nil
		},
		"no_version_gate": func(g *HookVersionGate) {
			g.Agent.Permissions[0].Writes = &agent.ManagedUserFile{}
		},
		"empty_minimum_version": func(g *HookVersionGate) {
			g.Agent.Permissions[0].Writes = &agent.ManagedUserFile{Version: &agent.VersionGate{}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := correctVersionGate()
			mutate(&g)
			if _, reason := versionGateUnderTest(g); reason == "" {
				t.Error("a wiring the contract cannot grade was accepted — it would run five " +
					"arms against a nil gate")
			}
		})
	}
}
