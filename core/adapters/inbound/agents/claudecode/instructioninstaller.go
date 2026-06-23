// instructioninstaller.go manages the Irrlicht-managed emission rules in the
// user-level Claude Code instruction file ~/.claude/CLAUDE.md: the task-eta
// progress marker (issue #558) and the task-summary marker (issue #738), each
// in its own BEGIN/END-delimited block. The blocks instruct the agent to emit
// in-band markers; with them in the user-level file every project inherits the
// rules without per-repo opt-in.
//
// Like the hook/statusline installers, consent lives in the permission
// wizard (issue #577): install/uninstall run as the claude-code/instructions
// permission's grant/revoke effects, and PermissionService.Start() re-asserts
// the block on startup while granted — nothing is written before consent.
// Install/patch is idempotent and replaces only the managed block, preserving
// all surrounding user content byte-for-byte; uninstall removes only the
// managed block.
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
// The example MUST sit inside a fenced code block: Claude Code strips bare
// HTML comments from CLAUDE.md at context-injection time (verified live on
// v2.1.161 — the model quoted the rule with the example line missing), so an
// unfenced example never reaches the model. The fence protects it; the
// BEGIN/END sentinels don't need protection (they exist for this installer's
// file patching, not for the model). Per-agent equivalents
// (~/.codex/AGENTS.md, ~/.config/opencode/AGENTS.md, ~/.gemini/GEMINI.md)
// are documented in the issue but not written in v1.
//
// v2 (#604/#602): asks for the first marker BEFORE any tool call (drives the
// 0/N "estimating…" chip within seconds) and permits carrying the marker in
// a Bash description — text directly before a tool call and tool inputs both
// survive the claude ≥2.1.162 transcript text-drop that eats mid-task prose.
// patchManagedBlock's content compare upgrades installed v1 blocks in place
// on the next daemon start.
//
// v3 (#617): the v2 "you may also" phrasing under-binds in prose-less
// sessions — a live debugging session (ad880389) emitted the first 0/5
// marker and then nothing across 41 Bash calls and two prose blocks,
// pinning the chip at "estimating…" for its whole duration. v3 makes the
// per-phase update mandatory and names the Bash `description` field as the
// required carrier when no response text is coming. No extra reinforcement
// clause beyond that: the 2026-05-31 emission experiment found the lightest
// phrasing binds best, so v3 adds obligation, not length.
//
// v4 (anthropics/claude-code#65620 feedback): upstream diagnosis revised —
// the text-drop is server-side (the model composes prose in thinking and
// never emits the text block; onset ~2026-06-04, version pinning does not
// mitigate), and PRE-tool-call prose is exactly the vulnerable shape. v3's
// "first marker in your first response, right before your first tool call"
// rode the one path known to be lossy. v4 moves the first marker onto the
// Bash `description` carrier too — tool inputs reach the daemon via the
// PreToolUse hook (#604) regardless of text-block fate. End-of-turn text
// survives upstream, so the no-Bash fallback stays response text.
const managedTaskEtaBlock = taskEtaBeginSentinel + `
## Task progress markers (managed by Irrlicht)

As you work on a multi-step task, periodically emit a hidden progress marker
so tools can show a task-completion estimate. Emit it as an HTML comment,
and update it as you make progress:

` + "```" + `
<!-- {"marker":"irrlicht-eta","total_rounds":N,"completed_rounds":M} -->
` + "```" + `

` + "`total_rounds`" + ` is your estimate of the task's phases; ` + "`completed_rounds`" + `
is how many you've finished. Emit the first marker by appending it to the
` + "`description`" + ` of your first Bash call (never to the command itself).
After each phase you complete, emit the updated marker the same way:
appended to the ` + "`description`" + ` of the next Bash call you make, or in your
response text when no Bash call is coming.
` + taskEtaEndSentinel

// Sentinels delimiting the task-summary managed block (issue #738). Distinct
// from the task-eta pair so both blocks can coexist and be patched/removed
// independently.
const (
	taskSummaryBeginSentinel = "<!-- BEGIN IRRLICHT MANAGED BLOCK (task-summary) -->"
	taskSummaryEndSentinel   = "<!-- END IRRLICHT MANAGED BLOCK (task-summary) -->"
)

