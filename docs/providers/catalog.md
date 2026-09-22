# Provider billing-product census

Part of epic #1977 (bounded attribution track). This is #2006's deliverable:
the candidate catalog of billing products, built by importing six pinned OSS
repositories' provider lists, plus the command that recounts it from the
same committed data every time. Nothing here is a roadmap commitment — a
census entry is not a decision to integrate that provider (#1977 §5).

**Non-goals** (from issue #2006): no provider implementation, no credential
read, no outbound request to any provider, no parser, no change under
`core/` or `platforms/`, and no change under `replaydata/agents/`. This
catalog intentionally lives under `docs/`, not `replaydata/providers/` — see
"Why `docs/`, not `replaydata/providers/`" below.

## How to regenerate the count

```sh
tools/provider-census.sh
```

The command reads every file in [`docs/providers/sources/`](sources/), one
per pinned source below, validates each one's schema, confirms every
source's pinned revision still answers at its `check_url` (skip with
`--offline`), and sums entry counts across `kind: "provider-list"` sources
into one total. **The count is never hand-typed here or anywhere else in
this file** — `tools/lib/provider-census_test.sh`'s vacuity guard greps this
document's stated figure and fails the `tools` preflight gate if it and the
command's live count ever disagree.

Real run, captured 2026-09-20 against the pinned revisions listed below
(network reachability check included, i.e. NOT `--offline`):

```
provider-census: sources read from docs/providers/sources
provider-census:   aiquokka             provider-list  entries=9    (counted)
provider-census:   codexbar             provider-list  entries=68   (counted)
provider-census:   herdr-agent-quota    provider-list  entries=10   (counted)
provider-census:   herdr-agent-usage    reference      entries=6    (excluded from total — see notes)
provider-census:   llm-usage            provider-list  entries=5    (counted)
provider-census:   openusage            provider-list  entries=36   (counted)
provider-census: TOTAL candidate billing-product mentions across provider-list sources: 128
```

**128** candidate billing-product mentions across 5 `provider-list` sources
(9 + 68 + 10 + 5 + 36), from `tools/provider-census.sh`, 2026-09-20. This is
raw mentions, not deduplicated distinct products: the same underlying
product (e.g. "claude", "codex", "grok") appears under multiple sources by
design — #1977 itself notes "one provider can sell several products [and]
one model family can be served through several billing providers." A
dedup/cross-reference pass needs human judgment neither this ticket nor its
command performs; the per-candidate table below tags every row with its
source instead, so a reader can see the overlap directly.

## Result states

Four, kept distinct (#1977 §5, issue #2006 §1.2): `unassessed`,
`fixture-verified`, `live-verified`, `source-specific unavailable`. **Every
entry in this census is `unassessed`.** Nothing in this ticket is
`fixture-verified` or `live-verified` — no probe of any kind ran here.
Reading a public repository at a pinned revision, which is all this ticket
did, is *source-verified* at best, a label for evidence quality, not one of
the four result states — it belongs in a candidate's evidence trail, not in
its `state` column. A blocked or failed probe is not a capability verdict
either; this census makes no such claim, positive or negative, about any
candidate.

## Why `docs/`, not `replaydata/providers/`

Issue #2006 §9 raised this as an open question; the triage comment settled
it: `replaydata/providers/` did not exist at the epic's inspected revision
(`6427a2c55`), and `prov(factory)` (#2008) is the ticket that designs that
manifest's layout — committing to it here would have fixed the shape before
its owner existed. A document plus a machine-readable list under `docs/` is
reversible. `replaydata/agents/` is untouched by this ticket, and this
catalog is deliberately kept separate from the agent scenario matrix there —
a provider limitation is not an agent capability, and vice versa
(`prov(muse-claims)` is the ticket that corrects a place this was
conflated).

**Correction, #2008.** The paragraph above originally also said that
`replaydata/providers/` "is covered by the replaydata deletion guard (#268),
which makes creating it here close to irreversible in CI terms." That was
not true of the guard as it was written. Its classification `case` in
[`.github/workflows/replaydata-deletion-guard.yml`](../../.github/workflows/replaydata-deletion-guard.yml)
had arms for `replaydata/agents/scenarios.json`,
`replaydata/orchestrators/*/scenarios/*`, `replaydata/agents/*/regressions/*`
and the two `replaydata/agents/*/scenarios/*` arms — and nothing else; every
other path fell through to `*) : ;;`, the allow arm. So a deletion anywhere
under `replaydata/providers/` would have triggered the workflow (its `paths:`
filter is `replaydata/**`) and passed it. #2008 added the missing arm and the
matching fixture line in
[`tools/lib/replaydata-deletion-guard_test.sh`](../../tools/lib/replaydata-deletion-guard_test.sh),
so the claim is now true; it was an unverified dismissal when written.

The tree itself now exists — `replaydata/providers/` holds one manifest per
billing provider, read by `of provider verify` / `of provider status` and by
the `of validate` gate. **This census does not move into it.** The two answer
different questions: the census is an import of six pinned upstream provider
*lists*, every entry `unassessed`, and it is a research input; a manifest is a
declaration about a route Irrlicht has actually looked at, and it has to carry
evidence. The manifests reference this document rather than absorbing it —
which is exactly what #2008's triage decided. See
[`replaydata/providers/README.md`](../../replaydata/providers/README.md).

## Sources

Every source's licence was read directly at the pinned revision (not the
default-branch licence GitHub's API reports), by fetching
`raw.githubusercontent.com/<repo>/<revision>/LICENSE` — all six are MIT.
Copyright lines are preserved in [`THIRD_PARTY_NOTICES.md`](../../THIRD_PARTY_NOTICES.md).
No source code was copied into this repository; only provider/product
*names*, which this ticket's non-goals permit (`docs/providers/sources/*.json`
records names and repo paths, not upstream source text).

| id | repo | revision | kind | licence | evidence date | inspected paths |
|---|---|---|---|---|---|---|
| [`codexbar`](sources/codexbar.json) | [steipete/CodexBar](https://github.com/steipete/CodexBar) | [`b6e65a83dc47`](https://github.com/steipete/CodexBar/tree/b6e65a83dc471817b7ff7678e68e0204c9dd604f) | provider-list | MIT | 2026-09-20 | `Sources/CodexBar/Providers/`, `docs/provider.md` |
| [`openusage`](sources/openusage.json) | [janekbaraniewski/openusage](https://github.com/janekbaraniewski/openusage) | [`4f042bcbd286`](https://github.com/janekbaraniewski/openusage/tree/4f042bcbd286832a00993012f379507f66b8ea8c) | provider-list | MIT | 2026-09-20 | `internal/providers/`, `docs/site/docs/providers/` |
| [`herdr-agent-quota`](sources/herdr-agent-quota.json) | [levi-qiao/herdr-agent-quota](https://github.com/levi-qiao/herdr-agent-quota) | [`530c94b721b8`](https://github.com/levi-qiao/herdr-agent-quota/tree/530c94b721b823a480072000012984ac8660cb2e) | provider-list | MIT | 2026-09-20 | `src/providers/` |
| [`llm-usage`](sources/llm-usage.json) | [denysvitali/llm-usage](https://github.com/denysvitali/llm-usage) | [`4c6f4e8544eb`](https://github.com/denysvitali/llm-usage/tree/4c6f4e8544eb68d19b3e4416b7d83112f45077a4) | provider-list | MIT | 2026-09-20 | `providers/` |
| [`aiquokka`](sources/aiquokka.json) | [McKean/aiquokka](https://github.com/McKean/aiquokka) | [`def6ca814d8d`](https://github.com/McKean/aiquokka/tree/def6ca814d8da68aa36394a189c1bd2d918d3357) | provider-list | MIT | 2026-09-20 | `internal/`, `cmd/` |
| [`herdr-agent-usage`](sources/herdr-agent-usage.json) | [senna-lang/herdr-agent-usage](https://github.com/senna-lang/herdr-agent-usage) | [`cfd84237738c`](https://github.com/senna-lang/herdr-agent-usage/tree/cfd84237738c3e653274b4f0090cf1ab779234e8) | **reference** | MIT | 2026-09-20 | `internal/providers/`, `internal/limits/billingmode.go` |

`herdr-agent-usage` is `kind: "reference"`, not `provider-list`, and
contributes 0 to the total by design. Per issue #2006 §1.1 what this source
has to import is "its separation of agent identity and billing mode," not a
provider-billing list: `internal/limits/billingmode.go`, read at the pinned
revision, classifies each pane as `BillingSubscription` or
`BillingPayAsYouGo` from account/session evidence (e.g. for `claude`:
`~/.claude.json` `oauthAccount.billingType`, or
`CLAUDE_CODE_USE_BEDROCK`/`VERTEX`/`FOUNDRY` env flags; for `codex`: rollout
`token_count` with vs. without `rate_limits`; for `opencode`: the assistant
message's `providerID`; for `grok`: `auth.json`'s `auth_mode`). Its six
`internal/providers/` modules (claude, codex, cursor, grok, omp, opencode)
are recorded as reference entries, not counted candidates — see
[`sources/herdr-agent-usage.json`](sources/herdr-agent-usage.json)'s
`notes` field for the full evidence, including that `omp/provider.go`
registers two agent identities (`omp` and `pi`) from one module.

Per-source exclusions, all source-verified at the pinned revision (never a
copy of upstream source, only directory/package names read via the GitHub
tree API):

- `codexbar`: excludes `Sources/CodexBar/Providers/Shared/` (a shared helper
  package, not a provider).
- `openusage`: excludes `internal/providers/providerbase/` and
  `internal/providers/shared/` (helper packages, not providers).
- `aiquokka`: excludes `internal/httpx/` and `internal/usage/` (shared
  helpers, not providers).

## Per-candidate table

Generated by `tools/provider-census.sh --offline --markdown` — one row per
imported entry, tagged by source, rather than a deduplicated view (see
"How to regenerate the count" above for why). Every `state` is `unassessed`;
`next research action` names the exact source path and revision a fixture-
or live-verification probe would start from. This table is data for the
follow-on provider tickets (#2009–#2013 per the triage comment) — it is not
itself a probe, a capability declaration, or a roadmap commitment.

| product | source | kind | revision | evidence date | state | next research action |
|---|---|---|---|---|---|---|
| antigravity | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| claude | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| codex | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| copilot | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| deepseek | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| grok | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| kimi | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| kiro | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| zai | aiquokka | provider-list | def6ca814d8d | 2026-09-20 | unassessed | Fixture-verify against `McKean/aiquokka`@`def6ca814d8d`: internal/; cmd/ |
| Abacus | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| AiAnd | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Alibaba | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Amp | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Antigravity | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Augment | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| AzureOpenAI | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Bedrock | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Chutes | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Claude | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| ClawRouter | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| ClinePass | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Codebuff | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Codex | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| CommandCode | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Copilot | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Crof | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Cursor | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Deepgram | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| DeepInfra | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| DeepSeek | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Devin | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Doubao | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| ElevenLabs | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Factory | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Fireworks | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Gemini | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Grok | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Groq | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| IBMBob | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| JetBrains | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Kilo | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Kimi | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Kiro | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| LiteLLM | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| LLMProxy | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| LongCat | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Manus | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| MiMo | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| MiniMax | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Mistral | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Moonshot | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| NeuralWatt | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Notion | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Ollama | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| OpenAI | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| OpenCode | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| OpenCodeGo | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| OpenRouter | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Perplexity | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Poe | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Qoder | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| QwenCloud | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Sakana | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| StepFun | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Sub2API | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Synthetic | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| T3Chat | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Venice | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| VertexAI | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Warp | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Wayfinder | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Windsurf | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| XAI | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Zai | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| Zed | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| ZenMux | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| ZoomMate | codexbar | provider-list | b6e65a83dc47 | 2026-09-20 | unassessed | Fixture-verify against `steipete/CodexBar`@`b6e65a83dc47`: Sources/CodexBar/Providers/; docs/provider.md |
| agy | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| claude | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| codex | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| cursor | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| devin | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| grok | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| muse | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| omp | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| opencode_go | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| statusline | herdr-agent-quota | provider-list | 530c94b721b8 | 2026-09-20 | unassessed | Fixture-verify against `levi-qiao/herdr-agent-quota`@`530c94b721b8`: src/providers/ |
| claude | herdr-agent-usage | reference | cfd84237738c | 2026-09-20 | n/a (reference, not a billing candidate) | n/a — see herdr-agent-usage's notes field |
| codex | herdr-agent-usage | reference | cfd84237738c | 2026-09-20 | n/a (reference, not a billing candidate) | n/a — see herdr-agent-usage's notes field |
| cursor | herdr-agent-usage | reference | cfd84237738c | 2026-09-20 | n/a (reference, not a billing candidate) | n/a — see herdr-agent-usage's notes field |
| grok | herdr-agent-usage | reference | cfd84237738c | 2026-09-20 | n/a (reference, not a billing candidate) | n/a — see herdr-agent-usage's notes field |
| omp | herdr-agent-usage | reference | cfd84237738c | 2026-09-20 | n/a (reference, not a billing candidate) | n/a — see herdr-agent-usage's notes field |
| opencode | herdr-agent-usage | reference | cfd84237738c | 2026-09-20 | n/a (reference, not a billing candidate) | n/a — see herdr-agent-usage's notes field |
| claude | llm-usage | provider-list | 4c6f4e8544eb | 2026-09-20 | unassessed | Fixture-verify against `denysvitali/llm-usage`@`4c6f4e8544eb`: providers/ |
| codex | llm-usage | provider-list | 4c6f4e8544eb | 2026-09-20 | unassessed | Fixture-verify against `denysvitali/llm-usage`@`4c6f4e8544eb`: providers/ |
| grok | llm-usage | provider-list | 4c6f4e8544eb | 2026-09-20 | unassessed | Fixture-verify against `denysvitali/llm-usage`@`4c6f4e8544eb`: providers/ |
| kimi | llm-usage | provider-list | 4c6f4e8544eb | 2026-09-20 | unassessed | Fixture-verify against `denysvitali/llm-usage`@`4c6f4e8544eb`: providers/ |
| minimax | llm-usage | provider-list | 4c6f4e8544eb | 2026-09-20 | unassessed | Fixture-verify against `denysvitali/llm-usage`@`4c6f4e8544eb`: providers/ |
| alibaba_cloud | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| amp | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| anthropic | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| antigravity | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| azure_openai | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| claude_code | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| codebuff | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| codex | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| copilot | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| crush | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| cursor | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| deepseek | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| droid | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| gemini_api | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| gemini_cli | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| goose | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| groq | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| hermes | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| kilocode | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| kimi_cli | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| kiro | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| mistral | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| moonshot | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| mux | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| ollama | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| openai | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| openclaw | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| opencode | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| openrouter | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| perplexity | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| pi | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| qwen_cli | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| roocode | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| xai | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| zai | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
| zed | openusage | provider-list | 4f042bcbd286 | 2026-09-20 | unassessed | Fixture-verify against `janekbaraniewski/openusage`@`4f042bcbd286`: internal/providers/; docs/site/docs/providers/ |
