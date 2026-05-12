package agent

import "regexp"

// ProcessMatcher is a sealed sum: only types in this package satisfy it.
// Adapters pick the variant that matches their OS-level binary layout.
type ProcessMatcher interface {
	isProcessMatcher()
}

// ExactName matches via `pgrep -x Name`. Use for stable, unique binary
// names.
type ExactName struct {
	Name string
}

func (ExactName) isProcessMatcher() {}

// CommandPattern matches via `pgrep -f Regex` — the full command line is
// matched, not just argv[0]. Use when the OS process name does not match
// the agent CLI name (e.g. a Python tool where argv[0]="python" and the
// agent script lives in argv[1]). The pattern is responsible for
// anchoring to the binary path to avoid wrapper-script false positives.
type CommandPattern struct {
	Regex *regexp.Regexp
}

func (CommandPattern) isProcessMatcher() {}
