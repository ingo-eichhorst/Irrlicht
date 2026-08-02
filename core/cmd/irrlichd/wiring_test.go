package main

import (
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/fswatcher"
	"irrlicht/core/adapters/inbound/agents/hermes"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/inbound"
)

// countWatcherKinds classifies watchers by concrete type — a process scanner,
// an fswatcher (FilesUnderRoot), or a per-adapter store watcher
// (ProcessOwnedStore: opencode, hermes) — failing the test on any unexpected
// type.
func countWatcherKinds(t *testing.T, name string, watchers []inbound.Watcher) (scanners, fswatchers, stores int) {
	t.Helper()
	for _, w := range watchers {
		switch fsw := w.(type) {
		case *processlifecycle.Scanner:
			scanners++
		case *fswatcher.Watcher:
			fswatchers++
			// Every fswatcher must carry the daemon's logger, or a failed
			// watch registration goes back to being discarded silently
			// (#1255). Nothing else can catch that: Watcher.logger is
			// unexported, so dropping the WithLogger call in wiring.go
			// otherwise leaves the whole suite green.
			if fsw.Logger() == nil {
				t.Errorf("%s: fswatcher for %s has no logger — wiring.go must pass one via WithLogger", name, fsw.Root())
			}
		case *opencode.Watcher:
			stores++
		case *hermes.Watcher:
			stores++
		default:
			t.Errorf("%s: unexpected watcher type %T", name, w)
		}
	}
	return scanners, fswatchers, stores
}

// watcherCounts bundles the fswatcher/store counts assertWatcherCountsForSource
// checks against a Source variant, keeping the parameter list within
// CodeScene's argument-count threshold (S107) instead of passing each count
// positionally.
type watcherCounts struct {
	FS    int
	Store int
}

// assertWatcherCountsForSource checks the fswatcher/store counts match the
// adapter's Source variant: one fswatcher per root (FilesUnderRoot, plus any
// ExtraDirs), exactly one store watcher (ProcessOwnedStore), or neither
// (FilesUnderCWD, scanner-only).
func assertWatcherCountsForSource(t *testing.T, name string, source agent.Source, counts watcherCounts) {
	t.Helper()
	switch s := source.(type) {
	case agent.FilesUnderRoot:
		// One fswatcher per root: the primary Dir plus any ExtraDirs
		// (Antigravity watches both the CLI and IDE brain stores).
		wantFs := 1 + len(s.ExtraDirs)
		if counts.FS != wantFs || counts.Store != 0 {
			t.Errorf("%s (FilesUnderRoot): fswatchers=%d stores=%d, want %d/0", name, counts.FS, counts.Store, wantFs)
		}
	case agent.ProcessOwnedStore:
		if counts.Store != 1 || counts.FS != 0 {
			t.Errorf("%s (ProcessOwnedStore): stores=%d fswatchers=%d, want 1/0", name, counts.Store, counts.FS)
		}
	case agent.FilesUnderCWD:
		if counts.FS != 0 || counts.Store != 0 {
			t.Errorf("%s (FilesUnderCWD): want scanner-only, got fswatchers=%d stores=%d", name, counts.FS, counts.Store)
		}
	default:
		t.Fatalf("%s: unhandled Source variant %T — update this test", name, source)
	}
}

// assertAgentWatcherSet verifies buildAgentWatchers for one adapter: exactly
// one process scanner, plus the Source variant's dedicated watcher.
func assertAgentWatcherSet(t *testing.T, a agent.Agent, noCheck func(string, int) bool) {
	t.Helper()
	watchers, _ := buildAgentWatchers(a, time.Minute, noCheck, e2eLog{})
	scanners, fswatchers, stores := countWatcherKinds(t, a.Identity.Name, watchers)
	if scanners != 1 {
		t.Errorf("%s: got %d process scanners, want 1", a.Identity.Name, scanners)
	}
	assertWatcherCountsForSource(t, a.Identity.Name, a.Source, watcherCounts{FS: fswatchers, Store: stores})
}

// buildAgentWatchers must dispatch every registered adapter's Source variant
// without panicking and build the right watcher set: exactly one process
// scanner for every adapter, plus the variant's dedicated watcher — an
// fswatcher for FilesUnderRoot, the adapter's own store watcher for
// ProcessOwnedStore, and none for FilesUnderCWD (scanner-only). The set is
// checked by concrete type, not just count, so wiring the wrong watcher is
// caught.
func TestBuildAgentWatchers_PerSourceVariant(t *testing.T) {
	noCheck := func(string, int) bool { return true }
	for _, a := range agents.All() {
		t.Run(a.Identity.Name, func(t *testing.T) {
			assertAgentWatcherSet(t, a, noCheck)
		})
	}
}
