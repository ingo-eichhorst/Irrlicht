---
name: ir:onboard-agent/assess
description: >
  Per-cell scenario assessment. Researches one (agent, scenario) cell
  via the agent's docs, changelog, and source, plus the adapter's
  transport, and writes a structured `assessment.json` artifact under
  `replaydata/agents/<agent>/scenarios/<scenario>/`. Invoked as
  `/ir:onboard-agent assess <agent> <scenario-id>`. Re-runs overwrite
  silently — git preserves history.
---

# Mode E: per-cell assessment

Produces the artifact for **Stage 1 of the cell lifecycle**: a
structured, dated, sourced record of "does this agent support this
scenario, and can irrlicht observe it" for ONE cell.

> Use [`../survey/SKILL.md`](../survey/SKILL.md) when you want to
> batch-survey every scenario for one agent (Stage 1 across a full
> matrix column). Use **this skill** when you want a deep, sourced
> assessment for a single cell — typically before authoring the
> recipe with [`../recipe/SKILL.md`](../recipe/SKILL.md).
>
> The end-to-end walkthrough across all five stages is
> [`../cell-lifecycle.md`](../cell-lifecycle.md).

## Working approach

A cell's `assessment.json` is the source-of-record for "what we
believe about this cell right now, and why." A maintainer transcribes
the verdict + a one-line note into the rollup matrix
(`.specs/agent-scenarios-coverage.json`), but the artifact is what
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
/ir:onboard-agent assess <agent> <scenario-id>
```

- `<agent>` — adapter slug (`claudecode`, `codex`, `pi`, `aider`,
  `opencode`).
- `<scenario-id>` — coverage id from `.specs/agent-scenarios-coverage.json`
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

## Steps

### Step 1 — Read the prose spec

```
.specs/agent-scenarios.md
```

Find the `### Feature:` heading whose kebab slug matches
`<scenario-id>`. Capture every `Scenario:` paragraph and every
`Expected:` bullet. These are the canonical assertions the verdict
must judge against.

If `.specs/` isn't in this checkout (it's gitignored), the maintainer
must copy it in or you fall back to `/api/catalog` (the viewer's
rollup of the same data). Don't fabricate a scenario.

► **Verify before moving on:**
- [ ] Found the Feature: heading. If not, the `<scenario-id>` is
  wrong or the spec is missing — stop and surface the gap.
- [ ] Captured every Expected: bullet. Each one is a candidate
  assertion you'll judge `irrlicht_observes` against.

### Step 2 — Read the current matrix verdict

```
.specs/agent-scenarios-coverage.json   →   .scenarios[].coverage[<agent>]
```

Or `curl http://127.0.0.1:8765/api/catalog | jq` when `.specs/` is
absent.

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

### Step 4 — Dispatch a focused research subagent

Spawn ONE general-purpose subagent (`Agent` tool with
`subagent_type: general-purpose`) and brief it with:

1. The scenario's full spec text (from Step 1).
2. The current matrix verdict (from Step 2).
3. A pointer to the adapter source you read (from Step 3).
4. The output schema from "Output" above.
5. The two caveat patterns from `cell-lifecycle.md`:
   feature-invisible-but-spec-compliant, metric-drift-downstream.
6. The two worked examples (`checkpoint-rewind`,
   `cloud-background-agent`) as anchors for prose style.

Ask it to:

- Read the agent's official docs site, changelog/release notes, and
  source files relevant to the feature.
- Decide `agent_supports` honestly.
- Derive `irrlicht_observes` from the transport read (Step 3) and
  whatever it learns about the agent's emission shape.
- Identify caveats — name each one explicitly with the pattern it
  fits.
- Calibrate `confidence` (`0.9+` only when behavior is documented
  explicitly).
- Cite at least one source per claim. URL for docs/changelog; `file`
  with a path for source.
- Output the full JSON document conforming to the schema. No
  surrounding markdown, no commentary.

The subagent's prompt should explicitly forbid:

- Made-up sources.
- Downgrading verdicts to "be safe" — the matrix authoring rule is
  honest verdicts + caveats, not defensive `partial`s.
- Reasoning purely from general LLM knowledge — every claim needs a
  primary source.

### Step 5 — Synthesize + write

Read the subagent's JSON. Sanity-check:

- `schema_version: 1`.
- `scenario_id`, `agent`, `assessed_at` correct.
- `agent_supports` and `irrlicht_observes` are one of the allowed
  enum values.
- `confidence` is a number in `[0, 1]`.
- `body` is non-empty markdown that explains the verdict.
- `caveats` is an array (may be empty).
- `sources` is an array with at least one entry; each entry has
  `kind`, `ref`, `note`.

If any check fails, push back on the subagent (one re-roll max) or
hand-edit before writing.

Write the final JSON to
`replaydata/agents/<agent>/scenarios/<scenario>/assessment.json`,
overwriting silently if a file exists. Use 2-space indent for
readability — the viewer parses any valid JSON shape.

### Step 6 — Report

Print to stdout:

```
✓ wrote replaydata/agents/<agent>/scenarios/<scenario>/assessment.json
  verdict: <agent_supports> / <irrlicht_observes> (confidence <n>)
  caveats: <count>
  sources: <count>
```

Then a transcription hint IF the new verdict differs from the matrix
(from Step 2):

```
ⓘ matrix says <old_supports>/<old_observes>; consider updating
  .specs/agent-scenarios-coverage.json -> .scenarios[<id>].coverage.<agent>
```

The matrix update is the maintainer's call — this skill never writes
`.specs/`.

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
  rollup in `.specs/agent-scenarios-coverage.json` is maintainer-
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

- It does not modify `.specs/agent-scenarios-coverage.json`. The
  matrix is the maintainer's editorial truth.
- It does not produce the recipe — that's
  [`../recipe/SKILL.md`](../recipe/SKILL.md).
- It does not run a recording. Stage 4 is `run-cell.sh` +
  `promote-recording.sh`.
- It does not validate against `expected.jsonl`. Stage 5 is
  `expected-validate`.
