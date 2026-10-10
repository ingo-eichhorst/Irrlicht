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

// OwnsSharedPID confirms ownership through this transcript's sibling lock,
// asking whether pid itself holds it for writing (processlifecycle.
// HoldsForWriting, #2079) rather than scanning every process for its writer
// the way DiscoverPID must. A missing lock, a pid that does not hold it, or a
// probe that could not run does not confirm ownership.
func OwnsSharedPID(cwd, transcriptPath string, pid int) bool {
	held, err := processlifecycle.HoldsForWriting(pid, sessionLockPath(transcriptPath))
	return err == nil && held
}

func sessionLockPath(transcriptPath string) string {
	dir := filepath.Dir(transcriptPath)
	if dir == "" || dir == "." {
		return ""
	}
	return filepath.Join(dir, sessionLockFilename)
}
