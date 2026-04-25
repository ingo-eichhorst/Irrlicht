---
name: ir:onboard-agent
description: >
  Produce the canonical scenario × adapter fixture matrix for irrlicht. Drives
  the real `claude` CLI (and future codex/pi drivers) through a shared scenario
  catalogue, records lifecycle events, stages curated fixtures under
  `.build/refresh/`, and summarizes material changes vs. committed fixtures.
  Unifies three operations: refresh, bootstrap, and new-agent onboarding.
  Use when user says "/ir:onboard-agent", "refresh fixtures", "onboard agent",
  "regenerate recordings", or "update replay fixtures".
---

# Irrlicht Agent Onboarding

Produce or refresh the canonical scenario × adapter fixture matrix. There
are two parallel axes:

1. **Agent scenarios** in `scenarios.json -> scenarios[]`. Agent-agnostic
   declarations with `requires: [capability]`; per-adapter prompt under
   `by_adapter`. Adapters declare `Capabilities` in
   `core/adapters/inbound/agents/<adapter>/config.go`. Valid cells run a
   real CLI under an isolated daemon and capture transcripts.
2. **Orchestrator scenarios** in `scenarios.json -> orchestrator_scenarios[]`.
   Per-orchestrator inputs under `by_orchestrator`. Each scenario
   references a `fixture_dir` under `replaydata/orchestrators/<adapter>/`
   containing seeded `gt`-style responses, sidecar files, and golden
   `orchestrator.State` snapshots. Verification is a Go test against the
   committed goldens.

The matrix of valid cells falls out automatically; running this skill
populates or refreshes them.

Auth is the user's responsibility — `claude` subscription login, `codex`
auth, etc. are set up out-of-band. If a CLI isn't authenticated it will say
so on stderr and the run will fail cleanly. Orchestrator scenarios are
hermetic and don't need auth.

## Invocations

- **`/ir:onboard-agent`** — no args: compute and print the matrix status (see
  Step 2). No cost spent.
- **`/ir:onboard-agent <adapter>`** — run every cell that applies to
  `<adapter>`. E.g. `/ir:onboard-agent claudecode` or
  `/ir:onboard-agent gastown`.
- **`/ir:onboard-agent <adapter> <scenario>`** — run one specific cell. E.g.
  `/ir:onboard-agent claudecode baseline-hello` or
  `/ir:onboard-agent gastown agent-discovery`.
- **`/ir:onboard-agent --diff`** — re-summarize the latest staged runs
  (under `.build/refresh/<adapter>/<scenario>-*/`) without re-running.
- **`/ir:onboard-agent --new <slug>`** — discover mode. Researches a
  previously unknown agent on the web via subagents and proposes a
  `capabilities.json`. See `discovery-instructions.md` for the dispatch
  recipe. No live agent CLI is invoked.

The adapter argument disambiguates which axis is being run: agent adapters
(`claudecode`, `codex`, `pi`) match `scenarios[].by_adapter`; orchestrator
adapters (`gastown`) match `orchestrator_scenarios[].by_orchestrator`.

## Step 1: Understand the task

Parse the invocation into one of:
- `list` — no args
- `run_all` — one adapter
- `run_one` — one adapter + one scenario
- `diff_only` — `--diff` flag
- `discover` — `--new <slug>`; load `discovery-instructions.md` and follow that recipe instead of Steps 2–5 below

If the invocation is ambiguous, ask the user which mode to run.

## Step 2: Compute matrix status

Before any run (or as the sole output of `list`), compute the current state
of **both** matrices.

### Cross-reference check (run first)

Every feature ID referenced by `scenarios.json -> scenarios[].requires` must
exist in `replaydata/agents/features.json`; same for
`orchestrator_scenarios[].requires` against
`replaydata/orchestrators/features.json`. Unknown IDs are a hard error:
report them as `scenario <name> requires <id> which is not in the canonical
features list — add the feature or fix the typo`. Do not proceed with
matrix computation until every reference resolves.

```bash
comm -23 \
  <(jq -r '.scenarios[].requires[]' .claude/skills/ir:onboard-agent/scenarios.json | sort -u) \
  <(jq -r '.features[].id' replaydata/agents/features.json | sort -u)
# any output = unknown IDs — block.
```

### Agent matrix (`scenarios[]` × agent adapters)

Each cell's state is one of:

- **OK** — fixture committed at `replaydata/agents/<adapter>/scenarios/<scenario>/{transcript,events}.jsonl`, and this session has not refreshed it.
- **stale** — fixture committed, this session refreshed it, and the summarize step found material change.
- **never-recorded** — capabilities match, `by_adapter.<adapter>` entry exists in `scenarios.json`, but no committed fixture.
- **missing-prompt** — capabilities match but `by_adapter.<adapter>` is absent from `scenarios.json`. Actionable: add the entry.
- **N/A (no <capability>)** — adapter's `capabilities.json` does not declare every required feature as `true`. Document which feature is missing or `unknown`.

