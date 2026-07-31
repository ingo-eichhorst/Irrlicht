// statusline_installer.go manages the Claude Code statusLine.command entry in
// ~/.claude/settings.json. Claude Code pipes statusline JSON to stdin of the
// configured command on every assistant message / mode toggle / /compact —
// the data carries rate_limits for Pro/Max users, which the daemon ingests
// via POST /api/v1/hooks/claudecode/statusline (issue #309).
package claudecode

import (
	"regexp"
	"strings"

	"irrlicht/core/pkg/daemonaddr"
)

// wrapPipeBoundary matches the `) | ` that closes the `tee >(…)` process
// substitution and opens the rest of the pipeline, in every wrap format we
// have shipped. Whitespace-tolerant so a hand-edited wrap with extra spaces
// still round-trips through unchainStatuslineCommand. It is the read half of
// wrapPipe below — keep the two in step.
var wrapPipeBoundary = regexp.MustCompile(`\)\s*\|\s*`)

// bashEnvelopeOpen / teeSubOpen / wrapPipe are the fixed parts of the current
// wrap format, `bash -c 'tee >(<our curl>) | <user command>'`. Everything
// between them varies: our curl carries the resolved daemon port (#1178) and
// the user's command is arbitrary, so unchainStatuslineCommand recognises a
// wrap by these anchors plus which side of the pipe our sentinel lands on —
// never by a longer literal prefix.
const (
	bashEnvelopeOpen = `bash -c '`
	teeSubOpen       = `tee >(`
	wrapPipe         = `) | `
)

// StatuslineEndpointPath is the daemon's statusline ingest path. Host and port
// are resolved at install time from the daemon's own bind address (#1178).
// Exported so the daemon's route comes from the same constant the installer
// writes — see HookEndpointPath for why drift matters.
const StatuslineEndpointPath = "/api/v1/hooks/claudecode/statusline"

// statuslineSentinel is the substring that identifies an irrlicht-managed
// statusline command. Used for idempotency checks and chained-command
// upgrades. Port-independent for the same reason as hookSentinel: a command
// installed by a daemon on one port must still be recognized — and rewritten
// in place — by a daemon on another, rather than being mistaken for a
// third-party command and wrapped a second time (#1178).
const statuslineSentinel = StatuslineEndpointPath

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
func installedStatuslineCommand() string {
	return "curl -fsS --max-time 1 -X POST --data-binary @- " +
		daemonaddr.LocalURL(StatuslineEndpointPath) + " >/dev/null 2>&1 || true"
}

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
	canonical := installedStatuslineCommand()
	if current == "" || current == canonical {
		return canonical
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
		return canonical
	}
	// Pure user command — wrap it.
	return wrapStatuslineCommand(current)
}

// wrapStatuslineCommand builds the canonical chained form for the given
// user command. Single-quotes inside the user's command are escaped so the
// command can be embedded in `bash -c '…'` without breaking quoting.
// curl sits in a process substitution so its stdout (silenced via
// >/dev/null) doesn't sit in the main pipeline; the user command
// runs last so its stdout reaches Claude Code directly.
func wrapStatuslineCommand(user string) string {
	escaped := strings.ReplaceAll(user, "'", `'\''`)
	return bashEnvelopeOpen + teeSubOpen + installedStatuslineCommand() + wrapPipe + escaped + `'`
}

// unchainStatuslineCommand returns the user's original command when current
// was a chained wrap of ours, or "" otherwise (standalone install, unknown
// shape, or a command that isn't ours at all).
//
// Every wrap we have shipped is `[bash -c ']tee >(A) | B`, differing only in
// which half holds the user's command:
//
//   - v3 (current):  `bash -c 'tee >(<our curl>) | <user>'`
//   - v2 (legacy):   `bash -c 'tee >(<user>) | curl … sentinel … || true'`
//   - v1 (broken):   `tee >(<user>) | curl … sentinel … || true`
//
// So one parse handles all three: strip the optional `bash -c '…'` envelope
// and the `tee >(` opener, split on the first pipe boundary, and take whichever
// half does *not* carry our sentinel. Deciding by sentinel side rather than by
// a literal prefix is what lets a wrap written by a daemon on one port unwind
// under another — our curl carries the resolved port (#1178), so a prefix match
// would fail and silently drop the user's command on a repoint.
//
// v1 and v2 are still recognised for migration; new installs always emit v3.
func unchainStatuslineCommand(current string) string {
	// Not ours at all — a third-party `tee >(x) | curl y` must be left whole
	// for chainStatuslineCommand to wrap, not truncated to its first half.
	if !strings.Contains(current, statuslineSentinel) {
		return ""
	}
	body := current
	if envelope := strings.TrimPrefix(body, bashEnvelopeOpen); envelope != body {
		if !strings.HasSuffix(envelope, `'`) {
			return ""
		}
		body = envelope[:len(envelope)-1]
	}
	if !strings.HasPrefix(body, teeSubOpen) {
		return "" // standalone install — no user command to preserve
	}
	body = body[len(teeSubOpen):]
	// The first boundary is always the one closing `tee >(`: our curl contains
	// no ")". A user command containing a literal ") | " round-trips wrong only
	// when it is the one inside the process substitution (v1/v2), which is the
	// same pathological case this has always had.
	loc := wrapPipeBoundary.FindStringIndex(body)
	if loc == nil {
		return ""
	}
	user := body[:loc[0]]
	if strings.Contains(user, statuslineSentinel) {
		user = body[loc[1]:] // v3: ours is in the process sub, theirs follows
	}
	// Reverse the single-quote escaping applied by wrapStatuslineCommand. v1
	// wraps were never escaped, but the replace is a safe no-op there.
	return strings.ReplaceAll(user, `'\''`, "'")
}
