// Package hermes provides an inbound adapter that monitors Hermes Agent
// (https://github.com/NousResearch/hermes-agent) sessions by polling its
// SQLite store at $HERMES_HOME/state.db.
//
// Hermes writes NO transcript files. Every session — CLI, TUI, and the
// messaging-gateway platforms — lands in one shared SQLite database with a
// `sessions` table (one row per session, carrying aggregate token counts,
// cost, cwd and parent linkage) and a `messages` table (one row per message,
// OpenAI-shaped: role, content, tool_calls, tool_call_id, finish_reason).
// That makes this the second agent.ProcessOwnedStore adapter after opencode,
// and the first whose store is shared across every surface the agent has.
//
// Three properties of the store shape the adapter, all verified against
// Hermes Agent v0.19.0 (2026.7.20) on macOS:
//
//   - A session row is INSERTed while the session is live — roughly 2s after
//     launch, on the first user message, with ended_at NULL — so the store is
//     a live discovery source, not just a post-hoc archive. ended_at and
//     end_reason are filled in when the turn loop exits.
//
//   - `finish_reason` on assistant rows is an explicit turn boundary:
//     "stop" ends the turn, "tool_calls" means more work follows. No
//     heuristic idle-flush is needed to close a turn.
//
//   - `sessions.cwd` is populated ONLY for TUI sessions. Every caller of
//     Hermes' update_session_cwd lives in its tui_gateway/server.py, so
//     `source='cli'` rows — including the interactive `hermes chat` REPL,
//     which records source='cli' — carry an empty cwd and must borrow the
//     project binding from the live process. Resolved in two places: the
//     watcher (once per scan, for discovery) and querySessionMetrics (so an
//     initially-unbound session backfills on later activity).
//
// The gateway platforms (whatsapp, slack, discord, …) share this table.
// They are not coding sessions, so every query filters on `source` — see
// the note above querySessionMetrics in metrics.go.
package hermes

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AdapterName identifies sessions originating from Hermes Agent. It doubles
// as the onboarding-factory column slug (replaydata/agents/hermes/).
const AdapterName = "hermes"

// ProcessName is the nominal OS-level executable name. Hermes ships as a
// Python console script, so the real process is the venv interpreter and
// this name never reaches a pgrep -x: the CommandPattern matcher below is
// what actually resolves a Hermes process. It exists because the daemon's
// scanner requires a name and processNameFor falls back to Identity.Name.
const ProcessName = "hermes"

// storeRelPath is the store path relative to the Hermes home directory.
const storeRelPath = "state.db"

// defaultHomeRelPath is the Hermes home relative to $HOME, used when
// HERMES_HOME is unset. Mirrors hermes_constants._get_platform_default_hermes_home
// for the non-Windows case.
const defaultHomeRelPath = ".hermes"

// homeEnvVar relocates the entire Hermes home — config, auth, and the
// session store. Hermes resolves it as: context-local override → this env
// var → the platform default. Only the env var is observable from outside
// the process, so it is the one the adapter honors, and it is also the
// isolation lever the onboarding factory uses to record without touching a
// real install.
const homeEnvVar = "HERMES_HOME"

// processCmdPattern recognizes a running Hermes session process on the full
// command line. Hermes is a Python console script with no setproctitle, so
// the OS process name is the interpreter and an ExactName{"hermes"} match
// would never fire. Two invocation shapes exist, both verified live:
//
//	<home>/hermes-agent/venv/bin/python3 <home>/hermes-agent/venv/bin/hermes -z ...
//	<home>/hermes-agent/venv/bin/python -m hermes_cli.main gateway run --replace
//
// so we anchor on either a `hermes` executable token bounded by "/" and a
// space/end, or the module form. The bounding matters: the Hermes home
// (`/Users/x/.hermes/state.db`) and the install dir (`.../hermes-agent/...`)
// both contain the substring "hermes" but neither is followed by a space or
// end-of-string, so the daemon's own watcher argv can never self-trip the
// matcher. Pinned by TestProcessCmdRegex.
const processCmdPattern = `(^|/)hermes( |$)|hermes_cli\.main`

var processCmdRegex = regexp.MustCompile(processCmdPattern)

