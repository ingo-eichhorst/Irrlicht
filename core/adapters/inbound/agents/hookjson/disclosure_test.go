package hookjson

import "testing"

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
