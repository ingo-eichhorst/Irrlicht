package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// The #1369 gates pass by construction against a correct catalog, so the only
// thing that demonstrates they work is a deliberate MUTATION of a valid
// fixture, seen red. Every test below does exactly that: build a repo that
// validates clean, break one thing, assert the finding names it.
//
// maturityRepo is the clean baseline. It is built from the REAL core scenario
// names and REAL trait ids rather than invented ones, because both are closed
// sets the gates resolve against — a fixture with made-up names would exercise
// the FK arms instead of the ones under test.
func maturityRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// The catalog carries every core scenario AND every scenario any trait
	// names. Both are closed sets the gates resolve against, so a fixture that
	// omitted one would fail on the FK arm instead of the arm under test —
	// which is also what makes the FK arm worth having: it is what catches a
	// scenario renamed in the catalog and not in the schema.
	type row struct{ id, name string }
	var rows []row
	seen := map[string]bool{}
	addRow := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		n := len(rows)
		rows = append(rows, row{id: strconv.Itoa(1+n/9) + "." + strconv.Itoa(1+n%9), name: name})
	}
	for _, n := range matrix.CoreScenarios() {
		addRow(n)
	}
	for _, tr := range matrix.Traits {
		addRow(tr.Scenario)
	}
	var b strings.Builder
	b.WriteString(`{"meta":{"min_versions":{"claudecode":"2.0.0","codex":"1.0.0"}},"scenarios":[`)
	for i, r := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"` + r.id + `","name":"` + r.name + `","description":"d","process":"p","acceptance_criteria":"a"}`)
	}
	b.WriteString(`]}`)
	write(t, filepath.Join(root, "replaydata", "agents", "scenarios.json"), b.String())

	// claudecode: every core scenario observed → earns stable. codex: the four
	// alpha-floor scenarios observed, the rest absent → earns alpha.
	for _, r := range rows {
		folder := r.name
		if !matrix.IsCoreScenario(r.name) {
			continue
		}
		cell(t, root, "claudecode", folder, "yes", "full", "ready")
		recording(t, root, "claudecode", folder, "r1", false)
		// Membership, not row index: an index would silently stop meaning
		// "the alpha floor" the moment CoreStateScenarios is reordered, and
		// the tests below would then assert something other than their
		// comments claim.
		if slices.Contains(matrix.CoreAlphaScenarios(), r.name) {
			cell(t, root, "codex", folder, "yes", "full", "ready")
			recording(t, root, "codex", folder, "r1", false)
		}
	}
	// A structurally dead pair with a directory, to exercise the agreement
	// check in its "cell exists" form.
	cell(t, root, "codex", "backchannel-control", "no", "n/a", "ready")

	writeCapModel(t, root, map[string]any{
		"schema_version": 1,
		"adapters": map[string]any{
			"claudecode": map[string]any{"maturity": "stable"},
			"codex": map[string]any{
				"maturity": "alpha",
				"capabilities": map[string]string{
					"backchannel_control": matrix.CapabilityAbsent,
					// No directory for the observe scenario: declaring it is what
					// makes the matrix synthesize the cell (#1369).
					"backchannel_observe": matrix.CapabilityAbsent,
				},
			},
		},
	})
	return root
}

// rm deletes a file the fixture wrote, for the mutations that test absence.
func rm(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func writeCapModel(t *testing.T, root string, model map[string]any) {
	t.Helper()
	b, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "replaydata", "agents", "adapters.json"), string(b))
}

// validateFindings runs `of validate --json` and returns the messages.
func validateFindings(t *testing.T, root string) []string {
	t.Helper()
	// Decoded into the package's own `finding` type, not a private copy of its
	// shape: a re-declared struct would keep decoding into empty strings after
	// a json-tag rename, and every assertFindingContains would then silently
	// assert nothing. Invoked through runOf like every other test here, so the
	// command dispatch is exercised too.
	_, out, _ := runOf("validate", "--json", "--repo-root", root)
	var res struct {
		OK       bool      `json:"ok"`
		Findings []finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	msgs := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		msgs = append(msgs, f.Path+": "+f.Msg)
	}
	return msgs
}

// assertFindingContains fails unless exactly one finding mentions every
// fragment. "Exactly one" matters: a mutation that trips three gates at once
// is not evidence that the gate under test works.
func assertFindingContains(t *testing.T, msgs []string, fragments ...string) {
	t.Helper()
	hits := 0
	for _, m := range msgs {
		all := true
		for _, f := range fragments {
			if !strings.Contains(m, f) {
				all = false
				break
			}
		}
		if all {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("want exactly 1 finding containing %v, got %d of %d:\n  %s",
			fragments, hits, len(msgs), strings.Join(msgs, "\n  "))
	}
}

// TestMaturityRepoIsCleanIsTheVacuityGuard proves the baseline validates. Every
// other test here mutates it, so a fixture that was already red would make all
// of them pass while testing nothing.
func TestMaturityRepoIsCleanIsTheVacuityGuard(t *testing.T) {
	if msgs := validateFindings(t, maturityRepo(t)); len(msgs) != 0 {
		t.Fatalf("baseline fixture must validate clean, got:\n  %s", strings.Join(msgs, "\n  "))
	}
}

// --- gate 1: the maturity ladder --------------------------------------------

func TestMaturityGateRejectsAClaimBeyondTheEvidence(t *testing.T) {
	root := maturityRepo(t)
	writeCapModel(t, root, map[string]any{
		"schema_version": 1,
		"adapters": map[string]any{
			"claudecode": map[string]any{"maturity": "stable"},
			// codex has only the four alpha-floor scenarios recorded.
			"codex": map[string]any{
				"maturity": "beta",
				"capabilities": map[string]string{
					"backchannel_control": matrix.CapabilityAbsent,
					// No directory for the observe scenario: declaring it is what
					// makes the matrix synthesize the cell (#1369).
					"backchannel_observe": matrix.CapabilityAbsent,
				},
			},
		},
	})
	assertFindingContains(t, validateFindings(t, root),
		"adapters.codex.maturity", `claims "beta"`, `earn "alpha"`, "session-end=absent")
}

func TestMaturityGateAllowsClaimingLessThanEarned(t *testing.T) {
	// pi and aider both earn stable and claim alpha on the real catalog. The
	// non-data criteria for a promotion (days since release, third-party use)
	// are invisible here, so under-claiming must never be a finding.
	root := maturityRepo(t)
	writeCapModel(t, root, map[string]any{
		"schema_version": 1,
		"adapters": map[string]any{
			"claudecode": map[string]any{"maturity": "planned"},
			"codex": map[string]any{
				"maturity": "planned",
				"capabilities": map[string]string{
					"backchannel_control": matrix.CapabilityAbsent,
					// No directory for the observe scenario: declaring it is what
					// makes the matrix synthesize the cell (#1369).
					"backchannel_observe": matrix.CapabilityAbsent,
				},
			},
		},
	})
	if msgs := validateFindings(t, root); len(msgs) != 0 {
		t.Fatalf("under-claiming must be allowed, got:\n  %s", strings.Join(msgs, "\n  "))
	}
}

func TestAlphaDoesNotRequireMetrics(t *testing.T) {
	// The whole meaning of the tier: an adapter with the four hook-reachable
	// state scenarios and NO metrics scenario at all still earns alpha.
	root := maturityRepo(t)
	m, err := matrix.LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.EarnedMaturity("codex"); got != matrix.MaturityAlpha {
		t.Fatalf("codex earns %q, want %q — codex has the alpha floor and no metrics cells", got, matrix.MaturityAlpha)
	}
	for _, s := range m.CoreStanding("codex") {
		if slices.Contains(matrix.CoreMetricsScenarios, s.Scenario) && s.Settled {
			t.Fatalf("metrics scenario %q must not be settled for codex", s.Scenario)
		}
	}
}

// --- gate 2: the capability derivation --------------------------------------

func TestCapModelGateCatchesADeclarationContradictingItsCell(t *testing.T) {
	// codex's backchannel-control cell is n/a (supports=no). Declaring the
	// trait `untraced` derives `unobservable` instead — the forward direction.
	root := maturityRepo(t)
	writeCapModel(t, root, map[string]any{
		"schema_version": 1,
		"adapters": map[string]any{
			"claudecode": map[string]any{"maturity": "stable"},
			"codex": map[string]any{
				"maturity":     "alpha",
				"capabilities": map[string]string{"backchannel_control": matrix.CapabilityUntraced},
			},
		},
	})
	assertFindingContains(t, validateFindings(t, root),
		"adapters.codex.capabilities.backchannel_control", `derives "unobservable"`, `the cell is "n/a"`)
}

func TestCapModelGateCatchesADeadCellTheModelDoesNotExplain(t *testing.T) {
	// The reverse direction, and the one that keeps the model from going
	// silently stale: drop the declaration and the dead cell is unexplained.
	root := maturityRepo(t)
	writeCapModel(t, root, map[string]any{
		"schema_version": 1,
		"adapters": map[string]any{
			"claudecode": map[string]any{"maturity": "stable"},
			"codex":      map[string]any{"maturity": "alpha"},
		},
	})
	assertFindingContains(t, validateFindings(t, root),
		`scenario "backchannel-control" is "n/a" for adapter codex`, `is "traced"`)
}

func TestCapModelGateExemptsADocumentedRecordBlockedDeferral(t *testing.T) {
	// Four real cells are n/a with agent_supports=yes because the RECORDING is
	// deferred, not because the adapter lacks anything. Modelling those as
	// capabilities would assert something false (codex plainly has a file
	// transcript), so a documented record_blocked reason exempts the cell.
	root := maturityRepo(t)
	dir := filepath.Join(root, "replaydata", "agents", "codex", "scenarios", "session-resume")
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "session-resume",
  "details": {
    "assessment": {"agent_supports": "no", "daemon_capability": "n/a", "driver_capability": "ready",
                   "record_blocked": "infra"}
  }
}`)
	if msgs := validateFindings(t, root); len(msgs) != 0 {
		t.Fatalf("a record_blocked cell must not require a capability declaration, got:\n  %s",
			strings.Join(msgs, "\n  "))
	}
	// …and removing only the reason makes it red, so the exemption is doing
	// the work rather than the cell being invisible to the gate.
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "session-resume",
  "details": {
    "assessment": {"agent_supports": "no", "daemon_capability": "n/a", "driver_capability": "ready"}
  }
}`)
	assertFindingContains(t, validateFindings(t, root),
		`scenario "session-resume" is "n/a" for adapter codex`)
}

func TestCapModelGateRejectsOffVocabularyDeclarations(t *testing.T) {
	root := maturityRepo(t)
	writeCapModel(t, root, map[string]any{
		"schema_version": 1,
		"adapters": map[string]any{
			"claudecode": map[string]any{"maturity": "stable"},
			"codex": map[string]any{
				"maturity": "gamma",
				"capabilities": map[string]string{
					"backchannel_control": matrix.CapabilityAbsent,
					"backchannel_observe": matrix.CapabilityAbsent,
					"telepathy":           matrix.CapabilityAbsent,
					"interrupt":           "sortof",
				},
			},
		},
	})
	msgs := validateFindings(t, root)
	assertFindingContains(t, msgs, "adapters.codex.maturity", `is "gamma"`)
	assertFindingContains(t, msgs, "adapters.codex.capabilities.telepathy", "not a known trait")
	assertFindingContains(t, msgs, "adapters.codex.capabilities.interrupt", `is "sortof"`)
}

func TestCapModelGateRequiresTheColumnSetsToMatch(t *testing.T) {
	root := maturityRepo(t)
	writeCapModel(t, root, map[string]any{
		"schema_version": 1,
		"adapters": map[string]any{
			"claudecode":  map[string]any{"maturity": "stable"},
			"ghostwriter": map[string]any{"maturity": "alpha"},
		},
	})
	msgs := validateFindings(t, root)
	assertFindingContains(t, msgs, `adapter "ghostwriter" is not an onboarded column`)
	assertFindingContains(t, msgs, `adapter "codex" is an onboarded column but has no entry`)
}

func TestCapModelGateFailsWhenTheModelIsMissing(t *testing.T) {
	root := maturityRepo(t)
	rm(t, filepath.Join(root, "replaydata", "agents", "adapters.json"))
	assertFindingContains(t, validateFindings(t, root), "capability model is missing")
}

// --- the synthesis mechanism ------------------------------------------------

func TestDeclaredDeadPairNeedsNoCellDirectory(t *testing.T) {
	// This is the onboarding saving: codex declares `backchannel: absent` once
	// and BOTH backchannel scenarios become cells, though only one has a
	// directory on disk. Without synthesis the second is a hole in the matrix.
	m, err := matrix.LoadRepo(maturityRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := m.Cell("codex", "backchannel-observe")
	if !ok {
		t.Fatal("backchannel-observe was not synthesized for codex")
	}
	if c.DisplayState != matrix.StateNotApplicable || !c.Derived {
		t.Fatalf("synthesized cell = {%q, derived=%v}, want {%q, derived=true}",
			c.DisplayState, c.Derived, matrix.StateNotApplicable)
	}
	if !c.Disposition.IsTerminal() {
		t.Fatalf("a synthesized cell must be terminal for the completeness gate, got %q", c.Disposition)
	}
	// A cell that exists on disk is NOT marked derived, so a reader can tell
	// a modelled cell from a written one.
	if w, _ := m.Cell("codex", "backchannel-control"); w.Derived {
		t.Error("an on-disk cell must not be reported as derived")
	}
}

// TestAgentAddLeavesTheTreeValid is the regression test for the review finding
// that `of agent add` registered a column in scenarios.json and wrote no
// capability-model entry — so the documented FIRST step of onboarding a new
// agent left `of validate` red, with a hand-edit of replaydata/ as the only
// remedy. That is both the factory's headline anti-pattern and the opposite of
// the onboarding cost #1369 exists to remove.
func TestAgentAddLeavesTheTreeValid(t *testing.T) {
	root := maturityRepo(t)
	code, _, errs := runOf("agent", "add", "--repo-root", root,
		"--id", "newagent", "--name", "New Agent", "--provider", "acme", "--min-version", "1.0.0")
	if code != exitOK {
		t.Fatalf("of agent add exit=%d stderr=%s", code, errs)
	}
	if msgs := validateFindings(t, root); len(msgs) != 0 {
		t.Fatalf("a freshly added column must leave the tree valid, got:\n  %s", strings.Join(msgs, "\n  "))
	}
	// It claims nothing, so nothing has to be earned.
	m, err := matrix.LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Capabilities().Maturity("newagent"); got != matrix.MaturityPlanned {
		t.Errorf("new column maturity = %q, want %q", got, matrix.MaturityPlanned)
	}
}

// TestAgentUpdateWritesTheCapabilityModel covers the other half: declaring a
// missing feature is a CLI verb, not a hand-edit, and `traced` removes a
// declaration rather than storing the default.
func TestAgentUpdateWritesTheCapabilityModel(t *testing.T) {
	root := maturityRepo(t)
	// `agent update` edits an existing column's metadata.json, so bring one
	// into being through the documented path rather than hand-writing it.
	if code, _, errs := runOf("agent", "add", "--repo-root", root,
		"--id", "newagent", "--name", "New Agent", "--provider", "acme", "--min-version", "1.0.0"); code != exitOK {
		t.Fatalf("of agent add exit=%d stderr=%s", code, errs)
	}
	if code, _, errs := runOf("agent", "update", "--repo-root", root,
		"--id", "newagent", "--capability", "interrupt=untraced"); code != exitOK {
		t.Fatalf("set exit=%d stderr=%s", code, errs)
	}
	m, err := matrix.LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Capabilities().CapabilityState("newagent", "interrupt"); got != matrix.CapabilityUntraced {
		t.Fatalf("after set: %q, want %q", got, matrix.CapabilityUntraced)
	}
	// The declared pair now exists as a synthesized cell with no directory —
	// the saving the whole model is for.
	if c, ok := m.Cell("newagent", "user-esc-interrupt"); !ok || !c.Derived || c.DisplayState != matrix.StateUnobservable {
		t.Fatalf("declared pair should synthesize an unobservable cell, got ok=%v %+v", ok, c)
	}

	if code, _, errs := runOf("agent", "update", "--repo-root", root,
		"--id", "newagent", "--capability", "interrupt=traced"); code != exitOK {
		t.Fatalf("unset exit=%d stderr=%s", code, errs)
	}
	m, err = matrix.LoadRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Capabilities().CapabilityState("newagent", "interrupt"); got != matrix.CapabilityTraced {
		t.Errorf("after unset: %q, want %q", got, matrix.CapabilityTraced)
	}
	if _, ok := m.Cell("newagent", "user-esc-interrupt"); ok {
		t.Error("removing the declaration must remove the synthesized cell")
	}
	// An unknown trait or state is refused rather than written.
	for _, bad := range []string{"telepathy=absent", "interrupt=sortof"} {
		if code, _, _ := runOf("agent", "update", "--repo-root", root, "--id", "newagent", "--capability", bad); code == exitOK {
			t.Errorf("--capability %s should have been refused", bad)
		}
	}
}
