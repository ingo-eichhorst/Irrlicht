# Scenario applicability assessment (row mode)

You are assessing ONE irrlicht scenario across every coding-agent CLI in the catalog. Your output becomes a candidate matrix-row for the maintainer to review and merge into `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`. You do not modify that file directly.

## Inputs you will be given

- `SCENARIO_ID` — the kebab-case scenario slug (e.g. `basic-turn`, `session-resume`, `cloud-background-agent`).
- The list of adapter slugs (`agents[]` in `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json`).
- The full scenario catalog, supplied in two forms:
  - `.specs/agent-scenarios.md` — prose `Feature → Scenario → Expected` block for the scenario you're assessing. Read this carefully.
  - `.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json` — canonical adapter list.

Read both. The IDs in the JSON are authoritative for output keys.

## What to read about each agent

For each adapter, prefer primary sources in this order:

1. The agent's official docs site and reference pages.
2. The agent's published changelog or release notes.
3. The agent's open-source code repository if one exists.
4. The adapter's source under `core/adapters/inbound/agents/<adapter>/` (especially `config.go` and `capabilities.json`) — tells you what irrlicht can see, which constrains the daemon-observability axis. (You're producing `agent_supports` here, but transport mismatches sometimes inform the verdict.)

Do **not** rely on:

- Third-party blog posts or general tutorials.
- Inference about "what a CLI like this probably does."
- The behavior you observe in `replaydata/` — that tells you what irrlicht has captured, not what the agent supports.

If a feature is genuinely undocumented for one adapter, set `agent_supports: "unknown"` for that adapter with low confidence and `notes` explaining what you searched.

## Output

Write a single JSON document validating against `assess/schema/row.schema.json`. The top-level shape:

```json
{
  "schema_version": 1,
  "scenario": "<SCENARIO_ID>",
  "surveyed_at": "<ISO-8601 UTC, now>",
  "source_catalog": ".specs/agent-scenarios.md",
  "adapters": {
    "<adapter-slug>": { ...verdict... },
    ...
  }
}
```

**Adapter keys must be valid slugs from `agents[]`.** Adapters whose capabilities clearly preclude the scenario (e.g. opencode lacks PID binding and the scenario requires PID-bound observation) may be omitted — implicit verdict is "n/a." Be conservative: only omit when the mismatch is hard and obvious from `capabilities.json`. When in doubt, include the adapter with `"unknown"`.

### Each verdict

```json
{
  "agent_supports": "yes" | "no" | "partial" | "unknown",
  "confidence": 0.0..1.0,
  "sources": [ { "kind": "...", "ref": "...", "excerpt": "..." }, ... ],
  "notes": "free-form, optional",
  "prerequisites_hint": "optional, see below"
}
```

#### `agent_supports`

- **`yes`** — the agent does this as-shipped, with no extra setup beyond a normal install + login.
- **`partial`** — the agent does it but with a caveat that affects observability (e.g. lazy file discovery, idle-flush turn-end, no formal session-id concept). Use `notes` to name the caveat.
- **`no`** — the agent demonstrably lacks the feature (e.g. aider has no `/clear` analogue, claudecode has no native cloud variant).
- **`unknown`** — primary sources don't speak to it for this adapter. Use `notes` to record what you looked at.

#### `confidence`

Calibrate roughly:

- `0.9–1.0` — explicit documentation, code, or release note states the behavior for this adapter.
- `0.6–0.9` — strong inference from related docs + at least one corroborating source.
- `0.3–0.6` — circumstantial; one source, ambiguous wording, or interpretation needed.
- `<0.3` — guess. Pair with `agent_supports: "unknown"` unless you have a reason not to.

#### `sources`

At least one entry per verdict — even `"unknown"` verdicts cite what you searched.

- `kind: "url"` — generic web URL.
- `kind: "docs"` — official documentation URL.
- `kind: "changelog"` / `"release_notes"` — official release log entry.
- `kind: "source_code"` — open-source repo file; `ref` may be a permalink.
- `kind: "issue"` — GitHub or similar issue URL, only when it reflects shipped behavior.
- `kind: "file"` — local repo-relative path (e.g. `core/adapters/inbound/agents/<adapter>/config.go`).

Include a verbatim `excerpt` (≤300 chars) whenever possible so the maintainer can sanity-check without re-fetching.

#### `notes`

- For `partial`: name the specific caveat that affects irrlicht's observability ("idle-flush turn-end adds ~5s before ready settles").
- For `no`: name the deliberate design choice ("aider has no `/clear` analogue; restart creates new history file").
- For `unknown`: name what you searched ("docs at <URL> don't mention; changelog 0.x..1.y doesn't mention").

#### `prerequisites_hint`

If the verdict is `yes` or `partial` but exercising the scenario requires maintainer-only setup (paid plan, signing cert, API key, cloud account), write a one-line description.

Omit the field if no such gate exists.

## Style

- Be terse. The maintainer reviews ~5 verdicts (one per adapter), but you may be doing 5 independent research passes — keep each tight.
- Don't speculate. `unknown` is a fine answer for an adapter whose docs don't speak to the feature.
- Don't invent URLs. Every `ref` must resolve to a real page or file you actually read.
- Don't editorialize about quality. Just record support / non-support / partial with the caveat.

When done, print the JSON document and stop. `run-batch.sh` writes it to disk and validates.
