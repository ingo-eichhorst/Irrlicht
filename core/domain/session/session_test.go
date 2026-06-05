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

func TestExtractWaitingCue(t *testing.T) {
	// Issue #381: agents often end a turn with an imperative or implicit
	// cue rather than a literal `?`. ExtractWaitingCue covers that gap.
	// Each case mirrors a row in the coverage matrix.
	cases := []struct {
		name string
		text string
		want bool // true = a cue is detected
	}{
		// 24 of the 30 matrix rows from issue #381; the remaining 6 are
		// `?`-bearing and stay the responsibility of ExtractQuestionSnippet
		// (covered by TestExtractQuestionSnippet).
		// Cases 1 and 2 quote the issue verbatim — these are the original
		// reported regressions and are the most load-bearing assertions.
		{"1 verbatim issue example", "PR #379 is marked draft. Take a look at the icon and let me know if it's right or needs tweaking before I commit and push phase 2.", true},
		{"2 verbatim issue example", "Bumped settings frame from 560pt → 640pt to accommodate the new toggle section + About footer. Restart done — try the Settings menu again and confirm the Done button is visible.", true},
		{"5 ready for your review", "Pushed PR #379 as draft. Ready for your review.", true},
		{"6 awaiting + before I", "Awaiting your go-ahead before I merge.", true},
		{"7 ping me", "Ping me when you've tested it.", true},
		{"8 your call", "Two options — A) revert, B) re-roll. Your call.", true},
		{"11 let me know", "Let me know when you're back online.", true},
		{"12 confirm the migration", "Confirm the migration ran.", true},
		{"14 holler", "Holler if anything looks off.", true},
		{"15 sign off", "Sign off on the diff when you can.", true},
		{"16 approve the staging", "Approve the staging deploy in #ops to continue.", true},
		{"17 need your input", "Need your input on whether to keep the fallback.", true},
		{"18 awaiting confirmation", "Awaiting confirmation that the cert installed.", true},
		{"19 drop the API key", "Drop the API key in .env and I'll re-run.", true},
		{"20 once you've reviewed", "Once you've reviewed, ship it.", true},
		{"21 I'll wait", "I'll wait for your green light before pushing.", true},
		{"23 lmk", "Lmk if you'd rather I split the PR.", true},
		{"24 tell me", "Tell me which approach you prefer.", true},
		{"25 please review", "Please review the diff.", true},
		{"29 stop me if", "Heads up — about to drop the table. Stop me if that's wrong.", true},
		{"30 verify locally + reply with", "Verify locally and reply with the diff output.", true},
		// The `?`-bearing rows in the matrix — verified here too because
		// IsWaitingForUserInput ORs both detectors; the cue detector
		// should be permissive enough that several still match independently.
		{"4 wdyt", "WDYT", true},
		{"22 thoughts trailing", "All good. Thoughts", true},
		{"27 any feedback", "Any feedback before I merge", true},

		// Negative cases — must NOT trigger the cue detector.
		{"neg done tests pass", "Done. The tests pass.", false},
		{"neg all green pushed", "All green. Pushed to main.", false},
		{"neg confirmed past tense", "Confirmed: the migration ran cleanly.", false},
		{"neg URL with ?", "See https://example.com/?foo=bar for details.", false},
		{"neg test failures substring", "Use a fixture, e.g. small.json. The tests pass.", false},
		{"neg empty", "", false},
		{"neg statement", "I am done.", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractWaitingCue(c.text) != ""
			if got != c.want {
				t.Errorf("ExtractWaitingCue(%q) detected=%v, want %v (snippet=%q)", c.text, got, c.want, ExtractWaitingCue(c.text))
			}
		})
	}
}

func TestIsWaitingForUserInput_ImperativeCues(t *testing.T) {
	// Public-API assertion: with the cue detector OR'd into the question
	// detector, turns that end with an imperative gate now register as
	// waiting. Sample the most representative shapes from issue #381 so a
	// future regression at either layer surfaces here too.
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"take a look + let me know", "Take a look at the icon and let me know if it's right.", true},
		{"ready for your review", "Pushed PR #379 as draft. Ready for your review.", true},
		{"your call", "Two options — A) revert, B) re-roll. Your call.", true},
		{"awaiting + before I", "Awaiting your go-ahead before I merge.", true},
		{"once you've reviewed", "Once you've reviewed, ship it.", true},
		{"please review", "Please review the diff.", true},
		{"stop me if", "Heads up — about to drop the table. Stop me if that's wrong.", true},

		// Done-state regression guards — must stay false.
		{"done tests pass stays ready", "Done. The tests pass.", false},
		{"all green pushed stays ready", "All green. Pushed to main.", false},
		{"confirmed past tense stays ready", "Confirmed: the migration ran cleanly.", false},
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
		// Tools that can legitimately run long must NOT qualify — duration
		// can't distinguish "blocked on prompt" from "executing" for them.
		{"open Bash", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Bash"}}, false},
		{"open WebFetch", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"WebFetch"}}, false},
		{"open Read", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"Read"}}, false},
		{"open mcp tool", &SessionMetrics{HasOpenToolCall: true, LastOpenToolNames: []string{"mcp__server__do"}}, false},
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
