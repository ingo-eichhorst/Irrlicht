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
				t.Skip("no events.jsonl — recording not yet captured (applicable: false)")
			}
			if !report.Pass {
				var failed []string
				for _, p := range report.Phases {
					if !p.Pass {
						failed = append(failed, p.Phase+": "+p.Reason)
					}
				}
				if report.Meta.KnownFailing {
					t.Logf("EXPECTED FAILURE (%s): %s\n  failed phases:\n    %s\n  (meta.known_failing=true — daemon-side gap; see expected.jsonl notes)",
						name, report.Summary, strings.Join(failed, "\n    "))
				} else {
					t.Errorf("validation failed (%s): %s\n  failed phases:\n    %s",
						name, report.Summary, strings.Join(failed, "\n    "))
				}
			} else if report.Meta.KnownFailing {
				// The "gap closed" signal: if a scenario is marked
				// known-failing but actually passes now, the test
				// fails LOUDLY so the maintainer notices and drops
				// the flag.
				t.Errorf("validation passed (%s) but meta.known_failing=true — the daemon-side gap appears to be CLOSED; remove the known_failing flag from expected.jsonl",
					name)
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

// TestValidateExpected_missingEventsReturnsNil — applicable:false
// scenarios have a committed expected.jsonl spec but no events.jsonl
// (driver can't produce a recording today). ValidateExpected must
// return (nil, nil) so the test wrapper and CLI both treat the cell
// as "nothing to validate" rather than erroring. Mirrors the missing-
// expected.jsonl branch.
func TestValidateExpected_missingEventsReturnsNil(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"p1","expected_state":"ready","relative_to":"start","text":"_"}`+"\n")
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report != nil {
		t.Fatalf("expected nil report when events.jsonl missing, got %+v", report)
	}
}

// TestValidateExpected_transcriptWithoutEventsErrors — a HALF-recorded
// cell (expected.jsonl + a promoted transcript.jsonl, but no events.jsonl)
// must ERROR, not silently skip. Returning (nil, nil) here made
// replay-fixtures report a vacuous PASS (#496 RC6: opencode/task-list).
// Distinct from the missing-events-AND-no-transcript case above, which is
// a genuine applicable:false cell and stays a silent skip.
func TestValidateExpected_transcriptWithoutEventsErrors(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"p1","expected_state":"ready","relative_to":"start","text":"_"}`+"\n")
	writeRec(t, dir, "transcript.jsonl", `{"type":"user"}`+"\n")
	report, err := ValidateExpected(dir)
	if err == nil {
		t.Fatalf("expected an error for a transcript-without-events cell, got report=%+v", report)
	}
	if report != nil {
		t.Fatalf("expected nil report alongside the error, got %+v", report)
	}
	if !strings.Contains(err.Error(), "events.jsonl missing") {
		t.Errorf("expected 'events.jsonl missing' in error, got %q", err.Error())
	}
}

// TestValidateExpected_corruptFileFails — a phase whose anchor doesn't
// exist (typo in relative_to) should produce a failing report, not a
// silent pass. This is the canary the user asked for: "manually
// corrupt one expected.jsonl and confirm the validator fails".
func TestValidateExpected_unknownAnchorFails(t *testing.T) {
	dir := t.TempDir()
	writeRec(t, dir, "events.jsonl",
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
	writeRec(t, dir, "events.jsonl",
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

// TestValidateExpected_sameSessionAs_filtersByID — same_session_as
// pins a phase to the session_id matched by an earlier phase. With two
// sessions both transitioning to ready, the second phase should match
// the OLDER session's ready, not the newer one.
func TestValidateExpected_sameSessionAs_filtersByID(t *testing.T) {
	dir := t.TempDir()
	writeRec(t, dir, "events.jsonl",
		`{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"sess-a"}`+"\n"+
			`{"ts":"2026-01-01T00:00:00.001Z","kind":"state_transition","session_id":"sess-a","new_state":"ready"}`+"\n"+
			`{"ts":"2026-01-01T00:00:01Z","kind":"state_transition","session_id":"sess-a","new_state":"working"}`+"\n"+
			`{"ts":"2026-01-01T00:00:02Z","kind":"state_transition","session_id":"sess-b","new_state":"ready"}`+"\n"+
			`{"ts":"2026-01-01T00:00:03Z","kind":"state_transition","session_id":"sess-a","new_state":"ready"}`+"\n")
	// turn_end pinned to sess-a should match the +3s ready, NOT the
	// +2s sess-b ready that happens earlier.
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"a_birth","expected_state":"ready","relative_to":"start","text":"sess-a appears"}`+"\n"+
			`{"phase":"a_working","expected_state":"working","relative_to":"a_birth","same_session_as":"a_birth","text":"sess-a goes working"}`+"\n"+
			`{"phase":"a_ready","expected_state":"ready","relative_to":"a_working","same_session_as":"a_birth","text":"sess-a returns to ready (must skip sess-b's intervening ready)"}`+"\n")
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("ValidateExpected: %v", err)
	}
	if !report.Pass {
		for _, p := range report.Phases {
			t.Logf("phase %s: pass=%v reason=%q", p.Phase, p.Pass, p.Reason)
		}
		t.Fatal("expected all phases to pass with same_session_as filtering")
	}
	// Verify a_ready matched at +3s, not +2s
	if got := report.Phases[2].DeltaMs; got != 2000 {
		t.Errorf("a_ready delta_ms = %d (expected 2000ms past a_working), reason=%q", got, report.Phases[2].Reason)
	}
}

// TestValidateExpected_sameSessionAs_rejectsWhenNoMatch — when no
// event for the required session_id exists after the anchor, the
// phase fails with a session-specific error message.
func TestValidateExpected_sameSessionAs_rejectsWhenNoMatch(t *testing.T) {
	dir := t.TempDir()
	writeRec(t, dir, "events.jsonl",
		`{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"sess-a"}`+"\n"+
			`{"ts":"2026-01-01T00:00:00.001Z","kind":"state_transition","session_id":"sess-a","new_state":"ready"}`+"\n"+
			`{"ts":"2026-01-01T00:00:01Z","kind":"transcript_removed","session_id":"OTHER-SESS"}`+"\n")
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"birth","expected_state":"ready","relative_to":"start","text":"sess-a birth"}`+"\n"+
			`{"phase":"ended","kind":"transcript_removed","same_session_as":"birth","relative_to":"birth","text":"transcript_removed for sess-a specifically — none exists"}`+"\n")
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("ValidateExpected: %v", err)
	}
	if report.Pass {
		t.Fatal("expected failure — no transcript_removed exists for sess-a")
	}
	reason := report.Phases[1].Reason
	if !strings.Contains(reason, "sess-a") {
		t.Errorf("expected reason to name the required session_id, got %q", reason)
	}
}

