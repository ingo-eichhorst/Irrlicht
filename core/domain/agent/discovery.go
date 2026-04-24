package agent

// PIDDiscoverFunc discovers the PID owning a session. Each adapter provides
// its own implementation (e.g. CWD-based for Claude Code, transcript-writer
// for Codex/Pi). The disambiguate callback selects one PID when multiple
// candidates match.
type PIDDiscoverFunc func(cwd, transcriptPath string, disambiguate func([]int) int) (int, error)
