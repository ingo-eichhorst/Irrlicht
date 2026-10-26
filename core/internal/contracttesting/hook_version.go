// hook_version.go holds the issue #1365 contract every hook-installing agent
// adapter must satisfy: it declares the minimum upstream CLI version its
// install requires, that floor is high enough that every event it writes
// exists at it, and the floor actually refuses an older CLI. An adapter that
// also declares a ceiling (#2105) has it graded the same way: it parses, sits
// above the floor, and actually refuses a newer CLI.
//
// An adapter that writes a different artifact per upstream major (#2108,
// agent.ManagedUserFile.Variants) has every variant graded the same way — its
// floor parses and covers its own events, it is selected across its own range
// and refused one step outside it — plus what only a set of variants can get
// wrong: two ranges overlapping, a gate whose envelope is not its variants'
// span, and an unknown version selecting an artifact.
//
// It is the static half of the obligation. The runtime half — that
// PermissionService consults the declaration before running the Apply closure
// — cannot be checked from a declaration and lives in the services package's
// own tests, the same split AssertPermissionGated draws.
//
// The family exists for the reason #1365 was filed: Codex grew a private
// codexSupportsHooks with its own parser and floor constants, Claude Code grew
// nothing and wrote seven entries into the user's settings.json at any version,
// and the third adapter was going to copy one of those two. What an adapter
// declares can be checked; what it remembers to implement cannot.
package contracttesting

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/cliversion"
)

// HookVersionGate wires one adapter's hooks permission into
// AssertHookVersionGate.
type HookVersionGate struct {
	// Agent is the adapter's registration, exactly as the daemon consumes it.
	Agent agent.Agent
	// PermissionKey is the hooks permission's key within that registration.
	PermissionKey string
	// Installed is the installer's event list — pass the same variable handed
	// to hookjson.Config.Events, never a copy, for the reason spelled out on
	// HookDisclosure.Installed. Leave it nil for a permission that declares
	// Variants (#2108): each variant carries its own list.
	Installed []string
	// Since maps each installed event to the upstream CLI version at which it
	// is proven to exist. Every entry in Installed needs one: an event with no
	// stated provenance is an event nobody checked, which is exactly the
	// position claudecode's seven-event install was in. Nil for a variant
	// declaration, whose variants carry their own.
	Since map[string]string
}

// AssertHookVersionGate runs the issue #1365 contract against g.
func AssertHookVersionGate(t *testing.T, g HookVersionGate) {
	t.Helper()

	gate, reason := versionGateUnderTest(g)
	if reason != "" {
		t.Fatal(reason)
	}
	writes := declaredWrites(g)

	t.Run("floor_is_parseable", func(t *testing.T) {
		assertFloorParses(t, gate.Min)
	})
	if len(writes.Variants) == 0 {
		t.Run("floor_covers_every_installed_event", func(t *testing.T) {
			assertFloorCoversEveryEvent(t, gate.Min, g.Installed, g.Since)
		})
	}
	t.Run("floor_refuses_an_older_cli", func(t *testing.T) {
		assertFloorRefusesOlder(t, gate)
	})
	t.Run("ceiling_refuses_a_newer_cli", func(t *testing.T) {
		assertCeilingRefusesNewer(t, gate)
	})
	t.Run("unknown_version_fails_open", func(t *testing.T) {
		assertUnknownFailsOpen(t, gate)
	})
	t.Run("version_source_is_declared", func(t *testing.T) {
		assertVersionSourceDeclared(t, g.PermissionKey, gate)
	})
	if len(writes.Variants) == 0 {
		return
	}

	// A variant declaration (#2108): the gate above is the envelope, whose
	// floor no single event list belongs to, so event coverage moves onto each
	// variant — with what only a set of variants can get wrong.
	t.Run("gate_spans_exactly_its_variants", func(t *testing.T) {
		assertGateSpansItsVariants(t, gate, writes.Variants)
	})
	t.Run("variants_are_disjoint", func(t *testing.T) {
		assertVariantsDisjoint(t, writes.Variants)
	})
	t.Run("unknown_version_selects_no_variant", func(t *testing.T) {
		assertUnknownSelectsNoVariant(t, writes)
	})
	for i := range writes.Variants {
		t.Run("variant_"+rangeName(&writes.Variants[i]), func(t *testing.T) {
			assertVariant(t, writes, i)
		})
	}
}

