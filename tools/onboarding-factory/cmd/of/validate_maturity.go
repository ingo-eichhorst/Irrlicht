package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// The three #1369 gates, kept in one file because they gate one mechanism:
// the capability model plus the core-12 tier ladder it feeds.
//
//	validateCoreSet    — the core twelve is well-formed and resolves to the catalog
//	validateCapModel   — adapters.json is schema-valid and agrees with every cell
//	validateMaturity   — no adapter claims a tier its core standing has not earned
//
// They live in cmd/of rather than in internal/matrix for the same reason
// ValidateAxes does NOT: ValidateAxes checks one cell's fields against a
// vocabulary and has to be callable by every writer of a cell, whereas these
// three need the whole loaded matrix. The vocabulary they enforce is still
// defined in the schema (vocabulary.go, capability.go) — only the walk is here.

// capModelRelPath names the capability model in finding messages.
const capModelRelPath = "replaydata/agents/adapters.json"

// validateMaturityModel runs the three gates — but only against a tree the
// maturity model applies to.
//
// The scoping predicate is deliberate rather than a convenience for tests. The
// core twelve and the trait table are global closed sets, while a catalog can
// legitimately be partial: `of validate` is run against small fixture trees
// that hold three scenarios and no adapters.json, and demanding twelve named
// scenarios of those would fail them for not being the production catalog.
//
// So the model is "in use" when EITHER the file exists (someone has declared
// maturities, so the claims must hold up) OR the catalog contains the whole
// core set (a full catalog, which therefore owes one). The disjunction is what
// keeps the two failure modes both covered: deleting adapters.json from the
// real repo still fails, because the second arm is still true.
func validateMaturityModel(repoRoot string, names map[string]bool, add func(path, msg string)) {
	_, statErr := os.Stat(matrix.CapabilityFile(repoRoot))
	modelPresent := statErr == nil
	if !modelPresent && !catalogHasWholeCoreSet(names) {
		return
	}

	validateCoreSet(names, add)
	// The other two gates need the assembled matrix, not just the files. A
	// load failure is reported rather than skipped: a matrix that will not
	// load is exactly when the maturity claims are least trustworthy.
	m, err := matrix.LoadRepo(absRoot(repoRoot))
	if err != nil {
		add(capModelRelPath, fmt.Sprintf("cannot load the matrix to check maturity/capabilities: %v", err))
		return
	}
	validateCapModel(m, names, add)
	validateMaturity(m, add)
}

// catalogHasWholeCoreSet reports whether every core scenario resolves in this
// catalog — the test for "this is a full catalog", used only by the scoping
// predicate above.
func catalogHasWholeCoreSet(names map[string]bool) bool {
	for _, n := range matrix.CoreScenarios() {
		if !names[n] {
			return false
		}
	}
	return true
}

// validateCoreSet checks the core-12 set itself: the right size, no
// duplicates, disjoint state/metrics halves, and every member resolving to a
// real catalog scenario.
//
// A core scenario renamed in the catalog and not here would otherwise fail
// SILENTLY and in the dangerous direction: CoreStanding would score it
// "absent", never settled, and every adapter would be pinned at planned with
// no indication why. The FK check is what turns that into a message.
func validateCoreSet(names map[string]bool, add func(path, msg string)) {
	core := matrix.CoreScenarios()
	if len(core) != 12 {
		add("internal/matrix/vocabulary.go",
			fmt.Sprintf("the core scenario set has %d members, expected 12 — "+
				"resizing it changes what every maturity claim means, so it is a deliberate edit", len(core)))
	}
	seen := map[string]bool{}
	for _, n := range core {
		if seen[n] {
			// Also the only way a scenario can be in BOTH halves, which would
			// put a metrics scenario in the alpha/beta floor and stop `alpha`
			// meaning "state only" — CoreScenarios() concatenates them, so
			// such a scenario necessarily appears twice here.
			add("internal/matrix/vocabulary.go",
				fmt.Sprintf("core scenario %q is listed twice (a scenario in both the state and metrics halves lands here)", n))
		}
		seen[n] = true
		if !names[n] {
			add("internal/matrix/vocabulary.go",
				fmt.Sprintf("core scenario %q does not resolve to a catalog scenario name", n))
		}
	}
}

