// instructioninstaller.go manages the Irrlicht-managed emission rules in the
// user-level Claude Code instruction file ~/.claude/CLAUDE.md: the task-eta
// progress marker (issue #558), in its own BEGIN/END-delimited block. Two
// earlier blocks are retired and now actively uninstalled rather than written
// — task-summary (issue #738, retired in #1186) and task-question (issue #759,
// retired in #1944 because its carrier was the rendered response text, which
// this surface shows to the user verbatim). The block instructs the agent to
// emit an in-band marker; in the user-level file every project inherits the
// rule without per-repo opt-in.
//
// The invariant #1944 installs: no irrlicht marker may be instructed into text
// the user reads. The only carrier a block may name is a tool input the surface
// does not render as prose — today the Bash `description` field.
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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/atomicfile"
)

// ManagedBlockSentinelPrefix is the opening text every irrlicht-managed block
// in ~/.claude/CLAUDE.md begins with. Exported because CLAUDE.md is a file the
// USER writes in: "is any irrlicht block still in there" is a question asked
// from outside this package — by `irrlichd --uninstall-task-eta`'s end-to-end
// test, and by anyone auditing what irrlicht left behind — and the honest
// answer is the installer's own string rather than a second copy of it.
//
// Every sentinel below is built from it, so the two cannot drift; a hand-copied
// literal that fell behind would report "no irrlicht blocks" over a file that
// still had them.
const ManagedBlockSentinelPrefix = "<!-- BEGIN IRRLICHT MANAGED BLOCK ("

// managedBlockEndPrefix is the closing counterpart. Unexported: nothing outside
// needs to recognize an end sentinel on its own, since a well-formed block is
// always found by its begin.
const managedBlockEndPrefix = "<!-- END IRRLICHT MANAGED BLOCK ("

// sentinelSuffix closes both forms.
const sentinelSuffix = ") -->"

// Sentinels delimiting the managed block. Detection keys on these full
// strings — never on a generic `<!--` scan — so the marker example comment
// nested inside the block can't confuse block detection.
const (
	taskEtaBeginSentinel = ManagedBlockSentinelPrefix + "task-eta" + sentinelSuffix
	taskEtaEndSentinel   = managedBlockEndPrefix + "task-eta" + sentinelSuffix
)

