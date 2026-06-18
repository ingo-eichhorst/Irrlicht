# Rubric — the contract for ir:doc-review

This file defines the **only** grounds on which a finding may be raised. Apply it literally.
A finding requires: (1) a named criterion that fails as a binary pass/fail, and (2) an anchor —
a verbatim quote at `file:line`/anchor, or a concrete unresolved reference. No criterion that
isn't mechanically checkable belongs here, and no finding may rest on taste.

## Reproducibility MUSTs

1. Every finding cites: **(a)** axis, **(b)** the exact criterion ID, **(c)** `file:line`/anchor
   (or the unresolved reference), **(d)** a verbatim quote of the offending text, **(e)** the
   authoritative source it was checked against (for C and V — see `inventory-sources.md`).
2. No subjective findings. "Reads awkwardly", "could be clearer", "I'd reword" are **not**
   findings. If you can't name the criterion and quote the failure, drop it.
3. Inventories are code-derived each run (`inventory-sources.md`), never remembered.
4. Severity comes only from the table below.
5. When in doubt, do **not** raise it. Convergence beats recall.

---

## Axis U — Understandability

*A reader matching the doc's stated audience, holding only the stated prerequisites, can act on
the doc without consulting unlinked external knowledge.*

- **U1 — Audience + prerequisites declared.** *Applies ONLY to onboarding/setup surfaces:*
  `README.md`, `site/docs/quickstart.html`, `installation.html`, `macos-setup.html`, and any
  "getting started" guide. On those, the doc names its intended reader and any required prior
  tools/knowledge; FAIL if neither is stated. Reference, policy, architecture, and contributor-
  workflow docs (AGENTS.md, SECURITY.md, api-reference, …) are **exempt** — stating an audience
  there is optional, so absence is not a finding.
- **U2 — Terms defined on first use.** Every project-specific term/acronym/product name
  (`adapter`, `relay`, `shard`, `cell`, `irrlichd`, `daemon`, `orchestrator`, …) is expanded or
  linked at its first occurrence in that doc. FAIL = the unglossed token + its line.
- **U3 — Runnable commands.** Every shell/code block is executable as written; any placeholder
  (`<N>`, `$VAR`, `…`) is defined immediately adjacent. FAIL = the block + the undefined
  placeholder.
- **U4 — Ordered, verifiable steps.** Procedures are numbered and each step states an observable
  outcome or expected output. FAIL = the step that has no checkable result.
- **U5 — Resolvable navigation.** Every "see X" / cross-reference in prose points to a target
  that exists and is reachable. FAIL = the dangling pointer. (Pure URL/anchor link-resolution is
  V3; U5 covers prose references like "see the configuration guide".)
- **U6 — No internal contradiction.** No two passages in the same doc give incompatible
  instructions. FAIL = both quotes, with locations.

## Axis C — Completeness

*Every shipped, user/contributor-relevant capability is documented somewhere in scope.* Computed
as a **set difference** between a code-derived inventory (`inventory-sources.md`) and documented
mentions.

- **C1 — Inventory coverage.** Every inventory item (adapter, user-facing CLI binary/flag,
  config var, public route, state, event, relay frame) has ≥1 in-scope documented mention.
  FAIL = the item with zero mention, named with its authoritative source location.
- **C2 — Exhaustive "supported X" lists.** Every documented support list equals the inventory
  set. FAIL = an inventory item missing from the list (name both).
- **C3 — Required surfaces exist.** Each of install, quickstart, configuration, API reference,
  contributing, security, changelog exists as a surface. FAIL = an expected surface absent.
- **C4 — Public capability documents inputs, outputs, and ≥1 example.** For each public route /
  user-facing command / config var that *is* documented, the doc states its inputs, its outputs/
  effect, and shows at least one example. FAIL = the documented item missing one of the three.

## Axis V — Validity (correctness / currency)

*Every concrete, checkable claim matches the current repository state.*

