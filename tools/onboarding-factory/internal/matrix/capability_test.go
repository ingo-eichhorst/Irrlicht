package matrix

import (
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/shard"
)

// These are LOCKS on the #1369 schema — they pass by construction against a
// correct trait table and a correct ladder, and their value is that they can
// fail when someone widens a trait or reshapes the core set. The red evidence
// for the three GATES lives in cmd/of/validate_maturity_test.go, where each
// one is driven by a deliberate mutation of a valid fixture.

func TestEachScenarioHasAtMostOneTrait(t *testing.T) {
	// Two traits gating one scenario would make StructuralState depend on the
	// order of the Traits slice — a derived cell state that changes when
	// someone sorts a list is the worst kind of silent break.
	owner := map[string]string{}
	for _, tr := range Traits {
		if prev, dup := owner[tr.Scenario]; dup {
			t.Errorf("scenario %q is gated by both %q and %q", tr.Scenario, prev, tr.ID)
		}
		owner[tr.Scenario] = tr.ID
	}
}

func TestTraitIDsAreUniqueAndNonEmpty(t *testing.T) {
	seen := map[string]bool{}
	for _, tr := range Traits {
		if tr.ID == "" || tr.Title == "" || tr.Scenario == "" {
			t.Errorf("trait %+v is incomplete: id, title and at least one scenario are all required", tr)
		}
		if seen[tr.ID] {
			t.Errorf("duplicate trait id %q", tr.ID)
		}
		seen[tr.ID] = true
	}
}

