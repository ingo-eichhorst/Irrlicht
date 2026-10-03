package claudecode

import (
	"strings"
	"testing"
)

func assistantLine(content ...interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type":    "assistant",
		"message": map[string]interface{}{"role": "assistant", "content": content},
	}
}

// The execution-confidence sample is measured from the FULL assistant text
// (issue #737): a hedge sitting before the 200-rune display tail must still
// count, which it could not if the sample were taken from AssistantText.
func TestParser_Hedge_MeasuredFromFullText(t *testing.T) {
	text := "I'm not sure this is the right file, the spec is ambiguous. " + strings.Repeat("Reading the parser now. ", 20)
	ev := (&Parser{}).ParseLine(assistantLine(map[string]interface{}{"type": "text", "text": text}))
	if strings.Contains(ev.AssistantText, "not sure") {
		t.Fatalf("test is not exercising the beyond-tail path: the hedge survived into AssistantText %q", ev.AssistantText)
	}
	if ev.Hedge == nil || ev.Hedge.Weight == 0 {
		t.Fatalf("Hedge = %+v, want a weighted sample from the full text", ev.Hedge)
	}
}

// An AskUserQuestion-only message carries the question as tool input, not as
// the agent's prose: it falls back into AssistantText for display but must not
// be scored.
func TestParser_Hedge_IgnoresAskUserQuestionFallback(t *testing.T) {
	ev := (&Parser{}).ParseLine(assistantLine(map[string]interface{}{
		"type": "tool_use", "id": "toolu_1", "name": "AskUserQuestion",
		"input": map[string]interface{}{"questions": []interface{}{
			map[string]interface{}{"question": "I'm not sure which is right — maybe A, perhaps B?"},
		}},
	}))
	if ev.AssistantText == "" {
		t.Fatal("precondition: the AskUserQuestion fallback did not populate AssistantText")
	}
	if ev.Hedge != nil {
		t.Errorf("Hedge = %+v, want nil — AskUserQuestion input is not assistant prose", ev.Hedge)
	}
}
