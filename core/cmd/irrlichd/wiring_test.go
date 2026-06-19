package main

import (
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/domain/agent"
)

// buildAgentWatchers must dispatch every registered adapter's Source variant
// without panicking, and produce the right watcher set: a process scanner for
// every adapter, plus a dedicated file/store watcher for FilesUnderRoot and
// ProcessOwnedStore. FilesUnderCWD is scanner-only.
func TestBuildAgentWatchers_PerSourceVariant(t *testing.T) {
	noCheck := func(string, int) bool { return true }
	for _, a := range agents.All() {
		a := a
		t.Run(a.Identity.Name, func(t *testing.T) {
			watchers, _ := buildAgentWatchers(a, time.Minute, noCheck)
			want := 2 // file/store watcher + scanner
			if _, ok := a.Source.(agent.FilesUnderCWD); ok {
				want = 1 // scanner only
			}
			if len(watchers) != want {
				t.Errorf("%s (%T): got %d watchers, want %d", a.Identity.Name, a.Source, len(watchers), want)
			}
		})
	}
}
