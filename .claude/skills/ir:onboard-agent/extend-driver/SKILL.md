---
name: ir:onboard-agent/extend-driver
description: >
  The verb that turns a `driver_gap` into queued, owned work instead of a
  frozen cell. Takes one (agent, primitive) and ports the missing step
  type into `scripts/drive-<agent>-interactive.sh` from the claudecode /
  codex tmux-REPL reference drivers — adapting tmux input + turn-detection
  + transcript export to the agent — then re-lints the column and reports
  which cells the new primitive unblocks. The dispatcher then sends each
  unblocked cell back through `implement` to record it. Invoked as
  `/ir:onboard-agent extend-driver <agent> <primitive>`.
---

# extend-driver

> **You are running as a focused subagent with no parent context.**
> Everything you need is here or in the docs named below. Don't ask the
> dispatcher anything. This stage edits a driver script and commits it;
> it does NOT drive a live agent or record (that's `implement`'s job).
> When done, return only the "Return contract" block.

## Why this verb exists

A `driver_gap` is **fixable tooling work, not an inapplicable cell.** The
recipe is sound and the daemon can observe the behavior — the only thing
missing is a step type the agent's interactive driver doesn't implement
yet (`keys`, `interrupt`, `reset_session`, `restart`, `resume`,
`sigkill`, `exit_clean`, …). Before this verb existed, `assess` said
"extend the driver, don't freeze the cell" but the only stage that could
act on a gap (`implement`) marked the cell `applicable: false` and called
extension "out of scope" — so "extend the driver first" pointed at a
stage that didn't exist and gapped cells froze permanently. This verb is
that stage. The `driver_gap` route now produces **queued driver work**
that ends in a recording.

## Inputs

- `<agent>` — adapter slug (`claudecode`, `codex`, `pi`, `aider`,
  `opencode`). claudecode/codex are the reference drivers and rarely need
  this.
- `<primitive>` — the missing step type named in the assessment's
  `driver_capability: gap:<primitive>` (e.g. `reset_session`, `keys`,
  `interrupt`, `sigkill`). One primitive per invocation; a primitive
  often unblocks several cells at once.

## Preconditions