// descriptionFieldLiteral is the backtick-quoted "`description`" carrier name
// referenced by the managed instruction block below. Since #1944 it is the only
// carrier a block may name: it is a tool input, not text the surface renders as
// prose to the user.
const descriptionFieldLiteral = "`description`"

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
// PreToolUse hook (#604) regardless of text-block fate. Its remaining
// no-Bash fallback was response text; v5 removes it.
//
// v5 (#1944): the fallback is deleted and the prohibition stated in its
// place. v4 kept response text as a carrier on the premise that the surface
// hides a bare HTML comment; that premise was probed in a real Claude Code
// TUI (claude 2.1.263, driven under tmux on 2026-09-11, panes recorded on
// issue #1944) and is false — the comment rendered verbatim both inline and
// after a blank line. Whether the Bash `description` carrier is itself
// hidden is NOT measured: the nested-`claude` probe that would settle it was
// refused by the sandbox both at triage and at implementation time, so the
// carrier v5 keeps is retained on the unverified assumption that the surface
// does not print it. `instructionmarkercarrier_test.go`'s
// TestInstalledInstructionBlocks_NeverInstructMarkerIntoRenderedText is the
// tripwire that keeps a rendered-text carrier from coming back by hand.
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
` + descriptionFieldLiteral + ` of your first Bash call (never to the command itself).
After each phase you complete, emit the updated marker the same way:
appended to the ` + descriptionFieldLiteral + ` of the next Bash call you make. That
field is the only carrier — never put the marker in your response text,
which the user reads.
` + taskEtaEndSentinel

// Sentinels delimiting the (now-retired) task-summary managed block. The
// irrlicht-summary agent instruction was removed in issue #1186 — everything
// the user sees now lives in the question headline, and TaskSummary /
// IntentHeadline have been decoded-but-never-rendered since #979. The
// sentinels stay so applyInstructionBlocks can UNINSTALL a block any prior
// version installed, cleaning it out of ~/.claude/CLAUDE.md on the next daemon
// start. The tailer's ScanTaskSummary is left tolerant so an older marker in a
// live transcript still parses harmlessly.
const (
	taskSummaryBeginSentinel = ManagedBlockSentinelPrefix + "task-summary" + sentinelSuffix
	taskSummaryEndSentinel   = managedBlockEndPrefix + "task-summary" + sentinelSuffix
)

// Sentinels delimiting the (now-retired) task-question managed block (issue
// #759), retired in #1944. The block asked the agent to put the marker "on its
// own line at the very end of your response", on the premise — written into
// this comment for three versions — that Claude Code hides a bare HTML comment
// at render time the way it strips one from CLAUDE.md at injection time.
//
// That premise was measured and is false. Probing a real Claude Code TUI
// (claude 2.1.263, driven under tmux on 2026-09-11; captured panes are on
// issue #1944) asked for the marker two ways: on the line after an anchor
// sentence, and after a blank line. Both rendered the comment verbatim in the
// pane, so the block was showing every irrlicht user the raw marker text. The
// blank-line probe is the one that matters: it rules out "the model glued the
// comment to the paragraph" as the explanation. The binary does ship
// HTML-comment-stripping regexes (visible in `strings` over the installed
// claude version), so the drop presumably applies on some other path; that the
// assistant-text path does not reach them is an inference from the probe, not
// something read out of the code.
//
// Nothing replaces the block, and waiting detection NARROWS rather than
// surviving intact. Behind PendingQuestionMarker in IsWaitingForUserInput sits
// PendingWaitingCue (#1150), but it is computed from tailer.WaitingScanWindow —
// the trailing MaxWaitingScanRunes (2 × 200 = 400) runes, not the whole message
// (core/pkg/tailer/parser.go's MaxWaitingScanRunes doc explains why the window
// is bounded at all). ScanTaskQuestion ran over the COMPLETE text. So the case
// the marker closed and the window does not is the one #1138 introduced it for:
// a question sitting further back than 400 runes in a long final message, with
// no cue in the tail — that turn now classifies ready rather than waiting. The
// Stop-hook path is no rescue; hooks.go uses the same window. What is lost is
// therefore that residual band plus the agent-authored headline wording; the
// headline itself still ships, composed daemon-side (#1186 topic prefix +
// extracted question). Accepted deliberately: the marker's own carrier was
// printing in every user's terminal on every question, which is a certain harm
// against an occasional one.
//
// The sentinels stay so applyInstructionBlocks can UNINSTALL a block any prior
// version installed, cleaning it out of ~/.claude/CLAUDE.md on the next granted
// daemon start — the same path task-summary took in #1186. Every parser is left
// tolerant (ScanTaskQuestion, scanValueForMarkers — which is the hook walk —
// and the replay path) so a live session still running an older CLAUDE.md, and
// every frozen replaydata transcript, keep parsing exactly as before.
const (
	taskQuestionBeginSentinel = ManagedBlockSentinelPrefix + "task-question" + sentinelSuffix
	taskQuestionEndSentinel   = managedBlockEndPrefix + "task-question" + sentinelSuffix
)

// claudeMemoryDisplayPath is the instruction file as the consent copy shows it
// — tilde-form, not the resolved absolute path, because the wizard text is read
// by a human and claudeMemoryPath() expands to whatever $HOME happens to be.
const claudeMemoryDisplayPath = "~/.claude/CLAUDE.md"

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

// ensureTaskEtaBlock writes-or-patches the task-eta managed block.
func ensureTaskEtaBlock() (bool, error) {
	return ensureBlockInstalled(taskEtaBeginSentinel, taskEtaEndSentinel, managedTaskEtaBlock)
}

// uninstallTaskEtaBlock removes only the task-eta managed block.
func uninstallTaskEtaBlock() (bool, error) {
	return uninstallBlock(taskEtaBeginSentinel, taskEtaEndSentinel)
}

// managedInstructionBlock is one irrlicht-managed block in ~/.claude/CLAUDE.md:
// the sentinels that delimit it, the content written between them, and the
// clause the consent copy uses to disclose it.
type managedInstructionBlock struct {
	// name is the identifier inside the sentinels, e.g. "task-eta" — the same
	// string a user finds in their own CLAUDE.md, so the copy can name the
	// block by what they will actually see there.
	name  string
	begin string
	end   string
	// content is the block written verbatim. Empty for a retired block: it is
	// removed, never written.
	content string
	// discloses says what this block asks the agent to emit, in consent prose.
	discloses string
}

// installedInstructionBlocks is the single declaration of what the instructions
// permission's Apply writes. applyInstructionBlocks iterates it and the consent
// copy is rendered from it (instructionsTouched/instructionsDetail), so the text
// the user grants against cannot come to describe a different set of blocks than
// the installer writes. Issue #1377 was exactly that drift — the hand-written
// copy named the retired task-summary block and never named task-question —
// arriving on the permission next door to #1356's, which is why the count and
// the list are derived here rather than restated there.
var installedInstructionBlocks = []managedInstructionBlock{
	{
		name:      "task-eta",
		begin:     taskEtaBeginSentinel,
		end:       taskEtaEndSentinel,
		content:   managedTaskEtaBlock,
		discloses: "a task-progress marker, which irrlicht reads to project a completion ETA",
	},
}

// retiredInstructionBlocks are blocks an earlier version installed that this one
// no longer uses. Apply removes them instead of writing them, which is why they
// are a separate list and carry no disclosure clause: the copy is rendered from
// the installed list alone, so a retired block cannot be presented as something
// irrlicht writes.
var retiredInstructionBlocks = []managedInstructionBlock{
	{name: "task-summary", begin: taskSummaryBeginSentinel, end: taskSummaryEndSentinel},
	{name: "task-question", begin: taskQuestionBeginSentinel, end: taskQuestionEndSentinel},
}

// instructionsTouched renders the Touches line of the instructions permission —
// the one-line summary the wizard row shows.
func instructionsTouched() string {
	var names []string
	for _, b := range installedInstructionBlocks {
		names = append(names, b.name)
	}
	// hookjson.EventList is the repo's one prose-list renderer for consent copy.
	// It is named for its first caller, but the serial comma it fixes is a
	// property of the copy, not of hooks: both disclosure contracts read names
	// back out of the rendered text, so a second copy of this switch is a second
	// place the punctuation can drift.
	return fmt.Sprintf("Maintains %d managed %s (%s) in %s",
		len(installedInstructionBlocks), agreeing(len(installedInstructionBlocks), "block", "blocks"),
		hookjson.EventList(names), claudeMemoryDisplayPath)
}

// instructionsDetail renders the Detail text behind the wizard's (i) expander.
// The retired-block cleanup is disclosed as a removal without naming the block:
// what the user needs to know is that Apply may delete an irrlicht block it did
// not write in this version, not which historical marker it was.
func instructionsDetail() string {
	var clauses []string
	for _, b := range installedInstructionBlocks {
		clauses = append(clauses, b.name+" — "+b.discloses)
	}
	n := len(installedInstructionBlocks)
	return fmt.Sprintf(
		"Writes %d irrlicht-managed %s (delimited by BEGIN/END sentinels) to %s, "+
			"instructing the agent to emit %s: %s. All surrounding file "+
			"content is preserved byte-for-byte, and a managed block an earlier "+
			"irrlicht version wrote but this one no longer uses is removed on the same "+
			"pass. Toggling off removes %s and any such retired block, and "+
			"nothing else (also available via the macOS Settings toggle).",
		n, agreeing(n, "block", "blocks"), claudeMemoryDisplayPath,
		agreeing(n, "a hidden marker", "hidden markers"),
		strings.Join(clauses, "; "), agreeing(n, "this block", "these blocks"))
}

// agreeing picks the form that agrees with n, so a single-block install does not
// ship broken grammar in the copy the user consents to. It used to be one
// function covering one phrase ("block"/"blocks"); #1944 retired task-question,
// took the installed set to one and made three phrases ungrammatical at once,
// which is the point at which one parameterised helper beats three near-copies.
func agreeing(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// applyInstructionBlocks installs the managed instruction blocks — the grant
// effect of the instructions permission. All are governed by a single toggle
// so it covers every irrlicht-managed instruction. Retired blocks (irrlicht-
// summary, issue #1186; irrlicht-question, issue #1944) are actively
// uninstalled here rather than installed, so a CLAUDE.md a prior version wrote
// is cleaned up on the next granted daemon start. Returns on the first error.
func applyInstructionBlocks() error {
	for _, b := range installedInstructionBlocks {
		if _, err := ensureBlockInstalled(b.begin, b.end, b.content); err != nil {
			return err
		}
	}
	for _, b := range retiredInstructionBlocks {
		if _, err := uninstallBlock(b.begin, b.end); err != nil {
			return err
		}
	}
	return nil
}

// removeInstructionBlocks removes all managed blocks — the revoke effect of
// the instructions permission.
func removeInstructionBlocks() error {
	_, err := UninstallInstructionBlocks()
	return err
}

// UninstallInstructionBlocks removes every managed instruction block —
// installed and retired alike — reporting whether ~/.claude/CLAUDE.md changed.
// It backs both the permission's revoke effect and the `irrlichd
// --uninstall-task-eta` escape hatch, driven by the same two inventories Apply
// is, so the two cleanup paths cannot come to sweep different sets. They had:
// the CLI carried its own hand-written pair (task-eta + task-summary) and so
// left the task-question block #759 installs sitting in the user's file with no
// way to remove it, while printing "No irrlicht managed blocks found" (#1377).
//
// Its (bool, error) shape is also agent.ManagedUserFile.Uninstall's: the
// declaration the recorder reads needs an undo callable from outside the
// adapter, independent of the permission service's Remove wiring (#1383).
func UninstallInstructionBlocks() (bool, error) {
	changed := false
	for _, b := range allInstructionBlocks() {
		modified, err := uninstallBlock(b.begin, b.end)
		if err != nil {
			return changed, err
		}
		changed = changed || modified
	}
	return changed, nil
}

// allInstructionBlocks is every block this package knows a sentinel for, in a
// fresh slice — never append-onto-a-package-var, which would write through the
// inventory's backing array whenever it had spare capacity.
func allInstructionBlocks() []managedInstructionBlock {
	all := make([]managedInstructionBlock, 0, len(installedInstructionBlocks)+len(retiredInstructionBlocks))
	all = append(all, installedInstructionBlocks...)
	return append(all, retiredInstructionBlocks...)
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

// writeMemoryFile writes content atomically, creating ~/.claude if needed.
// Shared with the settings.json writer (writeClaudeSettings in
// hookinstaller.go) via atomicfile.WriteFile so a hardening change applies to
// both — CLAUDE.md is the more sensitive, user-authored file and must not
// silently keep a weaker write.
func writeMemoryFile(path, content string) error {
	return atomicfile.WriteFile(path, []byte(content))
}
