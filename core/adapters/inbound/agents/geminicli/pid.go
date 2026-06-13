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
// Node worker (see isHeapBumpWorker) — both sharing the cwd. Unlike the
// Scanner, this raw pgrep-style match applies no argv filter, so we pass
// isHeapBumpWorker as the excludeArgv predicate to drop the worker before
// disambiguation; otherwise the disambiguator's "highest unclaimed PID" would
// select the higher-PID worker (the process treated as a ghost everywhere
// else) instead of the launcher the pre-session path binds (#664). With the
// worker gone, when two sessions share a cwd both launchers match, so we honor
// the caller's claimed-aware disambiguate (PIDManager.TryDiscoverPID: prefer
// the highest unclaimed PID) to give each session its own launcher. With no
// disambiguate (nil caller) we fall back to lowestPID — the launcher ancestor.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	if disambiguate == nil {
		disambiguate = lowestPID
	}
	return discoverByCWDAndCmdLine(commandPattern.String(), cwd, disambiguate, isHeapBumpWorker)
}

// discoverByCWDAndCmdLine is the OS-level CWD+cmdline match with the heap-bump
// worker excluded by argv. It's a package variable so tests can stub it in
// place of the real pgrep call.
var discoverByCWDAndCmdLine = processlifecycle.DiscoverPIDByCWDAndCmdLineExcludingArgv

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
