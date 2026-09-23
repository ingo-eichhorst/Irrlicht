# Gateways, upstream billers and local runtimes

Issue #2013, work package F of epic [#1977](https://github.com/ingo-eichhorst/Irrlicht/issues/1977).
Companion to the [provider billing-product census](catalog.md) (#2006) and the
[`replaydata/providers/` manifest tree](../../replaydata/providers/README.md) (#2008).

This ticket landed **rules and fixtures, and no integration**. There is no
consented AWS, Azure or Google Cloud account on this machine, so no cloud
billing route was probed and none was implemented — #2013 §4 and its triage
comment both scope it that way, and §9's answer is that the rules are worth
landing on their own because they protect the attribution model from the
shortcuts most likely to be taken later.

## The three outcomes this exists to keep apart

| Outcome | What it means | What it does **not** mean |
|---|---|---|
| `direct` | a non-local endpoint was observed **and** a provider reported its own quota for this session | — |
| `gateway` | a non-local endpoint was observed and nothing confirmed who was billed | not "the adapter's usual provider" |
| `local-or-private-endpoint` | the endpoint's host is a loopback or private-network address | **not** local inference, and **not** a zero charge |
| `local-runtime` | inference happened here and nothing left the machine | nothing Irrlicht observes establishes this today — the constant exists, the resolver never returns it |
| `unobserved` | the route was denied, unreadable, malformed, or named no override | not "the default provider" |

`core/domain/session/billing_attribution.go` is the whole implementation.
`ResolveBillingAttribution` reads exactly two evidence inputs — a
`RouteObservation` (#2002) and a `RateLimitSnapshot` (#1994) — plus one
explicitly demoted `catalogHint`. It reads no credential, makes no request,
and imports nothing.

**Nothing in `core/` calls it yet.** That is the "no integration" half, and it
is also waiting on the one open maintainer gate below.

## The four rules, and where each one is enforced

Each is a new guard, so none of them has a "before the fix" to run red. Each
is proved instead by a committed mutation in
[`tools/lib/cloud-attribution-mutations_test.sh`](../../tools/lib/cloud-attribution-mutations_test.sh),
which flips the rule and requires the named test to fail — and to fail for the
right reason, not because the package stopped compiling. Six mutations are
committed: the four below, plus two for guards review added (see "Fixed in
review").

| Rule | Enforced by | Test it reddens |
|---|---|---|
| `litellm_provider` is catalog metadata, never a billing provider | `BillingAttribution.CatalogHint`, which is never copied to `Provider` | `TestResolveBillingAttribution_CatalogHintNeverBecomesTheProvider` |
| a loopback endpoint never implies local inference or a zero charge | the `route.Local` branch returns `local-or-private-endpoint`; the `Charges` vocabulary has no zero member | `TestResolveBillingAttribution_LoopbackIsNeverARuntimeOrAZeroCharge` |
| a throughput ceiling is never rendered as a subscription allowance | `RateLimitWindow.LimitKind` + `ImminentWindow`'s skip, failing closed on any unrecognised kind | `TestImminentWindow_SkipsThroughputWindows`, `TestIsSubscriptionAllowance_FailsClosedOnAnUnrecognisedKind` |
| "known gateway, unknown account" stays a distinct result | `BillingQualityGatewayKnownAccountUnknown`, its own value in its own vocabulary | `TestResolveBillingAttribution_GatewayKeepsTheAccountUnknown` |

Two **locks** pass before and after and are not red-first evidence:
`core/pkg/capacity`'s `TestDeriveFamilyFromLiteLLM` (`litellm_provider` keeps
its catalog meaning and still derives `ModelCapacity.Family`), and
`services.ProviderForSession`'s `TestProviderForSession` (#1994's evidence
ranking is untouched — this ticket adds a derived reading beside it and
changes nothing it does).

## Why `RateLimitWindow` gained a field

#2013 §3 allows a `core/domain` change only where a real response proves
#1994's fields insufficient. `Measure` is that proof: it already records
`"requests"`, and `"requests"` is the unit of both a requests-per-minute
service limit and a requests-per-month plan allowance. One field cannot hold
both meanings, so `LimitKind` is a second one. Its zero value reads as an
allowance, which is what every window any adapter emits today actually is, so
no stored row and no live reading changes behaviour.

## Unprobed-route record

Epic #1977 §5: *"A missing account or failed probe means the check was blocked
or failed. It does not prove that the provider has no quota source."* Every
route below is recorded as **blocked**, with the date and the reason. None of
these is a statement that the provider has no quota source.

Recorded **2026-09-23**. Re-open any row when the blocker named in it is
removed.

| Route | State | Blocker, as of 2026-09-23 |
|---|---|---|
| AWS Bedrock | blocked, unprobed | No consented AWS account with agent traffic on it. Additionally unobservable — see the route-observation gap below. |
| Google Vertex AI | blocked, unprobed | No consented Google Cloud account. Same observation gap. |
| Azure OpenAI / Microsoft Foundry | blocked, unprobed | No consented Azure account. Same observation gap. |
| A local gateway forwarding to a paid upstream (e.g. LiteLLM on `:4000`) | assessed without a probe | No probe needed, and none would settle it: the observation is identical to a true local runtime's. See below. |
| A true local runtime (Ollama, LM Studio) | assessed without a probe | Same. Distinguishing it needs evidence that nothing left the machine, which no observation Irrlicht makes provides. |

### Rows 3 and 4 were settled by comparison, not by a probe

#2013 §6 asks for a local gateway and a true local runtime to be told apart.
They cannot be, from a route observation: `session.RouteObservation` carries
`Status`, `Endpoint`, `Source` and `Local`, and a LiteLLM instance forwarding
to a paid Bedrock account produces the same four values as an Ollama server
on the same port. `TestLoopbackIsIndistinguishable_GatewayVersusRuntime`
asserts that equality by constructing both and comparing them, rather than by
saying so here. That is why `local-runtime` is unreachable and
`local-or-private-endpoint` is the honest answer.

### The route-observation gap for Bedrock, Vertex and Foundry

Claude Code selects a hosted backend with the `CLAUDE_CODE_USE_BEDROCK`,
`CLAUDE_CODE_USE_VERTEX` and `CLAUDE_CODE_USE_FOUNDRY` environment flags, not
by overriding a base URL. Source-cited, not live-probed: both citations are
readings of third-party or upstream material already committed here —
[`docs/providers/sources/herdr-agent-usage.json`](sources/herdr-agent-usage.json)'s
`notes`, recording `internal/limits/billingmode.go` at pinned revision
`cfd84237738c`, and
`replaydata/agents/claudecode/scenarios/5-5_provider-failover-midturn/metadata.json`.

#2002's endpoint observation reads exactly one key —
`endpointEnvKeys = {"ANTHROPIC_BASE_URL"}` in
`core/adapters/inbound/agents/processlifecycle/endpoint_route.go` — so a
Bedrock- or Vertex-routed Claude Code session that sets no base URL yields
`RouteAbsent`: *looked and found no override*, which is already the right
answer for that key set and is not a claim about the session's route.

**So these three rows are blocked twice over**, and that is worth stating
plainly: no consented account to probe, *and* no observation that would see
the route even with one. Adding those keys is new-provider integration, which
#2002 §2 excluded from that ticket and #2013 §2 excludes from this one.
`endpointSourceKey` is named once in that file specifically so it stays a
small change when someone owns it.

## Why nothing was added under `replaydata/providers/`

#2013 §5 anticipated "manifests and redacted fixtures for the gateway and
local cases". **This ticket added none, and that is a deliberate deviation**
from the issue body — not from the triage plan, whose design section scopes
the work to "the four mutation guards and their fixtures" and never mentions a
manifest.

The reason is the shipped verifier, and it was measured rather than assumed.
An *honest* Bedrock manifest — one that invents nothing, because nothing was
observed — was written to a scratch tree and run through the real verifier:

```console
$ go run ./tools/onboarding-factory/cmd/of provider verify --repo-root <scratch>
of provider verify: 3 violation(s):
  replaydata/providers/bedrock/manifest.json: fixtures is empty — #1977 §8 asks for a manifest plus redacted input and output fixtures
  replaydata/providers/bedrock/manifest.json: observation.strategy "" is not one of: native-hook, native-transcript, account-api, agent-store, browser-session
  replaydata/providers/bedrock/manifest.json: products is empty — a provider entry names at least one billed product
```

Three violations, none of which an unprobed route can answer truthfully: it
has no observed billed product, no captured response to commit as a fixture,
and no transport — the five strategies in `internal/provider/schema.go` are
all real ones Irrlicht implements. (A fourth check, the non-empty
`redaction.allowed_fields` at `verify.go:278`, only fires once a fixture
exists, so it does not appear above.) A true local runtime fails the first of
those by definition: it has no billed product at all.

Satisfying those checks would mean inventing data — in a tree the CI deletion
guard makes hard to remove. Loosening the verifier to accept an unprobed
provider would be worse: a validator that cannot read its input with
confidence checks *more*, never less. The gateway and local cases are
therefore committed as Go fixtures next to the rules they exercise, in
`core/domain/session/billing_attribution_test.go`, where nothing has to be
made up. The census in [`catalog.md`](catalog.md) already carries `Bedrock`,
`VertexAI`, `AzureOpenAI` and `Ollama` as `unassessed` rows, which is the
correct home for a candidate nobody has looked at.

## Fixed in review

Two defects were found by review and fixed before this landed. Both were
measured by running the code, not by reading it.

1. **An unrecognised `LimitKind` read as a subscription allowance.**
   `IsSubscriptionAllowance` was `w.LimitKind != LimitKindThroughput`, so every
   value but the exact string `"throughput"` passed. A window spelled
   `"throughtput"` — outside `LimitKinds`, recognised by nothing — was surfaced
   by `ImminentWindow` at 92% as the window to forecast on, which is exactly the
   rendering §1.4 forbids. It now fails closed: only `""` (the legacy zero value
   every shipped window carries) and an explicit `allowance` read as one.
2. **A doc comment asserted an invariant the code did not hold.**
   `OutcomeDirect` claimed to be "the only outcome that carries a Provider". It
   is not: `Outcome` grades the *route* and `Quality` grades the *biller*, so a
   session whose route was never observed still carries a confirmed provider
   from its own statusline snapshot. The behaviour is right and the comment was
   wrong, so the comment was corrected and the missing case is now covered by
   `TestResolveBillingAttribution_ConfirmedBillerSurvivesAnUnobservedRoute`.

A third, narrower hole in the same distinction was closed while in there:
`ForecastCap` matched the earlier sample on duration and reset instant alone,
so a throughput ceiling sharing a reset time could pair with an allowance and
contribute its slope to a plan-cap forecast.

## Open maintainer gate

**How should a "known gateway, unknown account" session read in the UI?**
#2013 §9 question 3, unresolved, and the reason no frontend consumes
`ResolveBillingAttribution` yet. It is the honest result and the least
satisfying one; triage asks for the label to be decided before it ships, the
same way #1996's `unattributed` bucket needed one.
