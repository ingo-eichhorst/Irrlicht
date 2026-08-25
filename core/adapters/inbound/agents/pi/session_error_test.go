package pi

import (
	"encoding/json"
	"os"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// recordedPiErrorLine is line 5 of the committed 2-14_turn-aborted-by-error
// recording, verbatim. Reading the real fixture rather than a hand-written
// approximation is the point: the shape under test is the one pi actually
// wrote, including errorMessage being a double-encoded JSON string.
const recordedPiErrorFixture = "../../../../../replaydata/agents/pi/scenarios/2-14_turn-aborted-by-error/recordings/2026-05-25-05-20-53_irrlichd-0.4.7+9d9c471/transcript.jsonl"

func recordedPiErrorLine(t *testing.T) map[string]interface{} {
	t.Helper()
	b, err := os.ReadFile(recordedPiErrorFixture)
	if err != nil {
		t.Fatalf("read recorded fixture: %v", err)
	}
	var last map[string]interface{}
	for _, line := range splitNonEmptyLines(string(b)) {
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("unmarshal fixture line: %v", err)
		}
		msg, _ := raw["message"].(map[string]interface{})
		if msg == nil {
			continue
		}
		if sr, _ := msg["stopReason"].(string); sr == "error" {
			last = raw
		}
	}
	if last == nil {
		t.Fatalf("no stopReason:\"error\" line in %s — the fixture this test is written against is gone", recordedPiErrorFixture)
	}
	return last
}

func splitNonEmptyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func TestParser_StopReasonError_IsASessionError(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(recordedPiErrorLine(t))
	if ev == nil {
		t.Fatal("ParseLine returned nil for the recorded errored turn")
	}
	if ev.EventType != "turn_done" {
		t.Errorf("EventType = %q, want \"turn_done\" — an errored turn has ended; "+
			"emitting \"assistant\" leaves the session stuck in working forever", ev.EventType)
	}
	if ev.SessionError == nil {
		t.Fatal("SessionError is nil — pi reports stopReason:\"error\" with an errorMessage " +
			"and the parser discards both")
	}
	if ev.SessionError.Phase != tailer.ErrorPhaseTerminal {
		t.Errorf("Phase = %q, want terminal", ev.SessionError.Phase)
	}
	if ev.SessionError.Message == "" {
		t.Error("Message is empty — errorMessage carries the provider's refusal text")
	}
	if ev.IsError {
		t.Error("IsError must stay false: it means a TOOL RESULT failed, not a session failure")
	}
}
