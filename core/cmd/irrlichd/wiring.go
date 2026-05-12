package main

import (
	"fmt"
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

// buildAgentWatchers dispatches on the Agent's Source variant and
// constructs the appropriate transcript watchers + process scanners.
//
// FilesUnderRoot adapters get both an fswatcher rooted at the variant's
// Dir and a process scanner keyed on the matcher. FilesUnderCWD adapters
// get only a scanner, configured with the variant's Filename so it emits
// transcript_new events on first appearance of the per-process file
// inside CWD. ProcessOwnedStore adapters get only a scanner; their
// dedicated DB-aware watcher is constructed separately by main.go.
//
// Returns the list of watchers and a human-readable label for the
// startup log line ("<adapter> (<root>)").
func buildAgentWatchers(
	a agent.Agent,
	maxSessionAge time.Duration,
	sessionChecker func(projectDir string, pid int) bool,
) ([]inbound.Watcher, []string) {
	var (
		watchers []inbound.Watcher
		labels   []string
	)

	if s, ok := a.Source.(agent.FilesUnderRoot); ok {
		w := fswatcher.New(s.Dir, a.Identity.Name, maxSessionAge).WithIdentity(a.Identity)
		watchers = append(watchers, w)
		labels = append(labels, fmt.Sprintf("%s (%s)", a.Identity.Name, w.Root()))
	}

	scanner := processlifecycle.NewScanner(processNameFor(a), a.Identity.Name, 0).WithIdentity(a.Identity)
	if m, ok := a.Process.Match.(agent.CommandPattern); ok {
		scanner.WithCommandLineMatch(m.Regex.String())
	}
	if s, ok := a.Source.(agent.FilesUnderCWD); ok {
		scanner.WithTranscriptFilename(s.Filename)
	}
	scanner.WithSessionChecker(sessionChecker)
	watchers = append(watchers, scanner)

	return watchers, labels
}

// processNameFor returns the OS process name suitable for pgrep -x. For
// ExactName matchers it's the matcher's Name. For CommandPattern
// matchers (e.g. aider runs under python) no reliable name exists, so
// we fall back to Identity.Name. The scanner's WithCommandLineMatch
// path bypasses pgrep -x in that case, so the fallback name is never
// actually matched against running processes.
func processNameFor(a agent.Agent) string {
	if e, ok := a.Process.Match.(agent.ExactName); ok {
		return e.Name
	}
	return a.Identity.Name
}
