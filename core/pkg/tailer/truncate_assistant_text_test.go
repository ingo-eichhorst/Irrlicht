package tailer

import (
	"strings"
	"testing"
)

func TestTruncateAssistantText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"short unchanged", "waiting for input?", "waiting for input?"},
		{"trims surrounding whitespace", "  hi there \n", "hi there"},
		{"exactly max, no ellipsis", strings.Repeat("x", MaxAssistantTextRunes), strings.Repeat("x", MaxAssistantTextRunes)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateAssistantText(tc.in); got != tc.want {
				t.Errorf("TruncateAssistantText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateAssistantText_LongKeepsTailWithEllipsis(t *testing.T) {
	// The head must be dropped and the tail kept: a trailing "?" is what
	// waiting-state detection reads, so it has to survive truncation.
	long := strings.Repeat("a", 50) + " " + strings.Repeat("b", 300) + "?"
	got := TruncateAssistantText(long)

	if !strings.HasPrefix(got, "…") {
		t.Errorf("want leading ellipsis, got %q…", string([]rune(got)[:10]))
	}
	if !strings.HasSuffix(got, "?") {
		t.Error("trailing question mark dropped — tail not preserved")
	}
	if strings.Contains(got, "a") {
		t.Error("head 'a' run leaked through — truncation kept the head, not the tail")
	}
	if n := len([]rune(got)); n != MaxAssistantTextRunes+1 {
		t.Errorf("rune count = %d, want %d (ellipsis + %d runes)", n, MaxAssistantTextRunes+1, MaxAssistantTextRunes)
	}
}

func TestTruncateAssistantText_CountsRunesNotBytes(t *testing.T) {
	// Multi-byte runes are counted as runes, not bytes.
	got := TruncateAssistantText(strings.Repeat("é", MaxAssistantTextRunes+50))
	if n := len([]rune(got)); n != MaxAssistantTextRunes+1 {
		t.Errorf("rune count = %d, want %d", n, MaxAssistantTextRunes+1)
	}
}
