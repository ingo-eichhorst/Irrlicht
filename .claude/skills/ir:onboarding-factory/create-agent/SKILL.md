---
name: ir:onboarding-factory/create-agent
description: >
  Onboard a brand-new agent CLI as a matrix COLUMN: research its identity +
  recording prerequisites, register it via `of agent add`, scaffold its
  interactive driver from the template, and predict which step types each
  scenario's recipe will need (the driver-needs punch-list). No live recording.
  Invoked as `/ir:onboarding-factory create-agent <slug>`.
---

# create-agent

> **You run as a focused subagent with no parent context.** Do the research
> yourself (web + file access). This task runs NO recording and invokes NO
> agent CLI. When done, return only the "Return contract" block.

## What this does

Adds a new agent COLUMN to the matrix — the counterpart to `create-scenario`'s
ROW. It does three things, all up front, before any cell is assessed or
recorded:

1. **Register the column** via `of agent add` — writes
   `replaydata/agents/<slug>/metadata.json` `{id, name, provider,
   prerequisites[]}` and registers the agent in the catalog `meta.min_versions`
   so `of status` and the viewer treat it as onboarded.
2. **Scaffold the interactive driver** from the template so `record` has
   something to drive.
3. **Predict driver-needs** — across the scenario catalog, name the step types
   the agent's recipes will need, so the driver gaps are known before the
   assess sweep instead of being rediscovered one `record` round-trip at a time.

## Prerequisite: the Go adapter (maintainer / code task)

The daemon only observes an agent it has an adapter for. Wiring a new adapter is
a Go change (a package under `core/adapters/inbound/agents/<slug>/` exporting
`Agent()`, plus one line in the `allAgents` slice in
`core/cmd/irrlichd/main.go` — see AGENTS.md "Adding a new agent adapter"). That
is out of scope for this skill. If no adapter exists yet, say so in `notes`:
the column can be registered and the driver scaffolded, but recordings can't be
observed until the adapter lands. Don't block — register what you can.

## Steps

### 1. Research the agent

Use web search + fetch on the agent's official site, changelog, and source
repo. Determine:

- **id** — kebab slug (the column key, the `replaydata/agents/<id>/` dir).
- **name** — display name (e.g. "Claude Code").
- **provider** — `anthropic` / `openai` / `google` / … (the model vendor).
- **prerequisites** — the human actions a recording needs that the subagent
  can't perform: auth mode (subscription vs API key), env vars, a local model
  server, a config file. These become `--prereq` strings and are surfaced by
  `of record prereq-check` before every recording. Be concrete — "switch the
  CLI from subscription auth to an API key (ANTHROPIC_API_KEY)" beats "set up
  auth".
- **transport shape** — does it write an append-only transcript under `$HOME`
  (FilesUnderRoot), one transcript per cwd (FilesUnderCWD), or a structured
  store / SQLite (ProcessOwnedStore)? This determines what the daemon can see
  and feeds the driver scaffold + later assessments. Read sibling adapters
  under `core/adapters/inbound/agents/` for the three variants.

### 2. Register the column

```bash
of agent add --id <slug> --name "<Display Name>" --provider <provider> \
  --min-version <x.y.z> \
  --prereq "<prerequisite 1>" --prereq "<prerequisite 2>" ...
```

`of` refuses a duplicate id and validates the slug. Then:

```bash
of validate
```

### 3. Scaffold the interactive driver

Copy the template into the agent folder and adapt the three agent-specific
seams (input mechanism, turn/effect detection, transcript export):

```bash
cp tools/onboarding-factory/scripts/templates/drive-interactive.sh.tmpl \
   replaydata/agents/<slug>/driver-interactive.sh
chmod +x replaydata/agents/<slug>/driver-interactive.sh
```

Start with the sparsest grammar that works (`send` / `wait_turn` / `sleep`) —
new agents grow richer step types (`keys`, `interrupt`, `restart`,
`reset_session`, …) as scenarios demand them. `record` ports each missing step
from the claudecode/codex reference drivers when it hits a gap. Sanity-check:
`bash -n replaydata/agents/<slug>/driver-interactive.sh`.

### 4. Predict driver-needs (the punch-list)

For each scenario, judge from its `process` which step types its recipe will
need, and which the freshly-scaffolded driver lacks:

```bash
of status --json | jq -r '.scenarios[] | "\(.id)\t\(.name)"'
# read each scenario's process via of coverage --json / of status, infer the
# step types it implies (multi-session ⇒ reset_session/restart; in-REPL picker
# ⇒ keys; forced kill ⇒ sigkill; …)
```

This is a prediction, not a verdict — `assess` settles each cell's **driver**
pillar (`ready` | `gap:<primitive>`) on real evidence. The punch-list just tells
the dispatcher which step types to prioritize porting into the driver.

### 5. Commit

```bash
git add replaydata/agents/scenarios.json replaydata/agents/<slug>/
git commit -m "feat(onboard): onboard <slug> agent column"
git rev-parse --short HEAD
```

> End commit messages with the trailer
> `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

## Return contract

Return ONLY this (≤6 lines). Shared semantics + envelope rules live in
[`../return-contract.md`](../return-contract.md):

```
agent: <slug>  (name "<Display Name>")
provider: <provider>   transport: FilesUnderRoot | FilesUnderCWD | ProcessOwnedStore
prereqs: <comma-separated prerequisites, or "none">
driver_needs: <step types scenarios will need that the scaffold lacks, or "none">
commit_sha: <short sha>
next: assess <slug> <scenario>  (per scenario, to fill the column) [— Go adapter NOT yet wired, if so]
```

## Anti-patterns

- **Don't write `replaydata/` by hand.** `of agent add` registers the column;
  the driver scaffold is a script file under `replaydata/agents/<slug>/`, copied
  from the template — never hand-edit the catalog.
- **Don't record or invoke the agent CLI.** Onboarding is research + scaffold
  only.
- **Don't block on the missing Go adapter.** Register the column, scaffold the
  driver, flag the adapter gap in `notes`.
- **Don't over-build the driver.** Sparsest grammar first; `record` grows it on
  demand.