// validateCapModel checks replaydata/agents/adapters.json against the closed
// vocabularies and then against every cell it makes a claim about.
//
// Both directions are checked, and the second is the one that earns its keep:
//
//	declared absent/untraced → the cell MUST derive the matching display state
//	declared (or defaulted) traced → the cell must NOT be structurally dead
//
// Without the second direction the model could be silently incomplete — a new
// dead cell would just never be mentioned — and the derivation would drift
// back into per-cell hand-typing one omission at a time. The exemption is a
// cell carrying a record_blocked reason: that is a documented DEFERRAL of a
// recording, not a statement about the adapter's capabilities, and conflating
// the two would have the model assert (for example) that codex has no file
// transcript.
func validateCapModel(m *matrix.Matrix, names map[string]bool, add func(path, msg string)) {
	caps := m.Capabilities()
	if !caps.Loaded() {
		add(capModelRelPath, "capability model is missing — every adapter column needs a maturity claim")
		return
	}

	// Every trait's scenarios must resolve; a trait naming a renamed scenario
	// derives nothing and would quietly stop gating.
	for _, t := range matrix.Traits {
		if !names[t.Scenario] {
			add("internal/matrix/capability.go",
				fmt.Sprintf("trait %q names scenario %q, which is not in the catalog", t.ID, t.Scenario))
		}
	}

	// Column set must match the catalog's, in both directions.
	declared := map[string]bool{}
	for _, a := range caps.AdapterNames() {
		declared[a] = true
		if !m.HasAgent(a) {
			add(capModelRelPath, fmt.Sprintf("adapter %q is not an onboarded column (see scenarios.json meta.min_versions)", a))
		}
	}
	for _, a := range m.Agents() {
		if !declared[a] {
			add(capModelRelPath, fmt.Sprintf("adapter %q is an onboarded column but has no entry", a))
		}
	}

	for _, a := range caps.AdapterNames() {
		validateCapModelAdapter(m, caps, a, add)
	}
}

// validateCapModelAdapter runs the vocabulary and per-cell agreement checks
// for one adapter.
func validateCapModelAdapter(m *matrix.Matrix, caps *matrix.CapabilityModel, a string, add func(path, msg string)) {
	entry := caps.Adapters[a]
	if !matrix.IsValidMaturity(entry.Maturity) {
		add(capModelRelPath, fmt.Sprintf("adapters.%s.maturity is %q (allowed: %s)",
			a, entry.Maturity, strings.Join(matrix.Maturities, ", ")))
	}
	ids := make([]string, 0, len(entry.Capabilities))
	for id := range entry.Capabilities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		v := entry.Capabilities[id]
		if _, ok := matrix.TraitByID(id); !ok {
			add(capModelRelPath, fmt.Sprintf("adapters.%s.capabilities.%s is not a known trait "+
				"(the closed set lives in internal/matrix/capability.go)", a, id))
			continue
		}
		if !matrix.IsValidCapabilityState(v) {
			add(capModelRelPath, fmt.Sprintf("adapters.%s.capabilities.%s is %q (allowed: %s)",
				a, id, v, strings.Join(matrix.CapabilityStates, ", ")))
		}
	}

	// Agreement, cell by cell, over every scenario a trait covers.
	for _, t := range matrix.Traits {
		{
			s := t.Scenario
			c, hasCell := m.Cell(a, s)
			derived, structural := caps.StructuralState(a, s)
			switch {
			case structural && !hasCell:
				// Nothing on disk and the model says dead — the synthesized
				// case. Load builds the cell, so reaching here means the pair
				// is not even in the catalog; the FK check above covers it.
			case structural && c.DisplayState != derived:
				add(capModelRelPath, fmt.Sprintf(
					"adapters.%s.capabilities.%s = %q derives %q for scenario %q, but the cell is %q — "+
						"the declaration and the cell's axes disagree; fix whichever is wrong",
					a, t.ID, caps.CapabilityState(a, t.ID), derived, s, c.DisplayState))
			case !structural && hasCell && isStructurallyDead(c.DisplayState) && recordBlockedReason(c) == "":
				add(capModelRelPath, fmt.Sprintf(
					"scenario %q is %q for adapter %s, but adapters.%s.capabilities.%s is %q — "+
						"declare the trait absent/untraced, or document a record_blocked reason on the cell",
					s, c.DisplayState, a, a, t.ID, caps.CapabilityState(a, t.ID)))
			}
		}
	}
}

// isStructurallyDead reports whether a display state is one the capability
// model is responsible for explaining.
func isStructurallyDead(displayState string) bool {
	return displayState == matrix.StateNotApplicable || displayState == matrix.StateUnobservable
}

// recordBlockedReason returns the cell's documented deferral reason, if any.
func recordBlockedReason(c matrix.CellState) string {
	if c.Assessment == nil {
		return ""
	}
	return c.Assessment.RecordBlocked
}

// validateMaturity fails an adapter whose declared tier outruns its core
// standing. It is one-directional on purpose: declaring LESS than you have
// earned is always allowed (pi and aider both earn stable today and claim
// alpha), because the non-data criteria for beta and stable — days since
// release, third-party use reports, an empty bug queue — live in
// site/docs/adapters.html and this gate cannot see them.
func validateMaturity(m *matrix.Matrix, add func(path, msg string)) {
	for _, a := range m.Agents() {
		declared := m.Capabilities().Maturity(a)
		if !matrix.IsValidMaturity(declared) {
			continue // already reported by validateCapModel
		}
		earned := m.EarnedMaturity(a)
		if matrix.MaturityRank(declared) <= matrix.MaturityRank(earned) {
			continue
		}
		var parts []string
		for _, s := range m.UnsettledCoreFor(a, declared) {
			parts = append(parts, fmt.Sprintf("%s=%s", s.Scenario, s.State))
		}
		add(capModelRelPath, fmt.Sprintf(
			"adapters.%s.maturity claims %q but the core scenarios only earn %q — unsettled: %s "+
				"(a core scenario settles when it is %q, or when the capability model derives it dead)",
			a, declared, earned, strings.Join(parts, ", "), matrix.StateObserved))
	}
}