- **V1 — References resolve.** Every file path, directory, symbol/function name, command, and
  flag a doc names exists in the repo / `--help`. FAIL = the reference + why it doesn't resolve.
- **V2 — Counts/enumerations match.** Stated numbers ("supports N agents", "three states",
  ports, durations, versions) equal the code-derived value. FAIL = stated vs actual.
- **V3 — Internal links/anchors resolve.** Markdown/HTML internal links and anchors resolve.
  External links are checked and reported (4xx/5xx) but are **never blocking and never raise a
  Critical/Major** — at most a Nit. FAIL (internal) = the broken link/anchor.
- **V4 — Cross-surface consistency.** A fact stated in two surfaces agrees — especially in-repo
  markdown vs the hand-maintained `site/docs/*.html`. FAIL = the two divergent quotes + locations.
- **V5 — No stale version/date claims.** Version strings, "latest release", and the changelog
  head match `version.json` / git tags. FAIL = stated vs source-of-truth.
- **V6 — Examples reflect the current API.** Code/command examples use symbols/flags that still
  exist with the documented signature. FAIL = the example + the changed/removed API.

---

## Severity rubric (fixed)

Severity is a pure function of criterion + audience. Look it up; do not judge.

| Severity | When |
|----------|------|
| **Critical** | A V-axis failure that will make a reader fail a documented task: wrong/unrunnable install command, wrong port, removed flag in a quickstart (V1/V2/V6 on a user-facing onboarding surface); **or** a missing user-facing surface (C3 on install/quickstart); **or** a V1/U3 failure that breaks a documented *canonical workflow* — a referenced script/command stated as the definition of done but that does not exist (e.g. a `validate.sh` the contributor guide says must exit 0). |
| **Major** | C1/C2 — a **user-facing** capability undocumented or a support list non-exhaustive; U3 unrunnable command (non-onboarding); V4 cross-surface contradiction on a user-facing fact; V1/V2/V6 on a non-onboarding user-facing surface. |
| **Minor** | Contributor-facing gaps (internal binary/flag/var undocumented); U1/U2/U4/U5; C4 missing example; V3 internal link. |
| **Nit** | Cosmetic / currency drift with no functional impact (stale aside, V5 patch-level lag, external dead link). |

"Onboarding surface" = `README.md`, `site/docs/quickstart.html`, `installation.html`,
`macos-setup.html`. "User-facing" = README, `site/index.html`, and `site/docs/*` other than the
internal-architecture pages. Everything else (AGENTS.md, contributing, tool/skill docs,
`events.md`, architecture pages) is contributor-facing.

---

## Finding-ID recipe (stable across runs)

The ID makes re-runs map 1:1 and dedupe exact. Compute it the same way every time:

1. Build the **anchor string**:
   - In-text findings (U*, V1/V3/V4/V6, C4): the offending **verbatim quote**.
   - Inventory findings (C1/C2): the missing **item name** (e.g. `geminicli`, `IRRLICHT_MDNS`).
   - Count findings (V2/V5): the literal **claim text** (e.g. `supports 6 agents`).
   - Missing-surface findings (C3): the **expected surface name** (e.g. `quickstart`).
2. **Normalize** the anchor: strip leading/trailing whitespace and collapse every internal run
   of whitespace (including newlines) to a single space.
3. Build the input as three lines joined by `\n`: `<repo-relative-surface>`, `<criterionID>`,
   `<normalized-anchor>`.
4. Hash and take the first 8 hex chars:

```bash
# $SURFACE, $CRIT (e.g. V2), $ANCHOR already normalized
printf '%s\n%s\n%s' "$SURFACE" "$CRIT" "$ANCHOR" | shasum -a 1 | cut -c1-8   # macOS
printf '%s\n%s\n%s' "$SURFACE" "$CRIT" "$ANCHOR" | sha1sum   | cut -c1-8     # Linux
```

The ID depends only on (surface, criterion, normalized anchor) — not on line numbers, ordering,
severity, or run time — so cosmetic reflows that don't change the quoted text keep the ID stable.

---

