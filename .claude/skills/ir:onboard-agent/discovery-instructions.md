# Discovery Mode — Reference

Loaded by `skill.md` when invoked as `/ir:onboard-agent --new <slug>`.
Discovery proposes a `capabilities.json` for an agent or orchestrator
without running its CLI; live install probing happens in WS06/13.

## Subagent dispatch

Spawn **three** Explore subagents in parallel, all seeded with the same
**preamble** assembled from `replaydata/agents/features.json` (or the
orchestrators variant, depending on the disambiguation answer).

The three agents probe distinct evidence types so disagreements surface
real ambiguity rather than single-source noise:

- **A — Official documentation** (authoritative spec)
- **B — Source code & examples** (current implementation reality)
- **C — Community signals** (third-party validation + edge cases)

### Preamble (rendered by `scripts/discover-agent.sh`)

```
You are researching the coding agent "<slug>" for the irrlicht onboarding skill.
You will report whether the agent supports each of the following capabilities,
based on documentation and community sources. Use only the IDs listed here.
If you observe a behavior that does not map to any listed ID, note it
separately under "candidate_new_features".

Closed vocabulary (id — title — description):
<dump from features.json: each line "  <id> — <title> — <description>">
```

### Subagent A — Official docs

Append to preamble:

> Task: read the OFFICIAL DOCUMENTATION. Search:
>   1. "<slug> github" — find the canonical repo
>   2. "<slug> docs" — find the official docs site
>   3. "<slug> CLI reference" / "--help" output
>
> Fetch the README, the docs landing page, and any CLI reference you can
> find linked. Be thorough — try at least 3 distinct doc URLs.
>
> For each capability ID in the closed vocabulary, report:
>
> ```json
> {
>   "id": "<id>",
>   "supported": "yes" | "no" | "unclear",
>   "evidence_url": "..."
> }
> ```
>
> If you observe a feature that does not match any listed ID, append to
> `candidate_new_features`:
>
> ```json
> { "title": "...", "description": "...", "evidence_url": "..." }
> ```
>
> Output a single JSON object with two top-level keys: `features` and
> `candidate_new_features`. Output ONLY the JSON object, wrapped in a
> single ```json code fence. No prose outside the fence.

### Subagent B — Source code & examples

Append to preamble:

> Task: inspect the SOURCE CODE and EXAMPLES on the canonical repo. Search:
>   1. "<slug> github" — find the canonical repo
>   2. Browse the repo's `src/`, `examples/`, recent commits, and CHANGELOG
>   3. Open recent issues and PRs that mention the capability vocabulary terms
>      (subagent, hook, mcp, resume, --session-id, plan-mode, etc.)
>
> Source code is the most current truth — docs lag, but code reflects what
> actually ships today. Look for: relevant filenames (e.g. `hooks.py`,
> `subagent.py`), command-line flag definitions, recent commit messages
> that introduce or remove a feature.
>
> Use the same output JSON shape as Subagent A.

### Subagent C — Community signals

Append to preamble:

> Task: cross-check against COMMUNITY SIGNALS — third-party reviews,
> comparisons, forum discussions. Search:
>   1. "<slug> vs claude code" or "<slug> vs codex" — comparisons
>   2. "<slug> features comparison"
>   3. "<slug> review" / "<slug> capabilities"
>   4. "<slug> vs cursor" or other alternatives
>
> Skim 3–4 distinct third-party sources. Look for claims that <slug> does
> or does not support each capability. Community sources often surface
> features the docs underplay (or oversell).
>
> Use the same output JSON shape as Subagent A.

## Merge rules

For each canonical feature ID, look at the three responses (A, B, C):

| A | B | C | Merged |
|---|---|---|---|
| yes | yes | yes | `true` |
| no | no | no | `false` |
| 2× yes + 1× any | | | `true` (majority) |
| 2× no + 1× any | | | `false` (majority) |
| all unclear / split 1-1-1 | | | **main agent makes a reasoned call** |

**Reasoned-call procedure** (when no clear majority):
1. Weight by source authority for the *kind* of question:
   - **Existence of a flag / CLI surface** — Subagent B (source) is most authoritative.
   - **Documented intent** — Subagent A (docs) is most authoritative.
   - **Real-world usage / edge cases** — Subagent C (community) is most authoritative.
2. If the strongest source for the question type says yes/no with evidence, take that value.
3. If still unclear, fall back to `"unknown"`.
4. **Document the call in `discovery-report.md`** with the rationale: which source won, and why the others were discounted.

Disagreement is not a bug — it surfaces real gaps in coverage. The reasoned call documents what we believe today; a future live probe (WS06/13 once the adapter is wired) settles it definitively.

For `candidate_new_features`:
- Deduplicate by title (case-insensitive) and concept (e.g. "repo_map" and "codebase_map" are the same).
- Drop entries with only one source unless the title is highly distinctive — single-source claims are too noisy unless the feature is unusual enough to be hard to invent.
- When proposing, **widen the description** to capture the underlying capability rather than the agent-specific implementation. The goal is cross-agent applicability — a candidate feature should be testable against future agents too. Find common ground: if `git_integration` covers both auto-commit (aider) and tool-driven git (claudecode), say so.
- Output as proposed entries to `features.json` schema.

## Outputs

Write three files under `.build/refresh/<slug>/<UTC-ts>/`:

1. **`proposed-capabilities.json`** — schema matches WS03's
   `capabilities.json`. Every `id` in `features` resolves against the
   current `replaydata/{agents,orchestrators}/features.json`. `source` is
   `"discovery_subagent"`. `discovered_at` is today's date.
2. **`proposed-features.json`** — partial features.json fragment listing
   only the candidate new features. Schema matches WS01. **Descriptions
   must be cross-agent.**
3. **`discovery-report.md`** — human-readable summary. Sections:
   - Slug + kind
   - Per-feature table with all three subagent verdicts + merged value + evidence URLs
   - Reasoned calls (which features required main-agent judgment, with rationale)
   - Candidate new features (with cross-agent description rationale)
   - Sampled source URLs
   - Next-step commands

## Next-step commands (printed at end of skill output)

```
# Review:
$EDITOR .build/refresh/<slug>/<ts>/discovery-report.md

