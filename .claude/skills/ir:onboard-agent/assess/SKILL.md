---
name: ir:onboard-agent/assess
description: >
  Stage 1 of the cell lifecycle. Researches one (agent, scenario)
  cell (or a column / row of cells) and writes structured assessment
  artifacts. Three forms:
  `/ir:onboard-agent assess <agent> <scenario>` writes a rich
  `assessment.json` under `replaydata/agents/<agent>/scenarios/<scenario>/`.
  `/ir:onboard-agent assess --column <agent>` writes a candidate
  matrix column at `.specs/agent-assess-<agent>.json` covering every
  scenario.
  `/ir:onboard-agent assess --row <scenario>` writes a candidate
  matrix row at `.specs/scenario-assess-<scenario>.json` covering
  every adapter. Re-runs overwrite silently — git preserves history.
---

# assess (Agent #2 / Stage 1)

> **You are running as a focused subagent with no parent context.**
> Everything you need is in this file and the repo. Do the research
> YOURSELF (you have web search + file access) — don't bounce the work
> back to the dispatcher. The single-cell form spends no API tokens on
> agent CLIs and runs no recording. When done, return only the summary
> in the "Return contract" section.

Produces the artifact for **Stage 1 of the cell lifecycle**: a
structured, dated, sourced record of "does this agent support this
scenario, and can irrlicht observe it." Three scopes share the same
verb:

- **Single cell** (deep, rich) — `assess <agent> <scenario>`. Writes
  a committed `assessment.json` with full `body` (markdown
  reasoning), `caveats`, and `sources`. The viewer renders this on
  the cell detail page.
- **Column** (one agent, all scenarios) — `assess --column <agent>`.
  Writes a candidate matrix column to `.specs/agent-assess-<agent>.json`
  (gitignored, maintainer-owned) with short per-scenario verdicts.
  Used at agent-onboarding time and on each agent version bump.
- **Row** (one scenario, all adapters) — `assess --row <scenario>`.
  Writes a candidate matrix row to `.specs/scenario-assess-<scenario>.json`
  with short per-adapter verdicts. Used when a new scenario lands
  in `.specs/agent-scenarios.md` and you want a first-pass column
  across all 5 adapters.

The column and row forms produce CANDIDATES for the maintainer to
review and transcribe into `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`.
The single-cell form is the source-of-record artifact the viewer
displays — typically you use it AFTER a column or row scan flagged
the cell as interesting (low confidence, surprising verdict, etc.).

> Stage 1 sits at the head of the pipeline. The verdict here
> determines whether Stage 2 (recipe) proceeds. Other stages:
> [`../recipe/SKILL.md`](../recipe/SKILL.md),
> [`../spec/SKILL.md`](../spec/SKILL.md),
> [`../record/SKILL.md`](../record/SKILL.md),
> [`../validate/SKILL.md`](../validate/SKILL.md). End-to-end
> walkthrough → [`../cell-lifecycle.md`](../cell-lifecycle.md).

## Working approach

A cell's `assessment.json` is the source-of-record for "what we
believe about this cell right now, and why." A maintainer transcribes
the verdict + a one-line note into the rollup matrix
(`.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`), but the artifact is what
the viewer renders on the detail page and what later audits read.

Three rules:

1. **Honest verdicts.** `yes` only when the docs/code state the
   behavior explicitly. `partial` only when there's a concrete
   carve-out (cite it). `no` when something fundamental blocks the
   scenario (cloud-only, missing API, etc.). `unknown` is allowed
   and preferred over a guess.
2. **Caveats over downgrades.** If the canonical spec is met but a
   narrow detail is gappy, keep the verdict honest (`yes`) and put
   the gap in `caveats`. The viewer surfaces caveats as a yellow
   callout — they're visible without misrepresenting the matrix.
3. **Cite primary sources.** Agent docs, official changelog, agent
   source files, irrlicht adapter source. Tutorials and blog posts
   don't count. Even an `unknown` verdict cites what you searched.

## Invocation

```
/ir:onboard-agent assess <agent> <scenario-id>    # single cell — rich assessment.json
/ir:onboard-agent assess --column <agent>         # one agent, all scenarios — candidate column
/ir:onboard-agent assess --row <scenario>         # one scenario, all adapters — candidate row
```

- `<agent>` — adapter slug (`claudecode`, `codex`, `pi`, `aider`,
  `opencode`).
- `<scenario-id>` — coverage id from `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`
  (e.g. `checkpoint-rewind`, `cloud-background-agent`).

Worked examples committed:

