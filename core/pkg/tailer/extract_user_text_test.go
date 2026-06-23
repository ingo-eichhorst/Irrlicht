package tailer

import "testing"

func TestExtractUserText_ClaudeCodeStringContent(t *testing.T) {
	raw := map[string]interface{}{
		"message": map[string]interface{}{
			"role":    "user",
			"content": "add a logout button to the navbar",
		},
	}
	if got := ExtractUserText(raw); got != "add a logout button to the navbar" {
		t.Errorf("got %q", got)
	}
}

func TestExtractUserText_TextBlocks(t *testing.T) {
	raw := map[string]interface{}{
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "text", "text": "first part"},
				map[string]interface{}{"type": "text", "text": "second part"},
			},
		},
	}
	if got := ExtractUserText(raw); got != "first part second part" {
		t.Errorf("got %q", got)
	}
}

func TestExtractUserText_ToolResultOnlyReturnsEmpty(t *testing.T) {
	// A user-role line that carries only a tool_result block is a tool
	// response, not a prompt — must not feed the heuristic summary.
	raw := map[string]interface{}{
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "content": "exit 0"},
			},
		},
	}
	if got := ExtractUserText(raw); got != "" {
		t.Errorf("got %q, want empty for a tool-result-only line", got)
	}
}

func TestExtractUserText_TopLevelContent(t *testing.T) {
	// Codex / others: top-level content (string).
	raw := map[string]interface{}{"content": "investigate the flaky test"}
	if got := ExtractUserText(raw); got != "investigate the flaky test" {
		t.Errorf("got %q", got)
	}
}

func TestExtractUserText_Empty(t *testing.T) {
	if got := ExtractUserText(map[string]interface{}{}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
