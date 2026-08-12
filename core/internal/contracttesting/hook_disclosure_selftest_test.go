// hook_disclosure_selftest_test.go is the committed mutation evidence for
// AssertHookDisclosureMatchesInstalled (#1356, extended by #1393), transcribed
// from PR #1370 and PR #1393 where it lived as prose (#1479).
//
// Each case builds a disclosure that is wrong in exactly ONE way and asserts
// the obligation that owns that wrongness is the one that reports it. The
// fixture is a purpose-built agent rather than a real adapter's, for the reason
// tools/lib's linter suites give: assertions pinned against a real declaration
// move whenever that adapter is edited, and then the evidence is about
// something else.
package contracttesting

import (
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

const (
	selfTestDisclosureAgent = "selftest-disclosure"
	selfTestCLIName         = "selftest-cli"
	selfTestFloor           = "2.1.122"
	selfTestConfigPath      = "~/.selftest/settings.json"
)

// selfTestInstalledEvents is the installer's event list. Three events, because
// the count obligation and the naming obligation are different claims and a
// one-event list makes them nearly the same one.
var selfTestInstalledEvents = []string{"SessionStart", "PostToolUse", "Notification"}

// correctDisclosure renders consent copy the way a correct adapter does: every
// sentence derived from selfTestInstalledEvents rather than hand-restated, which
// is the fix #1356 shipped. Each mutation below overrides exactly one part.
func correctDisclosure() HookDisclosure {
	return disclosureWith(
		hookjson.EntriesTouched(selfTestConfigPath, selfTestInstalledEvents),
		"Adds "+hookjson.EventList(selfTestInstalledEvents)+" entries. "+
			hookjson.RequiresVersion(selfTestCLIName, selfTestFloor),
	)
}

// disclosureWith builds the wiring around one Touches/Detail pair. The version
// floor is declared on the permission (not just mentioned in the copy) because
// the fourth obligation reads it off the declaration.
func disclosureWith(touches, detail string) HookDisclosure {
	return HookDisclosure{
		Agent: agent.Agent{
			Identity: agent.Identity{Name: selfTestDisclosureAgent},
			Permissions: []agent.Permission{{
				Key:     agent.HooksPermissionKey,
				Kind:    permission.KindModify,
				Touches: touches,
				Detail:  detail,
				Apply:   func() error { return nil },
				Writes:  &agent.ManagedUserFile{Version: selfTestGate()},
			}},
		},
		PermissionKey: agent.HooksPermissionKey,
		Installed:     selfTestInstalledEvents,
	}
}

// disclosureArms names each obligation, the fragment of its own failure message
// that proves IT fired rather than a neighbour, and how to drive it. Keeping the
// four in one table is what makes the vacuity guard below cheap: every arm is
// run against the correct fixture as well as against its own mutation.
type disclosureArm struct {
	want string
	run  func(t armT, d HookDisclosure)
}

func disclosureArms() map[string]disclosureArm {
	return map[string]disclosureArm{
		"names_every_installed_event": {
			want: "but the installer writes it",
			run: func(t armT, d HookDisclosure) {
				assertNamesEveryInstalledEvent(t, d.Installed, disclosureWords(d))
			},
		},
		"states_the_installed_count": {
			want: "consent copy declares",
			run: func(t armT, d HookDisclosure) {
				assertStatesInstalledCount(t, d.Installed, disclosureText(d))
			},
		},
		"names_no_uninstalled_event": {
			want: "which reads as a hook event but the installer",
			run: func(t armT, d HookDisclosure) {
				assertNamesNoUninstalledEvent(t, d, disclosureWords(d))
			},
		},
		"states_the_version_floor": {
			want: "never states the",
			run: func(t armT, d HookDisclosure) {
				perm, _ := disclosureUnderTest(d)
				assertStatesVersionFloor(t, perm, disclosureText(d))
			},
		},
		"permission_is_modify_kind": {
			want: "installing hooks writes to the user's config",
			run: func(t armT, d HookDisclosure) {
				perm, _ := disclosureUnderTest(d)
				assertModifyKind(t, d.PermissionKey, perm.Kind)
			},
		},
	}
}

// wantOf returns the fragment of an obligation's OWN failure message that
// proves IT fired. Taking it from the table rather than retyping it at the call
// site is the review finding from PR #1498: three of these tests originally
// passed the interpolated VALUE ("Notification", the floor string) as the
// fragment, which a NEIGHBOURING obligation's message also contains — so an arm
// that reported the wrong prose for the right condition was graded green. That
// is #1453's second instance reproduced inside its own fix.
func wantOf(t *testing.T, obligation string) string {
	t.Helper()
	arm, ok := disclosureArms()[obligation]
	if !ok {
		t.Fatalf("no obligation named %q — the table and these tests have drifted", obligation)
	}
	return arm.want
}

// mustAlsoName asserts the failure names the value the fixture was wrong about,
// on top of naming the obligation. Both halves matter: the obligation fragment
// says WHICH check fired, the value says the message is actionable.
func mustAlsoName(t *testing.T, obs observation, value, what string) {
	t.Helper()
	if !strings.Contains(obs.report(), value) {
		t.Errorf("%s: the failure never names %q, so a reader cannot act on it: %s",
			what, value, obs.report())
	}
}

func disclosureText(d HookDisclosure) string {
	perm, _ := disclosureUnderTest(d)
	return disclosureTextOf(perm)
}

func disclosureWords(d HookDisclosure) map[string]bool { return wordsIn(disclosureText(d)) }

// TestDisclosureArms_PassACorrectDisclosure is the family's vacuity guard. Every
// arm is run against copy that is right in every respect: an arm that reported
// unconditionally would satisfy all four mutations below and look like coverage.
func TestDisclosureArms_PassACorrectDisclosure(t *testing.T) {
	for name, arm := range disclosureArms() {
		t.Run(name, func(t *testing.T) {
			d := correctDisclosure()
			mustBeSilent(t, observe(t, func(at armT) { arm.run(at, d) }), "a correct disclosure")
		})
	}
}

// TestDisclosureArm_NamesEveryInstalledEvent is #1356 itself, reduced to a
// fixture: Claude Code's copy named six events for a seven-event install, so
// the Notification hook was written to the user's settings.json undisclosed.
// PR #1370's mutation was rendering Detail from installedHookEvents[:len-1];
// this is that mutation.
func TestDisclosureArm_NamesEveryInstalledEvent(t *testing.T) {
	short := selfTestInstalledEvents[:len(selfTestInstalledEvents)-1]
	d := disclosureWith(
		hookjson.EntriesTouched(selfTestConfigPath, selfTestInstalledEvents),
		"Adds "+hookjson.EventList(short)+" entries. "+
			hookjson.RequiresVersion(selfTestCLIName, selfTestFloor),
	)

	rec := observe(t, func(at armT) {
		assertNamesEveryInstalledEvent(at, d.Installed, disclosureWords(d))
	})
	const what = "copy rendered from installed[:len-1] — the last event is installed but never named (#1356)"
	mustReport(t, rec, wantOf(t, "names_every_installed_event"), what)
	mustAlsoName(t, rec, "Notification", what)
}

// TestDisclosureArm_StatesTheInstalledCount pins the other half of the same
// defect: PR #1370's M1 hand-restated the count as 6 against a seven-event
// install. Here the count is one short of what is installed.
func TestDisclosureArm_StatesTheInstalledCount(t *testing.T) {
	t.Run("a_count_that_is_wrong", func(t *testing.T) {
		d := disclosureWith(
			hookjson.EntriesTouched(selfTestConfigPath, selfTestInstalledEvents[:1]),
			"Adds "+hookjson.EventList(selfTestInstalledEvents)+" entries.",
		)
		rec := observe(t, func(at armT) {
			assertStatesInstalledCount(at, d.Installed, disclosureText(d))
		})
		const what = "a hand-restated entry count, one the installer disagrees with (#1356 M1)"
		mustReport(t, rec, wantOf(t, "states_the_installed_count"), what)
		mustAlsoName(t, rec, "declares 1 hook entries, installer writes 3", what)
	})

	// A count stated NOWHERE is a different failure from a count stated wrongly,
	// and it is the one an adapter reaches by writing its own prose instead of
	// calling EntriesTouched. Without this case the arm could be satisfied by
	// only ever comparing numbers it found.
	t.Run("no_count_stated_at_all", func(t *testing.T) {
		d := disclosureWith("Writes to "+selfTestConfigPath,
			"Adds "+hookjson.EventList(selfTestInstalledEvents)+" entries.")
		rec := observe(t, func(at armT) {
			assertStatesInstalledCount(at, d.Installed, disclosureText(d))
		})
		mustReport(t, rec, "consent copy states no entry count",
			"copy that never states how many entries are written")
	})
}

// TestDisclosureArm_NamesNoUninstalledEvent is PR #1370's M3: codex's Detail
// also named PreCompact, which codex never installs. An over-promise does not
// hide a write from the user, but it is still not the contract they agreed to.
//
// The second case is the review finding from the same PR — the arm originally
// knew only names the DOMAIN models, so copy promising an upstream event
// irrlicht has no constant for passed clean. It is covered here because it is
// the half that is easy to lose in a refactor of eventShapedToken.
func TestDisclosureArm_NamesNoUninstalledEvent(t *testing.T) {
	for name, extra := range map[string]string{
		"a_modelled_event_that_is_not_installed":    "PreCompact",
		"an_event_shaped_name_the_domain_never_saw": "UserPromptSubmit",
	} {
		t.Run(name, func(t *testing.T) {
			d := disclosureWith(
				hookjson.EntriesTouched(selfTestConfigPath, selfTestInstalledEvents),
				"Adds "+hookjson.EventList(selfTestInstalledEvents)+" entries, plus "+extra+".",
			)
			rec := observe(t, func(at armT) {
				assertNamesNoUninstalledEvent(at, d, disclosureWords(d))
			})
			what := "consent copy naming " + extra + ", which the installer never writes"
			mustReport(t, rec, wantOf(t, "names_no_uninstalled_event"), what)
			mustAlsoName(t, rec, extra, what)
		})
	}
}

// TestDisclosureArm_StatesTheVersionFloor is #1393's arm: the permission
// declares a floor, so the copy has to say so — otherwise it describes an
// install that may not happen on the user's machine. The mutation is copy that
// is correct about every event and silent about the precondition.
func TestDisclosureArm_StatesTheVersionFloor(t *testing.T) {
	d := disclosureWith(
		hookjson.EntriesTouched(selfTestConfigPath, selfTestInstalledEvents),
		"Adds "+hookjson.EventList(selfTestInstalledEvents)+" entries.",
	)

	rec := observe(t, func(at armT) {
		perm, _ := disclosureUnderTest(d)
		assertStatesVersionFloor(at, perm, disclosureText(d))
	})
	const what = "a declared version floor the consent copy never mentions (#1393)"
	mustReport(t, rec, wantOf(t, "states_the_version_floor"), what)
	mustAlsoName(t, rec, selfTestFloor, what)
}

// TestDisclosureArm_PermissionIsModifyKind covers the kind obligation, which is
// the one that decides which wizard row the user consents at: a permission that
// writes the user's config while declaring itself observe-kind is disclosed as
// a read.
func TestDisclosureArm_PermissionIsModifyKind(t *testing.T) {
	rec := observe(t, func(at armT) {
		assertModifyKind(at, agent.HooksPermissionKey, permission.KindObserve)
	})
	mustReport(t, rec, wantOf(t, "permission_is_modify_kind"),
		"a hooks permission declaring itself observe-kind")
}

// TestDisclosureUnderTest covers the preconditions. A wiring the contract cannot
// grade must be refused out loud: silently running three of four arms is
// indistinguishable from a pass, which is the failure mode this whole file is
// about.
func TestDisclosureUnderTest(t *testing.T) {
	if _, reason := disclosureUnderTest(correctDisclosure()); reason != "" {
		t.Fatalf("a well-formed disclosure was rejected: %s", reason)
	}

	for name, mutate := range map[string]func(*HookDisclosure){
		"permission_key_names_nothing": func(d *HookDisclosure) { d.PermissionKey = "no-such-key" },
		"installed_list_is_empty":      func(d *HookDisclosure) { d.Installed = nil },
	} {
		t.Run(name, func(t *testing.T) {
			d := correctDisclosure()
			mutate(&d)
			if _, reason := disclosureUnderTest(d); reason == "" {
				t.Error("a disclosure the contract cannot grade was accepted — it would run a " +
					"reduced set of arms silently")
			}
		})
	}
}
