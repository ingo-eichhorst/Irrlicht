# `replaydata/providers/` — the billing-product catalog

One directory per **billing provider**, each holding a `manifest.json` and its
redacted `fixtures/`. Read and checked by `of provider verify` and
`of provider status` (`tools/onboarding-factory/internal/provider`), and wired
into the `of validate` CI gate.

This tree is issue #2008's deliverable, work package C of epic #1977. It exists
only because two real integrations proved the contracts first — #1977 §8:
"First prove the contracts with real integrations. Then extract."

## This is not the agent matrix

`replaydata/agents/` models **agent adapters**: CLIs Irrlicht observes, graded
on a `planned → alpha → beta → stable` maturity ladder against a core-12
scenario set. This tree models **billed products**, graded on four evidence
states that are not a ladder at all. The two never mix (#1977 §8, #2008 §1.5):
a provider limitation is not an agent capability, and vice versa.

Muse is why that matters. The coding-agent adapter is `muse`, its billing
provider is `meta`, and the pseudo-adapter holding the account-API consent row
is `muse-account-api`. One integration, three identifiers, and the manifest
carries them in three separate fields rather than assuming one name per
provider.

## Claim states

The four from [`docs/providers/catalog.md`](../../docs/providers/catalog.md)
(#2006), reused verbatim so the census and the manifests speak one language:

| state | means |
|---|---|
| `unassessed` | nothing has looked. Needs no evidence. |
| `fixture-verified` | a committed fixture exercises the axis. |
| `live-verified` | a dated live probe or a real recording exercised it against the provider. |
| `source-specific-unavailable` | the route was reached and does **not** carry this axis. |

The last one is the state this tree exists to be able to express. The Muse
account API answers `200` with an active subscription, a named tier, and no
usage field of any kind — so `meta`'s `quota_windows` is
`source-specific-unavailable`, which is a finding, not a gap. A schema that
required a quota block would have expressed the first account API Irrlicht
ever shipped as an incomplete provider.

`source-specific-unavailable` is not a weaker `live-verified`: it is off the
ladder, and it is compared by its evidence requirement instead of by rank. A
negative finding still has to name where it was observed (#1977 §5).

`live-verified` is earned only by evidence of kind `live-probe` or `recording`,
never by a shape fixture or a source citation — #1977 §11.11, "Provider status
cannot claim live verification from a fixture alone". `of provider status`
prints claimed next to earned and exits non-zero on the difference, so an
unearned claim is reported rather than believed.

## The four capability axes

Closed set, defined in Go (`internal/provider/schema.go`), and every manifest
declares all four. A manifest that could omit an axis could omit the
inconvenient one — `unassessed` is how you say that out loud instead.

`billing_mode` · `quota_windows` · `balance` · `account_identity`

## A manifest is not a licence to skip code

`observation.implementation.kind` is `code-backed` or `data-only` (#2008 §1.4).
A code-backed route names a package directory and its test files, and
`of provider verify` requires every one of those paths to exist, so the claim
cannot be made by JSON alone. A data-only route — recognition metadata over an
already-reviewed strategy — may name neither.

## Deletion is guarded

`.github/workflows/replaydata-deletion-guard.yml` fails a PR that deletes a
manifest or a fixture from this tree; `tools/lib/replaydata-deletion-guard_test.sh`
carries the fixture that proves it. Before #2008 the guard's `case` statement
had no arm for this path at all, so everything here would have fallen through
to its allow arm.

## Adding a provider

1. `mkdir replaydata/providers/<billing-provider>/fixtures`
2. Write `manifest.json`. The top-level field set is closed — an unknown key is
   a violation, so start from an existing manifest.
3. Commit the redacted fixtures the manifest references. Every fixture's
   top-level keys must sit inside `redaction.allowed_fields`; the verifier
   fails on one that does not.
4. `go run ./tools/onboarding-factory/cmd/of provider verify`
5. `go run ./tools/onboarding-factory/cmd/of provider status`

Claim only what the cited evidence earns. The verifier will tell you when it
does not.
