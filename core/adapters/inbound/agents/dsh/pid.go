package dsh

import (
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/processlifecycle"
)

// DiscoverPID asks which process holds this session's lock file open for
// writing. The 0.1.5-rc.2 process keeps no equivalent transcript descriptor,
// so an empty lock result has no transcript-file fallback.
func DiscoverPID(cwd, transcriptPath string, disambiguate func([]int) int) (int, error) {
	lockPath := sessionLockPath(transcriptPath)
	if lockPath == "" {
		return 0, nil
	}
	return processlifecycle.DiscoverPIDByTranscriptWriter(lockPath)
}

func sessionLockPath(transcriptPath string) string {
	dir := filepath.Dir(transcriptPath)
	if dir == "" || dir == "." {
		return ""
	}
	return filepath.Join(dir, sessionLockFilename)
}
