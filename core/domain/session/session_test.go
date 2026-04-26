package session

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIsWaitingForUserInput_TrailingMarkdown(t *testing.T) {
	// Models routinely wrap questions in markdown; the literal last
	// byte is often a delimiter, not '?'. Pin that the classifier
	// strips trailing markdown noise before the check.
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"plain", "What now?", true},
		{"trailing whitespace", "What now?   \n", true},
		{"bold", "**What now?**", true},
		{"italic asterisk", "*What now?*", true},
		{"italic underscore", "_What now?_", true},
		{"strikethrough", "~~What now?~~", true},
		{"inline code", "`What now?`", true},
		{"quoted", "\"What now?\"", true},
		{"single-quoted", "'What now?'", true},
		{"mixed bold + whitespace", "**What now?**\n", true},
		{"production gemma case (asterisks)", "Are there any conventions you follow?**", true},
		{"parenthetical", "Is this true (yes/no)?)", true},
		{"bracketed citation", "Did you mean foo?]", true},
		{"statement", "I am done.", false},
		{"declarative ending in *", "**Done**", false},
		{"empty", "", false},
		{"only delimiters", "***", false},
		// Mid-paragraph questions — issue #236.
		{"mid-paragraph question with trailing status", "What would you like? In the meantime I'll move step 7 to blocked.", true},
		{"question on first line, status after newline", "Do you want me to refactor?\nLet me know.", true},
		{"two questions, both detected", "What's first? Or what's second?", true},
		{"URL with ? not a question", "See https://example.com/?foo=bar for details.", false},
		{"abbreviation e.g. is not a question", "Use a fixture, e.g. small.json. The tests pass.", false},
		// Rhetorical Q&A — agent answers itself, not waiting on user.
		{"joke with Because answer", "Why do programmers prefer dark mode? Because light attracts bugs.", false},
		{"joke with Because answer across newline", "Why do programmers prefer dark mode?\nBecause light attracts bugs.", false},
		{"Since-prefixed answer is rhetorical", "Why bother? Since the cache already has it, we skip.", false},
		{"rhetorical Q followed by real Q", "Why? Because reasons. Should I proceed?", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &SessionMetrics{LastAssistantText: c.text}
			if got := m.IsWaitingForUserInput(); got != c.want {
				t.Errorf("text=%q: got %v, want %v", c.text, got, c.want)
			}
		})
	}
}

func TestExtractQuestionSnippet(t *testing.T) {
	// Issue #236: when a question is detected, the rendered snippet should be
	// the question sentence — not the full surrounding paragraph.
	cases := []struct {
		name string
		text string
		want string
	}{
		{"plain", "What now?", "What now?"},
		{"mid-paragraph trims trailing status", "What would you like? In the meantime I'll move step 7 to blocked and not touch your daemon.", "What would you like?"},
		{"multi-question keeps first (top-level question over bullet options)", "What's first? Or what's second?", "What's first?"},
		{"bullet options after lead question keep lead", "what would you like me to test? For example:\n- run tests?\n- something else?", "what would you like me to test?"},
		{"newline-separated", "Looking at the code.\nDo you want me to refactor?\nLet me know.", "Do you want me to refactor?"},
		{"bold-wrapped preserved", "**What now?**", "**What now?**"},
		{"trailing whitespace stripped", "What now?   \n", "What now?"},
		{"leading ellipsis from truncation", "…end of context. Hello, what's next?", "Hello, what's next?"},
		{"no question returns empty", "Done. The tests pass.", ""},
		{"empty input", "", ""},
		{"only punctuation", "***", ""},
		// Rhetorical Q&A is skipped — the agent isn't waiting on the user.
		{"joke with Because answer returns empty", "Why do programmers prefer dark mode? Because light attracts bugs.", ""},
		{"joke with Because across newline returns empty", "Why do programmers prefer dark mode?\nBecause light attracts bugs.", ""},
		{"rhetorical Q followed by real Q returns the real one", "Why? Because reasons. Should I proceed?", "Should I proceed?"},
		{"non-answer continuation is not rhetorical", "What would you like? In the meantime I'll move on.", "What would you like?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractQuestionSnippet(c.text); got != c.want {
				t.Errorf("text=%q: got %q, want %q", c.text, got, c.want)
			}
		})
	}
}

func TestIsStale(t *testing.T) {
	now := time.Now().Unix()

	tests := []struct {
		name      string
		updatedAt int64
		maxAge    time.Duration
		want      bool
	}{
		{"fresh session", now - 60, 5 * 24 * time.Hour, false},
		{"stale session", now - 6*24*60*60, 5 * 24 * time.Hour, true},
		{"exactly at boundary", now - 5*24*60*60 - 1, 5 * 24 * time.Hour, true},
		{"zero maxAge disables", now - 999*24*60*60, 0, false},
		{"negative maxAge disables", now - 999*24*60*60, -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &SessionState{UpdatedAt: tt.updatedAt}
			if got := s.IsStale(tt.maxAge); got != tt.want {
				t.Errorf("IsStale(%v) = %v, want %v", tt.maxAge, got, tt.want)
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

func TestSessionState_LauncherJSONRoundTrip(t *testing.T) {
	// With Launcher present.
	in := &SessionState{
		SessionID: "abc",
		State:     StateWorking,
		PID:       1234,
		Launcher: &Launcher{
			TermProgram:    "iTerm.app",
			ITermSessionID: "w0t0p0",
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SessionState
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Launcher == nil {
		t.Fatal("Launcher lost in round-trip")
	}
	if out.Launcher.TermProgram != "iTerm.app" || out.Launcher.ITermSessionID != "w0t0p0" {
		t.Errorf("launcher round-trip mismatch: %+v", out.Launcher)
	}

	// Without Launcher — backwards compat with pre-170 session JSON files.
	legacy := []byte(`{"session_id":"xyz","state":"ready","pid":99}`)
	var legacyOut SessionState
	if err := json.Unmarshal(legacy, &legacyOut); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacyOut.Launcher != nil {
		t.Errorf("legacy session should have nil Launcher, got %+v", legacyOut.Launcher)
	}
}
