// hook_variant_selftest_test.go is the committed mutation evidence for the arms
// #2108 added to AssertHookVersionGate and AssertHookDisclosureMatchesInstalled
// for a permission that writes a different artifact per upstream major
// (agent.ManagedUserFile.Variants).
//
// Each case starts from a correct two-variant declaration and breaks exactly
// one thing, and asserts the arm that owns that wrongness reports it with its
// own message. Two arms grade domain code no declaration can get wrong —
// unknown_version_selects_no_variant (SelectVariant refusing an unreadable
// version) and the selection half of each variant arm (SelectVariant choosing
// by range) — and their mutations are in the domain, run by
// tools/lib/opencode-v2-hooks-mutations_test.sh against opencode's real wiring.
// They are driven here by the vacuity guard only, and named as such.
package contracttesting

import (
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

const (
	selfTestVariantCeilingA = "3.0.0"
	selfTestVariantFloorB   = "3.1.0"
	selfTestVariantCeilingB = "4.0.0"
)

// selfTestEventsB is the second artifact's event list: one more than the first
// artifact's, so the two counts differ and the count arm has two numbers to
// hold the copy to.
var selfTestEventsB = []string{"SessionStart", "PostToolUse", "Notification", "PreCompact"}

// selfTestVariants is a correct two-variant declaration: [2.1.122, 3.0.0) with
// three events, a gap, then [3.1.0, 4.0.0) with four.
func selfTestVariants() []agent.HookVariant {
	sinceB := map[string]string{}
	for _, e := range selfTestEventsB {
		sinceB[e] = selfTestVariantFloorB
	}
	noop := func() error { return nil }
	verify := func() (agent.HookEntryStatus, error) { return agent.HookEntryStatus{}, nil }
	return []agent.HookVariant{
		{Min: selfTestFloor, Max: selfTestVariantCeilingA, Events: selfTestInstalledEvents,
			Since: selfTestSince(), Apply: noop, Verify: verify},
		{Min: selfTestVariantFloorB, Max: selfTestVariantCeilingB, Events: selfTestEventsB,
			Since: sinceB, Apply: noop, Verify: verify},
	}
}

// selfTestVariantWrites is the managed file a correct variant declaration
// carries: the envelope gate plus the variants.
func selfTestVariantWrites() *agent.ManagedUserFile {
	return &agent.ManagedUserFile{
		Version: &agent.VersionGate{
			Min:   selfTestFloor,
			Max:   selfTestVariantCeilingB,
			Probe: []string{selfTestCLIName, "--version"},
		},
		Variants: selfTestVariants(),
	}
}

// selfTestVariantCopy is consent copy right in every respect for
// selfTestVariantWrites: both counts, the union of events, all four bounds.
func selfTestVariantCopy() (touches, detail string) {
	return "Writes 3 hook entries (selftest-cli 2.x) or 4 hook entries (selftest-cli 3.x) to " +
			selfTestConfigPath,
		"Adds SessionStart, PostToolUse, and Notification entries for selftest-cli " + selfTestFloor +
			" or newer and older than " + selfTestVariantCeilingA + ", and SessionStart, PostToolUse, " +
			"Notification, and PreCompact entries for selftest-cli " + selfTestVariantFloorB +
			" or newer and older than " + selfTestVariantCeilingB + "."
}

// variantDisclosure wires a variant declaration and copy into the disclosure
// contract, the way a correct adapter does: no Installed list.
func variantDisclosure(touches, detail string, writes *agent.ManagedUserFile) HookDisclosure {
	return HookDisclosure{
		Agent: agent.Agent{
			Identity: agent.Identity{Name: selfTestDisclosureAgent},
			Permissions: []agent.Permission{{
				Key:     agent.HooksPermissionKey,
				Kind:    permission.KindModify,
				Touches: touches,
				Detail:  detail,
				Apply:   func() error { return nil },
				Writes:  writes,
			}},
		},
		PermissionKey: agent.HooksPermissionKey,
	}
}

// variantVersionGate wires a variant declaration into the version contract.
func variantVersionGate(writes *agent.ManagedUserFile) HookVersionGate {
	return HookVersionGate{
		Agent: agent.Agent{
			Identity: agent.Identity{Name: selfTestVersionAgent},
			Permissions: []agent.Permission{{
				Key:    agent.HooksPermissionKey,
				Kind:   permission.KindModify,
				Apply:  func() error { return nil },
				Writes: writes,
			}},
		},
		PermissionKey: agent.HooksPermissionKey,
	}
}

// TestVariantArms_PassACorrectDeclaration is the vacuity guard for every arm
// #2108 added, and the only driver of the two that grade domain code (see the
// file comment): an arm that reported unconditionally would satisfy every
// mutation below.
func TestVariantArms_PassACorrectDeclaration(t *testing.T) {
	writes := selfTestVariantWrites()
	if _, reason := versionGateUnderTest(variantVersionGate(writes)); reason != "" {
		t.Fatalf("a well-formed variant wiring was rejected: %s", reason)
	}
	touches, detail := selfTestVariantCopy()
	d := variantDisclosure(touches, detail, writes)
	perm, reason := disclosureUnderTest(d)
	if reason != "" {
		t.Fatalf("a well-formed variant disclosure was rejected: %s", reason)
	}
	copyText := disclosureTextOf(perm)

	arms := map[string]func(armT){
		"gate_spans_exactly_its_variants": func(at armT) {
			assertGateSpansItsVariants(at, writes.Version, writes.Variants)
		},
		"variants_are_disjoint":              func(at armT) { assertVariantsDisjoint(at, writes.Variants) },
		"unknown_version_selects_no_variant": func(at armT) { assertUnknownSelectsNoVariant(at, writes) },
		"states_every_variant_count": func(at armT) {
			assertStatesEveryVariantCount(at, writes.Variants, copyText)
		},
		"states_every_variant_bound": func(at armT) { assertStatesVersionFloor(at, perm, copyText) },
	}
	for i := range writes.Variants {
		i := i
		arms["variant_"+rangeName(&writes.Variants[i])] = func(at armT) { assertVariant(at, writes, i) }
		arms["variant_selection_"+rangeName(&writes.Variants[i])] = func(at armT) {
			assertVariantSelectedAcrossItsRange(at, writes, i)
		}
	}
	for name, arm := range arms {
		t.Run(name, func(t *testing.T) {
			mustBeSilent(t, observe(t, arm), "a correct variant declaration")
		})
	}

	// And through the two entry points an adapter wires, so the variant
	// branches in them are exercised end to end, not only arm by arm.
	t.Run("AssertHookVersionGate", func(t *testing.T) { AssertHookVersionGate(t, variantVersionGate(writes)) })
	t.Run("AssertHookDisclosureMatchesInstalled", func(t *testing.T) { AssertHookDisclosureMatchesInstalled(t, d) })
}

// TestVariantArm_ACeilingThatNeverRefuses: one variant's ceiling is unparseable,
// so its range fails open upward and the artifact is installed on every newer
// major — #2105's harm, arriving through one variant.
func TestVariantArm_ACeilingThatNeverRefuses(t *testing.T) {
	writes := selfTestVariantWrites()
	writes.Variants[1].Max = "four"
	rec := observe(t, func(at armT) { assertVariant(at, writes, 1) })
	mustReport(t, rec, "never refuses anything", "a variant whose ceiling cannot be parsed")
}

// TestVariantArm_AFloorBelowItsSince: one variant's floor is below the version
// one of its events is known at, so the artifact is written to a CLI that may
// not have the event — #1365's defect, inside one variant.
func TestVariantArm_AFloorBelowItsSince(t *testing.T) {
	writes := selfTestVariantWrites()
	writes.Variants[1].Since["PreCompact"] = "3.5.0"
	rec := observe(t, func(at armT) { assertVariant(at, writes, 1) })
	mustReport(t, rec, "is below", "a variant floor below one of its own events' Since")
	mustAlsoName(t, rec, "3.5.0", "a variant floor below one of its own events' Since")
}

// TestVariantArm_AVariantWithNoApply: the service runs the SELECTED variant's
// Apply, so a variant without one is an install that is chosen and never
// written.
func TestVariantArm_AVariantWithNoApply(t *testing.T) {
	writes := selfTestVariantWrites()
	writes.Variants[0].Apply = nil
	rec := observe(t, func(at armT) { assertVariant(at, writes, 0) })
	mustReport(t, rec, "declares no Apply or no Verify", "a variant declaring no Apply")
}

// TestVariantArm_OverlappingVariants: two ranges share versions, and
// SelectVariant silently installs whichever was declared first there.
func TestVariantArm_OverlappingVariants(t *testing.T) {
	writes := selfTestVariantWrites()
	writes.Variants[1].Min = "2.9.0"
	rec := observe(t, func(at armT) { assertVariantsDisjoint(at, writes.Variants) })
	mustReport(t, rec, "overlap", "two variants whose ranges share 2.9.0 up to 3.0.0")
}

// TestVariantArm_AShadowedVariantIsNotSelected drives the selection arm with a
// declaration that makes it fail: the second variant's floor sits inside the
// first's range, so SelectVariant returns the first there and the second is
// never installed at its own floor.
func TestVariantArm_AShadowedVariantIsNotSelected(t *testing.T) {
	writes := selfTestVariantWrites()
	writes.Variants[1].Min = "2.9.0"
	rec := observe(t, func(at armT) { assertVariantSelectedAcrossItsRange(at, writes, 1) })
	mustReport(t, rec, "is not the range it is installed at", "a variant shadowed by an earlier one")
}

// TestVariantArm_AGateWiderThanItsVariants: the envelope's floor sits below the
// lowest variant, so the consent copy states a floor no artifact is installed
// at.
func TestVariantArm_AGateWiderThanItsVariants(t *testing.T) {
	writes := selfTestVariantWrites()
	writes.Version.Min = "2.0.0"
	rec := observe(t, func(at armT) { assertGateSpansItsVariants(at, writes.Version, writes.Variants) })
	mustReport(t, rec, "must be exactly the span", "an envelope floor below the lowest variant's")
}

// TestVariantArm_CopyStatingOneVariantsCount: the copy states the first
// artifact's count only, so a user on the second version reads a number that
// is not theirs.
func TestVariantArm_CopyStatingOneVariantsCount(t *testing.T) {
	writes := selfTestVariantWrites()
	_, detail := selfTestVariantCopy()
	d := variantDisclosure("Writes 3 hook entries to "+selfTestConfigPath, detail, writes)
	perm, _ := disclosureUnderTest(d)
	rec := observe(t, func(at armT) {
		assertStatesEveryVariantCount(at, writes.Variants, disclosureTextOf(perm))
	})
	mustReport(t, rec, "never states 4 hook entries", "copy stating only the first variant's count")
}

// TestVariantArm_CopyStatingACountNoVariantWrites: a third number in the copy,
// one no artifact writes.
func TestVariantArm_CopyStatingACountNoVariantWrites(t *testing.T) {
	writes := selfTestVariantWrites()
	touches, detail := selfTestVariantCopy()
	d := variantDisclosure(touches+", or 7 hook entries", detail, writes)
	perm, _ := disclosureUnderTest(d)
	rec := observe(t, func(at armT) {
		assertStatesEveryVariantCount(at, writes.Variants, disclosureTextOf(perm))
	})
	mustReport(t, rec, "but no hook artifact writes that many", "copy stating a count no variant writes")
}

// TestVariantArm_CopyMissingAVariantsFloor: the copy never states where the
// second artifact starts, so it cannot tell a user which file their version
// gets.
func TestVariantArm_CopyMissingAVariantsFloor(t *testing.T) {
	writes := selfTestVariantWrites()
	touches, detail := selfTestVariantCopy()
	detail = strings.ReplaceAll(detail, selfTestVariantFloorB, "the next minor")
	d := variantDisclosure(touches, detail, writes)
	perm, _ := disclosureUnderTest(d)
	rec := observe(t, func(at armT) { assertStatesVersionFloor(at, perm, disclosureTextOf(perm)) })
	const what = "copy that never states the second variant's floor"
	mustReport(t, rec, "of the hook artifact for", what)
	mustAlsoName(t, rec, selfTestVariantFloorB, what)
}

// TestVariantPreconditions: a variant declaration wired with a second event
// list is refused out loud by both contracts, rather than graded against the
// list it was handed.
func TestVariantPreconditions(t *testing.T) {
	g := variantVersionGate(selfTestVariantWrites())
	g.Installed = selfTestInstalledEvents
	if _, reason := versionGateUnderTest(g); !strings.Contains(reason, "pass neither Installed nor Since") {
		t.Errorf("a variant wiring passed an Installed list was not refused: %q", reason)
	}

	touches, detail := selfTestVariantCopy()
	d := variantDisclosure(touches, detail, selfTestVariantWrites())
	d.Installed = selfTestInstalledEvents
	if _, reason := disclosureUnderTest(d); !strings.Contains(reason, "so pass none") {
		t.Errorf("a variant disclosure passed an Installed list was not refused: %q", reason)
	}

	writes := selfTestVariantWrites()
	writes.Variants[0].Events = nil
	if _, reason := disclosureUnderTest(variantDisclosure(touches, detail, writes)); !strings.Contains(reason, "declares no Events") {
		t.Errorf("a variant with no events was not refused: %q", reason)
	}
}
