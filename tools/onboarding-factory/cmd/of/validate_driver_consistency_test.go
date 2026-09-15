package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// #1968: a cell that is recorded:true must not still carry a
// gap:<primitive> driver_capability pillar on either tier. A committed
// recording is direct evidence the driver already drove the cell, so a
// surviving driver gap on a recorded cell is provably stale, not a
// legitimate state — see matrix.ValidateDriverRecordedConsistency for the
// full argument (confirmed against the two real violations #1968 found and
// fixed in the tree: gemini-cli/3-4_subagent-orphan-cleanup and
// gemini-cli/4-1_multiple-sessions-same-cwd).

// TestValidateRejectsRecordedCellWithDriverGap is the RED-FIRST defect proof
// AGENTS.md requires for an added check: it mutates a REAL, CURRENTLY-PASSING
// fixture (validRepo's own recorded claudecode cell, driver_capability=ready)
// to driver_capability=gap:keys on both tiers — the exact shape #1968 found
// twice in the committed tree — and confirms `of validate` goes red and names
// the offending cell and both tiers.
func TestValidateRejectsRecordedCellWithDriverGap(t *testing.T) {
	root := validRepo(t)

	// Sanity: the fixture passes BEFORE the mutation — this is the "currently
	// passing" half of the mutation-test requirement, not merely asserted.
	if code, _, errs := runOf("validate", "--repo-root", root); code != exitOK {
		t.Fatalf("validRepo() must pass `of validate` before the mutation; exit=%d\nstderr:\n%s", code, errs)
	}

	// Mutate: the SAME recorded cell (1-1_session-start, which already has a
	// recording written by validRepo/recording()), driver_capability
	// ready -> gap:keys on both tiers — mirroring what write.go's
	// mirrorAssessmentPillars keeps in sync on a real write, so a checker that
	// reads only one tier would miss half of what a real cell looks like.
	dir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start")
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "session-start",
  "metadata": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "gap:keys"},
  "details": {"assessment": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "gap:keys"}}
}`)

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("of validate accepted a recorded cell with driver_capability=gap:keys; want exitFail, got exit=%d\nstderr:\n%s",
			code, errs)
	}
	for _, want := range []string{
		"claudecode/scenarios/1-1_session-start", `"gap:keys"`, "#1968", "recorded",
		"metadata.driver_capability", "details.assessment.driver_capability",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("finding should mention %q; stderr:\n%s", want, errs)
		}
	}
}

// TestValidateAcceptsRecordedCellWithReadyDriver is a LOCK, not red-first
// evidence: it passes on main today by construction (validRepo's fixture IS
// exactly this shape already). Its job is to catch a #1968 check that is too
// strict — e.g. one that fires on every recorded cell regardless of the
// driver value, rather than only on a surviving gap:.
func TestValidateAcceptsRecordedCellWithReadyDriver(t *testing.T) {
	root := validRepo(t)
	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("of validate rejected a recorded cell with driver_capability=ready; want exitOK, got exit=%d\nstderr:\n%s",
			code, errs)
	}
}

// TestValidateAcceptsUnrecordedCellWithDriverGap is a LOCK: the NORMAL,
// expected pre-recording state — a cell assessed driver_capability=gap:* with
// NO recording yet (exactly richRepo's own "3-1_three" cell, and the
// overwhelming majority of gap: cells in the real tree) — must keep passing.
// The #1968 check must fire only when the cell IS recorded; a driver gap
// alone is not a violation, it is the ordinary pending state.
func TestValidateAcceptsUnrecordedCellWithDriverGap(t *testing.T) {
	root := validRepo(t)
	cell(t, root, "claudecode", "2-1_basic-turn", "yes", "full", "gap:keys")
	// Deliberately no recording() call — the cell is assessed, not recorded.

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitOK {
		t.Fatalf("of validate rejected an unrecorded cell with driver_capability=gap:keys; want exitOK, got exit=%d\nstderr:\n%s",
			code, errs)
	}
}

// TestValidateDriverConsistencyFailsLoudlyOnUnparseableAssessment proves the
// #1968 check obeys AGENTS.md's rule for a validator that cannot parse its
// input: check MORE, never less. A recorded cell whose details.assessment
// carries a wrong-typed driver_capability (so the narrow decode this check
// uses fails) must fail loudly — absence of a finding and inability to read
// the pillar must never look the same as "no violation".
func TestValidateDriverConsistencyFailsLoudlyOnUnparseableAssessment(t *testing.T) {
	root := validRepo(t)
	dir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start")
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "session-start",
  "metadata": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready"},
  "details": {"assessment": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": 5}}
}`)

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("an unreadable details.assessment tier must fail loudly, not read as no violation: want exitFail, got exit=%d\nstderr:\n%s",
			code, errs)
	}
	if !strings.Contains(errs, "#1968 driver/recorded consistency check cannot read") {
		t.Errorf("the #1968 check's own failure-to-read finding must be reported; stderr:\n%s", errs)
	}
}

