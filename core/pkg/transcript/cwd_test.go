package transcript

import "testing"

func TestExtractCWDFromLine(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]interface{}
		want string
	}{
		{
			name: "claude top-level cwd",
			raw: map[string]interface{}{
				"cwd": "/Users/test/claude",
			},
			want: "/Users/test/claude",
		},
		{
			name: "classic codex environment context",
			raw: map[string]interface{}{
				"type": "message",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "<environment_context><cwd>/Users/test/classic</cwd></environment_context>"},
				},
			},
			want: "/Users/test/classic",
		},
		{
			name: "wrapped session metadata cwd",
			raw: map[string]interface{}{
				"type": "session_meta",
				"payload": map[string]interface{}{
					"cwd": "/Users/test/wrapped-meta",
				},
			},
			want: "/Users/test/wrapped-meta",
		},
		{
			name: "wrapped tool call workdir",
			raw: map[string]interface{}{
				"type": "response_item",
				"payload": map[string]interface{}{
					"type":      "function_call",
					"name":      "shell_command",
					"arguments": `{"command":["pwd"],"workdir":"/Users/test/wrapped-tool"}`,
				},
			},
			want: "/Users/test/wrapped-tool",
		},
		{
			name: "non tool arguments ignored",
			raw: map[string]interface{}{
				"type":      "event_msg",
				"arguments": `{"workdir":"/Users/test/ignored"}`,
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractCWDFromLine(tt.raw); got != tt.want {
				t.Errorf("ExtractCWDFromLine() = %q, want %q", got, tt.want)
			}
		})
	}
}
