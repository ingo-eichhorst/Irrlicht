package session

import "testing"

func TestExtractStatusFallback_Empty(t *testing.T) {
	if got := ExtractStatusFallback(""); got != "" {
		t.Errorf("ExtractStatusFallback(\"\") = %q, want empty", got)
	}
}

func TestExtractStatusFallback_PrefersHighSignalSentence(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			"state verb beats plain lead sentence",
			"Here's some context about the session. Fixed the merge conflict and tests are green now.",
			"Fixed the merge conflict and tests are green now.",
		},
		{
			"issue reference beats plain lead sentence",
			"Just poking around the repo. Two open follow-ups remain in #905 and #906.",
			"Two open follow-ups remain in #905 and #906.",
		},
		{
			"file path beats plain lead sentence",
			"Let me look at this. The bug is in core/domain/session/metrics.go near the fallback.",
			"The bug is in core/domain/session/metrics.go near the fallback.",
		},
		{
			"either/or framing beats plain lead sentence",
			"Just wrapping up now. We can either ship this today or wait for review first.",
			"We can either ship this today or wait for review first.",
		},
		{
			"no signal anywhere falls back to first line",
			"Let me know how you want to proceed.\nSecond line should be ignored.",
			"Let me know how you want to proceed.",
		},
		{
			"tie between two signals favors the later sentence",
			"Fixed the first bug in a.go. Fixed the second bug in b.go.",
			"Fixed the second bug in b.go.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractStatusFallback(c.text); got != c.want {
				t.Errorf("ExtractStatusFallback(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}
