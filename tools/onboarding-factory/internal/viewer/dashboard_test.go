package viewer

import (
	"strings"
	"testing"
)

// TestInjectBeforeClosingTag covers the robust replacement for the old
// brittle exact-string </head> splice (issue #461 finding #4): the
// diagnostic script must land before </head> regardless of tag casing,
// fall back to </body>, and never be silently dropped when neither exists.
func TestInjectBeforeClosingTag(t *testing.T) {
	const script = "<!--SCRIPT-->"

	cases := []struct {
		name       string
		html       string
		mustBefore string // substring the script must precede; "" means appended at end
	}{
		{"lowercase head", "<head><title>x</title></head><body></body>", "</head>"},
		{"uppercase head", "<HEAD></HEAD><BODY></BODY>", "</HEAD>"},
		{"mixed case head", "<head></Head><body></body>", "</Head>"},
		{"no head falls back to body", "<body>hi</body>", "</body>"},
		{"neither tag appends", "<p>no closing tags</p>", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := injectBeforeClosingTag(tc.html, script)
			if !strings.Contains(out, script) {
				t.Fatalf("script not injected: %q", out)
			}
			if tc.mustBefore == "" {
				if !strings.HasSuffix(out, script) {
					t.Errorf("expected script appended at end, got %q", out)
				}
				return
			}
			si := strings.Index(out, script)
			ti := strings.Index(out, tc.mustBefore)
			if si < 0 || ti < 0 || si > ti {
				t.Errorf("script (at %d) must precede %q (at %d): %q", si, tc.mustBefore, ti, out)
			}
		})
	}
}
