package copilot

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID finds the Copilot CLI process for a session by CWD match: the
// `copilot` process keeps the session's working directory as its OS cwd.
//
// No sidecar fallback is needed, unlike kiro-cli and mistral-vibe. Copilot
// records the working directory in session.start.data.context.cwd — the very
// first line of the transcript — and the parser carries it on every subsequent
// event, so a caller that has read any line at all already has a cwd.
//
// Matching is by process name rather than command line because the agent is
// the native binary, whose accounting name is exactly "copilot" (the node
// loader that spawns it reports as "node" and is therefore never matched).
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	return processlifecycle.DiscoverPIDByCWD(ProcessName, cwd, disambiguate)
}