func TestStructuralStateMapping(t *testing.T) {
	// The whole derivation. absent and untraced are NOT interchangeable:
	// collapsing them is the two-valued model #529 pruned. Asserted through
	// StructuralState, which routes via StructuralAxes and the real
	// DeriveDisplayState — so this also pins that the synthesized axes are the
	// ones that actually produce these states.
	m := &CapabilityModel{Adapters: map[string]AdapterModel{
		"a": {Maturity: MaturityAlpha, Capabilities: map[string]string{
			"cloud_agent": CapabilityAbsent,
			"interrupt":   CapabilityUntraced,
			"task_list":   CapabilityTraced,
		}},
	}}
	for _, tc := range []struct {
		scenario string
		want     string
		ok       bool
	}{
		{"cloud-background-agent", StateNotApplicable, true},
		{"user-esc-interrupt", StateUnobservable, true},
		{"task-list", "", false},
		{"basic-turn", "", false}, // no trait at all
	} {
		got, ok := m.StructuralState("a", tc.scenario)
		if got != tc.want || ok != tc.ok {
			t.Errorf("StructuralState(a, %q) = (%q, %v), want (%q, %v)", tc.scenario, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCapabilityStateDefaultsToTraced(t *testing.T) {
	// A new adapter declares only what is MISSING; everything unmentioned has
	// to read as present-and-traced or the model would mark a fresh adapter
	// dead everywhere.
	m := &CapabilityModel{Adapters: map[string]AdapterModel{
		"a": {Maturity: MaturityAlpha, Capabilities: map[string]string{"interrupt": CapabilityUntraced}},
	}}
	if got := m.CapabilityState("a", "interrupt"); got != CapabilityUntraced {
		t.Errorf("declared trait = %q, want %q", got, CapabilityUntraced)
	}
	if got := m.CapabilityState("a", "cloud_agent"); got != CapabilityTraced {
		t.Errorf("undeclared trait = %q, want %q", got, CapabilityTraced)
	}
	if got := m.CapabilityState("nobody", "cloud_agent"); got != CapabilityTraced {
		t.Errorf("undeclared adapter = %q, want %q", got, CapabilityTraced)
	}
}

func TestCoreSetShape(t *testing.T) {
	core := CoreScenarios()
	if len(core) != 12 {
		t.Fatalf("core set has %d members, want 12", len(core))
	}
	if len(CoreAlphaScenarios()) != 4 {
		t.Errorf("alpha floor has %d members, want 4", len(CoreAlphaScenarios()))
	}
	// alpha ⊂ beta ⊂ stable. The gate compares RANKS rather than sets, which
	// is only sound while the floors nest.
	for _, pair := range [][2]string{{MaturityAlpha, MaturityBeta}, {MaturityBeta, MaturityStable}} {
		lo, hi := MaturityFloor(pair[0]), MaturityFloor(pair[1])
		for _, s := range lo {
			if !slices.Contains(hi, s) {
				t.Errorf("%q floor requires %q but %q floor does not — floors must nest", pair[0], s, pair[1])
			}
		}
	}
	if len(MaturityFloor(MaturityPlanned)) != 0 {
		t.Error("planned must require nothing")
	}
	// `alpha` MEANS state-only. A metrics scenario reaching the alpha or beta
	// floor would quietly repeal that.
	for _, mScenario := range CoreMetricsScenarios {
		if slices.Contains(MaturityFloor(MaturityBeta), mScenario) {
			t.Errorf("metrics scenario %q is in the beta floor — alpha/beta must not require metrics", mScenario)
		}
		if !IsCoreScenario(mScenario) {
			t.Errorf("IsCoreScenario(%q) = false", mScenario)
		}
	}
	if IsCoreScenario("cloud-background-agent") {
		t.Error("cloud-background-agent must not be core: it is dead for all 11 adapters")
	}
}

func TestMaturityRankOrder(t *testing.T) {
	if MaturityRank(MaturityPlanned) >= MaturityRank(MaturityAlpha) ||
		MaturityRank(MaturityAlpha) >= MaturityRank(MaturityBeta) ||
		MaturityRank(MaturityBeta) >= MaturityRank(MaturityStable) {
		t.Fatalf("Maturities is not in ascending order: %v", Maturities)
	}
	if MaturityRank("gamma") != -1 {
		t.Error("an unknown maturity must rank -1, not 0 — 0 is `planned` and would read as valid")
	}
	if IsValidMaturity("") {
		t.Error("empty maturity must be invalid: an adapter in the model has to state a claim")
	}
}

// TestTraitCoverageCensus prints the trait/scenario split the Traits doc
// comment cites, instead of that figure being typed by hand and re-typed on
// every catalog change. It said "32 traits ... 32 scenarios ... other 14" for
// long enough to be wrong the moment #1803 added four of each; a number that
// documents behaviour states the command that produces it (AGENTS.md).
//
// It is a CENSUS, not a threshold: the only thing it fails on is a trait whose
// scenario is not in the committed catalog — the same condition `of validate`
// fails on (validate_maturity.go's "trait %q names scenario %q, which is not in
// the catalog"), asserted here too so a Go-only change is caught by `go test`
// without needing the CLI gate.
//
//	go test ./tools/onboarding-factory/internal/matrix/ -run TestTraitCoverageCensus -v -count=1
func TestTraitCoverageCensus(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..", "..")
	shards := shard.LoadAll(repoRoot)
	// FATAL, not Skip. This test's whole job is the FK arm below — a trait
	// naming a scenario the catalog does not have — and `t.Skip` on an empty
	// load would disarm exactly that while the package still reports `ok`.
	// Anything that makes LoadAll return empty (a moved catalog, a rename, a
	// parse error) would then turn the gate off silently, which is the failure
	// AGENTS.md names: absence of a finding and inability to look must not
	// produce the same output. The neighbouring TestShardCellEquivalence does
	// skip, and that is a different trade — it asserts a property OF the
	// corpus, so no corpus means nothing to assert. This one asserts a property
	// of the Go trait table, which exists either way.
	if len(shards) == 0 {
		t.Fatalf("no scenarios loaded from %s — the trait/catalog FK check cannot run, "+
			"and a check that cannot run must fail rather than pass quietly", shard.File(repoRoot))
	}

	inCatalog := map[string]bool{}
	for _, sh := range shards {
		inCatalog[sh.Name] = true
	}

	traited := map[string]bool{}
	for _, tr := range Traits {
		if !inCatalog[tr.Scenario] {
			t.Errorf("trait %q names scenario %q, which is not in the committed catalog", tr.ID, tr.Scenario)
			continue
		}
		traited[tr.Scenario] = true
	}

	var untraited []string
	for _, sh := range shards {
		if !traited[sh.Name] {
			untraited = append(untraited, sh.Name)
		}
	}
	sort.Strings(untraited)

	t.Logf("trait coverage census: %d traits, %d scenarios, %d untraited",
		len(Traits), len(shards), len(untraited))
	t.Logf("untraited scenarios: %v", untraited)
}
