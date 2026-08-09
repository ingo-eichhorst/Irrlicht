package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// #1367: the not-applicable capability value is spelled "n/a". The dotted
// "n.a." spelling is retired (retired-spelling-ok), and `of validate` — already a CI gate over the
// whole catalog — is what keeps it off disk. A display-time alias would leave
// both spellings alive in the data, which is the actual problem.
//
// Lines naming the retired spelling carry `retired-spelling-ok` so
// matrix.TestNoSourceEmitsRetiredSpelling skips them.

// capabilityCell writes a cell whose capability axes are set on BOTH tiers —
// the overview `metadata` block and the `details.assessment` block. Both are
// written by the factory (write.go mirrors one into the other), so both have
// to be validated or the migration can leave half the data behind.
func capabilityCell(t *testing.T, root, agent, folder, scenarioID, metaField, metaValue, assessField, assessValue string) {
	t.Helper()
	dir := filepath.Join(root, "replaydata", "agents", agent, "scenarios", folder)
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "`+scenarioID+`",
  "metadata": {"agent_supports": "yes", "`+metaField+`": "`+metaValue+`"},
  "details": {"assessment": {"agent_supports": "yes", "`+assessField+`": "`+assessValue+`"}}
}`)
	write(t, filepath.Join(dir, "expected.jsonl"), `{"schema_version":1}`+"\n")
}

// TestValidateRejectsRetiredNotApplicableSpelling is the schema-level half of
// #1367: every field that can carry the not-applicable token, on both tiers,
// must be rejected when it uses the retired dotted spelling.
func TestValidateRejectsRetiredNotApplicableSpelling(t *testing.T) {
	const retired = "n.a." // retired-spelling-ok

	cases := []struct {
		name                   string
		metaField, assessField string
		metaValue, assessValue string
	}{
		{"metadata.daemon_capability", "daemon_capability", "daemon_capability", retired, "full"},
		{"metadata.driver_capability", "driver_capability", "driver_capability", retired, "ready"},
		{"details.assessment.daemon_capability", "daemon_capability", "daemon_capability", "full", retired},
		{"details.assessment.driver_capability", "driver_capability", "driver_capability", "ready", retired},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := validRepo(t)
			capabilityCell(t, root, "claudecode", "2-1_basic-turn", "basic-turn",
				tc.metaField, tc.metaValue, tc.assessField, tc.assessValue)

			code, _, errs := runOf("validate", "--repo-root", root)
			if code != exitFail {
				t.Fatalf("of validate accepted the retired %q spelling in %s; want exitFail, got exit=%d\nstderr:\n%s",
					retired, tc.name, code, errs)
			}
			// The finding must name the canonical replacement — a validator
			// that only says "invalid" leaves the reader to guess which of the
			// two spellings won.
			if !strings.Contains(errs, `"n/a"`) {
				t.Fatalf("finding should name the canonical spelling %q; stderr:\n%s", "n/a", errs)
			}
		})
	}
}

// TestValidateRejectsUnknownCapabilityValue checks that the retired spelling is
// rejected because the schema defines a closed vocabulary — not because "n.a." (retired-spelling-ok)
// was special-cased. Without this, a bespoke string check would pass the test
// above while leaving every other typo unvalidated.
func TestValidateRejectsUnknownCapabilityValue(t *testing.T) {
	root := validRepo(t)
	capabilityCell(t, root, "claudecode", "2-1_basic-turn", "basic-turn",
		"daemon_capability", "kinda", "daemon_capability", "full")

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("of validate accepted daemon_capability=%q; want exitFail, got exit=%d\nstderr:\n%s", "kinda", code, errs)
	}
	if !strings.Contains(errs, "kinda") {
		t.Fatalf("finding should quote the offending value; stderr:\n%s", errs)
	}
}

// TestValidateAcceptsCanonicalVocabulary is a LOCK, not a red-first defect
// test: it pins behaviour that must NOT change, and it passes on main by
// construction (today `of validate` checks no capability values at all, so it
// accepts these too). Its job is to catch a new validator that is too strict —
// e.g. one that rejects the open-ended `gap:<primitive>` driver grammar, or the
// pre-existing driver_capability="full" values in two kiro-cli cells.
func TestValidateAcceptsCanonicalVocabulary(t *testing.T) {
	canonical := []struct{ field, value string }{
		{"daemon_capability", "full"},
		{"daemon_capability", "bug"},
		{"daemon_capability", "incapable"},
		{"daemon_capability", "unknown"},
		{"daemon_capability", "n/a"},
		{"driver_capability", "ready"},
		{"driver_capability", "n/a"},
		{"driver_capability", "gap:interrupt"},
		{"driver_capability", "gap:seed_instruction"},
	}
	for _, c := range canonical {
		t.Run(c.field+"="+c.value, func(t *testing.T) {
			root := validRepo(t)
			capabilityCell(t, root, "claudecode", "2-1_basic-turn", "basic-turn",
				c.field, c.value, c.field, c.value)

			code, _, errs := runOf("validate", "--repo-root", root)
			if code != exitOK {
				t.Fatalf("of validate rejected canonical %s=%q; want exitOK, got exit=%d\nstderr:\n%s",
					c.field, c.value, code, errs)
			}
		})
	}
}
