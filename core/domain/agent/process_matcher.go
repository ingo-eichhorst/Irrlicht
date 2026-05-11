package agent

import "regexp"

// ProcessMatcher is a sealed sum: only types in this package satisfy it.
// Adapters pick the variant that matches their OS-level binary layout.
//
// Phase C will add HostedByEditor for adapters that live inside a shared
// editor renderer process (Cline, Roo, Continue, Cody, Augment, Copilot,
// PearAI, JetBrains AI). Adding new variants is a non-breaking change.
type ProcessMatcher interface {
	isProcessMatcher()
}

// ExactName matches via `pgrep -x Name`. Use for stable, unique binary
// names: claude, codex, pi, opencode, goose, crush, zed.
type ExactName struct {
	Name string
}

func (ExactName) isProcessMatcher() {}

// CommandPattern matches via `pgrep -f Regex` — the full command line is
// matched, not just argv[0]. Use when the OS process name does not match
// the agent CLI name — e.g. python tools where argv[0]="python" and the
// agent script lives in argv[1]. The leading "/" in a pattern like
// "/aider($| )" anchors to the binary path so wrapper scripts (tmux, sh)
// that merely mention the agent name in their own argv don't get matched.
type CommandPattern struct {
	Regex *regexp.Regexp
}

func (CommandPattern) isProcessMatcher() {}