// versionGateUnderTest returns the declared gate the contract grades, or a
// reason it cannot grade one. It reports a reason rather than failing so the
// shapes it rejects are themselves drivable from a negative self-test — the
// same split malformedGateReason draws.
func versionGateUnderTest(g HookVersionGate) (*agent.VersionGate, string) {
	perm, ok := findPermission(g.Agent, g.PermissionKey)
	if !ok {
		return nil, fmt.Sprintf("agent %q declares no permission with key %q",
			g.Agent.Identity.Name, g.PermissionKey)
	}
	if perm.Writes == nil {
		return nil, fmt.Sprintf("permission %q declares no agent.ManagedUserFile — a hooks "+
			"permission must, so that --uninstall-hooks and the version gate can both see it",
			g.PermissionKey)
	}
	if perm.Writes.Version == nil || perm.Writes.Version.Min == "" {
		return nil, fmt.Sprintf("hooks permission %q declares no minimum CLI version (#1365) — "+
			"an adapter that installs into an agent's config at any version writes entries an "+
			"older CLI may reject, and the user sees a granted permission whose channel never fires",
			g.PermissionKey)
	}
	if len(perm.Writes.Variants) > 0 && (g.Installed != nil || g.Since != nil) {
		return nil, fmt.Sprintf("hooks permission %q declares per-major variants (#2108), and each "+
			"carries its own Events and Since — pass neither Installed nor Since, or the contract "+
			"grades a second list against the declaration instead of the declaration itself",
			g.PermissionKey)
	}
	return perm.Writes.Version, ""
}

// declaredWrites returns the managed file of the permission versionGateUnderTest
// accepted. Only called after it did, so the lookup cannot miss.
func declaredWrites(g HookVersionGate) *agent.ManagedUserFile {
	perm, _ := findPermission(g.Agent, g.PermissionKey)
	return perm.Writes
}

// assertVariant grades one variant: the single-gate arms over its own range
// and its own events, then the selection itself — the variant is chosen at its
// floor and just under its ceiling, and not one step outside either.
func assertVariant(t reporter, writes *agent.ManagedUserFile, i int) {
	t.Helper()
	v := &writes.Variants[i]
	if v.Apply == nil || v.Verify == nil {
		t.Errorf("the hook artifact for %s declares no Apply or no Verify — the service runs the "+
			"selected variant's Apply and the #1372 loop its Verify, so a nil one is an install "+
			"that can be selected and never written, or written and never checked", rangeName(v))
	}
	assertFloorParses(t, v.Min)
	assertFloorCoversEveryEvent(t, v.Min, v.Events, v.Since)
	assertCeilingRefusesNewer(t, &agent.VersionGate{Min: v.Min, Max: v.Max})
	assertVariantSelectedAcrossItsRange(t, writes, i)
}

// assertVariantSelectedAcrossItsRange is the selection arm: SelectVariant —
// the function PermissionService installs with — picks this variant at its
// floor and one patch under its ceiling, and refuses it one step below its
// floor and at or above its ceiling, each refusal naming the version refused.
// It walks the same neighbours the single-gate arms walk, for the same reason:
// an off-by-one in a field-wise comparison shows up at exactly those.
func assertVariantSelectedAcrossItsRange(t reporter, writes *agent.ManagedUserFile, i int) {
	t.Helper()
	v := &writes.Variants[i]
	floor, ok := cliversion.Parse(v.Min)
	if !ok {
		return // reported by assertFloorParses
	}
	inside := []cliversion.Version{floor}
	var outside []cliversion.Version
	outside = append(outside, predecessors(floor)...)
	if ceiling, ok := cliversion.Parse(v.Max); ok {
		if top, ok := justBelow(ceiling); ok && top.Compare(floor) >= 0 {
			inside = append(inside, top)
		}
		outside = append(outside, append([]cliversion.Version{ceiling}, successors(ceiling)...)...)
	}
	for _, in := range inside {
		if got, bound, why := writes.SelectVariant(in.String()); got != v {
			t.Errorf("%s selects %s (bound %d: %q), but it is inside the hook artifact for %s — "+
				"the range this variant declares is not the range it is installed at",
				in, describeSelection(got), bound, why, rangeName(v))
		}
	}
	for _, out := range outside {
		got, _, why := writes.SelectVariant(out.String())
		if got == v {
			t.Errorf("%s selects the hook artifact for %s, outside its declared range — the "+
				"version that artifact is known not to load at gets it anyway", out, rangeName(v))
			continue
		}
		if got == nil && !strings.Contains(why, out.String()) {
			t.Errorf("refusal %q does not name the installed version %s; a user cannot act on it", why, out)
		}
	}
}