```bash
SCENARIOS_JSON=.claude/skills/ir:onboard-agent/scenarios.json

# Per-adapter capabilities (one file per adapter):
for a in claudecode codex pi; do
  echo "== $a =="
  jq -r '.features | to_entries[] | "\(.key)=\(.value)"' \
    replaydata/agents/$a/capabilities.json
done

# Committed scenario fixtures:
ls replaydata/agents/{claudecode,codex,pi}/scenarios/ 2>/dev/null
```

Cell applicability rule: a scenario is applicable to an adapter iff every
ID in `scenarios[].requires` maps to `true` in the adapter's
`capabilities.json -> features`. `false` and `"unknown"` both block.

Print a table: rows = agent adapters, columns = scenario names. For any
**missing-prompt** or **never-recorded** cell, append a one-line hint.

### Orchestrator matrix (`orchestrator_scenarios[]` × orchestrator adapters)

Each cell's state is one of:

- **OK** — committed `golden/state-NNN.json` files cover all `poll_ticks` declared in `by_orchestrator.<adapter>`.
- **stale** — this session ran `drive-gastown.sh` and the summarize step reported `verdict: CHANGED`.
- **never-recorded** — `by_orchestrator.<adapter>` entry exists but the scenario's `golden/` directory is empty or missing.
- **missing-fixture** — `by_orchestrator.<adapter>` entry exists but `replaydata/orchestrators/<adapter>/<scenario>/input/` is missing. Actionable: add the input fixtures.

```bash
# Orchestrator scenarios + their fixture dirs:
jq -r '.orchestrator_scenarios[] | "\(.name)\t\(.by_orchestrator.gastown.fixture_dir)\t\(.by_orchestrator.gastown.poll_ticks)"' \
  $SCENARIOS_JSON

# Existing goldens:
find replaydata/orchestrators/gastown/scenarios -name 'state-*.json' 2>/dev/null | sort
```

Print a second table: rows = orchestrator adapters (currently just `gastown`),
columns = orchestrator scenario names.

## Step 3: Execute cells

Dispatch by adapter type:

### Agent cells

For each (agent-adapter, scenario) cell, invoke
`scripts/run-cell.sh <adapter> <scenario>`. The script handles:
- `scripts/precheck.sh` (pgrep, git-clean, CLI version, build daemon)
- isolated daemon launch with `IRRLICHT_RECORDINGS_DIR`
- driver invocation via `scripts/drive-<adapter>.sh`
- graceful shutdown (SIGINT → 6s → SIGTERM → SIGKILL)
- transcript resolution via session UUID
- `scripts/curate-lifecycle-fixture.sh -d <staging>/replaydata/agents …`
- replay report generation (staged + committed, if any)
- `run-manifest.json` writeback

**Pre-cell gate** (only relevant for agents new to irrlicht): if the
adapter has a `replaydata/agents/<name>/capabilities.json` but no entry
in `core/cmd/irrlichd/main.go:agentCfgs` yet, the daemon literally
cannot see it. Before running cells for a new adapter, complete the
post-discovery live recording smoke described in
`discovery-instructions.md → Post-discovery gate`. That step adds the
minimum stub adapter, drives the agent in tmux, and produces a recording
that classifies the daemon's detection as PASS / PARTIAL / FAIL.

**On cell failure**: when `run-cell.sh` exits nonzero or the manifest's
`error` field is set, run `scripts/lib/classify-failure.sh <staging>`. The
output is `{"code": "<code>", "summary": "...", "evidence": "..."}`. Look
up `<code>` in `install-instructions.md` for the matching action recipe.
Use `AskUserQuestion` to present three options: "show install/auth
instructions", "I'll fix manually — wait", "skip this adapter". On
"show", paste the relevant install-instructions.md section verbatim and
pause. On "wait", continue paused until the user says "retry". On "skip",
mark the cell `BLOCKED` in the Step 4 summary and move on.

### Orchestrator cells

