// instructioninstaller.go manages the Irrlicht-managed task-eta emission rule
// in the user-level Claude Code instruction file ~/.claude/CLAUDE.md
// (issue #558). The block instructs the agent to periodically emit an in-band
// task-progress marker; with it in the user-level file every project inherits
// the rule without per-repo opt-in.
//
// Unlike the hook/statusline installers this is NEVER invoked unconditionally
// at startup — the user must opt in once (consent lives in the activation
// service); writes happen only after consent. Install/patch is idempotent and
// replaces only the managed block, preserving all surrounding user content
// byte-for-byte; uninstall removes only the managed block.
package claudecode

import (
	"os"
	"path/filepath"
	"strings"
)

// Sentinels delimiting the managed block. Detection keys on these full
// strings — never on a generic `<!--` scan — so the marker example comment
// nested inside the block can't confuse block detection.
const (
	taskEtaBeginSentinel = "<!-- BEGIN IRRLICHT MANAGED BLOCK (task-eta) -->"
	taskEtaEndSentinel   = "<!-- END IRRLICHT MANAGED BLOCK (task-eta) -->"
)

// managedTaskEtaBlock is the full block (sentinels inclusive) written
// verbatim. The lightest "emit periodically" phrasing won the 2026-05-31
// emission experiment; the marker example stays minimal per the v1 contract.
// Per-agent equivalents (~/.codex/AGENTS.md, ~/.config/opencode/AGENTS.md,
// ~/.gemini/GEMINI.md) are documented in the issue but not written in v1.
const managedTaskEtaBlock = taskEtaBeginSentinel + `
## Task progress markers (managed by Irrlicht)

As you work on a multi-step task, periodically emit a hidden progress marker
so tools can show a task-completion estimate. Emit it as an HTML comment, and
update it as you make progress:

<!-- {"marker":"irrlicht-eta","total_rounds":N,"completed_rounds":M} -->

` + "`total_rounds`" + ` is your estimate of the task's phases; ` + "`completed_rounds`" + `
is how many you've finished. Update every few steps.
` + taskEtaEndSentinel

// claudeMemoryPath returns the user-level Claude Code instruction file path.
func claudeMemoryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "CLAUDE.md"), nil
}

// EnsureTaskEtaBlockInstalled writes-or-patches the managed block in
// ~/.claude/CLAUDE.md. Creates the file if missing. Idempotent: a
// byte-identical existing block is a no-op; a stale block is replaced in
// place; surrounding content is preserved byte-for-byte.
func EnsureTaskEtaBlockInstalled() (bool, error) {
	path, err := claudeMemoryPath()
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	patched, changed := patchManagedBlock(string(existing), managedTaskEtaBlock)
	if !changed {
		return false, nil
	}
	return true, writeMemoryFile(path, patched)
}

// UninstallTaskEtaBlock removes only the managed block from
// ~/.claude/CLAUDE.md, leaving all other content untouched. No-op when the
// file or block is absent.
func UninstallTaskEtaBlock() (bool, error) {
	path, err := claudeMemoryPath()
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stripped, changed := removeManagedBlock(string(existing))
	if !changed {
		return false, nil
	}
	return true, writeMemoryFile(path, stripped)
}

// patchManagedBlock returns existing with the managed block inserted or
// replaced. Pure string→string; the unit-test surface for byte preservation.
//
//   - Both sentinels present and ordered: replace the span (inclusive) with
//     block — the stale-block upgrade path. Identical span → no change.
//   - No begin sentinel: append, separated from prior content by exactly one
//     blank line (trailing newlines are normalized first so re-running never
//     grows the separator). A stray end-only sentinel is left untouched
//     rather than guessed at — removeManagedBlock only ever cuts well-formed
//     pairs, so it can never corrupt user content.
func patchManagedBlock(existing, block string) (string, bool) {
	beginIdx := strings.Index(existing, taskEtaBeginSentinel)
	if beginIdx >= 0 {
		rest := existing[beginIdx:]
		if endOff := strings.Index(rest, taskEtaEndSentinel); endOff >= 0 {
			end := beginIdx + endOff + len(taskEtaEndSentinel)
			if existing[beginIdx:end] == block {
				return existing, false
			}
			return existing[:beginIdx] + block + existing[end:], true
		}
		// Begin without end — a damaged half-block. Fall through to append a
		// fresh well-formed block; the damaged remnant stays as-is.
	}
	if strings.TrimSpace(existing) == "" {
		return block + "\n", true
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + block + "\n", true
}

// removeManagedBlock returns existing with the managed block (sentinels
// inclusive) removed, also consuming the single blank-line separator the
// install owns so install→uninstall round-trips to the original bytes. A
// half-block (only one sentinel, or out of order) is a no-op — never guess,
// never corrupt.
func removeManagedBlock(existing string) (string, bool) {
	beginIdx := strings.Index(existing, taskEtaBeginSentinel)
	if beginIdx < 0 {
		return existing, false
	}
	endOff := strings.Index(existing[beginIdx:], taskEtaEndSentinel)
	if endOff < 0 {
		return existing, false
	}
	end := beginIdx + endOff + len(taskEtaEndSentinel)

	before := existing[:beginIdx]
	after := existing[end:]
	// Collapse the whitespace the block occupied: trim the newline runs on
	// both sides and rejoin with one blank line. Round-trips a canonical
	// install exactly; user-authored blank-line runs around the block
	// collapse to a single separator (cosmetic only, content untouched).
	before = strings.TrimRight(before, "\n")
	after = strings.TrimLeft(after, "\n")
	switch {
	case before == "":
		return after, true
	case after == "":
		return before + "\n", true
	default:
		return before + "\n\n" + after, true
	}
}

// writeMemoryFile writes content atomically (temp + rename), creating
// ~/.claude if needed. Mirrors writeClaudeSettings.
func writeMemoryFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
