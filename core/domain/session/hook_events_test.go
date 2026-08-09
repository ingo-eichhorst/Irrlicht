package session

import (
	"os"
	"regexp"
	"testing"
)

// hookConstPattern matches one tab-indented `HookXxx = "Value"` const
// declaration in hook_signal.go. It deliberately does not match the
// AllHookEvents var's entries (no `=`, no string literal) nor AllHookEvents
// itself (no word boundary before "Hook" in "AllHookEvents").
var hookConstPattern = regexp.MustCompile(`(?m)^\tHook\w* += +"([^"]+)"`)

// TestAllHookEvents_CoversEveryConstant is the guard that keeps AllHookEvents
// from becoming the very thing issue #1356 was about: a hand-maintained
// restatement of a list, drifting silently from it. AllHookEvents is the
// universe contracttesting.AssertHookDisclosureMatchesInstalled checks an
// adapter's consent copy against, so a constant missing from it weakens that
// contract rather than breaking anything loudly.
//
// Reading the package's own source is the only mechanical route: constants are
// erased at compile time, so there is nothing to reflect over.
func TestAllHookEvents_CoversEveryConstant(t *testing.T) {
	src, err := os.ReadFile("hook_signal.go")
	if err != nil {
		t.Fatalf("read hook_signal.go: %v", err)
	}
	matches := hookConstPattern.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no Hook* constants found in hook_signal.go — the scan pattern has drifted from the source")
	}

	listed := make(map[string]bool, len(AllHookEvents))
	for _, e := range AllHookEvents {
		listed[e] = true
	}

	declared := make(map[string]bool, len(matches))
	for _, m := range matches {
		declared[m[1]] = true
		if !listed[m[1]] {
			t.Errorf("hook event %q is declared as a constant but missing from AllHookEvents", m[1])
		}
	}
	for _, e := range AllHookEvents {
		if !declared[e] {
			t.Errorf("AllHookEvents lists %q, which no Hook* constant declares", e)
		}
	}
}
