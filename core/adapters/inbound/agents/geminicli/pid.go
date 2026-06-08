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
// Node worker (see isHeapBumpWorker) — both sharing the cwd. We bind the
// launcher: it is the lower-PID ancestor and the process the scanner keeps
// after ExcludeArgv drops the worker, so the proc-pre-session and the
// transcript converge on the same PID. The caller's disambiguate (prefer-
// highest) is therefore ignored in favour of lowest-PID.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWDAndCmdLine(commandPattern.String(), cwd, lowestPID)
}

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
