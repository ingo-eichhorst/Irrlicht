# Agent applicability survey

You are surveying ONE coding-agent CLI to decide, for each scenario in irrlicht's catalog, whether that agent **as-shipped** supports the behavior the scenario tests. Your output becomes a candidate `coverage[<agent>]` column for the maintainer to review and merge into `.specs/agent-scenarios-coverage.json`. You do not modify that file directly.

## Inputs you will be given

- `AGENT_SLUG` — irrlicht's adapter slug for the agent (e.g. `claudecode`, `codex`, `aider`, `pi`, `opencode`).
- `AGENT_VERSION` — the agent's published version at survey time, if known (e.g. `2.1.140`). If unknown, write `"unknown"`.
- The full scenario catalog, supplied in two forms:
  - `.specs/agent-scenarios.md` — prose `Feature → Scenario → Expected` triples.
  - `.specs/agent-scenarios-coverage.json` — the canonical list of scenario IDs (the `id` field of each entry in `scenarios[]`).

Read both. The IDs in the JSON are authoritative for output keys; the prose in the markdown is where the actual behavior is described.

## What to read about the agent

Prefer primary sources, in this order:

1. The agent's official docs site and reference pages.
2. The agent's published changelog or release notes.
3. The agent's open-source code repository if one exists (the README, `--help` text, and any `docs/` tree).
4. The agent's CHANGELOG.md / RELEASES section on GitHub.
5. Public issues that explicitly discuss the feature in question — but only when they reflect shipped behavior, not aspirational discussion.

Do **not** rely on:

- Third-party blog posts, tutorials, or Reddit threads.
- Inference about "what a CLI like this probably does."
- The behavior you observe in `replaydata/`. The replay data tells you what irrlicht has captured, not what the agent supports.

If a feature is genuinely undocumented, set `agent_supports: "unknown"` with low confidence and `notes` explaining what you searched.

## Output

Write a single JSON document validating against `survey/schema/survey-result.schema.json`. The top-level shape:

```json
{
  "schema_version": 1,
  "agent": "<AGENT_SLUG>",
  "agent_version": "<AGENT_VERSION>",
  "surveyed_at": "<ISO-8601 UTC, now>",
  "source_catalog": ".specs/agent-scenarios.md",
  "scenarios": {
    "<scenario-id>": { ...verdict... },
    ...
  }
}
```

**Every scenario ID from `.specs/agent-scenarios-coverage.json` must appear exactly once.** Missing or extra keys cause `run-survey.sh` to reject the output.

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
- **`unknown`** — primary sources don't speak to it. Use `notes` to record what you looked at.

#### `confidence`

Calibrate roughly:

- `0.9–1.0` — explicit documentation, code, or release note states the behavior.
- `0.6–0.9` — strong inference from related docs + at least one corroborating source.
- `0.3–0.6` — circumstantial; one source, ambiguous wording, or interpretation needed.
- `<0.3` — guess. Pair with `agent_supports: "unknown"` unless you have a reason not to.

#### `sources`

At least one entry per verdict — even `"unknown"` verdicts cite what you searched (`docs` URL with a note like "no mention of compaction").

- `kind: "url"` — generic web URL.
- `kind: "docs"` — official documentation URL.
- `kind: "changelog"` / `"release_notes"` — official release log entry.
- `kind: "source_code"` — open-source repo file; `ref` may be a permalink (`github.com/.../blob/<sha>/path/to/file.go#L42`).
- `kind: "issue"` — GitHub or similar issue URL, only when it reflects shipped behavior.
- `kind: "file"` — local repo-relative path (e.g. when surveying against bundled vendored docs).

Include a verbatim `excerpt` (≤300 chars) whenever possible so the maintainer can sanity-check without re-fetching.

#### `notes`

- For `partial`: name the specific caveat that affects irrlicht's observability ("idle-flush turn-end adds ~5s before ready settles").
- For `no`: name the deliberate design choice ("aider has no `/clear` analogue; restart creates new history file").
- For `unknown`: name what you searched ("docs at <URL> don't mention; changelog 0.x..1.y doesn't mention").

#### `prerequisites_hint`

If the verdict is `yes` or `partial` but exercising the scenario requires maintainer-only setup (paid plan, signing cert, API key, cloud account), write a one-line description that the maintainer can copy into `replaydata/agents/<agent>/prerequisites.md`. Examples:

- `"Codex Cloud account required to record session-resume across local↔cloud handoff."`
- `"Apple Developer ID cert needed to exercise the codesigning path."`
- `"Paid Anthropic API key required; subscription-only login does not unlock this turn type."`

Omit the field if no such gate exists.

## Style

- Be terse. The maintainer reviews 38+ verdicts per agent.
- Don't speculate. `unknown` is a fine answer.
- Don't invent URLs. Every `ref` must resolve to a real page or file you actually read.
- Don't editorialize about quality. Just record support / non-support / partial with the caveat.

When done, print the JSON document and stop. `run-survey.sh` writes it to disk and validates.
