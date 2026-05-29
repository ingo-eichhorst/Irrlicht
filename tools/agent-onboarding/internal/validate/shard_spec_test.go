package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// rawLines turns JSON strings into the []json.RawMessage shape a shard's
// details.expected[] block carries.
func rawLines(lines ...string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(lines))
	for _, l := range lines {
		out = append(out, json.RawMessage(l))
	}
	return out
}

func TestParseShardSpec_OK(t *testing.T) {
	meta, phases, ok, err := ParseShardSpec(
		json.RawMessage(`{"schema_version":1,"scenario_id":"basic-turn","source":"spec","notes":"n"}`),
		rawLines(
			`{"phase":"birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000}`,
			`{"phase":"pid","kind":"pid_discovered","relative_to":"birth"}`,
		),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("ok should be true when phases are present")
	}
	if meta.ScenarioID != "basic-turn" || meta.SchemaVersion != 1 {
		t.Fatalf("meta not parsed: %+v", meta)
	}
	if len(phases) != 2 || phases[0].Phase != "birth" || phases[1].Kind != "pid_discovered" {
		t.Fatalf("phases not parsed: %+v", phases)
	}
}

func TestParseShardSpec_NoPhases(t *testing.T) {
	// No phase lines is "nothing to validate", not an error.
	meta, phases, ok, err := ParseShardSpec(json.RawMessage(`{"scenario_id":"x"}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("ok should be false with no phases")
	}
	if phases != nil {
		t.Fatalf("phases should be nil, got %+v", phases)
	}
	if meta.ScenarioID != "x" {
		t.Fatalf("meta should still parse: %+v", meta)
	}
}

func TestParseShardSpec_RejectsInvalidPhases(t *testing.T) {
	cases := map[string][]json.RawMessage{
		"missing phase name":       rawLines(`{"expected_state":"ready"}`),
		"neither state nor kind":   rawLines(`{"phase":"p"}`),
		"both state and kind":      rawLines(`{"phase":"p","expected_state":"ready","kind":"pid_discovered"}`),
		"same+new session at once": rawLines(`{"phase":"p","expected_state":"ready","same_session_as":"q","new_session":true}`),
		"malformed json":           rawLines(`{"phase":`),
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, ok, err := ParseShardSpec(nil, lines); err == nil || ok {
				t.Fatalf("expected error for %q, got ok=%v err=%v", name, ok, err)
			}
		})
	}
}

// TestParseShardSpec_ParityWithExpectedJSONL asserts a shard spec and the
// equivalent on-disk expected.jsonl parse to identical meta + phases — the two
// storage forms must be interchangeable.
func TestParseShardSpec_ParityWithExpectedJSONL(t *testing.T) {
	metaLine := `{"schema_version":1,"scenario_id":"basic-turn","source":"spec"}`
	phaseLine1 := `{"phase":"birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000}`
	phaseLine2 := `{"phase":"pid","kind":"pid_discovered","relative_to":"birth"}`

	dir := t.TempDir()
	expectedPath := filepath.Join(dir, "expected.jsonl")
	if err := os.WriteFile(expectedPath, []byte(metaLine+"\n"+phaseLine1+"\n"+phaseLine2+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diskMeta, diskPhases, err := loadExpected(expectedPath)
	if err != nil {
		t.Fatalf("loadExpected: %v", err)
	}
	shardMeta, shardPhases, ok, err := ParseShardSpec(json.RawMessage(metaLine), rawLines(phaseLine1, phaseLine2))
	if err != nil || !ok {
		t.Fatalf("ParseShardSpec: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(diskMeta, shardMeta) {
		t.Fatalf("meta differs:\n disk=%+v\nshard=%+v", diskMeta, shardMeta)
	}
	if !reflect.DeepEqual(diskPhases, shardPhases) {
		t.Fatalf("phases differ:\n disk=%+v\nshard=%+v", diskPhases, shardPhases)
	}
}

// TestValidatePhases_AgainstEvents drives the shared core directly with parsed
// phases and a tiny events.jsonl, exercising the path the shard readers use.
func TestValidatePhases_AgainstEvents(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, "events.jsonl")
	events := `{"ts":"2026-01-01T00:00:00Z","kind":"pid_discovered","session_id":"s1"}
{"ts":"2026-01-01T00:00:00.5Z","kind":"state_transition","session_id":"s1","new_state":"ready"}
`
	if err := os.WriteFile(eventsPath, []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}

	meta, phases, ok, err := ParseShardSpec(
		json.RawMessage(`{"scenario_id":"basic-turn"}`),
		rawLines(`{"phase":"birth","expected_state":"ready","relative_to":"start","max_delay_ms":1000}`),
	)
	if err != nil || !ok {
		t.Fatalf("ParseShardSpec: ok=%v err=%v", ok, err)
	}

	rep, err := ValidatePhases(meta, phases, eventsPath)
	if err != nil {
		t.Fatalf("ValidatePhases: %v", err)
	}
	if rep == nil || !rep.Pass {
		t.Fatalf("expected a passing report, got %+v", rep)
	}
	if rep.Meta.ScenarioID != "basic-turn" {
		t.Fatalf("meta not carried through: %+v", rep.Meta)
	}
}

// TestValidatePhases_NoEvents returns (nil,nil) when the recording is absent —
// the same "nothing to validate yet" shape ValidateExpectedAgainst relies on.
func TestValidatePhases_NoEvents(t *testing.T) {
	meta, phases, _, err := ParseShardSpec(nil, rawLines(`{"phase":"p","expected_state":"ready"}`))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ValidatePhases(meta, phases, filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Fatalf("absent events must not error, got %v", err)
	}
	if rep != nil {
		t.Fatalf("absent events must yield a nil report, got %+v", rep)
	}
}
