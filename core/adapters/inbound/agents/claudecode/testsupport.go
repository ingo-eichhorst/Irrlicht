package claudecode

// ReplaceTestDeps swaps DiscoverPID's filesystem dependencies (sessionsDir,
// pidAlive, discoverByCWD) with caller-provided stubs and returns a closure
// that restores the originals. Intended only for tests in other packages
// (e.g. services) that need to drive the real DiscoverPID against controlled
// filesystem state. Not for production callers.
//
// Any of cwd / alive may be nil to keep the current implementation.
func ReplaceTestDeps(
	dir string,
	alive func(int) bool,
	cwd func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error),
) (restore func()) {
	origDir := sessionsDir
	origAlive := pidAlive
	origCWD := discoverByCWD

	sessionsDir = dir
	if alive != nil {
		pidAlive = alive
	}
	if cwd != nil {
		discoverByCWD = cwd
	}

	return func() {
		sessionsDir = origDir
		pidAlive = origAlive
		discoverByCWD = origCWD
	}
}
