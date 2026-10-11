package agent

import (
	"regexp"
	"testing"
)

// These tests assert compile-time that each declared variant satisfies its
// sealed sum interface. The sealed-sum guarantee — that only types in this
// package can implement ProcessMatcher / Source / FileParser — is enforced
// by the unexported marker methods (isProcessMatcher / isSource /
// isFileParser). A consumer in another package that tried to declare its
// own variant would fail to compile because it can't satisfy the
// unexported marker.

func TestProcessMatcherSatisfaction(t *testing.T) {
	var _ ProcessMatcher = ExactName{Name: "claude"}
	var _ ProcessMatcher = CommandPattern{Regex: regexp.MustCompile("/aider")}
}

func TestSourceSatisfaction(t *testing.T) {
	var _ Source = FilesUnderRoot{}
	var _ Source = FilesUnderCWD{}
	var _ Source = ProcessOwnedStore{}
}

func TestFileParserSatisfaction(t *testing.T) {
	var _ FileParser = JSONLineParser{}
	var _ FileParser = RawLineParser{}
}

// TestAgentZeroValue confirms an Agent{} zero-value compiles; useful for
// adapter authors who construct an Agent literal step-by-step.
func TestAgentZeroValue(t *testing.T) {
	var a Agent
	if a.Identity.Name != "" {
		t.Fatalf("zero-value Identity.Name should be empty, got %q", a.Identity.Name)
	}
	if a.Process.Match != nil {
		t.Fatalf("zero-value Process.Match should be nil interface")
	}
	if a.Source != nil {
		t.Fatalf("zero-value Source should be nil interface")
	}
}

// Issue #2082: the scanner mints no pre-session for a process that either
// ExcludeArgv rejects or SessionHostArgv recognizes; a nil predicate matches
// nothing.
func TestProcessSkipsPreSession(t *testing.T) {
	is := func(word string) func([]string) bool {
		return func(argv []string) bool { return len(argv) > 1 && argv[1] == word }
	}
	for _, tc := range []struct {
		name string
		p    Process
		argv []string
		want bool
	}{
		{"neither declared", Process{}, []string{"codex", "app-server"}, false},
		{"excluded", Process{ExcludeArgv: is("daemon")}, []string{"claude", "daemon"}, true},
		{"session host", Process{SessionHostArgv: is("app-server")}, []string{"codex", "app-server"}, true},
		{"both declared, host matches", Process{ExcludeArgv: is("daemon"), SessionHostArgv: is("app-server")}, []string{"codex", "app-server"}, true},
		{"both declared, neither matches", Process{ExcludeArgv: is("daemon"), SessionHostArgv: is("app-server")}, []string{"codex", "--yolo"}, false},
	} {
		if got := tc.p.SkipsPreSession(tc.argv); got != tc.want {
			t.Errorf("%s: SkipsPreSession(%q) = %t, want %t", tc.name, tc.argv, got, tc.want)
		}
	}
}
