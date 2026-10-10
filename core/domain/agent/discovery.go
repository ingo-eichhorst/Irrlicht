package agent

// PIDDiscoverFunc discovers the PID owning a session. Each adapter provides
// its own implementation (e.g. CWD-based for Claude Code, transcript-writer
// for Codex/Pi). The disambiguate callback selects one PID when multiple
// candidates match.
type PIDDiscoverFunc func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error)

// SharedPIDOwnerFunc confirms that one session still owns a PID that another
// root session also uses. An inconclusive read must return false.
type SharedPIDOwnerFunc func(cwd, transcriptPath string, pid int) bool

// ReleasedPIDFunc reports that pid verifiably no longer holds this session's
// transcript, for an agent whose sessions live inside a long-running host
// process that outlives them (#2080). Only a definitive "does not hold" may
// return true; a probe that could not run, or a transcript still held, must
// return false — "could not ask" is not "released".
type ReleasedPIDFunc func(cwd, transcriptPath string, pid int) bool