1. **The gap is real, not a false positive.** Re-run the lint to confirm
   the driver genuinely lacks the primitive (not a `slash`-vs-`keys`
   mislabel — an inline-argument slash command like `/model <id>` is
   `slash`, not `keys`; see `recipe/SKILL.md` "Slash command vs picker
   navigation"):

   ```bash
   SK=.claude/skills/ir:onboard-agent
   source $SK/scripts/lib/recipe-lint.sh
   driver_step_types_from_file $SK/scripts/drive-<agent>-interactive.sh
   # <primitive> must NOT appear in that list
   ```

2. **A reference implementation exists.** The primitive must already be
   handled by `drive-claudecode-interactive.sh` or
   `drive-codex-interactive.sh` (the full tmux-REPL drivers). Every gap
   the opencode/aider/pi columns hit (`keys`, `interrupt`, `restart`,
   `sigkill`, `reset_session`, `resume`) has a claudecode reference. If
   no reference exists, the primitive is a NEW grammar element — stop and
   return `needs_design` (add it to `step-grammar.md` + claudecode first).

## Steps

### 1. Read the reference `case` arm

Find the primitive's handler in the reference driver and read it end to
end — input mechanism, turn/effect detection, and any contract files it
writes:

```bash
SK=.claude/skills/ir:onboard-agent
grep -n '<primitive>)' $SK/scripts/drive-claudecode-interactive.sh \
                       $SK/scripts/drive-codex-interactive.sh
```

Note what it depends on: tmux session handle, the per-agent turn-done
poll (transcript `stop_reason` for claudecode, rollout `task_complete`
for codex, SQLite `step-finish` for opencode), and the multi-session
contract (`session.uuids` / `transcript.paths`) for primitives that mint
a new session (`reset_session`, `restart`, `resume`, `fork`).

### 2. Port it into the target driver

Add the `case "$type"` arm to `scripts/drive-<agent>-interactive.sh`,
adapting the three agent-specific seams:

- **Input** — tmux `send-keys` for a TUI driver; for a headless-per-turn
  driver (opencode) the primitive almost always needs the **live-TUI
  path** (`run_live`, established for opencode in #495): headless
  `opencode run` can't deliver in-REPL slash commands or signals, so a
  recipe carrying a TUI primitive must drive the agent under tmux. Extend
  the existing `run_live` dispatch rather than the headless one.
- **Turn / effect detection** — reuse the driver's own `wait_turn`
  mechanism; a `reset_session`/`restart`/`resume` must detect the NEW
  session id (poll the agent's store/transcript for a fresh session
  row/UUID), not the old one.
- **Contract files** — if the primitive mints a new session, emit the
  multi-session contract (`session.uuids`, `transcript.paths`) exactly
  like codex's `reset_session`, so `run-cell.sh` +
  `curate-lifecycle-fixture.sh` capture BOTH the pre- and post-boundary
  sessions.

Keep the arm's shape and comment style consistent with the reference and
with the driver's existing arms. Don't refactor unrelated code.

### 3. Declare the primitive ELICITED

Add `<primitive>` to the driver's top-level `DRIVE_ELICITS` constant
(#508 #4) so recipe-lint's semantic check treats the new arm as genuinely
produced, not just accepted. A case arm WITHOUT a matching `DRIVE_ELICITS`
entry would still trip a `semantic_gap` (exit 4) and block recording.

### 4. Verify the driver still parses + the gap closed

```bash
SK=.claude/skills/ir:onboard-agent
bash -n $SK/scripts/drive-<agent>-interactive.sh    # syntax
source $SK/scripts/lib/recipe-lint.sh
driver_step_types_from_file $SK/scripts/drive-<agent>-interactive.sh \
  | grep -qx '<primitive>' && echo "case arm handled (grammar)"
driver_elicits_from_file $SK/scripts/drive-<agent>-interactive.sh \
  | grep -qx '<primitive>' && echo "declared elicited (semantic)"
```

If the driver has a `*_test.sh` sibling under `scripts/lib/` or a smoke
path, run it.

### 4. Commit the driver change (driver only)

```bash
git add .claude/skills/ir:onboard-agent/scripts/drive-<agent>-interactive.sh
git commit -m "feat(onboard): teach <agent> driver the <primitive> step type"
git rev-parse --short HEAD
```

> Repo commit convention: end commit messages with the trailer
> `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.

Commit ONLY the driver here. Recording the now-unblocked cells is
`implement`'s job (a separate commit per cell), dispatched by the parent
after you return.

### 5. Re-lint the column → report unblocked cells

A primitive usually unblocks more than the one cell that triggered it.
Find every cell for this agent whose recipe needed it and now lints
clean:

```bash
SK=.claude/skills/ir:onboard-agent
for s in $(jq -r --arg a "<agent>" 'select(.agents[$a]) | .name' \
             replaydata/scenarios/*.json); do
  if $SK/scripts/lib/recipe-lint.sh "$s" <agent> >/dev/null 2>&1; then
    echo "unblocked: $s"
  fi
done
```

Report the cells whose ONLY remaining gap was this primitive — those are
ready for `implement` now.

## Return contract

Return ONLY this (≤6 lines), no transcripts. Shared-field semantics
(`commit_sha`, `unblocked_cells`, the envelope rules) are defined once in
[`../return-contract.md`](../return-contract.md):

```
status: extended | needs_design | false_gap
primitive: <primitive>   agent: <agent>
commit_sha: <short sha>            # the driver commit ("n/a" for needs_design/false_gap)
unblocked_cells: <comma-separated scenario names now ready for implement, or "none">
notes: <one sentence — what was ported and from which reference, or why not>
```

Status meanings:

- **`extended`** — the primitive is now handled; the driver is committed;
  `unblocked_cells` lists what the dispatcher should send through
  `implement` next.
- **`needs_design`** — the primitive has no claudecode/codex reference;
  it's a new grammar element to design in `step-grammar.md` + the
  reference driver first. Nothing committed.
- **`false_gap`** — the lint was a false positive (e.g. a `slash`
  mislabel); no driver change needed. Note the correct step type so the
  recipe can be fixed.

## Anti-patterns

- **Don't record here.** This stage extends + commits the driver only;
  the dispatcher routes the unblocked cells to `implement`.
- **Don't mark any cell `applicable: false`.** A driver gap is fixable
  tooling work — that's the whole point of this verb. `applicable: false`
  is for cells the agent fundamentally can't do.
- **Don't invent a new primitive.** Only port step types that already
  have a claudecode/codex reference; a genuinely new one is
  `needs_design`.
- **Don't refactor the driver.** Add the one arm, keep everything else
  byte-stable, so the diff is reviewable and other cells don't regress.