- `claudecode × checkpoint-rewind` — `yes/yes` with two caveats
  (rewind invisible to file-watching; context-utilization drift).
  Read this one when the scenario is spec-compliant but has known
  observability gaps.
- `claudecode × cloud-background-agent` — `yes/no` — agent has the
  feature, irrlicht has no transport for it. Read this one when the
  blocker is at the adapter layer, not the agent.

## Output

A single file at:

```
replaydata/agents/<agent>/scenarios/<scenario>/assessment.json
```

Shape (per `cell-lifecycle.md` Stage 1):

```json
{
  "schema_version": 1,
  "scenario_id": "<scenario-id>",
  "agent": "<agent-slug>",
  "assessed_at": "2026-05-17T00:00:00Z",
  "agent_supports":   "yes" | "no" | "partial" | "unknown",
  "irrlicht_observes": "yes" | "no" | "partial" | "unknown" | "n/a",
  "confidence": 0.85,
  "body": "## Verdict\n\nagent_supports: yes\nirrlicht_observes: yes\n\n## Reasoning\n\n...",
  "caveats": [
    "One short sentence per caveat — viewer renders as yellow callout."
  ],
  "sources": [
    {"kind": "url",  "ref": "https://...",     "note": "..."},
    {"kind": "file", "ref": "path/to/file.go", "note": "..."}
  ]
}
```

Overwrite the existing file silently when re-running. Git preserves
the previous version.

## Steps (single cell)

