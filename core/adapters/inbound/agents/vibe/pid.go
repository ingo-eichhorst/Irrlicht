package vibe

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Vibe process for a session by CWD match against the
// full command line. Vibe is a Python console-script, so its OS process name
// is the interpreter, not "vibe" — discovery has to match the command line
// (processCmdPattern), and narrow to the session by working directory: the
// `vibe` process keeps the session's cwd as its OS cwd.
//
// The transcript carries no cwd; when the caller has none yet (the first
// discovery attempt races sidecar enrichment), fall back to the meta.json
// sidecar next to the transcript.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	if cwd == "" {
		cwd = cwdFromSidecar(transcriptPath)
	}
	return processlifecycle.DiscoverPIDByCWDAndCmdLine(processCmdPattern, cwd, disambiguate)
}