// assertGateSpansItsVariants: the envelope's floor is the lowest variant's and
// its ceiling the highest's. A wider envelope states a floor or ceiling in the
// consent copy that no artifact is installed at; a narrower one refuses a
// version a variant was declared for.
func assertGateSpansItsVariants(t reporter, gate *agent.VersionGate, variants []agent.HookVariant) {
	t.Helper()
	lowest, highest := variants[0].Min, variants[0].Max
	for _, v := range variants[1:] {
		if cliversion.Less(v.Min, lowest) {
			lowest = v.Min
		}
		if highest != "" && (v.Max == "" || cliversion.Less(highest, v.Max)) {
			highest = v.Max
		}
	}
	if gate.Min != lowest {
		t.Errorf("the gate's floor is %q but its lowest variant starts at %q — the envelope "+
			"must be exactly the span of the variants it gates", gate.Min, lowest)
	}
	if gate.Max != highest {
		t.Errorf("the gate's ceiling is %q but its highest variant ends at %q — the envelope "+
			"must be exactly the span of the variants it gates", gate.Max, highest)
	}
}

// assertVariantsDisjoint: no version selects two variants. SelectVariant
// returns the first match, so an overlap is not an error anywhere at runtime —
// it silently writes whichever artifact was declared first.
func assertVariantsDisjoint(t reporter, variants []agent.HookVariant) {
	t.Helper()
	for i := range variants {
		for j := i + 1; j < len(variants); j++ {
			a, b := &variants[i], &variants[j]
			if startsBeforeEnd(a.Min, b.Max) && startsBeforeEnd(b.Min, a.Max) {
				t.Errorf("the hook artifacts for %s and %s overlap — a version in both installs "+
					"whichever was declared first, silently", rangeName(a), rangeName(b))
			}
		}
	}
}

// assertUnknownSelectsNoVariant is the variant counterpart of
// unknown_version_fails_open, and it points the other way on purpose (#2108):
// one artifact may be installed blind, two cannot, because the wrong guess
// writes exactly the file the other major rejects. Every unreadable version
// must select nothing and be refused as unknown, with a reason.
func assertUnknownSelectsNoVariant(t reporter, writes *agent.ManagedUserFile) {
	t.Helper()
	for _, unknown := range []string{"", "not a version", "2.1"} {
		got, bound, why := writes.SelectVariant(unknown)
		if got != nil || bound != agent.VersionUnknown || why == "" {
			t.Errorf("unreadable version %q selected %s (bound %d, reason %q), want no variant "+
				"refused as unknown — guessing the major installs the file the other one rejects",
				unknown, describeSelection(got), bound, why)
		}
	}
}

// startsBeforeEnd reports whether a range starting at start begins before one
// ending at end (exclusive; "" is unbounded). An unparseable bound is reported
// by the parse arms and treated here as not overlapping.
func startsBeforeEnd(start, end string) bool {
	if end == "" {
		return true
	}
	return cliversion.Less(start, end)
}

// justBelow is a version just under v — the top of a range ending at v — with
// the fields below the one decremented saturated, so a major-boundary ceiling
// like 3.0.0 yields 2.99999.99999 rather than nothing. False only for 0.0.0.
func justBelow(v cliversion.Version) (cliversion.Version, bool) {
	const top = 99999
	switch {
	case v.Patch > 0:
		return cliversion.Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch - 1}, true
	case v.Minor > 0:
		return cliversion.Version{Major: v.Major, Minor: v.Minor - 1, Patch: top}, true
	case v.Major > 0:
		return cliversion.Version{Major: v.Major - 1, Minor: top, Patch: top}, true
	}
	return cliversion.Version{}, false
}

// rangeName names a variant by its range for subtest names and messages.
func rangeName(v *agent.HookVariant) string {
	if v.Max == "" {
		return v.Min + "+"
	}
	return v.Min + "-" + v.Max
}

// describeSelection names what SelectVariant chose, for a message.
func describeSelection(v *agent.HookVariant) string {
	if v == nil {
		return "no variant"
	}
	return "the hook artifact for " + rangeName(v)
}

// assertVersionSourceDeclared is the arm that keeps a floor from being
// decorative: claudecode's was probe-only, and the daemon runs under launchd
// with a minimal PATH, so the probe always missed, unknown failed open, and no
// version was ever checked.
func assertVersionSourceDeclared(t reporter, key string, gate *agent.VersionGate) {
	t.Helper()
	if gate.Observed == nil && len(gate.Probe) == 0 {
		t.Errorf("hooks permission %q declares a floor of %q but no way to learn the "+
			"installed version — neither Observed nor Probe — so the gate can never "+
			"refuse anything and reads as protection that isn't there",
			key, gate.Min)
	}
}