The steps below cover the **single-cell** form. For
[`--column`](#column-and-row-batch-modes) and `--row`, jump to
"Batch modes" further down.

### Step 1 — Slice the cell

```
.claude/skills/ir:onboard-agent/scripts/slice-cell.sh <scenario-id> <agent>
```

One call prints exactly three things and nothing else — the
`scenarios.json` entry, the `### <scenario-id>` scenario-meanings block,
and the `agent-scenarios-coverage.json` cell — so you slice instead of
reading the whole catalogs. Step 2 reads the coverage cell from this same
output.

From the scenario-meanings block capture all five fields (Essence,
User-observable signal, Primitive exercised, Not to be confused with,
Conceptual flow). The **User-observable signal** lines are the candidate
assertions you judge `irrlicht_observes` against; **Primitive exercised**
is the canonical capability-key anchor (use it in Step 4 to pick the
right `capabilities.json` key).

`slice-cell.sh` exits non-zero if the scenario is missing from
`scenarios.json`/`scenario-meanings.md` — the row hasn't been created, so
STOP and surface "run `scenario-create <slug>` first". If a richer
`.specs/agent-scenarios.md` happens to be present (gitignored, usually
absent), read its matching `### Feature:` block for extra precision; the
slice is sufficient without it.

► **Verify before moving on:**
- [ ] Ran `slice-cell.sh` and captured the **Primitive exercised** field
  verbatim from the scenario-meanings block.
- [ ] Captured every User-observable signal (and any `.specs/` Expected:
  bullet, if present). Each is a candidate assertion for
  `irrlicht_observes`.

### Step 2 — Read the current matrix verdict

Use the coverage cell already printed by `slice-cell.sh` in Step 1
(`agent-scenarios-coverage.json → .scenarios[].coverage[<agent>]`). Or
`curl http://127.0.0.1:8765/api/catalog | jq` against a running daemon.

You're not bound by the existing verdict — the whole point of
re-assessing is to confirm or refine. But knowing the prior verdict
+ notes tells you whether your conclusion ratifies the matrix or
flips it. The report at Step 6 calls this out so the maintainer
knows whether to update `.specs/`.

If no entry exists for this `(agent, scenario)`, the cell is
unmapped — still produce the assessment, and flag in the report
that the maintainer should add a column.

► **Verify before moving on:**
- [ ] Current verdict captured (or "no entry"). Don't proceed if you
  haven't tried — drift detection depends on the comparison.

### Step 2.5 — Confirm the agent's surface BEFORE declaring `agent_supports: no`

If you're about to write `agent_supports: no`, the bar is higher than
"`agent --help` doesn't mention it." Many features live inside the
REPL as slash commands or hooks, not as top-level CLI flags. Before
locking in `no`:

1. **`strings <agent-binary>` for the feature's keywords.** Run e.g.
   `strings $(which claude) | grep -iE "<feature>|/<slash>"` for any
   string the feature would mention — slash command syntax, telemetry
   event names, preamble constants, error messages. This catches
   REPL-only features that `--help` never lists.
2. **Search the agent's official docs / changelog / source repo** for
   the same keywords. Vendor docs sometimes lag the binary; the
   binary's strings are authoritative for "what shipped."
3. **Scan obvious related slash commands.** `/help`, `/cost`,
   `/model`, `/agents`, `/clear` are well known; less obvious ones
   (`/goal`, `/compact`, `/init`, `/rewind`) are easy to miss when
   you're only reading `--help`.

**Worked example (the canonical miss):** the 2026-05-17 batch
assessment wrote `claudecode/autonomous-loop: agent_supports: no`
because `claude --help` lacks any `--auto` / `--goal` flag. A user
correction pointed at the live `/goal` slash command. Re-running
`strings $(which claude) | grep -iE "goal|autonomous"` surfaced
`/goal <condition>`, `/goal clear`, `/goal active`,
`AUTONOMOUS_LOOP_PREAMBLE`, `goal_status` / `goal_set` / `goal_met`
telemetry events, and the Stop-hook re-prompt mechanism — all of
which made the correct verdict `agent_supports: yes`,
`irrlicht_observes: partial`. The fix landed in commits `3e33768`
and `6898561`; corrected assessments live at
`replaydata/agents/claudecode/scenarios/autonomous-loop/assessment.json`
and `.../autonomous-loop-iteration-limit/assessment.json`.

If the strings scan still finds nothing, THEN `agent_supports: no` is
honest — and the `sources` array MUST cite the binary scan that
came up empty so future audits don't re-litigate the same question.

► **Verify before moving on:**
- [ ] Ran a `strings` (or equivalent) scan against the agent binary
  for the feature's keywords. Empty result is fine but cite it.
- [ ] Checked vendor docs / changelog for the feature.
- [ ] For REPL-driven agents, considered whether the feature is a
  slash command rather than a CLI flag.

### Step 3 — Read the adapter's transport

```
core/adapters/inbound/agents/<agent>/
  config.go              # ProcessName, TranscriptFilename, Capabilities, DiscoverPID
  capabilities.json      # feature flags per agent
  <parser source>        # what events the daemon can emit from this agent
```

This is how you judge `irrlicht_observes`. The verdict isn't "is this
theoretically observable" — it's "does the daemon, AS IT EXISTS
TODAY, emit the lifecycle events the spec requires?"

Concrete check: for each Expected: bullet from Step 1, ask "what
event in `events.jsonl` would prove this?" Then ask "does the
adapter's parser produce that event?" If yes for all bullets →
`irrlicht_observes: yes`. If yes for some → `partial` (and the
specific gap goes in caveats). If no transport at all (e.g. cloud
session with no local file) → `no`.

The `cloud-background-agent` assessment is the canonical example of
a clear `no` derived from transport mismatch (no local file ⇒ no
FilesUnderRoot watch can see the session).

► **Verify before moving on:**
- [ ] Read `config.go` and identified the `Source` variant
  (FilesUnderRoot / FilesUnderCWD / ProcessOwnedStore).
- [ ] Read the parser source enough to know which event kinds it
  emits.
- [ ] For each Expected: bullet, you can name the event(s) the daemon
  would emit (or honestly say "no event would prove this").

### Step 4 — Research the verdict (you do this directly)

You are the research agent. Using the spec text (Step 1), the matrix
verdict (Step 2), and the adapter transport read (Step 3):

- Read the agent's official docs site, changelog/release notes, and
  source files relevant to the feature. Use web search + fetch.
- Decide `agent_supports` honestly (see the three rules above and the
  Step 2.5 strings-scan guard before any `no`).
- Derive `irrlicht_observes` from the transport read (Step 3) and what
  you learn about the agent's emission shape. The agent emitting an
  event ≠ the daemon parsing it — ground this claim in `config.go` +
  the parser source, not docs alone.
- Map the **Primitive exercised** field from Step 1 to the matching key
  in `replaydata/agents/<agent>/capabilities.json`. Derive the key
  directly from the primitive text and the existing keys — do NOT infer
  it from general knowledge. Confirm (or correct) that key.
- Identify caveats, naming each with the pattern it fits
  (feature-invisible-but-spec-compliant; metric-drift-downstream — both
  defined in `cell-lifecycle.md`).
- Calibrate `confidence` (`0.9+` only when behavior is documented
  explicitly; `0.7–0.85` is the honest band for a thorough multi-source
  read).
- Cite at least one source per claim — URL for docs/changelog, `file`
  with a path for source. The two committed worked examples
  (`checkpoint-rewind`, `cloud-background-agent`) are anchors for prose
  style.

Forbidden:

- Made-up sources.
- Downgrading verdicts to "be safe" — the authoring rule is honest
  verdicts + caveats, not defensive `partial`s.
- Reasoning purely from general LLM knowledge — every claim needs a
  primary source.

If the research is broad enough to threaten your own context window,
you MAY fan a single `general-purpose` Agent out for the docs/source
sweep and synthesize its findings — but the verdict and the written
artifact are yours.

### Step 5 — Synthesize + write

Assemble the JSON document conforming to the "Output" schema. Sanity-check:

- `schema_version: 1`.
- `scenario_id`, `agent`, `assessed_at` correct.
- `agent_supports` and `irrlicht_observes` are one of the allowed
  enum values.
- `confidence` is a number in `[0, 1]`.
- `body` is non-empty markdown that explains the verdict.
- `caveats` is an array (may be empty).
- `sources` is an array with at least one entry; each entry has
  `kind`, `ref`, `note`.

If any check fails, fix the document before writing (re-run the
research for the specific gap if needed).

Write the final JSON to
`replaydata/agents/<agent>/scenarios/<scenario>/assessment.json`,
overwriting silently if a file exists. Use 2-space indent for
readability — the viewer parses any valid JSON shape.

### Step 6 — Return contract

Compute `applicable` from the two verdicts — this is the signal
`implement` keys on to decide whether to proceed:

| agent_supports | irrlicht_observes | `applicable` | meaning |
|---|---|---|---|
| `yes` / `partial` | `yes` / `partial` | `yes`  | proceed to `implement` |
| `no`              | (any)             | `no`   | agent lacks the feature — pipeline frozen |
| `yes` / `partial` | `n/a` / `no`      | `n/a`  | agent has it, irrlicht can't observe it — no recording |
| `unknown`         | (any)             | `n/a`  | inconclusive — re-assess after the gap named in `body` closes |

Return ONLY this (≤5 lines), no transcripts:

```
verdict: <agent_supports> / <irrlicht_observes> (confidence <n>)
applicable: yes | no | n/a
summary: <one sentence — the load-bearing reason for the verdict>
wrote: replaydata/agents/<agent>/scenarios/<scenario>/assessment.json
matrix_drift: none | matrix says <old>/<old>, coverage row should update
```

**If `applicable` is `no` or `n/a`, stop here.** There is no recipe or
recording follow-up — the assessment.json documents *why* the cell is
frozen and what would unblock it. The maintainer transcribes the
verdict into the coverage matrix; this stage never writes
`agent-scenarios-coverage.json`.

## Column and row batch modes

The two batch forms produce **candidate** matrix scans instead of
rich per-cell artifacts. They're the right tool when the goal is
"first-pass verdicts across many cells, fast, for the maintainer to
review before committing." Both go through `assess/run-batch.sh`
under the hood.

### `--column <agent>` workflow

```
/ir:onboard-agent assess --column <agent>
```

Produces `.specs/agent-assess-<agent>.json` — one verdict entry per
scenario in the canonical catalog. Steps:

1. **Gather inputs.**
   ```bash
   .claude/skills/ir:onboard-agent/assess/run-batch.sh prepare --column <agent>
   ```
   Prints to stdout the agent slug, catalog paths, the canonical
   scenario ID list, and the inline column-mode prompt
   (`assess/prompts/column.md`).
2. **Dispatch a research subagent** (`Agent` tool,
   `subagent_type: general-purpose`) with the full output of Step 1
   as the prompt. The subagent reads the agent's docs site,
   changelog, source, and decides a verdict for every scenario in
   the canonical list — missing or extra IDs are a hard failure.
   Cites ≥1 source per verdict.
3. **Capture** the subagent's JSON to
   `.build/assess/<agent>-<TS>.json` (local-only, never committed).
4. **Validate.**
   ```bash
   .claude/skills/ir:onboard-agent/assess/run-batch.sh validate <candidate.json>
   ```
   Checks shape against `assess/schema/column.schema.json` AND
   cross-references every scenario ID against the canonical
   catalog. Common failures + their fixes:
   - *scenario id not in catalog: `<id>`* — subagent invented an
     ID. Tell it to use only the canonical list.
   - *scenario id missing from column: `<id>`* — subagent skipped
     one. Add a verdict (often `"unknown"` with a brief note).
   - *`<sid>`: sources must be a non-empty array* — verdict lacks
     citations. Reject and re-prompt.
5. **Commit.**
   ```bash
   .claude/skills/ir:onboard-agent/assess/run-batch.sh commit --column <agent> <candidate.json>
   ```
   Writes `.specs/agent-assess-<agent>.json` (backs up any prior
   version to `.bak`). Still gitignored; the maintainer transcribes
   into `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json` after review.
6. **Surface low-confidence cells.**
   ```bash
   jq -r '.scenarios | to_entries[]
          | select(.value.confidence < 0.7)
          | "\(.key)  [\(.value.agent_supports), conf=\(.value.confidence)]  \(.value.notes // "")"' \
     .specs/agent-assess-<agent>.json
   ```
   These are the cells worth deep-diving with single-cell `assess`.

### `--row <scenario>` workflow

```
/ir:onboard-agent assess --row <scenario>
```

Produces `.specs/scenario-assess-<scenario>.json` — one verdict per
adapter in `agents[]`. Same flow as `--column` but inverted axis:

1. `run-batch.sh prepare --row <scenario>` — prints scenario spec,
   the adapter list, and the row-mode prompt (`assess/prompts/row.md`).
2. Dispatch a subagent; for each adapter, it reads docs + adapter
   source under `core/adapters/inbound/agents/<adapter>/` and
   produces a verdict.
3. Capture to `.build/assess/<scenario>-<TS>.json`.
4. `run-batch.sh validate <candidate.json>` (auto-detects row vs
   column by inspecting top-level fields).
5. `run-batch.sh commit --row <scenario> <candidate.json>` — writes
   `.specs/scenario-assess-<scenario>.json`.
6. Low-confidence sweep:
   ```bash
   jq -r '.adapters | to_entries[]
          | select(.value.confidence < 0.7)
          | "\(.key)  [\(.value.agent_supports), conf=\(.value.confidence)]"' \
     .specs/scenario-assess-<scenario>.json
   ```

Adapters whose capabilities clearly preclude the scenario (e.g.
opencode has no PID binding for a PID-required scenario) may be
omitted from the row — implicit verdict is `"n/a"`. The subagent's
column prompt covers when to omit vs include with `"unknown"`.

### When to use which form

- **`--column <agent>`** — agent onboarding day-1, agent version
  bump, or matrix maintenance for one column.
- **`--row <scenario>`** — a new scenario landed in
  `.specs/agent-scenarios.md`; want quick verdicts for all adapters
  at once before promoting any to recipe+record.
- **Single cell** — depth-dive after a column/row scan flagged
  uncertainty, OR when a maintainer wants the viewer-rendered
  `assessment.json` artifact (with body + caveats) for one
  important cell.

## Anti-patterns

- **Don't downgrade to `partial` for a narrow gap.** If the canonical
  spec is met, the verdict is `yes`. Use `caveats` for the gap. The
  authoring rule from `cell-lifecycle.md` exists because every
  defensive `partial` makes the matrix less actionable.
- **Don't fabricate sources.** An empty `sources` array is honest;
  a fake citation poisons future re-assessments. If a primary source
  doesn't exist, set `confidence` low and say so in the body.
- **Don't set `confidence: 0.9+` from general knowledge.** That band
  is reserved for "the docs literally say this" or "the source file
  has the exact behavior." `0.7-0.85` is the honest band for a
  thorough multi-source read.
- **Don't write the matrix.** Phase 2 here is the artifact; the
  rollup in `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json` is maintainer-
  owned. Report the transcription hint and stop.
- **Don't dispatch the subagent without the adapter-transport read.**
  Step 3 is what grounds the `irrlicht_observes` claim. A subagent
  guessing from agent docs alone will overstate observability —
  the agent emitting an event ≠ the daemon parsing it.

## When to re-run

- A new agent version ships features relevant to the scenario.
- An irrlicht release adds parser support for an event kind the spec
  asserts (e.g. `pid_discovered` for an agent that didn't have it
  before).
- The canonical spec text in `.specs/agent-scenarios.md` changes
  meaningfully (re-read Step 1, re-judge).
- A drift signal: the viewer shows a pipeline-strip outline because
  the verdict says `partial` but the recording's measurement is
  `pass`. Re-assess to either upgrade the verdict to `yes` or
  document why the recording overshoots.

## What this mode does NOT do

- It does not modify `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`. The
  matrix is the maintainer's editorial truth.
- It does not produce the recipe — that's
  [`../recipe/SKILL.md`](../recipe/SKILL.md).
- It does not run a recording. Stage 4 is `run-cell.sh` +
  `promote-recording.sh`.
- It does not validate against `expected.jsonl`. Stage 5 is
  `expected-validate`.
