package hookjson

import (
	"regexp"
	"strings"
	"testing"
)

func TestEventList(t *testing.T) {
	tests := []struct {
		name   string
		events []string
		want   string
	}{
		{"empty", nil, ""},
		{"one", []string{"Stop"}, "Stop"},
		{"two", []string{"PostToolUse", "Stop"}, "PostToolUse and Stop"},
		{"three", []string{"PermissionRequest", "PostToolUse", "Stop"},
			"PermissionRequest, PostToolUse, and Stop"},
		{"many", []string{"A", "B", "C", "D"}, "A, B, C, and D"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EventList(tt.events); got != tt.want {
				t.Errorf("EventList(%v) = %q, want %q", tt.events, got, tt.want)
			}
		})
	}
}

func TestRequiresVersion(t *testing.T) {
	got := RequiresVersion("Claude Code", "2.1.122")
	for _, want := range []string{"Claude Code", "2.1.122", "nothing is written"} {
		if !strings.Contains(got, want) {
			t.Errorf("RequiresVersion() = %q, missing %q", got, want)
		}
	}
	if RequiresVersion("Claude Code", "") != "" {
		t.Error("RequiresVersion rendered a sentence for an undeclared floor")
	}
}

// TestRequiresVersion_DoesNotTripTheDisclosureContract guards a real collision
// between the two #1356/#1365 mechanisms. AssertHookDisclosureMatchesInstalled
// scans the same copy for CamelCase event-shaped tokens and for "N hook
// entries" counts, so a version sentence worded slightly differently — "Adds 1
// hook entry on ClaudeCode 2.1.122+" — would fail an adapter's disclosure test
// for a reason having nothing to do with its install.
func TestRequiresVersion_DoesNotTripTheDisclosureContract(t *testing.T) {
	s := RequiresVersion("Claude Code", "2.1.122")
	if regexp.MustCompile(`(\d+) hook entr(?:y|ies)`).MatchString(s) {
		t.Errorf("version sentence %q states a hook-entry count; it would be read as the "+
			"install's count and contradict the real one", s)
	}
	for _, w := range regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*`).FindAllString(s, -1) {
		if regexp.MustCompile(`^[A-Z][a-z]+(?:[A-Z][a-z]+)+$`).MatchString(w) {
			t.Errorf("version sentence contains %q, which reads as a hook event name and "+
				"would be reported as an undisclosed over-promise", w)
		}
	}
}
