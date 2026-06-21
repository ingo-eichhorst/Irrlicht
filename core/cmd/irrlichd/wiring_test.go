package main

import (
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/fswatcher"
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/domain/agent"
)

// buildAgentWatchers must dispatch every registered adapter's Source variant
// without panicking and build the right watcher set: exactly one process
// scanner for every adapter, plus the variant's dedicated watcher — an
// fswatcher for FilesUnderRoot, the opencode store watcher for
// ProcessOwnedStore, and none for FilesUnderCWD (scanner-only). The set is
// checked by concrete type, not just count, so wiring the wrong watcher is
// caught.
func TestBuildAgentWatchers_PerSourceVariant(t *testing.T) {
	noCheck := func(string, int) bool { return true }
	for _, a := range agents.All() {
		t.Run(a.Identity.Name, func(t *testing.T) {
			watchers, _ := buildAgentWatchers(a, time.Minute, noCheck)

			var scanners, fswatchers, stores int
			for _, w := range watchers {
				switch w.(type) {
				case *processlifecycle.Scanner:
					scanners++
				case *fswatcher.Watcher:
					fswatchers++
				case *opencode.Watcher:
					stores++
				default:
					t.Errorf("%s: unexpected watcher type %T", a.Identity.Name, w)
				}
			}

			if scanners != 1 {
				t.Errorf("%s: got %d process scanners, want 1", a.Identity.Name, scanners)
			}
			switch s := a.Source.(type) {
			case agent.FilesUnderRoot:
				// One fswatcher per root: the primary Dir plus any ExtraDirs
				// (Antigravity watches both the CLI and IDE brain stores).
				wantFs := 1 + len(s.ExtraDirs)
				if fswatchers != wantFs || stores != 0 {
					t.Errorf("%s (FilesUnderRoot): fswatchers=%d stores=%d, want %d/0", a.Identity.Name, fswatchers, stores, wantFs)
				}
			case agent.ProcessOwnedStore:
				if stores != 1 || fswatchers != 0 {
					t.Errorf("%s (ProcessOwnedStore): stores=%d fswatchers=%d, want 1/0", a.Identity.Name, stores, fswatchers)
				}
			case agent.FilesUnderCWD:
				if fswatchers != 0 || stores != 0 {
					t.Errorf("%s (FilesUnderCWD): want scanner-only, got fswatchers=%d stores=%d", a.Identity.Name, fswatchers, stores)
				}
			default:
				t.Fatalf("%s: unhandled Source variant %T — update this test", a.Identity.Name, a.Source)
			}
		})
	}
}
