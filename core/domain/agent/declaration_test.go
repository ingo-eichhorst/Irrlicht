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
