package tailer

import "testing"

// toolNameCase is one (tool name → expected verdict) row for this package's
// tool-name predicates, both of which live in tailer_config.go.
type toolNameCase struct {
	name string
	want bool
}

// assertToolNamePredicate table-drives a tool-name predicate. The two
// predicates classify overlapping-but-distinct sets, so they keep separate
// tables rather than sharing one — see surviveTurnDone's doc comment on why
// the overlap is intentional.
func assertToolNamePredicate(t *testing.T, fnName string, fn func(string) bool, cases []toolNameCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := fn(tt.name); got != tt.want {
				t.Errorf("%s(%q) = %v, want %v", fnName, tt.name, got, tt.want)
			}
		})
	}
}

func TestSurviveTurnDone(t *testing.T) {
	assertToolNamePredicate(t, "surviveTurnDone", surviveTurnDone, []toolNameCase{
		{"Agent", true},
		{"SendMessage", true},
		{"AskUserQuestion", true},
		{"ExitPlanMode", true},
		{"Bash", false},
		{"Read", false},
		{"Write", false},
		{"", false},
	})
}

// TestIsUserBlockingToolName pins the user-blocking tool set this package
// matches. The canonical list is duplicated at session.isUserBlockingTool —
// kept local here to avoid a domain-package import — and
// TestNeedsUserAttention_UserBlockingToolNames in that package is this test's
// twin. The two must be updated together.
func TestIsUserBlockingToolName(t *testing.T) {
	assertToolNamePredicate(t, "isUserBlockingToolName", isUserBlockingToolName, []toolNameCase{
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
	})
}
