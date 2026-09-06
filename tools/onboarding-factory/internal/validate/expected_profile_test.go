package validate

import (
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// The CLI and Claude Desktop create a session by different acts, and the
// session's state AT BIRTH differs as a result.
//
//   - `claude` launches and then idles, so the daemon's first transition for
//     the new row is `ready` ("new session created").
//   - Under Desktop, SUBMITTING THE PROMPT is what creates the session. The
//     row therefore appears already `working`, and reaches `ready` only when
//     that first turn ENDS.
//
// A single `session_birth` phase asserting `ready` within 1s consequently
// measures two different things. Under cli-local it measures birth latency
// (~21 ms measured). Under desktop-local it measures THE DURATION OF THE
// FIRST TURN, which is why the failures scaled with the cell's workload
// rather than with anything about session creation:
//
//	5-2 model-identification    2463 ms
//	2-1 basic-turn              4921 ms
//	2-4 self-correction         9572 ms
//	3-3 background-process     16531 ms
//
// The birth EVENT itself landed at +36..71 ms in all eleven Desktop
// recordings — comfortably inside the existing 1000 ms budget. So the budget
// and the anchor were both right; only the expected STATE was wrong.
//
// `profiles.<profile>.expected_states` lets one phase name the birth states
// each profile's launch mechanism can legitimately produce.

const (
	// desktopBirthEvents is the shape 10 of 11 Desktop recordings had: the
	// row is created already working, and ready arrives 4.9s later when the
	// first turn ends.
	desktopBirthEvents = `{"ts":"2026-01-01T00:00:00.000Z","kind":"transcript_new","session_id":"x"}` + "\n" +
		`{"ts":"2026-01-01T00:00:00.060Z","kind":"state_transition","session_id":"x","new_state":"working","reason":"new session created"}` + "\n" +
		`{"ts":"2026-01-01T00:00:04.900Z","kind":"state_transition","session_id":"x","new_state":"ready","reason":"agent finished turn → ready"}` + "\n"

	// presessionBirthEvents is the shape cell 1-3 had: PID discovery won the
	// race against transcript activity, so a presession row was created in
	// `ready` first. Both shapes occur under desktop-local.
	presessionBirthEvents = `{"ts":"2026-01-01T00:00:00.000Z","kind":"transcript_new","session_id":"x"}` + "\n" +
		`{"ts":"2026-01-01T00:00:00.041Z","kind":"presession_created","session_id":"x"}` + "\n" +
		`{"ts":"2026-01-01T00:00:00.041Z","kind":"state_transition","session_id":"x","new_state":"ready","reason":"new session created"}` + "\n"

	// birthSpec is the real 1-1 shape plus the per-profile override.
	birthSpec = `{"schema_version":1,"scenario_id":"test","source":"unit test"}` + "\n" +
		`{"phase":"session_birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000,` +
		`"profiles":{"desktop-local":{"expected_states":["working","ready"]}},` +
		`"text":"Session appears within 1s of agent launch"}` + "\n"
)

// writeSpecAndEvents lays out a cell whose spec and events sit at known paths,
// for the path-explicit validation API.
func writeSpecAndEvents(t *testing.T, spec, events string) (specPath, eventsPath string) {
	t.Helper()
	dir := t.TempDir()
	specPath = filepath.Join(dir, "expected.jsonl")
	eventsPath = filepath.Join(dir, "recordings", "rec", "events.jsonl")
	mustWrite(t, specPath, spec)
	mustWrite(t, eventsPath, events)
	return specPath, eventsPath
}

// A Desktop session born `working` satisfies session_birth at +60 ms, because
// the desktop-local override names `working` as a legitimate birth state.
//
// RED-FIRST: before the override existed this failed with
// "event arrived 4900 ms after anchor, exceeds max_delay_ms=1000" — the
// validator ignored the profiles block and matched the post-turn `ready`.
func TestValidateExpected_desktopProfileAcceptsWorkingBirth(t *testing.T) {
	specPath, eventsPath := writeSpecAndEvents(t, birthSpec, desktopBirthEvents)

	report, err := ValidateExpectedAgainstForProfile(specPath, eventsPath, matrix.ProfileDesktopLocal)
	if err != nil {
		t.Fatalf("ValidateExpectedAgainstForProfile: %v", err)
	}
	if report == nil {
		t.Fatal("report is nil — the validator skipped instead of validating")
	}
	if !report.Pass {
		t.Fatalf("desktop-local validation failed: %v", failedPhaseSummaries(report.Phases))
	}
	if got := report.Phases[0].DeltaMs; got != 60 {
		t.Errorf("session_birth bound to the wrong event: delta=%d ms, want 60 ms (the birth transition, not the turn end)", got)
	}
}

// The 1-3 shape: a presession row born `ready`. The override lists both
// states because which one occurs depends on whether PID discovery or
// transcript activity reaches the daemon first — a race, not a property of
// the scenario.
func TestValidateExpected_desktopProfileAcceptsReadyBirth(t *testing.T) {
	specPath, eventsPath := writeSpecAndEvents(t, birthSpec, presessionBirthEvents)

	report, err := ValidateExpectedAgainstForProfile(specPath, eventsPath, matrix.ProfileDesktopLocal)
	if err != nil {
		t.Fatalf("ValidateExpectedAgainstForProfile: %v", err)
	}
	if !report.Pass {
		t.Fatalf("desktop-local validation failed on a ready-born session: %v", failedPhaseSummaries(report.Phases))
	}
}

// LOCK (passes by construction, before and after — not red-first evidence).
//
// The whole point of a per-profile override is that it changes NOTHING for
// cli-local. The same spec and the same Desktop-shaped stream must still fail
// under cli-local, where a session born `working` is a genuine defect.
func TestValidateExpected_desktopOverrideDoesNotLeakToCLI(t *testing.T) {
	specPath, eventsPath := writeSpecAndEvents(t, birthSpec, desktopBirthEvents)

	report, err := ValidateExpectedAgainstForProfile(specPath, eventsPath, matrix.ProfileCLILocal)
	if err != nil {
		t.Fatalf("ValidateExpectedAgainstForProfile: %v", err)
	}
	if report.Pass {
		t.Fatal("cli-local accepted a session born `working` — the desktop-local override leaked across profiles")
	}
	if !strings.Contains(report.Phases[0].Reason, "exceeds max_delay_ms") {
		t.Errorf("expected the original max_delay_ms failure under cli-local, got %q", report.Phases[0].Reason)
	}
}

// The default-profile entry points must keep behaving exactly as before.
func TestValidateExpected_defaultEntryPointStaysCLILocal(t *testing.T) {
	specPath, eventsPath := writeSpecAndEvents(t, birthSpec, desktopBirthEvents)

	report, err := ValidateExpectedAgainst(specPath, eventsPath)
	if err != nil {
		t.Fatalf("ValidateExpectedAgainst: %v", err)
	}
	if report.Pass {
		t.Fatal("the profile-less entry point accepted a Desktop birth — it must stay cli-local")
	}
}

// A profiles block naming a profile that does not exist is an AUTHORING
// error, not a silent no-op. Without this, `"desktop_local"` (underscore) or
// `"desktop"` would parse, apply to nothing, and leave the cell failing for a
// reason nobody could see — the "fail loudly when it cannot run" rule.
func TestValidateExpected_unknownProfileKeyIsRejected(t *testing.T) {
	spec := `{"schema_version":1,"scenario_id":"test","source":"unit test"}` + "\n" +
		`{"phase":"session_birth","expected_state":"ready","relative_to":"start",` +
		`"profiles":{"desktop_local":{"expected_states":["working"]}}}` + "\n"
	specPath, eventsPath := writeSpecAndEvents(t, spec, desktopBirthEvents)

	_, err := ValidateExpectedAgainstForProfile(specPath, eventsPath, matrix.ProfileDesktopLocal)
	if err == nil {
		t.Fatal("a profiles block naming an unknown execution profile was accepted silently")
	}
	if !strings.Contains(err.Error(), "desktop_local") {
		t.Errorf("the error should name the offending key, got %q", err)
	}
}

// An override naming a state outside session.CanonicalStates() is likewise an
// authoring error. A typo such as "workign" would otherwise match no event and
// present as a scenario failure.
func TestValidateExpected_unknownOverrideStateIsRejected(t *testing.T) {
	spec := `{"schema_version":1,"scenario_id":"test","source":"unit test"}` + "\n" +
		`{"phase":"session_birth","expected_state":"ready","relative_to":"start",` +
		`"profiles":{"desktop-local":{"expected_states":["workign"]}}}` + "\n"
	specPath, eventsPath := writeSpecAndEvents(t, spec, desktopBirthEvents)

	_, err := ValidateExpectedAgainstForProfile(specPath, eventsPath, matrix.ProfileDesktopLocal)
	if err == nil {
		t.Fatal("an override naming a non-canonical state was accepted silently")
	}
	if !strings.Contains(err.Error(), "workign") {
		t.Errorf("the error should name the offending state, got %q", err)
	}
}

// An override that carries no states at all is an empty edit — it reads as
// "this profile overrides the state" while overriding nothing.
func TestValidateExpected_emptyOverrideIsRejected(t *testing.T) {
	spec := `{"schema_version":1,"scenario_id":"test","source":"unit test"}` + "\n" +
		`{"phase":"session_birth","expected_state":"ready","relative_to":"start",` +
		`"profiles":{"desktop-local":{}}}` + "\n"
	specPath, eventsPath := writeSpecAndEvents(t, spec, desktopBirthEvents)

	_, err := ValidateExpectedAgainstForProfile(specPath, eventsPath, matrix.ProfileDesktopLocal)
	if err == nil {
		t.Fatal("an override with no expected_states was accepted silently")
	}
}

// A kind-matching phase has no state to override, so a states override on one
// is a contradiction rather than a refinement.
func TestValidateExpected_overrideOnKindPhaseIsRejected(t *testing.T) {
	spec := `{"schema_version":1,"scenario_id":"test","source":"unit test"}` + "\n" +
		`{"phase":"pid_bind","kind":"pid_discovered","relative_to":"start",` +
		`"profiles":{"desktop-local":{"expected_states":["working"]}}}` + "\n"
	specPath, eventsPath := writeSpecAndEvents(t, spec, desktopBirthEvents)

	_, err := ValidateExpectedAgainstForProfile(specPath, eventsPath, matrix.ProfileDesktopLocal)
	if err == nil {
		t.Fatal("expected_states on a kind-matching phase was accepted silently")
	}
}

// The failure text for an unmatched multi-state phase must name every state
// it looked for. "no event matching \"\"" would be unreadable.
func TestValidateExpected_multiStateNoMatchNamesEveryState(t *testing.T) {
	events := `{"ts":"2026-01-01T00:00:00.000Z","kind":"transcript_new","session_id":"x"}` + "\n"
	specPath, eventsPath := writeSpecAndEvents(t, birthSpec, events)

	report, err := ValidateExpectedAgainstForProfile(specPath, eventsPath, matrix.ProfileDesktopLocal)
	if err != nil {
		t.Fatalf("ValidateExpectedAgainstForProfile: %v", err)
	}
	if report.Pass {
		t.Fatal("a stream with no state transition at all somehow passed")
	}
	for _, want := range []string{"working", "ready"} {
		if !strings.Contains(report.Phases[0].Reason, want) {
			t.Errorf("failure reason %q does not name the accepted state %q", report.Phases[0].Reason, want)
		}
	}
}
