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

// capabilityCell overwrites validRepo's second cell so that `field` carries
// metaValue on the overview `metadata` tier and assessValue on the
// `details.assessment` tier. Both tiers are written by the factory (write.go
// mirrors one into the other), so both have to be validated or a migration can
// leave half the data behind — which is why the two values are separate knobs.
func capabilityCell(t *testing.T, root, field, metaValue, assessValue string) {
	t.Helper()
	dir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "2-1_basic-turn")
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "basic-turn",
  "metadata": {"agent_supports": "yes", "`+field+`": "`+metaValue+`"},
  "details": {"assessment": {"agent_supports": "yes", "`+field+`": "`+assessValue+`"}}
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
		field                  string
		metaValue, assessValue string
	}{
		{"metadata.daemon_capability", "daemon_capability", retired, "full"},
		{"metadata.driver_capability", "driver_capability", retired, "ready"},
		{"details.assessment.daemon_capability", "daemon_capability", "full", retired},
		{"details.assessment.driver_capability", "driver_capability", "ready", retired},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := validRepo(t)
			capabilityCell(t, root, tc.field, tc.metaValue, tc.assessValue)

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
	capabilityCell(t, root, "daemon_capability", "kinda", "full")

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
			capabilityCell(t, root, c.field, c.value, c.value)

			code, _, errs := runOf("validate", "--repo-root", root)
			if code != exitOK {
				t.Fatalf("of validate rejected canonical %s=%q; want exitOK, got exit=%d\nstderr:\n%s",
					c.field, c.value, code, errs)
			}
		})
	}
}

// TestValidateChecksAssessmentTierDespiteWrongTypedSibling is a review finding
// from PR #1402. Go's json decoder populates every field that DID decode and
// still returns an error when a sibling is wrong-typed. Gating the
// details.assessment tier on `err == nil` therefore let one unrelated type slip
// switch off the retired-spelling check for all three axes in that block — and
// the cell validated completely clean, defeating the CI gate this whole ticket
// rests on.
//
// The population that hand-authors these files is exactly the population that
// also makes type slips, so this is the reachable path, not a hypothetical.
func TestValidateChecksAssessmentTierDespiteWrongTypedSibling(t *testing.T) {
	const retired = "n.a." // retired-spelling-ok
	root := validRepo(t)

	// metadata tier is entirely canonical; the retired spelling is in the
	// details.assessment tier, alongside a wrong-typed sibling.
	dir := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "2-1_basic-turn")
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "basic-turn",
  "metadata": {"agent_supports": "yes", "daemon_capability": "full", "driver_capability": "ready"},
  "details": {"assessment": {"agent_supports": "yes", "daemon_capability": "`+retired+`", "driver_capability": 5}}
}`)
	write(t, filepath.Join(dir, "expected.jsonl"), `{"schema_version":1}`+"\n")

	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("a wrong-typed sibling silenced the whole details.assessment tier: want exitFail, got exit=%d\nstderr:\n%s", code, errs)
	}
	if !strings.Contains(errs, "details.assessment.daemon_capability") || !strings.Contains(errs, "retired spelling") {
		t.Errorf("the retired spelling in details.assessment must still be reported; stderr:\n%s", errs)
	}
	// The undecodable remainder is itself reported, rather than passing silently.
	if !strings.Contains(errs, "not a well-formed assessment object") {
		t.Errorf("the decode error should be its own finding; stderr:\n%s", errs)
	}
}
