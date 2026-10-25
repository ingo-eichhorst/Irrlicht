// Package opencode provides an inbound adapter that monitors OpenCode
// (https://github.com/anomalyco/opencode) sessions by polling its SQLite
// database (StorePath; ~/.local/share/opencode/opencode.db by default).
//
// Unlike the other adapters (claudecode, codex, pi, aider) which tail JSONL
// transcript files, OpenCode stores all session state in a single SQLite
// database using WAL mode. The adapter watches the database file for writes
// via fsnotify and reads whichever schema the store carries:
//
//   - opencode 1.x: sessions from the `session` table, activity from new or
//     updated rows in `part`, with `message` giving each part's role.
//   - opencode 2.x (#2106): sessions from `session_v2`, activity from new or
//     updated rows in `session_message` — one row per user prompt, per
//     assistant step and per idle (turn end).
package opencode

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
)

// AdapterName identifies sessions originating from OpenCode.
const AdapterName = "opencode"

// processCmdPattern matches an opencode process by its command line: the
// executable — argv[0] — is `opencode`, `opencode2` or `opencode.exe`. The
// 2.x TUI runs as `opencode` through Homebrew and as `opencode2` through the
// npm package's bin of that name, and that package's binary — which both of
// its bins symlink to (`ls -la ~/.local/opencode-v2/bin` on the dev host) — is
// `opencode.exe` (#2106). Process.SessionHostArgv (IsServiceArgv) says which
// of the matched processes is the 2.x service rather than a client.
//
// Anchored on argv[0] because the matchers read the WHOLE command line
// (`pgrep -f` on macOS, /proc/<pid>/cmdline on Linux): unanchored, a process
// that only names an opencode binary in its arguments matched — the dev
// daemon minted placeholder rows for the tui-detach.py driver and its shell
// wrapper, and `pgrep -lf '(^|/)opencode(2|\.exe)?( |$)'` listed
// `sh -c sleep 20; echo /x/shim/opencode` where the anchored pattern listed
// only the service and a TUI. The cost: an argv[0] containing a space does
// not match.
//
// `( |$)` rather than `\s`: macOS pgrep's regex dialect has no `\s` —
// `pgrep -lf '(^|/)zsh(\s|$)'` printed nothing on the dev host while
// `pgrep -lf '(^|/)zsh( |$)'` listed three zsh processes.
// TestDiscoverPID_FindsAnOpencode2ClientOnThisHost runs it through the host's
// real matcher.
const processCmdPattern = `^([^ ]*/)?opencode(2|\.exe)?( |$)`

var processCmdRegex = regexp.MustCompile(processCmdPattern)

// dataRelPath is opencode's data directory relative to $HOME when
// $XDG_DATA_HOME is unset, and storeName the database file in it.
const (
	dataRelPath = ".local/share/opencode"
	storeName   = "opencode.db"
)

// dbRelPath is the default store relative to $HOME.
const dbRelPath = dataRelPath + "/" + storeName

// StorePath is the absolute path of the OpenCode SQLite database the daemon
// reads — the ONE resolver both the watcher (which tails it) and the hook
// receiver (which composes a session key from it) go through.
//
// A single function rather than two `filepath.Join(home, dbRelPath)` call sites
// for the same reason pi's Source() is a function: the hook receiver's key and
// the watcher's key must be the same string or they are two sessions, and a
// second, independently-written composition is exactly the drift #1361 records.
//
// It resolves the store the way opencode does (#2106), read out of the 2.1.4
// bundle with `strings` rather than by running it: the data dir is
// `$XDG_DATA_HOME || ~/.local/share` joined with "opencode", and the database
// is `$OPENCODE_DB ?? "opencode.db"` resolved against it
// (`function LD(o){let e=process.env.OPENCODE_DB??(…"opencode.db"…);
// return e===":memory:"?e:r.resolve(o,e)}`, called as `LD(o.data)`). So an
// absolute OPENCODE_DB is used as is and a relative one lands in the data dir.
// `:memory:` names no file to read; it falls back to the default store.
//
// Not followed: a non-stable release channel's `opencode-<channel>.db`, which
// the same function picks when OPENCODE_DB is unset. The daemon reads the
// stable store only.
func StorePath() string {
	db := os.Getenv("OPENCODE_DB")
	if db == ":memory:" {
		db = ""
	}
	if filepath.IsAbs(db) {
		return db
	}
	if db == "" {
		db = storeName
	}
	return filepath.Join(dataDir(), db)
}

// dataDir is opencode's data directory: $XDG_DATA_HOME/opencode, or
// ~/.local/share/opencode.
func dataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("opencode: $HOME not set, using relative path: %v", err)
	}
	return filepath.Join(home, dataRelPath)
}

// transcriptPathFor renders the string every downstream consumer keys an
// OpenCode session on: the write-ahead log beside the database, with the
// session id carried in a query parameter (metrics.go's parseTranscriptPath
// reads it back out).
//
// The WAL rather than the database itself is deliberate and predates #1719:
// isStaleTranscript() stats whatever this names, and OpenCode flushes every
// write to the WAL while the main file only moves on checkpoint.
func transcriptPathFor(sessionID string) string {
	return StorePath() + "-wal" + sessionQueryParam + sessionID
}
