package backchannel

import "testing"

func TestDetectUI(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   UIKind
	}{
		{"empty", "", UIKindNone},
		{
			"claude permission prompt",
			"Bash(rm -rf build)\n\n Do you want to proceed?\n ❯ 1. Yes\n   2. No",
			UIKindTrustDialog,
		},
		{
			"folder trust on launch",
			"Do you trust the files in this folder?\n\n /Users/x/project",
			UIKindTrustDialog,
		},
		{
			"tool allow prompt",
			"Do you want to allow this tool to run?",
			UIKindTrustDialog,
		},
		{
			"vibe permission dialog title",
			"┌─ Permission for the bash tool (execute) ─┐\n│ Allow once                                │",
			UIKindTrustDialog,
		},
		{
			"vibe permission dialog remainder option",
			"│ Allow for remainder of this session       │\n│ Always allow                              │",
			UIKindTrustDialog,
		},
		{
			"ordinary output is not a dialog",
			"Running tests...\n ok  irrlicht/core/...  0.42s\n>",
			UIKindNone,
		},
		{
			"plain question is not a trust dialog",
			"What would you like me to work on next?",
			UIKindNone,
		},
	}
	for _, c := range cases {
		if got := DetectUI(c.screen); got != c.want {
			t.Errorf("%s: DetectUI = %q, want %q", c.name, got, c.want)
		}
	}
}
