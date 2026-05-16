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
//   - some other command → wrap with tee so both ours and theirs receive stdin.
//
// The wrap form is:
//
//	tee >(<user command>) | curl -fsS … >/dev/null 2>&1 || true
//
// `tee >(…)` duplicates stdin to the user's process; the second branch flows
// through the pipeline to curl. Bash process substitution is bash-only, but
// Claude Code already invokes statusLine.command via bash on macOS/Linux.
func chainStatuslineCommand(current string) string {
	if current == "" || current == installedStatuslineCommand {
		return installedStatuslineCommand
	}
	if strings.Contains(current, statuslineSentinel) {
		// Already managed by us; force back to canonical form (and drop any
		// stale wrap of a removed user command).
		return installedStatuslineCommand
	}
	// Wrap: user's command runs alongside ours via process substitution.
	return "tee >(" + current + ") | curl -fsS --max-time 1 -X POST --data-binary @- " +
		"http://localhost:7837/api/v1/hooks/claudecode/statusline >/dev/null 2>&1 || true"
}

// unchainStatuslineCommand returns the user's original command when current
// was a chained wrap, or "" when current is our standalone install. Used by
// uninstall to restore the prior state.
func unchainStatuslineCommand(current string) string {
	const prefix = "tee >("
	if !strings.HasPrefix(current, prefix) {
		return ""
	}
	rest := current[len(prefix):]
	// Find the matching close paren of `tee >(...)`. The user's command
	// shouldn't contain an unbalanced ")"; if it does, we conservatively
	// give up on round-tripping and return empty.
	end := strings.Index(rest, ") | curl ")
	if end < 0 {
		return ""
	}
	return rest[:end]
}