# If accepted, merge proposed features into the canonical list:
jq -s '.[0].features += .[1].features | .[0]' \
   replaydata/agents/features.json \
   .build/refresh/<slug>/<ts>/proposed-features.json \
   > /tmp/merged.json && mv /tmp/merged.json replaydata/agents/features.json

# Copy the proposed capabilities into place:
mkdir -p replaydata/agents/<slug>
cp .build/refresh/<slug>/<ts>/proposed-capabilities.json \
   replaydata/agents/<slug>/capabilities.json

# Add the same new feature keys (as "unknown") to existing adapters'
# capabilities.json so the cross-reference check stays clean:
for adapter in claudecode codex pi; do
  jq '.features += {<new keys: "unknown">}' \
    replaydata/agents/$adapter/capabilities.json > /tmp/c.json && \
    mv /tmp/c.json replaydata/agents/$adapter/capabilities.json
done

# Add the Go adapter scaffold (Phase-0 plumbing — separate manual step):
#   core/adapters/inbound/agents/<slug>/{config.go,parser.go,...}
```

The skill never copies these files itself.

## Post-discovery gate: live recording smoke

Before any full adapter implementation for the discovered agent, the
maintainer **MUST** prove `irrlichd` actually detects the agent live.
Skipping this step has historically meant writing a parser blind against
a daemon that turns out to need format/discovery work first.

The smoke is a scripted two-process test (no human-in-terminal):

### 1. Stub adapter (~50–100 LOC, depending on agent shape)

Under `core/adapters/inbound/agents/<slug>/`:
- `adapter.go` — `AdapterName`, `ProcessName`, `rootDir` constants
- `config.go` — `Config()` returning `agents.Config{...}` with a no-op parser
- `parser.go` — `NoOpParser` whose `ParseLine` returns nil (skip)
- `pid.go` — `DiscoverPID` matching the adapter's process-discovery shape

**Template: `core/adapters/inbound/agents/aider/`** — covers the most
demanding case (Python wrapper + non-JSONL + per-CWD transcript).
Discovery does NOT propose this scaffold; the maintainer adds it
manually as a distinct commit before adapter implementation begins.

#### Adapter shape decision tree

Pick the smallest set of `agents.Config` fields the agent needs:

| Agent shape | Required fields | DiscoverPID helper |
|---|---|---|
| Native binary, JSONL transcript under fixed `$HOME/.<x>/` | `Name`, `ProcessName`, `RootDir` | `DiscoverPIDByCWD(ProcessName, cwd, ...)` |
| Wrapper-launched (Python, npx, etc.), JSONL under fixed root | + `CommandLineMatch: "/<bin>"` | `DiscoverPIDByCWDAndCmdLine(pattern, cwd, ...)` |
| Native binary, transcript per-project in CWD | + `TranscriptFilename: ".<x>.history.md"` | `DiscoverPIDByCWD(...)` |
| Wrapper-launched + per-project transcript (e.g. aider) | + both `CommandLineMatch` AND `TranscriptFilename` | `DiscoverPIDByCWDAndCmdLine(pattern, cwd, ...)` |

**Critical gotcha**: when you set `CommandLineMatch`, the `DiscoverPID`
function in `pid.go` MUST use the matching `DiscoverPIDByCWDAndCmdLine`
helper with the same pattern. If the scanner uses `pgrep -f` but the
discoverer uses `pgrep -x`, the PID never resolves → the kqueue death
monitor never registers → `process_exited` never fires. Aider hit this:
recovery cost a debugging round.

### 2. Wire into `core/cmd/irrlichd/main.go`

One import, one line in the `agentCfgs` slice.

### 3. Build the daemon

```bash
go build -o .build/irrlichd ./core/cmd/irrlichd
```

### 4. Run the tmux driver alongside `irrlichd --record`

```bash
# Pre-flight: ensure no other irrlichd is on port 7837.
pgrep -x irrlichd && { echo "stop the running daemon first"; exit 1; }

