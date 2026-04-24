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

Produce or refresh the canonical scenario × adapter fixture matrix. Each
scenario in `scenarios.json` is agent-agnostic and declares `requires:
[capability]`. Each adapter declares `Capabilities` in
`core/adapters/inbound/agents/<adapter>/config.go`. The matrix of valid cells
falls out automatically; running this skill populates or refreshes them.

Auth is the user's responsibility — `claude` subscription login, `codex`
auth, etc. are set up out-of-band. If a CLI isn't authenticated it will say
so on stderr and the run will fail cleanly.

## Invocations

- **`/ir:onboard-agent`** — no args: compute and print the matrix status (see
  Step 2). No cost spent.
- **`/ir:onboard-agent <adapter>`** — run every cell that applies to
  `<adapter>`. E.g. `/ir:onboard-agent claudecode`.
- **`/ir:onboard-agent <adapter> <scenario>`** — run one specific cell. E.g.
  `/ir:onboard-agent claudecode baseline-hello`.
- **`/ir:onboard-agent --diff`** — re-summarize the latest staged runs
  (under `.build/refresh/<adapter>/<scenario>-*/`) without re-running.

## Step 1: Understand the task

Parse the invocation into one of:
- `list` — no args
- `run_all` — one adapter
- `run_one` — one adapter + one scenario
- `diff_only` — `--diff` flag

If the invocation is ambiguous, ask the user which mode to run.

## Step 2: Compute matrix status

Before any run (or as the sole output of `list`), compute the current state
of the matrix. Each cell's state is one of:

- **OK** — fixture committed at `testdata/replay/<adapter>/<scenario>.{jsonl,events.jsonl}`, and this session has not refreshed it.
- **stale** — fixture committed, this session refreshed it, and the summarize step found material change.
- **never-recorded** — capabilities match, `by_adapter.<adapter>` entry exists in `scenarios.json`, but no committed fixture.
- **missing-prompt** — capabilities match but `by_adapter.<adapter>` is absent from `scenarios.json`. Actionable: add the entry.
- **N/A (no <capability>)** — adapter's `Capabilities` don't satisfy the scenario's `requires`. Document which capability is missing.

To compute the status:

```bash
# Read all scenarios
SCENARIOS_JSON=.claude/skills/ir:onboard-agent/scenarios.json

# Read each adapter's capabilities from its Go config file.
# Grep for "Capabilities:" and the agents.Cap... constants inside the literal.
grep -A10 "Capabilities: \[\]agents.Capability" \
  core/adapters/inbound/agents/claudecode/config.go \
  core/adapters/inbound/agents/codex/config.go \
  core/adapters/inbound/agents/pi/config.go

# Committed fixtures:
ls testdata/replay/claudecode/ testdata/replay/codex/ testdata/replay/pi/ 2>/dev/null
```

Print a table: rows = adapters (claudecode, codex, pi), columns = scenario
names. For any **missing-prompt** or **never-recorded** cell, append a one-line
hint: "add `by_adapter.codex` entry to scenarios.json" or "run
`/ir:onboard-agent codex baseline-hello`".

## Step 3: Execute cells

For each cell (in order), invoke `scripts/run-cell.sh <adapter> <scenario>`.
The script handles:
- `scripts/precheck.sh` (pgrep, git-clean, CLI version, build daemon)
- isolated daemon launch with `IRRLICHT_RECORDINGS_DIR`
- driver invocation via `scripts/drive-<adapter>.sh`
- graceful shutdown (SIGINT → 6s → SIGTERM → SIGKILL)
- transcript resolution via session UUID
- `scripts/curate-lifecycle-fixture.sh -d <staging>/testdata/replay …`
- replay report generation (staged + committed, if any)
- `run-manifest.json` writeback

Stream the script's output as it runs. On nonzero exit, read
`<staging>/run-manifest.json` and report the failing step specifically. Stop
the batch on first failure.

## Step 4: Summarize (the important step — read carefully)

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

### Per-cell output format

```
[claudecode / baseline-hello]  verdict: OK
  transitions: 6 → 6 (sequence unchanged)
  flicker_count: 0 → 0
  tool_calls: (none) → (none)
  final_state: ready → ready
```

## Step 5: Next steps

After summarizing, print the concrete commands the maintainer runs to
commit accepted cells:

```
# Review each staged fixture vs current committed one:
diff testdata/replay/claudecode/baseline-hello.jsonl \
     .build/refresh/claudecode/baseline-hello-20260424-152301/testdata/replay/claudecode/baseline-hello.jsonl

# If satisfied, copy into testdata/:
cp .build/refresh/claudecode/baseline-hello-*/testdata/replay/claudecode/baseline-hello.* \
   testdata/replay/claudecode/

# Verify replay tests still pass:
go test ./core/cmd/replay/... -run TestReplayWithSidecar

# Commit:
git add testdata/replay/claudecode/baseline-hello.* && git commit -m "..."
```

Do NOT run the `cp` or `git commit` yourself. The maintainer reviews and
commits by hand — this skill never touches `testdata/` directly.

## Anti-patterns

- **Don't** skip normalization in Step 4. UUID/timestamp drift will make
  every refresh look like a break. This is the single most important
  instruction.
- **Don't** write fixtures directly to `testdata/replay/`. All writes go to
  `.build/refresh/`; maintainer copies manually.
- **Don't** run with an existing `irrlichd` up. Both daemons would race on
  port 7837 and hooks would route to the wrong one. `precheck.sh` refuses;
  don't override.
- **Don't** invent prompts for adapters that don't have `by_adapter`
  entries. Report "missing-prompt" as actionable state — the user adds the
  prompt by editing `scenarios.json` and runs again.
- **Don't** diff exact text from transition reasons, assistant output, or
  tool-call arguments. Only structural invariants are stable across runs.
