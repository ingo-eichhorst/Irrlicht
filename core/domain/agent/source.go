package agent

// Source is a sealed sum describing where session data lives and how the
// runtime discovers it. Each adapter picks exactly one variant; the
// daemon's runtime (introduced in PR5 of #159) constructs the appropriate
// watcher from the variant.
//
// Phase C will add EditorSharedKVStore for adapters that park chat history
// in the editor's shared SQLite (Cody, Augment, Copilot's newer chat-
// sessions path). Adding new variants is a non-breaking change.
type Source interface {
	isSource()
}

// FilesUnderRoot — transcripts live one-file-per-session under a fixed
// directory under $HOME. The runtime fswatches Dir and emits new/activity/
// removed events. Used by claude-code, codex, pi, gemini-cli, openhands.
type FilesUnderRoot struct {
	Dir    string     // path relative to $HOME, e.g. ".claude/projects"
	Parser FileParser // sub-sum: JSONLineParser, RawLineParser (DocumentParser in Phase C)
}

func (FilesUnderRoot) isSource() {}

// FilesUnderCWD — each running process writes a known filename inside its
// own working directory. The runtime polls each matching process's CWD
// for the file. Used by aider (.aider.chat.history.md).
type FilesUnderCWD struct {
	Filename string        // basename only, e.g. ".aider.chat.history.md"
	Parser   RawLineParser // raw line + idle flush, non-JSONL
}

func (FilesUnderCWD) isSource() {}

// ProcessOwnedStore — session state lives in a structured store (typically
// SQLite) whose path is derivable from the process PID or a stable
// app-data directory. The runtime polls the store directly. Used by
// opencode (and in Phase C: goose v1.10+, crush, cursor, zed).
type ProcessOwnedStore struct {
	// PathForPID resolves the store path for a given session PID.
	// Adapters with a constant app-data dir (zed, opencode) ignore the
	// pid argument; adapters whose store is per-instance (cursor's
	// workspaceStorage) use it.
	PathForPID func(pid int) string
	Reader     MetricsReader
}

func (ProcessOwnedStore) isSource() {}
