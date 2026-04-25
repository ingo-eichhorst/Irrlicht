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
