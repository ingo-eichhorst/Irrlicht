# Discovery Mode — Reference

Loaded by `skill.md` when invoked as `/ir:onboard-agent --new <slug>`.
Discovery proposes a `capabilities.json` for an agent or orchestrator
without running its CLI; live install probing happens in WS06/13.

## Subagent dispatch

Spawn **two** Explore subagents in parallel, both seeded with the same
**preamble** assembled from `replaydata/agents/features.json` (or the
orchestrators variant, depending on the disambiguation answer).

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

> Task: read the official documentation. Search:
>   1. "<slug> github" — find the canonical repo
>   2. "<slug> docs" — find the official docs site
>   3. "<slug> CLI reference" / "--help" output
>
> Fetch the README, the docs landing page, and any CLI reference you can
> find linked. For each capability ID in the closed vocabulary, report:
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
> `candidate_new_features`. No prose outside the JSON.

### Subagent B — Community signals

Append to preamble:

> Task: cross-check against community signals. Search:
>   1. "<slug> vs claude code" / "<slug> vs codex"
>   2. "<slug> features comparison"
>   3. "<slug> review" / "<slug> capabilities"
>
> Skim 2–3 third-party comparisons and reviews. Use the same output
> shape as the docs subagent. Mark every supported claim with the
> source URL.

## Merge rules

For each canonical feature ID:

| Subagent A | Subagent B | Result |
|---|---|---|
| yes | yes | `true` |
| no | no | `false` |
| any other combination | | `"unknown"` |

Disagreement is not a bug — it just means we need a live probe (WS06/13)
or human judgment.

For `candidate_new_features`:
- Deduplicate by title (case-insensitive).
- Drop entries with only one source (evidence_url) unless the title is
  highly distinctive — single-source claims are too noisy.
- Output as proposed entries to `features.json` schema.

## Outputs

Write three files under `.build/refresh/<slug>/<UTC-ts>/`:

1. **`proposed-capabilities.json`** — schema matches WS03's
   `capabilities.json`. Every `id` in `features` resolves against the
   current `replaydata/{agents,orchestrators}/features.json`. `source` is
   `"discovery_subagent"`. `discovered_at` is today's date.
2. **`proposed-features.json`** — partial features.json fragment listing
   only the candidate new features. Schema matches WS01.
3. **`discovery-report.md`** — human-readable summary. Sections:
   - Slug + kind
   - Per-feature verdict table with evidence URLs from both subagents
   - Candidate new features (if any)
   - Disagreements (where the two subagents differed)
   - Next-step commands (see below)

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

# Add the Go adapter scaffold (Phase-0 plumbing — separate manual step):
#   core/adapters/inbound/agents/<slug>/{config.go,parser.go,...}
```

The skill never copies these files itself.

## Anti-patterns

- **Don't** run the agent's CLI in discovery mode. That's WS06/13.
- **Don't** invent capability IDs. Use only what's in the closed vocabulary.
  Surface unknowns in `candidate_new_features` for humans to canonicalize.
- **Don't** auto-merge `proposed-features.json` into the canonical list.
  The skill never edits `features.json` directly.
- **Don't** rate-limit or budget-cap the subagents. Discovery uses
  ordinary WebSearch/WebFetch budgets — no extra ceremony.
