// statusline_installer.go manages the Claude Code statusLine.command entry in
// ~/.claude/settings.json. Claude Code pipes statusline JSON to stdin of the
// configured command on every assistant message / mode toggle / /compact —
// the data carries rate_limits for Pro/Max users, which the daemon ingests
// via POST /api/v1/hooks/claudecode/statusline (issue #309).
package claudecode

import (
	"strings"
)

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
//	tee >(<user command>) | curl -fsS … >/dev/null 2>&1 || true
//
// The user's command sees a copy of stdin via `tee`; the trailing curl
// branch carries stdin onward to our endpoint. The `|| true` keeps the
// overall command status zero so Claude Code doesn't surface a failure.
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
func wrapStatuslineCommand(user string) string {
	escaped := strings.ReplaceAll(user, "'", `'\''`)
	return `bash -c 'tee >(` + escaped + `) | curl -fsS --max-time 1 -X POST --data-binary @- ` +
		`http://localhost:7837/api/v1/hooks/claudecode/statusline >/dev/null 2>&1 || true'`
}

// unchainStatuslineCommand returns the user's original command when current
// was a chained wrap, or "" otherwise (standalone install, unknown shape).
//
// Recognises both wrap formats:
//   - v1 (broken under sh): `tee >(<user>) | curl … sentinel … || true`
//   - v2 (canonical):       `bash -c 'tee >(<user>) | curl … sentinel … || true'`
//
// The v1 form is still recognised for migration; new installs always emit v2.
func unchainStatuslineCommand(current string) string {
	for _, prefix := range []string{`bash -c 'tee >(`, `tee >(`} {
		if !strings.HasPrefix(current, prefix) {
			continue
		}
		rest := current[len(prefix):]
		end := strings.Index(rest, `) | curl `)
		if end < 0 {
			continue
		}
		inner := rest[:end]
		// Reverse the single-quote escaping for v2 wraps; v1 wraps were
		// never escaped, but the replace is a safe no-op when no escapes
		// are present.
		return strings.ReplaceAll(inner, `'\''`, "'")
	}
	return ""
}