## Worked examples (one pass + one fail per criterion)

Illustrative patterns showing what does and doesn't trip each criterion. They teach the
boundary; recompute against the live tree each run.

- **U1** — PASS: a doc opens with "Audience: contributors with Go installed." FAIL: a setup doc
  that never says who it's for or what must be installed first.
- **U2** — PASS: "the **daemon** (`irrlichd`, the background process) …" on first use. FAIL:
  "register the adapter's capabilities" where `adapter` and `capabilities` are never defined or
  linked.
- **U3** — PASS: ```gh issue view <N>``` immediately followed by "where `<N>` is the issue
  number". FAIL: a block referencing `$IRRLICHT_HOME` with no prior definition.
- **U4** — PASS: "3. Run `go test ./core/...` — it prints `ok`." FAIL: "Set everything up and
  run it" with no command and no expected result.
- **U5** — PASS: "see [CONTRIBUTING.md](../CONTRIBUTING.md)" and that file exists. FAIL: "see the
  onboarding guide" with no such surface anywhere.
- **U6** — PASS: one consistent install path. FAIL: §2 says "default port 7837" and §5 says
  "listens on 7838" in the same doc.
- **C1** — PASS: `geminicli` appears in the agents grid. FAIL: an adapter present in `All()` with
  zero mention in any in-scope doc.
- **C2** — PASS: the agents grid lists exactly the 7 adapters from `All()`. FAIL: the grid lists
  6 while `All()` returns 7.
- **C3** — PASS: `site/docs/quickstart.html` exists. FAIL: no quickstart/install surface at all.
- **C4** — PASS: a route documented with method, params, response, and a `curl` example. FAIL: a
  route named with no example or no response shape.
- **V1** — PASS: a doc cites `core/cmd/irrlichd/main.go` and it exists. FAIL: a doc references
  `core/cmd/irrlicht-watch/` which does not exist.
- **V2** — PASS: "three session states" equals the 3 constants in `session.go`. FAIL: "supports
  6 agents" while `All()` returns 7.
- **V3** — PASS: an in-page anchor that resolves. FAIL: a relative link to a renamed/missing file.
- **V4** — PASS: `CONTRIBUTING.md` and `site/docs/contributing.html` describe the same PR flow.
  FAIL: README says "squash and delete branch" while the site says "merge commit".
- **V5** — PASS: changelog head equals `version.json`. FAIL: a footer reading "v0.3.x" while
  `version.json` is `0.5.1`.
- **V6** — PASS: an example using a flag that still exists in `--help`. FAIL: an example passing
  `--watch` to a binary whose `flag` set no longer defines it.

## False-positive traps (do NOT flag these)

These look like findings but are reconciled in-context. Flagging them breaks convergence by
adding noise that careful readers wouldn't.

- **Relay "v0" vs `protocol_version: 1`.** `docs/relay-protocol.md` is titled "Relay wire
  protocol (v0)" and says "This document covers only what v0 builds", *and* states
  `hello.protocol_version` is "currently 1". "v0" is the feature-milestone name; `1` is the
  wire-format version. The doc reconciles them — **not** a V2/V6 finding. Flag relay validity
  only on a genuine mismatch (a frame type in the doc absent from `envelope.go`, or vice versa).
- **`+`-suffixed dev versions.** `0.5.1+abc1234.dirty` is the documented dev-build format
  (`tools/version.sh`), not a stale-version drift from `version.json`'s `0.5.1`.
- **Three-states intentionally excludes "cancelled".** Docs stating exactly three states
  (working/waiting/ready) are correct; cancellation maps to `ready` by design. Do not "complete"
  the list with a fourth state.
- **`CLAUDE.md` → `AGENTS.md`.** `CLAUDE.md` intentionally just includes `AGENTS.md`; its
  brevity is not a U1/C-axis gap.
- **Internal/dev tools.** Missing docs for `tools/onboarding-factory/cmd/*` (`of`, `viewer`,
  `replay`) are at most Minor — they're contributor tools, not user-facing CLI.
