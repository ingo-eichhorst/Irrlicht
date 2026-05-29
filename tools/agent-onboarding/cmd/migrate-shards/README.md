# migrate-shards

One-shot **migration blueprint**: reads the pre-#510 on-disk onboarding layout
(`scenarios.json` + per-adapter `capabilities.json` + per-cell
`assessment.json`) and emits the per-scenario "shard" data model
(`replaydata/scenarios/<name>.json` + `_meta.json`).

It generated the committed shards once (P1). The shards are now the live read
source — `internal/shard`, `internal/matrix`, the viewer, AND the bash recording
pipeline all read them (P2/P3, then #511) — so this tool is **no longer part of
any read path**. It stays in the tree as a documented, idempotent blueprint for
how the layout→shard transform worked.

> **Status (#511): the legacy inputs are gone.** #511 deleted
> `scenarios.json`, the per-adapter `capabilities.json`, and the per-cell
> `assessment.json` (the agent capability vocabulary moved into
> `replaydata/scenarios/_meta.json` `capability_vocab`). With its inputs
> removed this tool can no longer regenerate, and `-check` now fails by
> construction (it finds no legacy layout to read). It is kept buildable as a
> **historical reference only** — do not wire it into CI. The forward-direction
> integrity checks now live in `internal/shard` (`TestNoOrphanRecordingFolders`),
> `cmd/matrix rollup --check`, and the bash `lib/cell-integrity.sh` gate.

## What it reads (the current layout)

- `.claude/skills/ir:onboard-agent/scenarios.json`
  - `catalog[]` — the 41 matrix rows (`{id, section, feature}`).
  - `scenarios[]` — the 44 recipe variants (`{name, coverage_id, requires,
    verify, description, by_adapter}`). A variant's `name` may differ from its
    `coverage_id` (e.g. `multi-turn-conversation` → `basic-turn`).
  - `min_versions` — agent → minimum CLI version.
- `replaydata/agents/<agent>/capabilities.json` — defines the **agent set**
  (any directory with a `capabilities.json` IS an agent column) and the optional
  `transcript_extension` (defaults to `jsonl`; `aider` is `md`).
- `replaydata/agents/<agent>/scenarios/<folder>/` — the per-cell recording +
  judgement: `assessment.json`, `manifest.json`, `events.jsonl`,
  `transcript.jsonl` / `transcript.md`, `*.replay.json.golden`, and
  `recordings/<ts>/` archive subdirs.

## What it writes

- `replaydata/scenarios/<catalog-id>.json` — **one shard per catalog row**.
- `replaydata/scenarios/_meta.json` — `min_versions` + `transcript_extensions`,
  keyed by agent (sorted).

## Mapping rule (one shard per catalog row)

For catalog row `cid`:

- **Row metadata** (`description`, `requires`, `verify`, `idle_only`) comes from
  a *representative* variant: the variant named `cid` if present, else the first
  variant with `coverage_id == cid`.
- **`candidateDirs`** for `cid` = `{ cid } ∪ { variant.name : variant.coverage_id
  == cid }` (mirrors `internal/matrix/matrix.go`). The recording for a row may
  live in a *variant* folder, not the `cid` folder — e.g. `pi`'s
  `user-blocking-question` recording lives in `agent-question-pending/`.
- **Per agent** (one `agents[<agent>]` cell):
  - **Artifacts + `recording_dir`** — the first candidate folder that holds a
    recording (`events.jsonl` or a transcript). Artifact paths are relative to
    `replaydata/agents/`.
  - **Assessment** — read from the recording folder; falls back to the first
    candidate folder with a parseable `assessment.json` (mirrors
    `recordedAndAssessment`). Overview axes + a one-paragraph note excerpt go in
    `metadata`; the full raw JSON goes in `details.assessment`.
  - **Recipe** — the `by_adapter[agent]` block of the best-fitting variant:
    prefer the variant whose `name` IS the recording folder, then the variant
    named `cid`, then the first with a block for this agent.
  - **Versions** — `agent_cli_version`, `daemon_version`, `expected_pass_rate`
    from the recording folder's `manifest.json`.
  - A cell is **omitted** when the agent has no recipe, no recording, AND no
    assessment for `cid` (an "absent" cell).

The shard `id` (`<section>.<index>`) is derived exactly like
`internal/viewer/catalog.go`'s `annotateCatalogCodes`: section numbers in
first-appearance order (from 1), index incrementing within each section
(from 1).

## Determinism guarantees

- Each shard is `json.MarshalIndent`'d with a 2-space indent and a trailing
  newline. Struct fields serialize in declaration order; the `agents` map
  serializes in sorted-key order (Go's `encoding/json`).
- Raw blobs (`verify`, `details.recipe`, `details.assessment`) are re-compacted
  with `json.Compact` so re-runs are byte-identical regardless of source
  whitespace — **without reordering keys** inside the blob (same as P0's
  `recipeHashOf` key-order fix).

## `-check` mode (historical — no longer functional)

`migrate-shards -check` regenerated everything in memory and compared it
byte-for-byte against the committed files (`DIFFERS`/`MISSING`/`EXTRA`; exit `0`
clean / `1` drift / `2` usage/IO). **Since #511 deleted the legacy inputs it can
no longer run** — there is no `scenarios.json` / `capabilities.json` /
`assessment.json` layout to regenerate from. Use `cmd/matrix rollup --check`
(coverage rollup) and `internal/shard` `TestNoOrphanRecordingFolders`
(orphan-recording guard) instead.

## Usage

```sh
# from the repo root (so go.work resolves irrlicht/core)
go run ./tools/agent-onboarding/cmd/migrate-shards -repo-root .          # write
go run ./tools/agent-onboarding/cmd/migrate-shards -repo-root . -check   # verify
```

---

**Regenerate shards with this tool; never hand-copy old shards or the pre-#509
#510 blobs. `expected.jsonl` is intentionally NOT folded into shards (it stays
on disk next to its recording).**
