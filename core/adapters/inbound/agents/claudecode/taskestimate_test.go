package claudecode

import (
	"strings"
	"testing"
	"time"
)

// Scan-level unit tests live with the shared scanner in
// core/pkg/tailer/taskestimate_scan_test.go; these cover the claudecode
// ParseLine integration (issue #558).

var estObservedAt = time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

func taskEstimateAssistantLine(text string) map[string]interface{} {
	return map[string]interface{}{
		"type":      "assistant",
		"timestamp": estObservedAt.Format(time.RFC3339),
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": text},
			},
		},
	}
}

func TestParser_TaskEstimate_FromAssistantText(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(taskEstimateAssistantLine(
		`Done with setup. <!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":2} -->`))
	if ev.TaskEstimate == nil {
		t.Fatal("expected TaskEstimate on assistant event")
	}
	if ev.TaskEstimate.TotalRounds != 10 || ev.TaskEstimate.CompletedRounds != 2 {
		t.Errorf("rounds = %d/%d, want 2/10", ev.TaskEstimate.CompletedRounds, ev.TaskEstimate.TotalRounds)
	}
}

// The truncation trap: ParsedEvent.AssistantText keeps only the last 200
// runes, so a marker early in a long message must still be parsed (the scan
// runs over the full text block, not the truncated field).
func TestParser_TaskEstimate_SurvivesLongMessage(t *testing.T) {
	p := &Parser{}
	text := `<!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":5} --> ` +
		strings.Repeat("filler prose ", 50)
	ev := p.ParseLine(taskEstimateAssistantLine(text))
	if ev.TaskEstimate == nil {
		t.Fatal("marker early in a long message must still be parsed")
	}
	if strings.Contains(ev.AssistantText, "irrlicht-eta") {
		t.Fatal("test precondition: marker should have been truncated out of AssistantText")
	}
	if ev.TaskEstimate.CompletedRounds != 5 {
		t.Errorf("CompletedRounds = %d, want 5", ev.TaskEstimate.CompletedRounds)
	}
}

func TestParser_TaskEstimate_UserPasteIgnored(t *testing.T) {
	p := &Parser{}
	line := map[string]interface{}{
		"type":      "user",
		"timestamp": estObservedAt.Format(time.RFC3339),
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": `<!-- {"marker":"irrlicht-eta","total_rounds":10,"completed_rounds":2} -->`},
			},
		},
	}
	if ev := p.ParseLine(line); ev.TaskEstimate != nil {
		t.Fatalf("user-pasted marker must not feed the estimate, got %+v", ev.TaskEstimate)
	}
}
