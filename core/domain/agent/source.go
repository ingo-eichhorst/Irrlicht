package agent

// Source is a sealed sum describing where session data lives and how
// the runtime discovers it. Each adapter picks exactly one variant; the
// daemon constructs the appropriate watcher from the variant.
//
// Deliberately not renamed for godre:S8196 ("-er" suffix convention for
// single-method interfaces): isSource is a sealing marker method, not a
// behavior contract, and Source is the exported domain noun referenced
// throughout adapters/ and application/ — renaming it would be an
// architectural change to the domain vocabulary, not a mechanical lint fix.
type Source interface {
	isSource()
}

// FilesUnderRoot — transcripts live one-file-per-session under a fixed
// directory under $HOME. The runtime fswatches Dir and emits
// new/activity/removed events.
//
// Four fields now answer "where is the root" — Dir, DirByOS, DirFunc, and
// ExtraDirs — resolved together by AllRootsFor, with a documented precedence
// between the first three. That is the ceiling: if a fifth is ever wanted,
// collapse the lot into one resolver (`Roots func(goos string) []string`, with
// constructors for the static cases) rather than extending the ladder.
type FilesUnderRoot struct {
	Dir    string // path relative to $HOME, e.g. ".claude/projects"
	Parser FileParser

	// DirFunc optionally replaces Dir with a resolver, for an adapter whose
	// root cannot be derived from constants and env vars alone and so must be
	// resolved by reading something. Nil (the common case) means "use Dir".
	//
	// It is the seam for a root whose resolution must be CONSENT-GATED: the
	// daemon calls it when it builds the adapter's watchers, which happens
	// only once that adapter's observe permission is granted, whereas Dir is
	// evaluated when the adapter declares itself — before any permission state
	// exists. An adapter that reads a file to find its root belongs here.
	//
	// Two consequences: the resolver runs on every grant, so a re-grant
	// re-resolves rather than reusing a declaration-time snapshot; and it must
	// stay cheap and side-effect-free, since tests and tooling call RootDirFor
	// too. See the vibe adapter's sessionsDir for the worked example.
	DirFunc func() string

	// DirByOS optionally overrides Dir on specific platforms, keyed by
	// runtime.GOOS ("windows", "linux", ...). A value follows the same rule
	// as Dir — absolute is used as-is, otherwise it resolves under $HOME.
	// This is the Windows seam: an adapter whose data lives under %APPDATA%
	// on Windows points DirByOS["windows"] there while darwin and linux keep
	// the $HOME-relative Dir. Empty (the common case) means "use Dir
	// everywhere." Resolved by the daemon in RootDirFor.
	DirByOS map[string]string

	// ExtraDirs lists ADDITIONAL root directories to watch beyond Dir, each
	// resolved the same way as Dir (absolute used as-is, otherwise under
	// $HOME). The daemon builds one fswatcher per root. Used by adapters whose
	// sessions are split across sibling stores that must not be unified under a
	// shared parent: Antigravity writes the CLI's transcripts under
	// ~/.gemini/antigravity-cli/brain and the IDE's under ~/.gemini/antigravity/
	// brain, and rooting at the common ~/.gemini parent would collide with the
	// Gemini CLI adapter's ~/.gemini/tmp. Empty (the common case) means "watch
	// only Dir". Collected by AllRootsFor.
	ExtraDirs []string

	// SessionIDFromPath optionally overrides how the watcher derives a
	// session's ID from a transcript file path. The default (nil) uses the
	// filename stem (`<uuid>.jsonl` → `<uuid>`). Adapters whose transcript
	// filename is a constant supply this to source the ID from a path
	// component and to skip sibling files: Antigravity writes
	// brain/<conv-id>/.system_generated/logs/transcript.jsonl, so the ID is the
	// <conv-id> directory and transcript_full.jsonl must be ignored. The
	// function receives the absolute file path and returns "" for any file the
	// adapter does not own (skipping it entirely).
	SessionIDFromPath func(path string) string

	// ParentSessionIDFromPath optionally derives a session's parent from its
	// transcript before the watcher emits the event. It is used by flat
	// transcript stores whose child relationship lives in a metadata header
	// rather than their directory layout. Returning "" leaves the session
	// top-level.
	ParentSessionIDFromPath func(path string) string
}

func (FilesUnderRoot) isSource() {
	// sealing marker — deliberately empty
}

// RootDirFor returns the directory the runtime should watch for this source
// on the given OS (pass runtime.GOOS): the DirByOS override when one is set
// for that OS, otherwise DirFunc's result when one is supplied, otherwise Dir.
// Keeping the lookup here means the daemon wiring and any tests resolve the
// path the same way.
//
// DirByOS outranks DirFunc so a per-OS override stays authoritative: DirFunc
// is "Dir, computed lazily", and DirByOS overrides Dir.
func (s FilesUnderRoot) RootDirFor(goos string) string {
	if d, ok := s.DirByOS[goos]; ok && d != "" {
		return d
	}
	if s.DirFunc != nil {
		return s.DirFunc()
	}
	return s.Dir
}

// AllRootsFor returns every directory the runtime should watch for this source
// on the given OS: RootDirFor(goos) followed by ExtraDirs in order. The daemon
// builds one fswatcher per returned root.
func (s FilesUnderRoot) AllRootsFor(goos string) []string {
	roots := make([]string, 0, 1+len(s.ExtraDirs))
	roots = append(roots, s.RootDirFor(goos))
	return append(roots, s.ExtraDirs...)
}

// FilesUnderCWD — each running process writes a known filename inside
// its own working directory. The runtime polls each matching process's
// CWD for the file.
type FilesUnderCWD struct {
	Filename string // basename only, e.g. ".aider.chat.history.md"
	Parser   RawLineParser
}

func (FilesUnderCWD) isSource() {
	// sealing marker — deliberately empty
}

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

func (ProcessOwnedStore) isSource() {
	// sealing marker — deliberately empty
}
