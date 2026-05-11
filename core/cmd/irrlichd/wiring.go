package main

import (
	"fmt"
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

// buildAgentWatchers dispatches on the Agent's Source variant and constructs
// the appropriate transcript watchers + process scanners for that adapter.
//
// FilesUnderRoot adapters (claudecode, codex, pi) get both:
//   - an fswatcher rooted at the variant's Dir
//   - a process scanner keyed on ProcessName/CommandPattern
//
// FilesUnderCWD adapters (aider) get only a process scanner, configured with
// the variant's Filename so the scanner emits transcript_new events on first
// appearance of the per-process file inside CWD.
//
// ProcessOwnedStore adapters (opencode) get only a process scanner. Their
// dedicated DB-aware watcher is constructed separately by main.go (e.g.
// opencode.New). The fswatcher that pre-A.2 code mistakenly created for these
// adapters (rooted at the DB directory) emitted no useful transcript events
// and is no longer constructed — drop is behavior-preserving in effect.
//
// Returns the list of watchers and a human-readable label for the startup
// log line (matches the pre-A.2 "<adapter> (<root>)" / "<adapter>-db (<root>)"
// format).
func buildAgentWatchers(
	a agent.Agent,
	maxSessionAge time.Duration,
	sessionChecker func(projectDir string, pid int) bool,
) ([]inbound.AgentWatcher, []string) {
	var (
		watchers []inbound.AgentWatcher
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
// ExactName matchers, that's the matcher's Name. For CommandPattern
// matchers (aider), no reliable name exists — aider runs under python —
// so we fall back to the Identity.Name, mirroring the historical value
// the legacy agents.Config.ProcessName carried for that adapter. The
// scanner's WithCommandLineMatch path bypasses pgrep -x and matches the
// full command line instead, so the fallback name is never actually
// matched against running processes.
func processNameFor(a agent.Agent) string {
	if e, ok := a.Process.Match.(agent.ExactName); ok {
		return e.Name
	}
	return a.Identity.Name
}
