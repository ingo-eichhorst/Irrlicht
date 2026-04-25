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
		{"statement", "I am done.", false},
		{"empty", "", false},
		{"only delimiters", "***", false},
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
