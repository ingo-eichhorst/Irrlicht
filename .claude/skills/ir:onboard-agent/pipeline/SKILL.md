---
name: ir:onboard-agent/pipeline
description: >
  Mode B of /ir:onboard-agent — the fully automated onboarding pipeline.
  Orchestrates probe → record → label → synth → gen → validate for one
  agent, optionally one scenario. Outputs generated Go adapter +
  interactive driver under staging until the maintainer reviews and
  promotes. Invoked as `/ir:onboard-agent pipeline <agent> [<scenario>]`.
---

# Mode B: full pipeline

Mode A (the existing 5-step driver flow) remains unchanged and is still
the right mode for refreshing one cell with a known driver script. Mode
B is for **onboarding a new agent end-to-end** or **regenerating an
adapter from scratch** after rule synthesis advances.

## Invocation

```
/ir:onboard-agent pipeline <agent>             # every scenario the agent supports
/ir:onboard-agent pipeline <agent> <scenario>  # one specific scenario
```

E.g.:

- `/ir:onboard-agent pipeline toyagent baseline-hello`
- `/ir:onboard-agent pipeline claudecode multi-turn-conversation`

## Stages

The pipeline runs six stages in order. Each stage's output is the next
stage's input; failures halt the run and surface a clear error message.

### Stage 1 — Probe

Verify the agent is callable, its prerequisites manifest is acknowledged,
and the recorder can attach. Refuses to continue if any check fails.

```bash
pipeline/run-pipeline.sh probe <agent>
```

The check is `tools/agent-onboarding/internal/preflight/prereqs.go`'s
gate — `.agent-onboarding/prereqs-<agent>.ok` must exist and be newer
than `replaydata/agents/<agent>/prerequisites.md`.

### Stage 2 — Record

Spawn the agent inside tmux, run `agent-onboard record` against it
with every applicable sensor enabled, drive the scenario via
`scripts/drive-<agent>-interactive.sh` (or the generated equivalent
once codegen has run), and capture signals.jsonl + frames/ +
recording-meta.json into staging.

```bash
pipeline/run-pipeline.sh record <agent> <scenario>
```

Output: `.build/agent-onboarding/staged/<agent>/<scenario>/{signals,events,recording-meta}.{jsonl,json}`.

### Stage 3 — Label

Convert the driver's `gt:<marker>` sidecar into a schema-conformant
`ground_truth.jsonl` and drop it in the staging dir.

```bash
pipeline/run-pipeline.sh label <agent> <scenario>
```

Output: `.build/agent-onboarding/staged/<agent>/<scenario>/ground_truth.jsonl`.

### Stage 4 — Synth

Run the greedy compositor over the staged signals + ground-truth.
Produces `ruleset.json`, `driver_protocol.json`, and
`synthesis_conflicts.json` in `.build/agent-onboarding/staged/<agent>/`.
Conflicts halt the pipeline so the maintainer can inspect why a label
was unresolvable.

```bash
pipeline/run-pipeline.sh synth <agent> <scenario>
```

### Stage 5 — Generate

Render the adapter Go files into a staging copy of
`core/adapters/inbound/agents/<agent>/` and the interactive driver
script into a staging copy of `.claude/skills/ir:onboard-agent/scripts/`.
Never writes into the real source trees — the maintainer reviews and
copies (or reverts) by hand.

```bash
pipeline/run-pipeline.sh gen <agent>
```

Output: `.build/agent-onboarding/staged/<agent>/generated/`.

### Stage 6 — Validate

Run the validator against the just-generated adapter using the same
scenario's events.jsonl and ground_truth.jsonl. Pass means the
synthesized rules reproduce the observed transitions; failure means
synthesis missed something and the maintainer should re-run with more
labels.

```bash
pipeline/run-pipeline.sh validate <agent> <scenario>
```

Output: `.build/agent-onboarding/staged/<agent>/<scenario>/<agent>-<scenario>-validate.json`.

## After a successful run

The pipeline does NOT commit anything. The maintainer:

1. Reviews `.build/agent-onboarding/staged/<agent>/generated/` for sanity.
2. Diffs against the existing adapter (if any).
3. Copies the generated files into `core/adapters/inbound/agents/<agent>/`
   and `.claude/skills/ir:onboard-agent/scripts/`.
4. Wires the new adapter into `core/cmd/irrlichd/main.go:agentCfgs` if
   it's a first-time onboarding.
5. Runs `go test ./core/... -race -count=1` and `tools/replay-fixtures.sh`
   to confirm nothing else broke.
6. Commits.

The pipeline prints the exact commands to copy + run as its final
output.

## When this mode fails

Most failures come from missing inputs or unresolved synthesis. The
script prints which stage failed and what to fix:

- **probe**: prerequisites manifest not acknowledged. Read the manifest,
  complete each item, `touch .agent-onboarding/prereqs-<agent>.ok`.
- **record**: agent CLI not on PATH or auth missing. Check
  `prerequisites.md` items 1 and 2.
- **label**: driver script didn't emit any `gt:` markers, so
  ground_truth.jsonl would be empty. Inspect the driver's sidecar log;
  the legacy `drive-<agent>-interactive.sh` may need a small patch to
  emit the markers — or the synthesized driver from a prior pipeline run
  may have the wrong typing rhythm.
- **synth**: synthesis_conflicts.json is non-empty. Open the file —
  each Conflict names the unresolvable label and why. Add more labels
  near the problematic transition or extend the rulelib with a new kind.
- **gen**: Go formatter rejected a template render. Open
  `.build/agent-onboarding/staged/<agent>/generated/<file>.go.unformatted`
  (codegen writes this on format failure) and fix the template.
- **validate**: emitted transitions don't match ground-truth within
  tolerance. The Phase 7 viewer
  (`agent-onboard viewer <agent>/<scenario>`) opens at the divergence
  moment with the firing rule highlighted.

## Differences from Mode A

| | Mode A (existing) | Mode B (pipeline) |
|---|---|---|
| Input | scenarios.json by_adapter prompt | replaydata/<agent>/prerequisites.md + agent CLI |
| Driver | hand-written drive-<agent>.sh | hand-written OR codegen'd drive-<agent>-interactive.sh |
| Sensors | transcript only | all 7 (transcript / pane / pipepane / pty / proc / fs / net) |
| Adapter | hand-coded | codegen'd from ruleset.json |
| Verifies | replay report diff vs committed | validator + ground-truth labels |
| Writes | .build/refresh/ staging | .build/agent-onboarding/staged/ |
| Commit | maintainer | maintainer |

Mode A is faster for refreshing known cells. Mode B is what you reach
for when adding a new agent or when synthesis advances enough to make
regenerating an existing adapter worthwhile.