// serviceSubcommands are Hermes subcommands that run a long-lived service
// rather than an agent session. The messaging gateway in particular is
// started once and outlives every session it dispatches, so binding a
// session to it would reproduce the claude-code --bg-spare ghost (#727): a
// pool process that never reaps, adopted as if it were a session.
//
// Matched against the token following the `hermes` executable (or the
// `-m hermes_cli.main` module form), so a prompt that merely mentions one
// of these words is not excluded.
var serviceSubcommands = map[string]bool{
	"gateway":   true,
	"serve":     true,
	"dashboard": true,
	"cron":      true,
	"webhook":   true,
	"mcp":       true,
	"proxy":     true,
	"portal":    true,
	"acp":       true,
	"desktop":   true,
	"gui":       true,
	"lsp":       true,
}

// homeDir returns the Hermes home directory, honoring $HERMES_HOME.
//
// A relative override is rejected rather than resolved: Hermes itself treats
// the value as a path with no shell expansion, so "~/custom" is a
// misconfiguration, and silently reinterpreting it would point the daemon at
// a directory the agent never writes to. This mirrors agentpaths.FromEnv,
// which cannot be reused here because it resolves a $HOME-relative default
// lazily for fswatcher while ProcessOwnedStore needs an absolute file path.
func homeDir() string {
	if v := strings.TrimSpace(os.Getenv(homeEnvVar)); v != "" {
		if filepath.IsAbs(v) {
			return filepath.Clean(v)
		}
		// Fall through to the default; the value is unusable.
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultHomeRelPath
	}
	return filepath.Join(home, defaultHomeRelPath)
}

// StorePath returns the absolute path to the Hermes SQLite session store.
func StorePath() string {
	return filepath.Join(homeDir(), storeRelPath)
}

// transcriptPathIn renders the string every downstream consumer keys a Hermes
// session on, for a given store path. Hermes has no per-session file, so this
// composed spelling IS the session's identity as far as the rest of the daemon
// is concerned, and ComputeMetrics splits it back apart.
//
// The watcher and the hook receiver must produce the same string or one
// session becomes two, so there is one function and two thin callers rather
// than two spellings — the shape opencode's transcriptPathFor already uses.
func transcriptPathIn(dbPath, sessionID string) string {
	return dbPath + sessionQueryParam + sessionID
}

// transcriptPathFor is transcriptPathIn against the store this daemon resolves
// — what the hook receiver uses, since a hook payload names no file.
func transcriptPathFor(sessionID string) string {
	return transcriptPathIn(StorePath(), sessionID)
}

// isServiceArgv reports whether argv belongs to a long-lived Hermes service
// rather than an agent session.
//
// Per the agent.Process.ExcludeArgv contract an unreadable (nil/empty) argv
// must NOT be excluded, so an argv carrying no recognizable subcommand falls
// through to false — the loop simply finds nothing to match.
func isServiceArgv(argv []string) bool {
	// One-shot mode carries arbitrary user prompt text in argv, so a prompt
	// like `-z "the gateway is down"` would otherwise read as the `gateway`
	// subcommand and exclude a real session. -z IS a session by definition,
	// so settle it before any subcommand scan.
	for _, a := range argv {
		if a == "-z" || a == "--oneshot" {
			return false
		}
	}
	for i, a := range argv {
		isEntry := a == "hermes_cli.main" || a == "hermes" || strings.HasSuffix(a, "/hermes")
		if !isEntry {
			continue
		}
		// The subcommand is the first following token that is neither a flag
		// nor a flag's VALUE. Skipping the value matters: `--model gateway`
		// would otherwise read as the gateway service and exclude a real
		// session.
		skipNext := false
		for _, next := range argv[i+1:] {
			if skipNext {
				skipNext = false
				continue
			}
			if strings.HasPrefix(next, "-") {
				skipNext = valueTakingFlags[next]
				continue
			}
			return serviceSubcommands[next]
		}
	}
	return false
}

// valueTakingFlags are the global flags whose NEXT argv entry is a value
// rather than a subcommand, taken from `hermes --help`. Only the global
// (pre-subcommand) ones matter here.
var valueTakingFlags = map[string]bool{
	"-m": true, "--model": true,
	"--provider": true,
	"-t":         true, "--toolsets": true,
	"-s": true, "--skills": true,
	"-r": true, "--resume": true,
	"-c": true, "--continue": true,
	"-p": true, "--profile": true,
	"--usage-file": true,
}
