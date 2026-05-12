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
	Dir    string     // path relative to $HOME, e.g. ".claude/projects"
	Parser FileParser
}

func (FilesUnderRoot) isSource() {}

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