For each (orchestrator-adapter, orchestrator-scenario) cell, invoke
`scripts/drive-<adapter>.sh <scenario>` directly (no run-cell.sh wrapper —
orchestrator scenarios are hermetic and don't need a live daemon). For
gastown, the script:

- Stages a writable copy of `replaydata/orchestrators/gastown/scenarios/<scenario>/` under `.build/refresh/gastown/<scenario>-<UTC-ts>/fixtures/<scenario>/`.
- Runs `go test -run TestGastownReplay/<scenario> -update-goldens` against the staged copy via `GASTOWN_FIXTURES_DIR`.
- Diffs the regenerated staged goldens against the committed `replaydata/` goldens.
- Writes `run-manifest.json` with `verdict` (`OK` / `CHANGED` / `ERROR`) and the list of differing golden files.

Stream the script's output as it runs. On nonzero exit, read
`<staging>/run-manifest.json` and report the failing step specifically. Stop
the batch on first failure.

## Step 4: Summarize (the important step — read carefully)

### Agent cells

For each staged cell, read `<staging>/reports/staged.json` and (if present)
`<staging>/reports/committed.json`. Produce a structured diff and a verdict.

### Normalization rules

Replay reports contain values that vary between runs even when behavior is
identical. **Normalize both sides identically before comparing**, otherwise
every refresh will look broken:

1. **`generated_at`** — strip entirely. It's the replay wall clock.
2. **UUIDs** — rewrite each distinct UUID seen (anywhere — `session_id`,
   `session_uuid`, ids embedded in reasons) to `UUID_1`, `UUID_2`, … in
   order of first appearance. Do this **per side independently**, not across
   sides — the goal is to collapse UUID identity to "first UUID seen in
   transitions," "second UUID seen," etc. If the two sides produce the
   same normalized sequence, UUIDs don't count as a change.
3. **Absolute timestamps** — replace with offsets (ms) from the first event's
   timestamp in that report.
4. **Durations** — quantize into 100ms buckets (`round(d/100)*100`). Small
   timing jitter shouldn't register as change.
5. **`virtual_time`** — likewise relative-to-first.
6. **File paths** — strip the `.build/refresh/<adapter>/<scenario>-<ts>/`
   prefix down to the scenario-relative path.

Do the normalization in your reasoning step — don't write a normalizer
script. The reports are JSON and usually under 2 KB per scenario; normalize
by reading the JSON and mentally rewriting the fields.

### Diff dimensions to report

After normalizing, compare the two reports on these dimensions only:

- **State transition sequence** — the list of `(prev_state, new_state)` pairs.
  This is the load-bearing one.
- **Flicker count** — `summary.flicker_count`.
- **Tool call presence** — did any tool fire? Which category (Bash / Write /
  Read / WebFetch / mcp__* / other)?
- **Hook firings** — which hook `kind`s appear in `extended_check`?
- **Final state** — last entry in transitions.
- **Session count** — number of sessions in `sessions[]`.
- **Estimated cost USD** — raw delta, not normalized.

Do NOT diff exact transition-reason strings, per-event latencies, or model
output text. Those are nondeterministic.

### Evaluating `verify` matchers

The scenario's `verify` block lists structural assertions you check against
the staged report (and, for new fixtures, against the transcript). Most are
self-explanatory — `final_state`, `has_waiting_state`, `tool_calls_max`,
`contains_tool_call`, `contains_hook`, `transitions_topology`,
`tool_call_failed`, `parent_linked_min`, `subagent_transcripts_min`. Three
matchers introduced for the aider scenarios in #217 are less obvious because
they require reading the staged transcript directly:

- **`min_turns: N`** — count `> Tokens:` lines in the staged transcript (or
  count transitions whose `last_event_type == "turn_done"` in the report).
  Pass if ≥ N.
- **`contains_interrupt: true`** — pass if the transcript shows more `####`
  user-prompt markers than `> Tokens:` turn-close markers (the missing
  Tokens line is the interrupt signature). Equivalent: at least one
  `####` block has no `> Tokens:` before the next `####`.
- **`model_changed: true`** — pass if the transcript contains two or more
  distinct `> Model:` lines, OR if the report's transitions show two
  different `model_name` values across turns.

### Verdict for each cell

Pick one:

- **OK: no material change** — staged matches committed on every dimension
  above.
- **CHANGED (review)** — at least one dimension differs. List specifically
  which dimension(s) and on which transition index. Example: "transitions[4]:
  prev_state changed from `working` to `waiting` — investigate the tailer
  change in PR #nnn."
- **FIRST-RECORD (new fixture)** — no committed fixture existed; verify the
  scenario's `verify` assertions against the staged report and report which
  passed.
- **VERIFY-FAIL** — staged report violates the scenario's `verify`
  assertions. Block the commit suggestion.
- **ERROR: <reason>** — `run-manifest.json` contains an error (transcript
  not found, daemon shutdown SIGKILL, etc.).

### Per-cell output format (agent)

```
[claudecode / baseline-hello]  verdict: OK
  transitions: 6 → 6 (sequence unchanged)
  flicker_count: 0 → 0
  tool_calls: (none) → (none)
  final_state: ready → ready
```

### Orchestrator cells

The driver already produced the verdict. Read `<staging>/run-manifest.json`:

- `verdict: OK` → no material change. The poller produced byte-identical
  goldens against committed.
- `verdict: CHANGED` → list `manifest.diffs` (the differing
  `state-NNN.json` filenames). For each, also produce a structural diff
  against the same dimensions used for agents where applicable: codebase
  count/names, worker counts by role, global-agent presence, top-level
  `running` flag. Diff the staged JSON against the committed JSON in
  `<scenario>/golden/` to surface specifically which fields moved.
- `verdict: ERROR` → read `<staging>/test.log` for the Go test failure and
  surface the relevant test output (panic, missing fixture, parse error,
  etc.).

Cross-check the scenario's `verify` block in `scenarios.json` against the
staged goldens:

- `codebases_count`, `codebases_names`, `global_agents_roles` — direct field comparison on `state-001.json`.
- `running`, `codebase_statuses` — direct field comparison.
- `polecat_count_per_tick`, `boot_present_per_tick`, `codebases_count_per_tick` — read each tick's `state-NNN.json`, derive the array, compare.
- `workers_with_session_min`, `expected_session_ids` — sum across worktrees and report.

A `verify` failure with `verdict: OK` should be flagged as a
**VERIFY-FAIL** verdict that blocks commit (the goldens reproduce, but
they no longer match the scenario's intent — fixture inputs likely need
adjusting).

### Per-cell output format (orchestrator)

```
[gastown / agent-discovery]  verdict: OK
  ticks: 1
  codebases: 2 [gastown, irrlicht]
  global_agents: 3 [mayor, deacon, boot]
  verify: PASS

[gastown / polling-lifecycle]  verdict: CHANGED
  ticks: 3
  diffs: state-002.json
  delta: tick 2 — boot_present went true→false (regression: gt-fail
         fallback path no longer drops the boot agent)
  verify: PASS  (the verify block accepts the change, so this is a real
                 regression in the poller, not a verify drift)
```

## Step 5: Next steps

After summarizing, print the concrete commands the maintainer runs to
commit accepted cells.

### Agent cells

```
# Review each staged fixture vs current committed one:
diff replaydata/agents/claudecode/baseline-hello.jsonl \
     .build/refresh/claudecode/baseline-hello-20260424-152301/replaydata/agents/claudecode/baseline-hello.jsonl

# If satisfied, copy into replaydata/:
cp .build/refresh/claudecode/baseline-hello-*/replaydata/agents/claudecode/baseline-hello.* \
   replaydata/agents/claudecode/

# Verify replay tests still pass:
go test ./core/cmd/replay/... -run TestReplayWithSidecar

# Commit:
git add replaydata/agents/claudecode/baseline-hello.* && git commit -m "..."
```

### Orchestrator cells

```
# Review the regenerated goldens vs committed:
diff -r replaydata/orchestrators/gastown/scenarios/agent-discovery/golden \
        .build/refresh/gastown/agent-discovery-20260425-101500/fixtures/agent-discovery/golden

# If accepted, regenerate goldens in place (touches replaydata/ only):
go test ./core/adapters/inbound/orchestrators/gastown/ \
        -run TestGastownReplay/agent-discovery -update-goldens

# Verify the test now passes against committed:
go test ./core/adapters/inbound/orchestrators/gastown/ -run TestGastownReplay

# Commit:
git add replaydata/orchestrators/gastown/scenarios/agent-discovery/ && git commit -m "..."
```

Do NOT run the `cp`, `-update-goldens`, or `git commit` yourself. The
maintainer reviews and commits by hand — this skill never touches
`replaydata/` directly.

## Anti-patterns

- **Don't** skip normalization in Step 4. UUID/timestamp drift will make
  every refresh look like a break. This is the single most important
  instruction.
- **Don't** write fixtures directly to `replaydata/agents/`. All writes go to
  `.build/refresh/`; maintainer copies manually.
- **Don't** run with an existing `irrlichd` up. Both daemons would race on
  port 7837 and hooks would route to the wrong one. `precheck.sh` refuses;
  don't override.
- **Don't** invent prompts for adapters that don't have `by_adapter`
  entries. Report "missing-prompt" as actionable state — the user adds the
  prompt by editing `scenarios.json` and runs again.
- **Don't** diff exact text from transition reasons, assistant output, or
  tool-call arguments. Only structural invariants are stable across runs.
- **Don't** run `go test -update-goldens` against `replaydata/orchestrators/`
  yourself. Always go through `drive-gastown.sh`, which writes to
  `.build/refresh/`. The maintainer chooses when (and whether) to
  regenerate the committed goldens.
