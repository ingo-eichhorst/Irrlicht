package rulelib

import (
	"encoding/json"
	"testing"
)

func mkRule(t *testing.T, kind, state string, params any) Rule {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return Rule{Kind: kind, TargetState: state, Params: b}
}

func mkInput(t *testing.T, sensor, kind string, payload any) MatchInput {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return MatchInput{Sensor: sensor, Kind: kind, Payload: b}
}

func TestMatch_transcriptTailRegex(t *testing.T) {
	r := mkRule(t, KindTranscriptTailRegex, "ready", map[string]string{"pattern": "end_turn"})
	in := mkInput(t, "transcript", "line", map[string]string{"line": `{"stop_reason":"end_turn"}`})
	if !Match(r, in) {
		t.Error("expected match on substring")
	}
	in2 := mkInput(t, "transcript", "line", map[string]string{"line": `{"stop_reason":"max_tokens"}`})
	if Match(r, in2) {
		t.Error("did not expect match")
	}
}

func TestMatch_transcriptFieldValue_topLevel(t *testing.T) {
	r := mkRule(t, KindTranscriptFieldValue, "ready", map[string]string{"field": "stop_reason", "value": "end_turn"})
	in := mkInput(t, "transcript", "line", map[string]string{"line": `{"stop_reason":"end_turn","ts":1}`})
	if !Match(r, in) {
		t.Error("expected field match")
	}
}

func TestMatch_transcriptFieldValue_nested(t *testing.T) {
	r := mkRule(t, KindTranscriptFieldValue, "ready", map[string]string{"field": "event_msg.type", "value": "task_complete"})
	in := mkInput(t, "transcript", "line", map[string]string{"line": `{"event_msg":{"type":"task_complete"}}`})
	if !Match(r, in) {
		t.Error("expected nested match")
	}
}

func TestMatch_paneSubstring(t *testing.T) {
	r := mkRule(t, KindPaneSubstringPresent, "working", map[string]string{"substring": "Booting MCP"})
	in := mkInput(t, "pane", "snapshot", map[string]string{"snapshot": "Some text\nBooting MCP server\nMore"})
	if !Match(r, in) {
		t.Error("expected substring match")
	}
}

func TestMatch_processSpawned(t *testing.T) {
	r := mkRule(t, KindProcessSpawned, "working", map[string]string{"name_pattern": "^python"})
	in := mkInput(t, "proc", "spawn", map[string]any{"args": []string{"python3", "-m", "pytest"}})
	if !Match(r, in) {
		t.Error("expected process match")
	}
}

func TestMatch_interruptMarker(t *testing.T) {
	r := mkRule(t, KindInterruptMarker, "ready", map[string]string{"marker_substring": "[Request interrupted by user]"})
	in := mkInput(t, "transcript", "line", map[string]string{"line": `{"type":"user","content":"[Request interrupted by user]"}`})
	if !Match(r, in) {
		t.Error("expected interrupt match")
	}
}

func TestMatch_unknownKindReturnsFalse(t *testing.T) {
	r := Rule{Kind: "bogus", Params: json.RawMessage(`{}`)}
	in := MatchInput{Sensor: "x"}
	if Match(r, in) {
		t.Error("unknown kind matched")
	}
}

func TestAllKinds_isExhaustive(t *testing.T) {
	if len(AllKinds()) != 12 {
		t.Errorf("AllKinds has %d entries, want 12", len(AllKinds()))
	}
}
