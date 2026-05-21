// statusline_installer.go manages the Claude Code statusLine.command entry in
// ~/.claude/settings.json. Claude Code pipes statusline JSON to stdin of the
// configured command on every assistant message / mode toggle / /compact —
// the data carries rate_limits for Pro/Max users, which the daemon ingests
// via POST /api/v1/hooks/claudecode/statusline (issue #309).
package claudecode

import (
	"regexp"
	"strings"
)

// unchainBoundary matches the boundary between the user's tee-fed
// command and the trailing curl pipeline in the legacy v1/v2 wrap format.
// Whitespace-tolerant so a hand-edited wrap with extra spaces still
// round-trips through unchainStatuslineCommand.
var unchainBoundary = regexp.MustCompile(`\)\s*\|\s*curl\s`)

// v3WrapPrefix is the fixed leading literal of the current wrap format.
// The user's command follows immediately after this prefix and is
// terminated by a closing single quote.
const v3WrapPrefix = `bash -c 'tee >(` + installedStatuslineCommand + `) | `

// statuslineSentinel is the substring that identifies an irrlicht-managed
// statusline command. Used for idempotency checks and chained-command
// upgrades.
const statuslineSentinel = "localhost:7837/api/v1/hooks/claudecode/statusline"

// installedStatuslineCommand is the canonical statusLine.command we install.
// Reads the statusline JSON from stdin, POSTs it to the daemon, then echoes
// nothing (so the menu-bar / terminal-prompt statusline area stays empty —
// the user already sees per-session data in the irrlicht overlay).
//
// `tee` duplicates stdin so a user-configured chained command can still run
// downstream when we wrap an existing entry (see chainStatuslineCommand).
// Flags mirror the hook command:
//   - -fsS  : fail silently on HTTP errors, but show curl errors on stderr
//   - --max-time 1 : abort if the daemon is unreachable, keeps statusline snappy
//   - || true: don't surface non-zero exit (e.g. daemon down) to Claude Code
const installedStatuslineCommand = "curl -fsS --max-time 1 -X POST --data-binary @- " +
	"http://localhost:7837/api/v1/hooks/claudecode/statusline >/dev/null 2>&1 || true"

// EnsureStatuslineInstalled adds (or upgrades) the statusLine.command entry
// in ~/.claude/settings.json. Returns true when the file was modified.
//
// Idempotency rules:
//   - If statusLine is absent, install it with our canonical command.
//   - If statusLine.command equals our canonical command verbatim, no-op.
//   - If statusLine.command contains our sentinel but differs from the
//     canonical form, rewrite in place (migration path).
//   - If statusLine.command is set to a third-party command (no sentinel),
//     wrap it: pipe stdin through `tee` to both the user's command and ours.
//     The user's statusline output is preserved.
func EnsureStatuslineInstalled() (bool, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return false, err
	}

	settings, err := readClaudeSettings(path)
	if err != nil {
		return false, err
	}

	current := readStatuslineCommand(settings)
	desired := chainStatuslineCommand(current)

	if current == desired {
		return false, nil
	}
	writeStatuslineCommand(settings, desired)
	return true, writeClaudeSettings(path, settings)
}

// UninstallStatusline removes the irrlicht statusline entry. When the entry
// was a chained wrap, the user's original command is restored; when it was
// our standalone install, statusLine is removed entirely.
func UninstallStatusline() (bool, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return false, err
	}

	settings, err := readClaudeSettings(path)
	if err != nil {
		return false, err
	}

	current := readStatuslineCommand(settings)
	if current == "" || !strings.Contains(current, statuslineSentinel) {
		return false, nil
	}

	user := unchainStatuslineCommand(current)
	if user == "" {
		// Standalone install — drop the whole statusLine block.
		delete(settings, "statusLine")
	} else {
		writeStatuslineCommand(settings, user)
	}
	return true, writeClaudeSettings(path, settings)
}

// readStatuslineCommand returns settings.statusLine.command when present,
// otherwise empty. Tolerates the entry being either a plain string (legacy
// older Claude Code versions) or the canonical { "type": "command",
// "command": "…" } object form.
func readStatuslineCommand(settings map[string]interface{}) string {
	sl, ok := settings["statusLine"]
	if !ok {
		return ""
	}
	switch v := sl.(type) {
	case string:
		return v
	case map[string]interface{}:
		if cmd, ok := v["command"].(string); ok {
			return cmd
		}
	}
	return ""
}

