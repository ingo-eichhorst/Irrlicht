package session

import "testing"

func TestHasOpenEditPermissionTool(t *testing.T) {
	tests := []struct {
		name    string
		metrics *SessionMetrics
		want    bool
	}{
		{"nil metrics", nil, false},
		{"no open tools", &SessionMetrics{HasOpenToolCall: false, LastOpenToolNames: []string{"Edit"}}, false},
		{"open Edit", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Edit"}}, true},
		{"open Write", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Write"}}, true},
		{"open MultiEdit", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"MultiEdit"}}, true},
		{"open NotebookEdit", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"NotebookEdit"}}, true},
		// Lowercase variants emitted by kiro-cli and pi must gate too (#588) —
		// the same fast in-process edit tools, just a different casing.
		{"open lowercase write (kiro)", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"write"}}, true},
		{"open lowercase edit (pi)", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"edit"}}, true},
		{"open lowercase multiedit", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"multiedit"}}, true},
		{"open lowercase notebookedit", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"notebookedit"}}, true},
		// Snake_case variants emitted by vibe/gemini-cli must gate too (#1087).
		// vibe was previously half-in: upstream's search_replace→edit rename
		// silently opted "edit" in, but "write_file" never matched "write".
		{"open write_file (vibe)", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"write_file"}}, true},
		{"open Write_File (case-folded)", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Write_File"}}, true},
		// Tools that can legitimately run long must NOT qualify — duration
		// can't distinguish "blocked on prompt" from "executing" for them.
		{"open Bash", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}}, false},
		{"open WebFetch", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"WebFetch"}}, false},
		{"open Read", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Read"}}, false},
		{"open mcp tool", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"mcp__server__do"}}, false},
		// codex's write_stdin is an interactive PTY session that can stream for
		// a long time — the near-miss that exact matching (not prefix/substring)
		// keeps out. Guards the write_file addition against regressing into a
		// substring match.
		{"open write_stdin (codex)", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"write_stdin"}}, false},
		{"open write_todos (gemini)", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"write_todos"}}, false},
		{"open AskUserQuestion", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"AskUserQuestion"}}, false},
		{"mixed: Bash + Edit", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash", "Edit"}}, true},
		{"open tool, no names", &SessionMetrics{HasOpenToolCall: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.metrics.HasOpenEditPermissionTool(); got != tt.want {
				t.Errorf("HasOpenEditPermissionTool() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNeedsUserAttention_UserBlockingToolNames pins the user-blocking tool set
// this package matches. The canonical list is duplicated at
// tailer.isUserBlockingToolName (kept local there to avoid a domain-package
// import); TestIsUserBlockingToolName in that package is this test's twin and
// the two must be updated together.
func TestNeedsUserAttention_UserBlockingToolNames(t *testing.T) {
	tests := []struct {
		name    string
		metrics *SessionMetrics
		want    bool
	}{
		{"nil metrics", nil, false},
		{"no open tools", &SessionMetrics{HasOpenToolCall: false, LastOpenToolNames: []string{"AskUserQuestion"}}, false},
		{"open AskUserQuestion", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"AskUserQuestion"}}, true},
		{"open ExitPlanMode", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"ExitPlanMode"}}, true},
		{"open question", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"question"}}, true},
		// vibe names the same always-blocks-for-input tool in snake_case
		// (live 2.19.1 tools_available). Without this the session fell through
		// to the trailing-'?' text heuristic instead of forcing waiting (#1087).
		{"open ask_user_question (vibe)", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"ask_user_question"}}, true},
		{"mixed: Bash + ask_user_question", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash", "ask_user_question"}}, true},
		// Auto-executing tools must not trigger waiting.
		{"open Bash", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}}, false},
		{"open Write", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Write"}}, false},
		{"open tool, no names", &SessionMetrics{HasOpenToolCall: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.metrics.NeedsUserAttention(); got != tt.want {
				t.Errorf("NeedsUserAttention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeMetrics_ContextWindowUnknown_StickyAcrossTransientPasses(t *testing.T) {
	// Sticky-unknown invariant: once a TailAndProcess pass has decided the
	// model has no known context window, a subsequent pass that hasn't yet
	// recomputed (zero metrics, flag at zero value) must not flip the UI
	// signal off. Otherwise the tentative bar flickers on every poll.
	oldM := &SessionMetrics{
		TotalTokens:          1500,
		ModelName:            "openai/google/gemma-4-26b-a4b",
		PressureLevel:        "unknown",
		ContextWindow:        0,
		ContextWindowUnknown: true,
	}
	newM := &SessionMetrics{
		ModelName: "openai/google/gemma-4-26b-a4b",
		// ContextWindowUnknown left at zero value (false) — simulates a
		// pre-tokens pass.
	}
	got := MergeMetrics(newM, oldM)
	if !got.ContextWindowUnknown {
		t.Error("ContextWindowUnknown should remain true across a transient zero-tokens pass")
	}

	// Once the next pass produces a real window, the flag must clear.
	resolvedM := &SessionMetrics{
		ModelName:            "claude-sonnet-4-6",
		TotalTokens:          1500,
		ContextWindow:        200000,
		ContextUtilization:   0.75,
		PressureLevel:        "safe",
		ContextWindowUnknown: false,
	}
	got2 := MergeMetrics(resolvedM, oldM)
	if got2.ContextWindowUnknown {
		t.Error("ContextWindowUnknown should clear once a real ContextWindow is computed")
	}
}

func TestMergeMetrics_CumFields(t *testing.T) {
	oldM := &SessionMetrics{
		CumInputTokens:         1000,
		CumOutputTokens:        500,
		CumCacheReadTokens:     200,
		CumCacheCreationTokens: 100,
		EstimatedCostUSD:       0.05,
	}
	// newM has zero Cum* and zero cost (e.g. after MergeMetrics dropped them).
	newM := &SessionMetrics{
		TotalTokens: 1500,
		ModelName:   "claude-sonnet-4-6",
	}
	got := MergeMetrics(newM, oldM)

	if got.CumInputTokens != 1000 {
		t.Errorf("CumInputTokens = %d, want 1000", got.CumInputTokens)
	}
	if got.CumOutputTokens != 500 {
		t.Errorf("CumOutputTokens = %d, want 500", got.CumOutputTokens)
	}
	if got.CumCacheReadTokens != 200 {
		t.Errorf("CumCacheReadTokens = %d, want 200", got.CumCacheReadTokens)
	}
	if got.CumCacheCreationTokens != 100 {
		t.Errorf("CumCacheCreationTokens = %d, want 100", got.CumCacheCreationTokens)
	}
	if got.EstimatedCostUSD != 0.05 {
		t.Errorf("EstimatedCostUSD = %f, want 0.05", got.EstimatedCostUSD)
	}

	// When newM has non-zero Cum* they should win over old.
	newM2 := &SessionMetrics{
		CumInputTokens:         2000,
		CumOutputTokens:        800,
		CumCacheReadTokens:     300,
		CumCacheCreationTokens: 50,
		EstimatedCostUSD:       0.10,
	}
	got2 := MergeMetrics(newM2, oldM)
	if got2.CumInputTokens != 2000 {
		t.Errorf("CumInputTokens = %d, want 2000", got2.CumInputTokens)
	}
	if got2.EstimatedCostUSD != 0.10 {
		t.Errorf("EstimatedCostUSD = %f, want 0.10", got2.EstimatedCostUSD)
	}
}

// MergeMetrics task-list semantics: nil = "no data yet" (carry the old list),
// non-nil empty = "no tasks" (clear it). The tailer emits the empty slice
// after a prune-to-empty — without the distinction the stale pre-prune list
// was resurrected in the session record forever. See issue #615.
func TestMergeMetrics_Tasks_EmptyClearsStaleNilCarries(t *testing.T) {
	oldM := &SessionMetrics{Tasks: []Task{{ID: "5", Subject: "stale", Status: "pending"}}}

	carried := MergeMetrics(&SessionMetrics{Tasks: nil}, oldM)
	if len(carried.Tasks) != 1 || carried.Tasks[0].ID != "5" {
		t.Errorf("nil Tasks must carry the old list, got %+v", carried.Tasks)
	}

	cleared := MergeMetrics(&SessionMetrics{Tasks: []Task{}}, oldM)
	if cleared.Tasks == nil || len(cleared.Tasks) != 0 {
		t.Errorf("empty Tasks must clear the old list, got %+v", cleared.Tasks)
	}
}
