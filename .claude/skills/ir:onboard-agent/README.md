# ir:onboard-agent

Developer documentation for the fixture-matrix refresh skill.

## What this skill does

Produces or refreshes the canonical scenario × adapter fixture matrix in
`replaydata/agents/`. Scenarios are defined once, agent-agnostically; each
scenario declares required capabilities. Each adapter declares what it
supports in `core/adapters/inbound/agents/<adapter>/config.go:Capabilities`.
Cells fall out: a cell is applicable when the adapter's capabilities satisfy
the scenario's `requires`.

The skill unifies four operations:
- **Discover** (`--new <slug>`) — research a previously unknown agent on
  the web via subagents and propose `replaydata/agents/<slug>/capabilities.json`.
  See `discovery-instructions.md`.
- **Refresh** (`<adapter>` or `<adapter> <scenario>`) — re-record an
  existing committed fixture to pick up upstream agent format changes.
- **Bootstrap** — record a fixture for a cell that applies but has no
  committed file yet (same invocation as Refresh; the skill detects
  "no committed fixture" and treats it as first-record).
- **Onboard a new adapter** — after adding the Go adapter scaffold under
  `core/adapters/inbound/agents/<name>/` plus `replaydata/agents/<name>/capabilities.json`
  (or via `--new` discovery), run `<adapter>` to populate every matching cell.

## Vision and overhaul

The current skill is the result of the onboard-agent overhaul; see
`/Users/ingo/projects/irrlicht/.specs/onboard-agent-overhaul.md` for the
high-level plan and `.specs/onboard-agent/NN-*.md` for per-workstream
deep plans.

## Setup

Auth is configured out-of-band per adapter — see `install-instructions.md`
for the per-adapter install + auth recipe.

When a recording fails, the skill runs `scripts/lib/classify-failure.sh`
to categorize the failure (cli_not_found / cli_too_old / auth_failed /
daemon_dirty / working_tree_dirty / transcript_missing / timeout /
unknown), then uses `AskUserQuestion` to surface the relevant install or
auth instructions and waits for the user to retry.

## Correctness guards

Not cost-related — these prevent broken runs, not unintended spend:

- `pgrep -x irrlichd` refusal — port 7837 would clash with a running
  production daemon.
- Git-clean check on `replaydata/agents/` — refuses if uncommitted fixture
  changes exist, so you don't layer confusion.
- CLI version minimum check against `scenarios.json.min_versions`.
- Wall-clock `timeout` per cell (hang protection, not spend cap).
- Staged outputs only — skill never writes to `replaydata/` directly.

## File layout

```
.claude/skills/ir:onboard-agent/
  skill.md          — the orchestration the user invokes via /ir:onboard-agent
  scenarios.json              — canonical scenario catalogue
  discovery-instructions.md   — discovery-mode (--new) recipe (WS04)
  install-instructions.md     — per-adapter install + auth recipes (WS06)
  scripts/
    precheck.sh               — fail-fast precondition bundle
    drive-{claudecode,codex,pi,gastown}.sh — adapter-specific drivers
    run-cell.sh               — glue: precheck → daemon → driver → curate → replay
    discover-agent.sh         — render discovery preamble for --new mode (WS04)
    lib/
      assert-staging-path.sh  — path-traversal guard
      classify-failure.sh     — categorize a failed staging dir (WS06)
  README.md                   — this file
```

The canonical features list and per-adapter capabilities live outside the
skill, alongside the scenario fixtures, in `replaydata/`:

```
replaydata/
  agents/
    features.json                     — canonical feature catalog (WS01)
    <adapter>/
      capabilities.json               — per-adapter feature support (WS03)
      scenarios/<scenario>/
        transcript.jsonl              — agent transcript (raw)
        events.jsonl                  — lifecycle events
        transcript.jsonl.replay.json.golden
        subagents/                    — child sessions, when applicable
        # Reserved by WS07–10 (emission deferred):
        # process.jsonl, files.jsonl, hooks.jsonl, recording.json
  orchestrators/
    features.json                     — canonical orchestrator features
    <orchestrator>/
      capabilities.json
      scenarios/<scenario>/
        input/, golden/, scenario.json
```