// assertFloorParses rejects a floor the comparison cannot read. An unparseable
// floor fails open on every comparison, so the declaration would look like a
// gate while gating nothing — the failure mode this whole family is about.
func assertFloorParses(t reporter, min string) {
	t.Helper()
	if _, ok := cliversion.Parse(min); !ok {
		t.Errorf("declared minimum version %q is not a major.minor.patch triple; "+
			"an unparseable floor silently permits every version", min)
	}
}

// assertFloorCoversEveryEvent is the scope-item-2 arm: the floor must dominate
// every installed event's own introduction, so that at any version where the
// adapter installs at all, every entry it writes names an event the CLI knows.
func assertFloorCoversEveryEvent(t reporter, min string, installed []string, since map[string]string) {
	t.Helper()
	if len(installed) == 0 {
		t.Fatal("HookVersionGate.Installed is empty — pass the installer's event list")
	}
	for _, event := range installed {
		assertFloorCoversEvent(t, min, event, since[event])
	}
	for event := range since {
		if !slices.Contains(installed, event) {
			t.Errorf("Since names %q, which is not installed — a stale provenance entry "+
				"makes the floor look better justified than it is", event)
		}
	}
}

// assertFloorCoversEvent checks one event's provenance against the floor.
// A missing or unparseable Since is reported rather than skipped: an event
// with no stated version is one nobody checked, which is the position
// claudecode's seven-event install was in before #1365.
func assertFloorCoversEvent(t reporter, min, event, since string) {
	t.Helper()
	if since == "" {
		t.Errorf("no Since entry for installed event %q — every event written into the "+
			"user's config needs a stated version it is known to exist at, or the floor "+
			"cannot be shown to cover it", event)
		return
	}
	if _, parsed := cliversion.Parse(since); !parsed {
		t.Errorf("Since[%q] = %q is not a major.minor.patch triple", event, since)
		return
	}
	if ok, known := cliversion.AtLeast(min, since); known && !ok {
		t.Errorf("declared floor %s is below %s, the version %q is known at — at a CLI "+
			"between the two the install writes an entry naming an event that CLI does "+
			"not have; raise the floor to at least %s", min, since, event, since)
	}
}

// assertFloorRefusesOlder proves the declaration actually blocks, rather than
// merely being present. It walks one patch level below the floor, which is the
// boundary an off-by-one in a comparison shows up at.
func assertFloorRefusesOlder(t reporter, gate *agent.VersionGate) {
	t.Helper()
	floor, ok := cliversion.Parse(gate.Min)
	if !ok {
		return // already reported by assertFloorParses
	}
	if allowed, _ := gate.Permits(gate.Min); !allowed {
		t.Errorf("gate refuses its own floor %s — the comparison is exclusive where it "+
			"should be inclusive", gate.Min)
	}
	// A floor with nothing below it exercises NOTHING: predecessors is empty,
	// the loop never runs, and the arm passes having asserted only that the
	// gate permits its own floor. Every other value of Min makes the loop run,
	// so this is the one input under which the obligation silently stops
	// discriminating — found by writing this family's negative self-tests
	// (#1479), which could otherwise not make this arm fail at all.
	below := predecessors(floor)
	if len(below) == 0 {
		t.Errorf("declared floor %s has no version below it, so this obligation asserts "+
			"nothing — a floor of %s permits every CLI and is not a gate", gate.Min, gate.Min)
		return
	}
	// Each field decremented independently rather than one synthetic
	// predecessor: a single "just below" value like 0.99.99 exercises only the
	// borrow path, so a comparison that is correct there and off by one at the
	// major boundary would still pass. These are the three boundaries a
	// field-wise comparison can get wrong.
	for _, older := range below {
		allowed, why := gate.Permits(older.String())
		if allowed {
			t.Errorf("gate permits %s, below the declared floor %s", older, gate.Min)
			continue
		}
		// The refusal string IS the user-visible text: PermissionService
		// records it as the permission's EffectError and both wizards render
		// "granted but NOT applied, because <reason>" (#1362). An empty or
		// version-free reason is a silent skip wearing an error's clothes —
		// the exact behaviour #1365 filed against Codex.
		if why == "" {
			t.Errorf("gate refuses %s without a reason; the refusal is what the user is shown", older)
			continue
		}
		if !strings.Contains(why, older.String()) || !strings.Contains(why, gate.Min) {
			t.Errorf("refusal %q names neither the installed version %s nor the required %s; "+
				"a user cannot act on it", why, older, gate.Min)
		}
	}
}

