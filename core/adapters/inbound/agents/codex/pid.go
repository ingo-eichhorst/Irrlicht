package codex

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Codex process owning a session by checking which
// process has the transcript file open for writing. Codex keeps transcript
// files open during the session lifetime, unlike Claude Code which opens,
// writes, and closes.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByTranscriptWriter(transcriptPath)
}
