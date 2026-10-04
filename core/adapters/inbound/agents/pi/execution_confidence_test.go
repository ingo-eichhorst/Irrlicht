package pi

import "testing"

// Pi's display text keeps only the LAST text block, but the
// execution-confidence sample (issue #737) must cover every block of the
// message — a hedge in an earlier block still counts, once.
func TestParser_Hedge_SumsEveryTextBlock(t *testing.T) {
	ev := (&Parser{}).ParseLine(map[string]interface{}{
		"type":      "message",
		"timestamp": ts(0),
		"message": map[string]interface{}{
			"role":       "assistant",
			"stopReason": "stop",
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "I'm not sure the cache is the cause."},
				map[string]interface{}{"type": "text", "text": "Done, the fix is in."},
			},
		},
	})
	if ev.AssistantText != "Done, the fix is in." {
		t.Fatalf("precondition: AssistantText = %q, want the last block", ev.AssistantText)
	}
	if ev.Hedge == nil || ev.Hedge.Weight == 0 {
		t.Fatalf("Hedge = %+v, want the first block's hedge counted", ev.Hedge)
	}
	if ev.Hedge.Words != 13 {
		t.Errorf("Hedge.Words = %d, want 13 (both blocks)", ev.Hedge.Words)
	}
}
