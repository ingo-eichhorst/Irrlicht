package tailer

import "testing"

// TestIsUserBlockingToolName pins the user-blocking tool set this package
// matches. The canonical list is duplicated at session.isUserBlockingTool —
// kept local here to avoid a domain-package import — and
// TestNeedsUserAttention_UserBlockingToolNames in that package is this test's
// twin. The two must be updated together.
func TestIsUserBlockingToolName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"AskUserQuestion", true},
		{"ExitPlanMode", true},
		{"question", true},
		// vibe names the same always-blocks-for-input tool in snake_case
		// (live 2.19.1 tools_available lists ask_user_question). Before #1087
		// this fell through, so the collapsed working→waiting transition was
		// never synthesised for vibe's structured-question path.
		{"ask_user_question", true},
		// Auto-executing tools must not be flagged.
		{"Bash", false},
		{"Write", false},
		{"write_file", false},
		{"Agent", false},
		{"", false},
		// Exact match, not substring: a tool merely containing a blocking name
		// must not qualify.
		{"ask_user_question_v2", false},
		{"AskUserQuestionTool", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUserBlockingToolName(tt.name); got != tt.want {
				t.Errorf("isUserBlockingToolName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
