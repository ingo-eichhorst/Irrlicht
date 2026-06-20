package main

import (
	"fmt"
	"runtime"
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
	"irrlicht/core/adapters/inbound/agents/opencode"
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
// inside CWD. ProcessOwnedStore adapters get a scanner plus their
// dedicated store watcher (OpenCode's SQLite poller — panics loudly for
// any other ProcessOwnedStore adapter, which must wire its watcher here;
// grant-all daemons exercise every factory at boot, so CI catches it).
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

	// Source-specific watcher. FilesUnderRoot gets an fswatcher rooted at its
	// Dir; ProcessOwnedStore gets its dedicated store watcher; FilesUnderCWD
	// gets none here — its transcript is discovered by the process scanner
	// below, configured with the variant's Filename. A new Source variant that
	// reaches the default fails loudly rather than silently running
	// scanner-only (grant-all daemons exercise every factory at boot, so CI
	// catches it).
	switch s := a.Source.(type) {
	case agent.FilesUnderRoot:
		// One fswatcher per root: most adapters watch a single Dir; an adapter
		// whose sessions span sibling stores (Antigravity's CLI + IDE brain
		// dirs) declares ExtraDirs and gets one watcher each. SessionIDFromPath,
		// when set, overrides the default filename-stem session-ID derivation
		// (Antigravity's constant transcript.jsonl filename).
		for _, root := range s.AllRootsFor(runtime.GOOS) {
			w := fswatcher.New(root, a.Identity.Name, maxSessionAge).WithIdentity(a.Identity)
			if s.SessionIDFromPath != nil {
				w = w.WithSessionID(s.SessionIDFromPath)
			}
			watchers = append(watchers, w)
			labels = append(labels, fmt.Sprintf("%s (%s)", a.Identity.Name, w.Root()))
		}
	case agent.ProcessOwnedStore:
		if a.Identity.Name != opencode.AdapterName {
			panic(fmt.Sprintf("buildAgentWatchers: no store watcher wired for ProcessOwnedStore adapter %q — add its construction here", a.Identity.Name))
		}
		w := opencode.New(maxSessionAge).WithIdentity(a.Identity)
		watchers = append(watchers, w)
		labels = append(labels, fmt.Sprintf("%s-db (%s)", a.Identity.Name, w.Root()))
	case agent.FilesUnderCWD:
		// Scanner-only (configured below); no dedicated file watcher.
	default:
		panic(fmt.Sprintf("buildAgentWatchers: unhandled agent.Source variant %T for adapter %q — add its watcher construction here", a.Source, a.Identity.Name))
	}

	scanner := processlifecycle.NewScanner(processNameFor(a), a.Identity.Name, 0).WithIdentity(a.Identity)
	if m, ok := a.Process.Match.(agent.CommandPattern); ok {
		scanner.WithCommandLineMatch(m.Regex.String())
	}
	if a.Process.ExcludeArgv != nil {
		scanner.WithArgvFilter(a.Process.ExcludeArgv)
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