// TestValidateDriverConsistencyFailsLoudlyOnAbsentDriverCapability is
// red-first evidence for a QA finding on the first #1968 cut: a recorded
// cell whose details.assessment OMITS the driver_capability key entirely
// decoded to the Go zero value "" — indistinguishable from an explicit
// "" and from a driver pillar that was simply never read — and "" is not
// gap:-prefixed, so ValidateDriverRecordedConsistency saw nothing wrong and
// `of validate` returned exit 0 "OK". This is exactly the inversion of "a
// validator that cannot parse its input checks MORE, never less": a missing
// driver pillar on a recorded cell is the LEAST readable input there is, and
// it produced the same output as a clean pass.
func TestValidateDriverConsistencyFailsLoudlyOnAbsentDriverCapability(t *testing.T) {
	root := validRepo(t)
	dir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start")
	// No driver_capability key at all in details.assessment.
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "session-start",
  "metadata": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready"},
  "details": {"assessment": {"agent_supports": "yes", "daemon_capability": "full"}}
}`)

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("of validate accepted a recorded cell whose details.assessment omits driver_capability entirely; "+
			"want exitFail, got exit=%d (%q)\nstderr:\n%s", code, exitLabel(code), errs)
	}
	for _, want := range []string{"details.assessment.driver_capability", "missing", "#1968", "recorded"} {
		if !strings.Contains(errs, want) {
			t.Errorf("finding should mention %q; stderr:\n%s", want, errs)
		}
	}
}

// TestValidateDriverConsistencyFailsLoudlyOnNullDriverCapability is the
// sibling red-first case: an explicit JSON `null` for driver_capability
// decodes to the same Go zero value "" as an absent key, so it hit the exact
// same silent-pass bug.
func TestValidateDriverConsistencyFailsLoudlyOnNullDriverCapability(t *testing.T) {
	root := validRepo(t)
	dir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start")
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "session-start",
  "metadata": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready"},
  "details": {"assessment": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": null}}
}`)

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("of validate accepted a recorded cell whose details.assessment.driver_capability is explicit null; "+
			"want exitFail, got exit=%d (%q)\nstderr:\n%s", code, exitLabel(code), errs)
	}
	for _, want := range []string{"details.assessment.driver_capability", "missing", "#1968", "recorded"} {
		if !strings.Contains(errs, want) {
			t.Errorf("finding should mention %q; stderr:\n%s", want, errs)
		}
	}
}

// exitLabel names an exit code for a failure message, so a red-first assert
// that prints "got exit=0" also spells out that 0 means of validate reported
// "OK" — the whole point of this QA finding.
func exitLabel(code int) string {
	if code == exitOK {
		return "of validate: OK"
	}
	return "of validate: FAIL"
}

// TestValidateDriverConsistencyFlagsOnlyTheDisagreeingTier pins a case QA
// verified by hand but that nothing in this file asserted: when the two
// tiers disagree — one still says gap:, the other was correctly updated to
// ready — only the stale tier is flagged, not both indiscriminately. write.go
// mirrors details.assessment into metadata, so it is a real committed cell
// only ever this shape mid-fix, but the check must still be tier-precise
// about which field to edit.
func TestValidateDriverConsistencyFlagsOnlyTheDisagreeingTier(t *testing.T) {
	root := validRepo(t)
	dir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_session-start")
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "session-start",
  "metadata": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready"},
  "details": {"assessment": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "gap:keys"}}
}`)

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("of validate accepted a cell whose details.assessment tier alone has a stale driver gap; "+
			"want exitFail, got exit=%d\nstderr:\n%s", code, errs)
	}
	if !strings.Contains(errs, "details.assessment.driver_capability") {
		t.Errorf("the disagreeing tier must be named; stderr:\n%s", errs)
	}
	if strings.Contains(errs, `metadata.driver_capability is "gap`) {
		t.Errorf("the CLEAN metadata tier (driver_capability=ready) must not be flagged; stderr:\n%s", errs)
	}
}