// writeStatuslineCommand sets settings.statusLine to the canonical object
// form ({"type":"command","command":cmd}), replacing whatever was there.
func writeStatuslineCommand(settings map[string]interface{}, cmd string) {
	settings["statusLine"] = map[string]interface{}{
		"type":    "command",
		"command": cmd,
	}
}

// chainStatuslineCommand returns the command to install given the current
// configured command.
//
//   - "" (nothing configured) → install our standalone command.
//   - already-our-canonical → return as-is (caller treats as no-op).
//   - contains our sentinel but isn't canonical → return canonical (rewrite).
//   - some other command → wrap so both ours and theirs receive stdin.
//
// The wrap form uses `bash -c` explicitly because Claude Code invokes
// statusLine.command via POSIX `sh` on Unix, and the process substitution
// (`tee >(…)`) we rely on to duplicate stdin is bash-only. Without the
// `bash -c` envelope, `sh` errors at parse time and the entire pipeline
// (including our curl) never runs.
//
// Internal shape, inside `bash -c "…"`:
//
//	tee >(curl -fsS … >/dev/null 2>&1 || true) | <user command>
//
// curl runs in a process substitution so it receives a copy of stdin
// without sitting in the main pipeline. The user's command runs last in
// the pipeline, so its stdout flows directly back to Claude Code, which
// reads it to display the status line text. Prior wrap formats (v1: bare
// tee pipeline; v2: user command in the process sub) are migrated on the
// next daemon start via unchainStatuslineCommand + re-chain.
func chainStatuslineCommand(current string) string {
	if current == "" || current == installedStatuslineCommand {
		return installedStatuslineCommand
	}
	// If current is already a managed wrap (old or new format), unchain to
	// recover the user's original command, then re-chain in the canonical
	// new format. This is the migration path from the v1 wrap (no `bash -c`
	// envelope, which silently failed under POSIX sh) to the v2 wrap that
	// works regardless of which shell Claude Code invokes us through.
	if user := unchainStatuslineCommand(current); user != "" {
		return wrapStatuslineCommand(user)
	}
	if strings.Contains(current, statuslineSentinel) {
		// Managed standalone — no user command to preserve. Force canonical.
		return installedStatuslineCommand
	}
	// Pure user command — wrap it.
	return wrapStatuslineCommand(current)
}

// wrapStatuslineCommand builds the canonical chained form for the given
// user command. Single-quotes inside the user's command are escaped so the
// command can be embedded in `bash -c '…'` without breaking quoting.
// curl sits in a process substitution so its stdin (and stdout via
// >/dev/null) don't interfere with the main pipeline; the user command
// runs last so its stdout reaches Claude Code directly.
func wrapStatuslineCommand(user string) string {
	escaped := strings.ReplaceAll(user, "'", `'\''`)
	return v3WrapPrefix + escaped + `'`
}

// unchainStatuslineCommand returns the user's original command when current
// was a chained wrap, or "" otherwise (standalone install, unknown shape).
//
// Recognises three wrap formats:
//   - v3 (current):  `bash -c 'tee >(<curl+sentinel>) | <user>'`
//   - v2 (legacy):   `bash -c 'tee >(<user>) | curl … sentinel … || true'`
//   - v1 (broken):   `tee >(<user>) | curl … sentinel … || true`
//
// v1 and v2 are still recognised for migration; new installs always emit v3.
func unchainStatuslineCommand(current string) string {
	// v3: user command is after the fixed curl-in-sub prefix.
	if strings.HasPrefix(current, v3WrapPrefix) && strings.HasSuffix(current, "'") {
		inner := current[len(v3WrapPrefix) : len(current)-1]
		return strings.ReplaceAll(inner, `'\''`, "'")
	}
	// v1/v2: user command is before the `) | curl ` boundary.
	for _, prefix := range []string{`bash -c 'tee >(`, `tee >(`} {
		if !strings.HasPrefix(current, prefix) {
			continue
		}
		rest := current[len(prefix):]
		// Whitespace-tolerant match for `) | curl ` so a hand-edited
		// wrap (e.g. with extra spaces around the pipe) still unwinds
		// cleanly. The first match wins — user commands containing a
		// literal ") | curl " inside their own command would still
		// round-trip wrong, but that's a pathological case.
		loc := unchainBoundary.FindStringIndex(rest)
		if loc == nil {
			continue
		}
		inner := rest[:loc[0]]
		// Reverse the single-quote escaping for v2 wraps; v1 wraps were
		// never escaped, but the replace is a safe no-op when no escapes
		// are present.
		return strings.ReplaceAll(inner, `'\''`, "'")
	}
	return ""
}