// managedTaskSummaryBlock instructs the agent to emit a one-line description
// of the current task so a human scanning sessions can tell what each is
// about — surfaced in both the waiting and ready states (issue #738). The
// summary is the stable companion to the task-eta progress marker: emitted
// once near the start, not churned per phase. The example marker MUST sit
// inside a fenced code block for the same reason as the task-eta block —
// Claude Code strips bare HTML comments from CLAUDE.md at injection time. The
// `+"`description`"+` carrier mirrors the eta block so the marker survives the
// claude ≥2.1.162 transcript text-drop via the PreToolUse hook.
const managedTaskSummaryBlock = taskSummaryBeginSentinel + `
## Task summary marker (managed by Irrlicht)

So a human scanning sessions can tell what each one is about, emit a one-line
summary of the overall task as a hidden marker, early in the task:

` + "```" + `
<!-- {"marker":"irrlicht-summary","summary":"<one sentence: what this task is about>"} -->
` + "```" + `

Emit it once near the start by appending it to the ` + "`description`" + ` of an
early Bash call (never to the command itself). Re-emit only if the task
fundamentally changes. Keep it under ~120 characters, plain prose, no secrets.
` + taskSummaryEndSentinel

// claudeMemoryPath returns the user-level Claude Code instruction file path.
func claudeMemoryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "CLAUDE.md"), nil
}

// ensureBlockInstalled writes-or-patches one managed block in
// ~/.claude/CLAUDE.md. Creates the file if missing. Idempotent: a
// byte-identical existing block is a no-op; a stale block is replaced in
// place; surrounding content is preserved byte-for-byte.
func ensureBlockInstalled(beginSentinel, endSentinel, block string) (bool, error) {
	path, err := claudeMemoryPath()
	if err != nil {
		return false, err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	patched, changed := patchManagedBlock(string(existing), beginSentinel, endSentinel, block)
	if !changed {
		return false, nil
	}
	return true, writeMemoryFile(path, patched)
}

// uninstallBlock removes one managed block from ~/.claude/CLAUDE.md, leaving
// all other content untouched. No-op when the file or block is absent.
func uninstallBlock(beginSentinel, endSentinel string) (bool, error) {
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
	stripped, changed := removeManagedBlock(string(existing), beginSentinel, endSentinel)
	if !changed {
		return false, nil
	}
	return true, writeMemoryFile(path, stripped)
}

// EnsureTaskEtaBlockInstalled writes-or-patches the task-eta managed block.
func EnsureTaskEtaBlockInstalled() (bool, error) {
	return ensureBlockInstalled(taskEtaBeginSentinel, taskEtaEndSentinel, managedTaskEtaBlock)
}

// UninstallTaskEtaBlock removes only the task-eta managed block.
func UninstallTaskEtaBlock() (bool, error) {
	return uninstallBlock(taskEtaBeginSentinel, taskEtaEndSentinel)
}

// EnsureTaskSummaryBlockInstalled writes-or-patches the task-summary managed
// block (issue #738) — installed alongside the task-eta block under the same
// instructions permission.
func EnsureTaskSummaryBlockInstalled() (bool, error) {
	return ensureBlockInstalled(taskSummaryBeginSentinel, taskSummaryEndSentinel, managedTaskSummaryBlock)
}

// UninstallTaskSummaryBlock removes only the task-summary managed block.
func UninstallTaskSummaryBlock() (bool, error) {
	return uninstallBlock(taskSummaryBeginSentinel, taskSummaryEndSentinel)
}

// applyInstructionBlocks installs both managed blocks — the grant effect of
// the instructions permission. Both are installed together so a single toggle
// governs all irrlicht-managed instruction text. Returns on the first error.
func applyInstructionBlocks() error {
	if _, err := EnsureTaskEtaBlockInstalled(); err != nil {
		return err
	}
	_, err := EnsureTaskSummaryBlockInstalled()
	return err
}

// removeInstructionBlocks removes both managed blocks — the revoke effect of
// the instructions permission.
func removeInstructionBlocks() error {
	if _, err := UninstallTaskEtaBlock(); err != nil {
		return err
	}
	_, err := UninstallTaskSummaryBlock()
	return err
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
func patchManagedBlock(existing, beginSentinel, endSentinel, block string) (string, bool) {
	beginIdx := strings.Index(existing, beginSentinel)
	if beginIdx >= 0 {
		rest := existing[beginIdx:]
		if endOff := strings.Index(rest, endSentinel); endOff >= 0 {
			end := beginIdx + endOff + len(endSentinel)
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
func removeManagedBlock(existing, beginSentinel, endSentinel string) (string, bool) {
	beginIdx := strings.Index(existing, beginSentinel)
	if beginIdx < 0 {
		return existing, false
	}
	endOff := strings.Index(existing[beginIdx:], endSentinel)
	if endOff < 0 {
		return existing, false
	}
	end := beginIdx + endOff + len(endSentinel)

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

// atomicWriteFile writes data to path via a temp file + rename, creating the
// parent dir. Shared by the settings.json (writeClaudeSettings) and CLAUDE.md
// (writeMemoryFile) writers so a hardening change applies to both — CLAUDE.md
// is the more sensitive, user-authored file and must not silently keep a
// weaker write.
func atomicWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeMemoryFile writes content atomically, creating ~/.claude if needed.
func writeMemoryFile(path, content string) error {
	return atomicWriteFile(path, []byte(content))
}