// TestValidateExpected_newSession_requiresFreshID — new_session: true
// rejects events whose session_id was matched by an earlier phase.
// Models the post-/clear path: v2_session_birth must NOT match a
// stale transition on the original UUID.
func TestValidateExpected_newSession_requiresFreshID(t *testing.T) {
	dir := t.TempDir()
	writeRec(t, dir, "events.jsonl",
		`{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"old"}`+"\n"+
			`{"ts":"2026-01-01T00:00:00.001Z","kind":"state_transition","session_id":"old","new_state":"ready"}`+"\n"+
			`{"ts":"2026-01-01T00:00:01Z","kind":"state_transition","session_id":"old","new_state":"working"}`+"\n"+
			`{"ts":"2026-01-01T00:00:02Z","kind":"state_transition","session_id":"old","new_state":"ready"}`+"\n"+
			`{"ts":"2026-01-01T00:00:03Z","kind":"state_transition","session_id":"new","new_state":"ready"}`+"\n")
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"v1","expected_state":"ready","relative_to":"start","text":"old session ready"}`+"\n"+
			`{"phase":"v1_done","expected_state":"ready","relative_to":"v1","same_session_as":"v1","max_delay_ms":5000,"text":"old session returns to ready"}`+"\n"+
			`{"phase":"v2_birth","expected_state":"ready","relative_to":"v1_done","new_session":true,"max_delay_ms":5000,"text":"new session ready — must skip the old session's intervening ready transitions"}`+"\n")
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("ValidateExpected: %v", err)
	}
	if !report.Pass {
		for _, p := range report.Phases {
			t.Logf("phase %s: pass=%v reason=%q", p.Phase, p.Pass, p.Reason)
		}
		t.Fatal("expected all phases to pass — new_session should skip 'old' and find 'new'")
	}
}

// TestValidateExpected_newSession_failsWhenOnlyOldSeen — when the
// only candidate is the already-matched session_id, the phase fails
// with a new-session-specific error message.
func TestValidateExpected_newSession_failsWhenOnlyOldSeen(t *testing.T) {
	dir := t.TempDir()
	writeRec(t, dir, "events.jsonl",
		`{"ts":"2026-01-01T00:00:00Z","kind":"transcript_new","session_id":"only"}`+"\n"+
			`{"ts":"2026-01-01T00:00:00.001Z","kind":"state_transition","session_id":"only","new_state":"ready"}`+"\n"+
			`{"ts":"2026-01-01T00:00:01Z","kind":"state_transition","session_id":"only","new_state":"ready"}`+"\n")
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"v1","expected_state":"ready","relative_to":"start","text":"only session"}`+"\n"+
			`{"phase":"v2_birth","expected_state":"ready","relative_to":"v1","new_session":true,"max_delay_ms":5000,"text":"no actual new session — should fail"}`+"\n")
	report, err := ValidateExpected(dir)
	if err != nil {
		t.Fatalf("ValidateExpected: %v", err)
	}
	if report.Pass {
		t.Fatal("expected failure — no new session_id ever appears")
	}
	reason := report.Phases[1].Reason
	if !strings.Contains(reason, "NEW session") {
		t.Errorf("expected reason to mention new-session requirement, got %q", reason)
	}
}

// TestValidateExpected_sameAndNewMutuallyExclusive — load-time
// rejection when a phase declares both same_session_as and
// new_session: true.
func TestValidateExpected_sameAndNewMutuallyExclusive(t *testing.T) {
	dir := t.TempDir()
	writeRec(t, dir, "events.jsonl", "")
	mustWrite(t, filepath.Join(dir, "expected.jsonl"),
		`{"schema_version":1,"scenario_id":"test","source":"unit test"}`+"\n"+
			`{"phase":"bad","expected_state":"ready","relative_to":"start","same_session_as":"x","new_session":true,"text":"can't have both"}`+"\n")
	_, err := ValidateExpected(dir)
	if err == nil {
		t.Fatal("expected load-time error for mutually-exclusive predicates")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected error to mention 'mutually exclusive', got %q", err.Error())
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeRec writes a recording artifact under dir/recordings/rec/<file>, mirroring
// the on-disk layout where every recording lives under recordings/<name>/. The
// spec (expected.jsonl) stays at the cell root.
func writeRec(t *testing.T, dir, file, content string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, "recordings", "rec", file), content)
}