// assertCeilingRefusesNewer is #2105's arm: a declared ceiling (VersionGate.Max)
// is parseable, sits above the floor, refuses AT itself and above, and refuses
// as a ceiling, with a reason naming the installed version and the ceiling. A
// gate that declares no ceiling has nothing for it to grade, and it reports
// nothing.
//
// Two of its checks grade the DECLARATION and are reachable by a wrong wiring:
// an unparseable Max fails open on every comparison, so the ceiling never
// refuses anything; a Max at or below Min refuses every version, so the
// install can never happen. The rest grade agent.VersionGate.Check — domain
// code every wiring shares, the same split this file's floor arm draws — and
// their mutation lives in the domain: tools/lib/opencode-v2-harmless-mutations_test.sh
// makes Check ignore Max and requires opencode's wiring of this contract to go
// red.
func assertCeilingRefusesNewer(t reporter, gate *agent.VersionGate) {
	t.Helper()
	if gate.Max == "" {
		return
	}
	ceiling, ok := cliversion.Parse(gate.Max)
	if !ok {
		t.Errorf("declared ceiling %q is not a major.minor.patch triple; an unparseable "+
			"ceiling fails open on every comparison, so it never refuses anything", gate.Max)
		return
	}
	floor, floorOK := cliversion.Parse(gate.Min)
	if floorOK && ceiling.Compare(floor) <= 0 {
		t.Errorf("declared ceiling %s is not above the floor %s, so the gate refuses every "+
			"version and the install can never happen", gate.Max, gate.Min)
		return
	}
	// The ceiling itself and one step up each field: an exclusive bound that a
	// comparison gets wrong by one shows up at the ceiling, and a field-wise
	// comparison that is wrong at one position shows up one step above it.
	for _, newer := range append([]cliversion.Version{ceiling}, successors(ceiling)...) {
		bound, why := gate.Check(newer.String())
		if bound != agent.AtOrAboveCeiling {
			t.Errorf("gate does not refuse %s as at or above its ceiling %s (it answered %d: %q)",
				newer, gate.Max, bound, why)
			continue
		}
		if !strings.Contains(why, newer.String()) || !strings.Contains(why, gate.Max) {
			t.Errorf("ceiling refusal %q names neither the installed version %s nor the "+
				"ceiling %s; a user cannot act on it", why, newer, gate.Max)
		}
	}
	// That the ceiling is EXCLUSIVE — a version below it still installs — is
	// floor_refuses_an_older_cli's first assertion, gate.Permits(gate.Min), run
	// on this same gate: Min is below Max (checked above), so a ceiling that
	// over-refuses reports there. Run during #2105: Check's `known && ok`
	// mutated to `known && (ok || true)` turned opencode's wiring red in
	// floor_refuses_an_older_cli ("gate refuses its own floor 1.14.50") while
	// this arm stayed green.
}

// successors returns the version one step above v in each field.
func successors(v cliversion.Version) []cliversion.Version {
	return []cliversion.Version{
		{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1},
		{Major: v.Major, Minor: v.Minor + 1, Patch: v.Patch},
		{Major: v.Major + 1, Minor: v.Minor, Patch: v.Patch},
	}
}

// predecessors returns every version reachable by decrementing exactly one
// field of v, skipping fields already at zero (which have no predecessor at
// that position).
func predecessors(v cliversion.Version) []cliversion.Version {
	var out []cliversion.Version
	if v.Patch > 0 {
		out = append(out, cliversion.Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch - 1})
	}
	if v.Minor > 0 {
		out = append(out, cliversion.Version{Major: v.Major, Minor: v.Minor - 1, Patch: v.Patch})
	}
	if v.Major > 0 {
		out = append(out, cliversion.Version{Major: v.Major - 1, Minor: v.Minor, Patch: v.Patch})
	}
	return out
}

// assertUnknownFailsOpen pins the direction chosen in cliversion.AtLeast. It is
// a LOCK, not a defect test: it passes by construction and exists so that
// flipping the default to fail-closed — which would silently disable hooks for
// every user whose binary the daemon's PATH cannot reach — cannot happen
// quietly.
func assertUnknownFailsOpen(t reporter, gate *agent.VersionGate) {
	t.Helper()
	for _, unknown := range []string{"", "not a version", "2.1"} {
		if allowed, why := gate.Permits(unknown); !allowed {
			t.Errorf("gate refuses on unreadable version %q (%s) — an unknown version is "+
				"not an old one, and failing closed here disables hooks for anyone whose "+
				"CLI the daemon cannot probe", unknown, why)
		}
	}
}
