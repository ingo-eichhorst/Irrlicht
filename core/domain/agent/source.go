package agent

// Source is a sealed sum describing where session data lives and how
// the runtime discovers it. Each adapter picks exactly one variant; the
// daemon constructs the appropriate watcher from the variant.
type Source interface {
	isSource()
}

// FilesUnderRoot — transcripts live one-file-per-session under a fixed
// directory under $HOME. The runtime fswatches Dir and emits
// new/activity/removed events.
type FilesUnderRoot struct {
	Dir    string // path relative to $HOME, e.g. ".claude/projects"
	Parser FileParser

	// DirByOS optionally overrides Dir on specific platforms, keyed by
	// runtime.GOOS ("windows", "linux", ...). A value follows the same rule
	// as Dir — absolute is used as-is, otherwise it resolves under $HOME.
	// This is the Windows seam: an adapter whose data lives under %APPDATA%
	// on Windows points DirByOS["windows"] there while darwin and linux keep
	// the $HOME-relative Dir. Empty (the common case) means "use Dir
	// everywhere." Resolved by the daemon in RootDirFor.
	DirByOS map[string]string
}

func (FilesUnderRoot) isSource() {}

// RootDirFor returns the directory the runtime should watch for this source
// on the given OS (pass runtime.GOOS): the DirByOS override when one is set
// for that OS, otherwise Dir. Keeping the lookup here means the daemon wiring
// and any tests resolve the path the same way.
func (s FilesUnderRoot) RootDirFor(goos string) string {
	if d, ok := s.DirByOS[goos]; ok && d != "" {
		return d
	}
	return s.Dir
}

// FilesUnderCWD — each running process writes a known filename inside
// its own working directory. The runtime polls each matching process's
// CWD for the file.
type FilesUnderCWD struct {
	Filename string // basename only, e.g. ".aider.chat.history.md"
	Parser   RawLineParser
}

func (FilesUnderCWD) isSource() {}

// ProcessOwnedStore — session state lives in a structured store (SQLite,
// typically) whose path is derivable from the process PID or a stable
// app-data directory.
type ProcessOwnedStore struct {
	// PathForPID resolves the store path for a given session PID.
	// Adapters with a constant app-data dir ignore the pid argument;
	// adapters whose store is per-instance use it.
	PathForPID func(pid int) string
	Reader     MetricsReader
}

func (ProcessOwnedStore) isSource() {}
