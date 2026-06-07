package claudecode

// IsInfraArgv reports whether a `claude`-binary process is Claude Code's
// background infrastructure rather than an interactive session. Claude Code
// 2.1.168 introduced a background-daemon architecture ("cc-daemon"): a
// long-lived `claude daemon run`, PTY-host wrappers (`--bg-pty-host`), and
// pre-warmed spares (`--bg-spare`). All run the `claude` binary, so the
// ExactName process matcher accepts them and the lifecycle scanner would mint
// permanent `proc-<pid>` ghost pre-sessions for them — they never own a
// transcript, so nothing ever supersedes them (issue #644).
//
// The match is intentionally positional / flag-position based, never a blanket
// substring scan, so a user prompt that merely mentions these tokens — e.g.
// `claude -p "explain --bg-pty-host"` — is not excluded:
//
//   - argv[1]=="daemon" && argv[2]=="run"  (the cc-daemon itself)
//   - any argv element (after argv[0]) equal to "--bg-pty-host" or "--bg-spare"
//     (the PTY-host wrappers and pre-warmed spares; a flag occupies its own
//     argv slot, whereas a prompt string is a single slot that won't equal the
//     flag exactly)
//
// A nil/empty argv (unreadable, e.g. a hardened-runtime process) is treated as
// a session — we never exclude on the absence of evidence.
//
// NOTE: this is a denylist of the infra flags shipped in 2.1.168. If a later
// Claude Code release adds a new background flag, its processes mint ghost
// pre-sessions again (the #644 symptom) until that flag is added here — when
// ghosts reappear after an upstream release, check for new `--bg-*` flags or
// daemon subcommands first.
func IsInfraArgv(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	if len(argv) >= 3 && argv[1] == "daemon" && argv[2] == "run" {
		return true
	}
	for _, a := range argv[1:] {
		if a == "--bg-pty-host" || a == "--bg-spare" {
			return true
		}
	}
	return false
}
