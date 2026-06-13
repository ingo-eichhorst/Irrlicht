package geminicli

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Gemini CLI process owning a session.
//
// Gemini does not keep its transcript file open (it appends and closes), so
// the transcript-writer strategy used by Codex/Pi doesn't apply, and the
// transcript path encodes only the project name — not the cwd. We instead
// match by the workspace cwd the parser extracts from the session's bootstrap
// context (state.CWD), narrowed to processes whose command line is the gemini
// script.
//
// A session has two matching processes — the launcher and its heap-bumped
// Node worker (see isHeapBumpWorker) — but ExcludeArgv drops the worker before
// the candidates reach here, leaving one launcher per session. When two
// sessions share a cwd both launchers match, so we honor the caller's claimed-
// aware disambiguate (PIDManager.TryDiscoverPID: prefer the highest unclaimed
// PID) to give each session its own launcher (#664). With no disambiguate
// (nil caller) we fall back to lowestPID — the launcher ancestor.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	if disambiguate == nil {
		disambiguate = lowestPID
	}
	return discoverByCWDAndCmdLine(commandPattern.String(), cwd, disambiguate)
}

// discoverByCWDAndCmdLine is the OS-level CWD+cmdline match. It's a package
// variable so tests can stub it in place of the real pgrep call.
var discoverByCWDAndCmdLine = processlifecycle.DiscoverPIDByCWDAndCmdLine

// lowestPID picks the smallest PID — the launcher ancestor, spawned before
// its heap-bump worker child.
func lowestPID(pids []int) int {
	best := 0
	for _, p := range pids {
		if best == 0 || p < best {
			best = p
		}
	}
	return best
}