mkdir -p .build/manual-<slug>/{recordings,workspace}
(cd .build/manual-<slug>/workspace && git init -q && \
  git config user.email t@l && git config user.name t && \
  echo "# t" > README.md && git add . && git commit -q -m init)

PROMPT="$(jq -r '.scenarios[] | select(.name=="baseline-hello") | .by_adapter.claudecode.prompt' \
  .claude/skills/ir:onboard-agent/scenarios.json)"

IRRLICHT_RECORDINGS_DIR=$(pwd)/.build/manual-<slug>/recordings \
  $(pwd)/.build/irrlichd --record > .build/manual-<slug>/daemon.log 2>&1 &
DAEMON_PID=$!
sleep 2

bash .claude/skills/ir:onboard-agent/scripts/drive-tmux-agent.sh \
  <slug>-smoke "$(pwd)/.build/manual-<slug>/workspace" "$PROMPT" -- \
  <agent-cli> [args...]

sleep 2  # let kqueue death detection fire
kill -INT $DAEMON_PID; wait $DAEMON_PID 2>/dev/null || true
```

The driver (`drive-tmux-agent.sh`) is REPL-agent agnostic: starts the
agent in a detached tmux session, sends the prompt via `tmux
send-keys`, polls the pane buffer until the agent's prompt indicator
returns (capped at 90s), captures the buffer + scrollback, tears the
session down with Ctrl-C → Ctrl-D → `kill-session`.

### 5. Inspect the recording and classify

```bash
RECORDING=$(ls -t .build/manual-<slug>/recordings/*.jsonl | head -1)
echo "=== aider event kinds (count) ==="
jq -r 'select(.adapter=="<slug>") | .kind' "$RECORDING" | sort | uniq -c
echo "=== ALL <slug> events in order ==="
jq -c 'select(.adapter=="<slug>")' "$RECORDING"
echo "=== process_exited (any non-claudecode pid) ==="
jq -c 'select(.kind=="process_exited" and .pid > 10000)' "$RECORDING"
```

#### Classification

- **PASS — full lifecycle**: recording contains all of:
  `presession_created`, `state_transition→ready`, `pid_discovered`,
  `transcript_new` (with `transcript_path`), `transcript_activity`
  (one or more, as the file grows), `process_exited`,
  `transcript_removed`. Every meaningful state of the session is
  observable. The next adapter PR is only the parser (markdown →
  tailer events) — all the daemon-side plumbing works.
- **PARTIAL — process-only**: `presession_created` fires but no
  `transcript_new` with a `transcript_path`. Means the
  `TranscriptFilename` probe didn't find the file in CWD. Either the
  filename is wrong, the agent writes elsewhere (e.g. under `$HOME`),
  or the file is created so late that the scanner missed it before
  shutdown. Add a longer `sleep` between `send-keys` and daemon kill,
  or fix the filename.
- **FAIL — nothing**: no events for `<slug>` in the recording. Stub
  adapter wasn't picked up — common causes:
  1. Forgot to add `<slug>.Config()` to `agentCfgs` in `main.go`
  2. `ProcessName` mismatch: agent runs as `python` (wrapper-launched)
     but stub only set `ProcessName`, no `CommandLineMatch`. Verify
     with `pgrep -fl <slug>`.
  3. `DiscoverPID` mismatch (see "Critical gotcha" above)

### Common false-PASS

If you see `pid_discovered` for a PID that isn't your agent — it's
probably `claude-code` matching against your own running irrlicht
session (the daemon you're running this script from). Filter event
inspection to `.adapter=="<slug>"` to avoid this confusion.

## Anti-patterns

- **Don't** run the agent's CLI in discovery mode. That's WS06/13.
- **Don't** invent capability IDs. Use only what's in the closed vocabulary.
  Surface unknowns in `candidate_new_features` for humans to canonicalize.
- **Don't** auto-merge `proposed-features.json` into the canonical list.
  The skill never edits `features.json` directly.
- **Don't** rate-limit or budget-cap the subagents. Discovery uses
  ordinary WebSearch/WebFetch budgets — no extra ceremony.
- **Don't** narrow candidate descriptions to the agent under study. Always
  widen so the description applies across agents — discovery is a feeder
  for a canonical list that grows monotonically.
- **Don't** default to `"unknown"` on every disagreement. Make a reasoned
  call when the evidence supports one. `"unknown"` is correct only when
  the sources are genuinely silent or contradictory beyond resolution.
