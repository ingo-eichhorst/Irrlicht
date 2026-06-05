package kirocli

import (
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	"irrlicht/core/pkg/transcript"
)

// DiscoverPID finds the Kiro CLI process for a session by CWD match: the
// `kiro-cli chat` parent process keeps the session's working directory as
// its OS cwd. Kiro does not keep the .jsonl open between writes, so
// transcript-writer discovery is not an option (verified live, see
// .build/refresh/kiro-cli-smoke/FINDINGS.md).
//
// The transcript itself carries no cwd; when the caller has none yet (the
// first discovery attempt races metadata enrichment), fall back to the
// <uuid>.json metadata sidecar next to the transcript.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	if cwd == "" {
		cwd = transcript.ExtractCWDFromSidecar(transcriptPath)
	}
	return processlifecycle.DiscoverPIDByCWD(ProcessName, cwd, disambiguate)
}
