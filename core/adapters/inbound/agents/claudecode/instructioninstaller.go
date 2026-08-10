// instructioninstaller.go manages the Irrlicht-managed emission rules in the
// user-level Claude Code instruction file ~/.claude/CLAUDE.md: the task-eta
// progress marker (issue #558) and the task-question marker (issue #759), each
// in its own BEGIN/END-delimited block. The task-summary marker (issue #738)
// was retired in #1186 — its block is now actively uninstalled, not written.
// The blocks instruct the agent to emit in-band markers; with them in the
// user-level file every project inherits the rules without per-repo opt-in.
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
// referenced by the managed instruction blocks below.
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
` + descriptionFieldLiteral + ` of your first Bash call (never to the command itself).
After each phase you complete, emit the updated marker the same way:
appended to the ` + descriptionFieldLiteral + ` of the next Bash call you make, or in your
response text when no Bash call is coming.
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

// Sentinels delimiting the task-question managed block (issue #759). Distinct
// from the eta/summary pairs so all three blocks coexist and are patched or
// removed independently.
const (
	taskQuestionBeginSentinel = ManagedBlockSentinelPrefix + "task-question" + sentinelSuffix
	taskQuestionEndSentinel   = managedBlockEndPrefix + "task-question" + sentinelSuffix
)

// managedTaskQuestionBlock instructs the agent to emit a terse one-line version
// of the question it is blocked on when it ends a turn waiting on the user
// (issue #759) — the preferred source for the surfaced waiting-state headline.
// Unlike the eta/summary markers, a pending question has no following Bash call
// to carry it, so the marker rides the end-of-turn response text — the one shape
// that survives the claude ≥2.1.162 transcript text-drop (end-of-turn text is
// not dropped; only mid-task pre-tool-call prose is). The example marker MUST
// sit inside a fenced code block for the same reason as the other blocks —
// Claude Code strips bare HTML comments from CLAUDE.md at injection time. The
// daemon always falls back to compacting the raw last-assistant text, so the
// marker is a refinement, not a requirement.
//
// v2 (#1142): the v1 guidance ("plain prose, under ~70 chars") produced
// context-free headlines — the raw question with no hint of what task it
// belongs to, cryptic to anyone scanning sessions. v2 asks for a
// context → state → ask structure with good/bad examples, and relaxes the
// hard ~70 cap to a soft ~90. That fuller headline survives to the UI: the
// daemon-side compaction caps at 200 runes (not 70) since #979, and
// ExtractQuestionSnippet keeps a single such sentence whole — the em-dash,
// semicolon and colon separators used in the examples are not sentence
// terminators (verified against core/domain/session's splitter). Only the
// example placeholder and prose change; the marker key and sentinels are
// untouched, so patchManagedBlock upgrades installed v1 blocks in place on
// the next daemon start.
//
// v3 (#1186): the headline shape is now the explicit "<3–5 word topic>:
// <question>" — a chat-conversation-title topic, then a colon, then the ask.
// This is the same shape the daemon composes for the no-marker fallback path
// (a topic derived from the first user prompt + the extracted question), so
// marker and heuristic paths surface one consistent form. The marker carries
// the whole line to question_headline verbatim (issue #1186 routes an
// agent-authored marker through the verbatim compaction kind — no
// sentence-selection that would drop the topic off a two-sentence marker).
// Only the placeholder and examples change; the marker key and sentinels stay
// put, so patchManagedBlock upgrades installed v1/v2 blocks in place.
const managedTaskQuestionBlock = taskQuestionBeginSentinel + `
## Pending-question marker (managed by Irrlicht)

When you end your turn by asking the user a question, also emit a hidden
one-line version of that question so tools can show a terse headline:

` + "```" + `
<!-- {"marker":"irrlicht-question","question":"<3–5 word topic>: <what happened — the choice>"} -->
` + "```" + `

Emit it on its own line at the very end of your response, only when you are
actually waiting on the user.

Start with a 3–5 word topic (like a chat conversation title), then ": ", then
the question. The topic is the context; what follows the colon is the state
then the ask — context, then state, then the ask — so someone who never saw
the session understands it at a glance. Keep it high-signal; aim for ~90
characters, and when clarity and brevity conflict, choose clarity.

- Bad "Should I proceed?" → Good "DB migration: drops users.legacy_id — run it or keep the column?"
- Bad "Which approach?" → Good "Auth refactor: 2 endpoints still untested — ship now or add tests first?"
- Bad "Yes or no?" → Good "PR #482 review: 1 blocking finding left — fix now or merge and follow up?"
` + taskQuestionEndSentinel

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

// ensureTaskQuestionBlock writes-or-patches the task-question managed block
// (issue #759) — installed alongside the task-eta block under the same
// instructions permission.
func ensureTaskQuestionBlock() (bool, error) {
	return ensureBlockInstalled(taskQuestionBeginSentinel, taskQuestionEndSentinel, managedTaskQuestionBlock)
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
	{
		name:    "task-question",
		begin:   taskQuestionBeginSentinel,
		end:     taskQuestionEndSentinel,
		content: managedTaskQuestionBlock,
		discloses: "a one-line version of the question it is waiting on, so a human can tell " +
			"what a session is blocked on",
	},
}

// retiredInstructionBlocks are blocks an earlier version installed that this one
// no longer uses. Apply removes them instead of writing them, which is why they
// are a separate list and carry no disclosure clause: the copy is rendered from
// the installed list alone, so a retired block cannot be presented as something
// irrlicht writes.
var retiredInstructionBlocks = []managedInstructionBlock{
	{name: "task-summary", begin: taskSummaryBeginSentinel, end: taskSummaryEndSentinel},
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
		len(installedInstructionBlocks), blockNoun(len(installedInstructionBlocks)),
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
	return fmt.Sprintf(
		"Writes %d irrlicht-managed %s (each between BEGIN/END sentinels) to %s, "+
			"instructing the agent to emit hidden markers: %s. All surrounding file "+
			"content is preserved byte-for-byte, and a managed block an earlier "+
			"irrlicht version wrote but this one no longer uses is removed on the same "+
			"pass. Toggling off removes these blocks and any such retired block, and "+
			"nothing else (also available via the macOS Settings toggle).",
		len(installedInstructionBlocks), blockNoun(len(installedInstructionBlocks)),
		claudeMemoryDisplayPath, strings.Join(clauses, "; "))
}

// blockNoun agrees the noun with the count so a future single-block install
// does not ship broken grammar in the copy the user consents to.
func blockNoun(n int) string {
	if n == 1 {
		return "block"
	}
	return "blocks"
}

// applyInstructionBlocks installs the managed instruction blocks — the grant
// effect of the instructions permission. All are governed by a single toggle
// so it covers every irrlicht-managed instruction. Retired blocks (the
// irrlicht-summary one, issue #1186) are actively uninstalled here rather than
// installed, so a CLAUDE.md a prior version wrote is cleaned up on the next
// granted daemon start. Returns on the first error.
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