Staged outputs:
```
.build/refresh/<adapter>/<scenario>-<UTC-ts>/
  recordings/                           — raw daemon recording
  replaydata/agents/<adapter>/scenarios/<scenario>/{transcript,events}.jsonl
  reports/
    staged.json                         — replay over staged fixture
    committed.json                      — replay over current replaydata (if any)
  settings.json                         — scenario's settings blob
  driver.log, driver.exit-reason
  daemon.log, daemon.shutdown
  run-manifest.json                     — summary the skill reads
```

## `scenarios.json` schema

```json
{
  "min_versions": {
    "claudecode": "2.0.0",
    "codex": "0.50.0",
    "pi": "1.0.0"
  },
  "scenarios": [
    {
      "name": "<slug>",                 — stable, doubles as fixture filename
      "description": "<one-liner>",
      "requires": ["<capability>", ...],
      "verify": { ... },                — structural assertions, adapter-agnostic
      "by_adapter": {
        "<adapter>": {
          "prompt": "...",
          "settings": { ... },          — adapter settings blob (JSON)
          "timeout_seconds": 180
        }
      }
    }
  ]
}
```

## Adding a scenario

1. Pick a slug (kebab-case, stable — it becomes the fixture filename).
2. Identify the `requires` capabilities. If you need a new capability,
   add it to `core/adapters/inbound/agents/config.go` first.
3. Define `verify` as structural invariants only — transition topology,
   tool-call category presence, hook firings. Never match exact text or
   per-event latency; those vary between model versions.
4. Start with `by_adapter.claudecode` (the only driver today). Add a
   per-adapter entry for every adapter that supports the `requires`
   capabilities.
5. Run `/ir:onboard-agent <adapter> <new-scenario>` to bootstrap the cell.
6. Review the staged fixture, then copy into `replaydata/` and commit.

## Adding an adapter column

When a new adapter (say, `aider`) is supported:

1. Add it via Phase-0 plumbing: `core/adapters/inbound/agents/aider/` with
   parser, PID discovery, and a `Config()` that declares `Capabilities`.
2. Write `.claude/skills/ir:onboard-agent/scripts/drive-aider.sh` —
   mirrors `drive-claudecode.sh`, using `aider`'s headless flag convention.
3. Update `scripts/precheck.sh` to add the `aider` case.
4. For every scenario whose `requires` matches `aider`'s capabilities, add
   `by_adapter.aider` with the eliciting prompt/settings.
5. Run `/ir:onboard-agent aider` to populate its column.

## Interpreting cell states

- **OK** — fixture matches the scenario's expected behavior.
- **stale** — refresh run produced a materially different report. Review
  and decide: accept (adapter changed) or reject (we regressed).
- **never-recorded** — prompt exists, fixture doesn't. Run the cell.
- **missing-prompt** — capabilities match but no `by_adapter` entry. Add
  one to `scenarios.json`.
- **N/A (no <capability>)** — adapter cannot run this scenario.

## Troubleshooting

- **"another irrlichd is running"** — stop the production daemon before
  running: `launchctl unload ~/Library/LaunchAgents/…` or kill the
  menu-bar app.
- **"replaydata/agents/ has uncommitted changes"** — commit or stash your
  current staged fixtures before refreshing.
- **"claude X.Y.Z is below pinned minimum"** — update `claude` CLI, or
  bump the minimum in `scenarios.json` if intentional.
- **"transcript_or_recording_missing"** — the daemon didn't see the
  agent's session. Check `daemon.log` in staging for recorder errors;
  check `driver.log` for CLI failures. Often caused by the CLI exiting
  too quickly — the scenario prompt may need to keep the session alive
  longer.
- **Every cell shows "CHANGED" on the first refresh** — check the
  normalization step in `skill.md`. UUID/timestamp drift will make every
  refresh look broken.

## See also

- `scripts/curate-lifecycle-fixture.sh` — the underlying curator (accepts
  `-d <agents-root>` for staging).
- `core/cmd/replay/main.go` — the replay engine that produces reports.
- `core/cmd/replay/main_test.go` — the golden-fixture regression tests
  that consume these fixtures.
- Phase 0 contract: `core/adapters/inbound/agents/config.go` (Capabilities
  declared here).
