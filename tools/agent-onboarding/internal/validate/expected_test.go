package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateExpected runs the spec-grounded expected.jsonl validator
// against every committed claudecode scenario that has an expected.jsonl
// file. The recordings should satisfy their expectations — if a daemon
// change drifts from the spec, one of these phases fails and the test
// turns red.
//
// Tests are intentionally not table-static — the list of scenarios is
// discovered at runtime from replaydata/, so adding a new scenario's
// expected.jsonl auto-extends coverage without touching this file.
func TestValidateExpected_committedScenarios(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "replaydata", "agents", "claudecode", "scenarios")
	matches, err := filepath.Glob(filepath.Join(root, "*", "expected.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Skip("no expected.jsonl files found — nothing to validate")
	}
	for _, path := range matches {
		scenarioDir := filepath.Dir(path)
		name := filepath.Base(scenarioDir)
		t.Run(name, func(t *testing.T) {
			report, err := ValidateExpected(scenarioDir)
			if err != nil {
				t.Fatalf("ValidateExpected: %v", err)
			}
			if report == nil {
				t.Fatal("report is nil despite expected.jsonl being present")
			}
			if !report.Pass {
				var failed []string
				for _, p := range report.Phases {
					if !p.Pass {
						failed = append(failed, p.Phase+": "+p.Reason)
					}
				}
				t.Errorf("validation failed (%s): %s\n  failed phases:\n    %s",
					name, report.Summary, strings.Join(failed, "\n    "))
			}
		})
	}
}

// TestValidateExpected_missingFileReturnsNil — when a scenario has no
// expected.jsonl, ValidateExpected returns (nil, nil). Distinguishes
// "not configured yet" from "validator broke".
func TestValidateExpected_missingFileReturnsNil(t *testing.T) {
	dir := t.TempDir()
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report != nil {
		t.Fatalf("expected nil report for empty dir, got %+v", report)
	}
}

// TestValidateExpected_corruptFileFails — a phase whose anchor doesn't
// exist (typo in relative_to) should produce a failing report, not a
// silent pass. This is the canary the user asked for: "manually
// corrupt one expected.jsonl and confirm the validator fails".
func TestValidateExpected_unknownAnchorFails(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "events.jsonl"),
		`{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"x"}`+"\n"+
			`{"ts":"2026-01-01T00:00:01Z","kind":"state_transition","session_id":"x","new_state":"ready"}`+"\n")
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"p1","expected_state":"ready","relative_to":"nonexistent","text":"will fail"}`+"\n")
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("ValidateExpected: %v", err)
	}
	if report.Pass {
		t.Fatal("expected validation to fail with unknown anchor, but it passed")
	}
	if len(report.Phases) != 1 || report.Phases[0].Pass {
		t.Fatalf("expected one failing phase, got %+v", report.Phases)
	}
	if !strings.Contains(report.Phases[0].Reason, "unknown anchor") {
		t.Errorf("expected 'unknown anchor' in reason, got %q", report.Phases[0].Reason)
	}
}

// TestValidateExpected_maxDelayFails — when max_delay_ms is set to 1,
// a state_transition that actually arrives a second later should be
// flagged. Proves the validator catches drift instead of silently
// matching.
func TestValidateExpected_maxDelayCatchesDrift(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "events.jsonl"),
		`{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"x"}`+"\n"+
			`{"ts":"2026-01-01T00:00:02Z","kind":"state_transition","session_id":"x","new_state":"ready"}`+"\n")
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"too_strict","expected_state":"ready","relative_to":"start","max_delay_ms":1,"text":"will fail — actual delta is ~2000 ms"}`+"\n")
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("ValidateExpected: %v", err)
	}
	if report.Pass {
		t.Fatal("expected validation to fail because max_delay_ms=1 is impossible to satisfy, but it passed")
	}
	if !strings.Contains(report.Phases[0].Reason, "exceeds max_delay_ms") {
		t.Errorf("expected reason mentioning max_delay_ms, got %q", report.Phases[0].Reason)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
