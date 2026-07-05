---
name: ir:doc-review
description: Audit all project documentation for understandability, completeness, and validity using objective, binary criteria, then fix every finding directly in place. Filing a GitHub issue is the rare fallback — only for a finding whose fix is genuinely ambiguous or that fails verification after being applied. Every finding is anchored to a quoted location and stamped with a stable ID so independent runs converge. Triggers on "/ir:doc-review", "audit docs", "review documentation", "check the docs", "doc audit". Supports a --report-only dry run that changes nothing and files nothing.
---

# ir:doc-review — Objective Documentation Audit

Goal: audit **every** in-scope documentation surface against three axes —
**understandability (U)**, **completeness (C)**, **validity (V)** — and **fix every finding
directly, in place**. Filing a GitHub issue is the fallback for the rare finding that can't be
safely applied (see step 7) — it is not the default outcome of a finding.

The audit must be **reproducible**: every finding is binary (a named criterion failed),
anchored to a verbatim quote at a location, scored by a fixed severity rubric, and stamped with
a stable finding-ID. Two runs on an unchanged tree must converge on the same findings.

Two companion files define the objective parts. **Read both before auditing and apply them
literally** — never invent criteria, never grade on subjective grounds (tone, elegance,
"feels unclear"):

- `references/rubric.md` — the criteria (U1–U6, C1–C4, V1–V6), the severity rubric, the
  finding-ID recipe, and a worked pass/fail example per criterion. **This is the contract.**
- `references/inventory-sources.md` — the bash recipes that derive the authoritative
  inventories (adapters, CLI flags, env vars, routes, states, events, relay frames, version)
  from code each run. Axes C and V check docs against these inventories, never against memory.

## Inputs

- `--report-only` — run the full audit and print per-surface findings, but change nothing and
  file nothing. Use this first, and for the convergence check.
- An optional path or glob — limit the audit to matching surfaces (default: all in-scope).

`ir:release` Step 4b runs this fix-by-default workflow on every release (#834, #837), so drift
can't silently accumulate the way the stale "no hooks" claim once did.

## Workflow

### 1. Locate the repo root and confirm `gh`
Work from `git rev-parse --show-toplevel`. Confirm the repo is `ingo-eichhorst/Irrlicht`
(`gh repo view --json nameWithOwner`) and `gh auth status` succeeds. If `--report-only`, the
`gh auth` check is optional.

### 2. Build the code-derived inventories
Run every recipe in `references/inventory-sources.md` and capture the resulting sets: adapters,
CLI binaries + flags, config env vars, daemon routes, session states, lifecycle event kinds,
relay frames + `ProtocolVersion`, the version string, and default ports/TTLs. These sets are
the authoritative truth for axes C and V. Never hardcode them — they change with the code. If a
recipe errors because code moved, fix the recipe in `references/inventory-sources.md` and note
the drift in the run summary; do not fall back to a remembered list.

### 3. Enumerate in-scope surfaces
In scope (each file is one **surface**):
- Top-level markdown: `README.md`, `AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md`,
  `CODE_OF_CONDUCT.md`, `SECURITY.md`, `CHANGELOG.md`, `events.md`
- `docs/**/*.md` (e.g. `docs/relay-protocol.md`)
- `tools/*/README.md` and `tools/*/SKILL.md`
- `.claude/skills/*/SKILL.md` and `.claude/skills/*/skill.md`
- Published site: `site/index.html` and `site/docs/*.html`
- Design-system surfaces: `tools/irrlicht-design-system/README.md` and its `ui_kits/*/README.md`

Out of scope — never audit, never count toward completeness: `replaydata/**`,
`.claude/worktrees/**`, `node_modules/**`, `.build/**`, `.aider.chat.history.md`, generated
replay goldens, vendored assets.

The published site drives axis V's cross-surface check: pair each `site/docs/<x>.html` with its
in-repo source where one exists, e.g. `CONTRIBUTING.md` ↔ `site/docs/contributing.html`,
`events.md` ↔ `site/docs/state-machine.html` / `session-detection.html`, the README feature/agent
list ↔ `site/index.html` agent grid, `CHANGELOG.md` ↔ `site/docs/changelog.html`.

### 4. Apply the rubric per surface
For each surface, evaluate **every** criterion in `references/rubric.md`. Raise a finding ONLY
when a criterion fails AND you can anchor it to `file:line` (or an HTML anchor) with a
**verbatim quote** of the offending text — and, for C/V findings, name the authoritative source
it was checked against. **If you cannot quote it, it is not a finding.**

You MAY fan out one read-only subagent per surface to parallelize, handing each the rubric text
and the pre-built inventories. Because the criteria, severity, and ID recipe are fully specified
in the rubric, fan-out must not change the result.

### 5. Score and ID each finding
- Severity comes solely from the fixed rubric (Critical / Major / Minor / Nit) — by criterion +
  audience, never reviewer taste.
- Compute the stable finding-ID exactly as `references/rubric.md` specifies. Same input → same
  ID across runs.

### 6. Draft the fix for each finding
For every finding, write out (this is scratch working state, not an issue body yet — step 7
reuses it verbatim for whatever remains unresolved):
- **Location** — `file:line` (or HTML anchor) + a verbatim quote of the offending text.
- **What's wrong** — one sentence, tied to the named criterion.
- **Exact fix** — the specific replacement text / precise addition / reference to correct.
  Agent-ready imperative, no "consider" or "maybe". For C1/C3/C4 (missing content) this means
  actually drafting the missing section/example/mention, grounded in the authoritative
  code-derived source — not just describing that something is missing. When the authoritative
  source is a comment claiming something is "reserved", "not yet implemented", or "future work",
  verify that against actual call sites/behavior (grep for where the constant/function is really
  used) before repeating the comment as fact — comments go stale faster than code, and a drafted
  fix that just echoes a stale comment is itself a new validity defect.
- **Verification** — the concrete re-check (e.g. "the path resolves", "the grid lists all 7
  adapters", "the count matches `All()`").

### 6b. Apply every determinable fix directly
Skip this step entirely if `--report-only` was passed — every finding then stays unresolved and
flows straight to step 8's report. Step 7 (which files/closes issues) is also skipped under
`--report-only`, so nothing is edited and no GitHub issue is touched either.

Otherwise, for each finding: re-locate the anchor by its verbatim quote (don't trust the
original line number — an earlier edit in the same file may have shifted it), apply the "Exact
fix" text from step 6, then immediately run that finding's own "Verification" check.

- If the quote no longer matches the live file, or verification fails after the edit, **revert
  the edit** and mark the finding **unresolved** — never leave a half-applied or unverified edit
  in a doc, and never fuzzy-match a quote that's moved.
- Skip applying (mark **unresolved**) only when the "Exact fix" is genuinely ambiguous — the
  finding itself supports two materially different valid corrections and picking one is a real
  judgment call, not just an authoring effort. This is a per-finding judgment, not a blanket
  exemption for a whole criterion or axis — most C1/C3/C4/U-axis findings have one obviously
  correct fix (add the missing mention, name the missing surface, state the missing example)
  and should be applied like any other.
- External-link liveness (V3, external links only) is never a fix target and never becomes an
  unresolved finding either — it's owner-side and reported informationally in step 8, nothing
  more.
- When a surface has more than one finding, apply them bottom-of-file-upward so an earlier edit
  can't shift a later finding's anchor off target.

Record every successfully applied fix (surface + finding-ID) for the step 8 summary.

### 7. File or close issues only for what's left unresolved (skip entirely if --report-only)

First, fetch every currently-open doc-review issue **once**, up front — this single list drives
both the create-or-update loop and the close-stale-issues step below, so a surface with zero
unresolved findings this run (nothing to loop over otherwise) can still be checked for a stale
issue to close:
`gh issue list --repo ingo-eichhorst/Irrlicht --state open --search "in:title docs(" --json number,title,body`
Extract each returned issue's surface from its title (`docs(<surface>): …`) into a
surface→issue-number map.

For each surface with ≥1 **unresolved** finding after step 6b:
- **Title:** `docs(<surface>): <N> findings — <c> Critical / <m> Major / <n> Minor / <k> Nit`
  (omit zero buckets). `<surface>` is the repo-relative path.
- **Body:**
  - First line: `> *Generated by AI (ir:doc-review). Verify before acting.*`
  - A findings table with columns: ID · Axis+Criterion · Severity · Location.
  - Then one `###` section per finding (Location / What's wrong / Exact fix / Verification, as
    drafted in step 6), plus a line stating *why* it's unresolved (ambiguous fix, or which
    verification check failed).
  - Last line — the hidden reconciliation marker (one line, exactly):
    `<!-- ir:doc-review surface=<path> findings=<id1,id2,...> -->`
- **No entry for this surface in the map →** `gh issue create` with the title/body above and
  labels: `documentation`, the matching `scope:*` (mapping below), and `needs-triage`.
- **Entry found →** read the existing body's marker and compare its finding-ID set to the new
  one. If the sets differ, rebuild title+body and `gh issue edit <N> --body-file … --title …` in
  a single call. If identical, leave the issue untouched.

For each surface **in the map** (has an existing open issue) that does **not** appear in the
≥1-unresolved-finding loop above — everything got fixed directly, or the prior findings no
longer reproduce:
`gh issue close <N> --comment "All findings resolved directly by ir:doc-review — see the fixes to <surface>."`

Never open a second issue for a surface that already has one. Leave priority and
`ready-for-agent` to `ir:triage`. Create no new labels.

Scope-label mapping:
- `site/**` → `scope:site`
- `tools/**`, `.claude/skills/**` → `scope:tooling`
- `docs/relay-protocol.md`, `events.md` → `scope:daemon`
- anything else (`README.md`, `CONTRIBUTING.md`, …) → no scope label

### 8. Print the run summary
Always print: surfaces scanned, a findings-by-axis-and-severity table, and the findings fixed
directly (surface + finding-ID, from step 6b). Unless `--report-only`, also print any issues
created / updated / closed with their URLs, and any findings still unresolved (with the reason)
— this list should normally be empty or very short.

## Conventions

- `gh` always targets `ingo-eichhorst/Irrlicht`.
- Reuse existing labels only; never create new labels.
- `--report-only` is the only mode that doesn't edit documentation. Every other run fixes
  every determinable finding directly and files (or closes) an issue only for what's left
  unresolved per step 6b/7.
- Direct fixes never get committed or turned into a PR by this skill itself — it leaves them as
  uncommitted working-tree changes for whatever invoked it (a release commit via `ir:release`
  Step 4b, or the user's own review when run standalone) to pick up.
- External-link liveness is reported but never blocks and never gets filed (see V3).
- When uncertain whether something is a finding, re-read the criterion: if it isn't a binary
  failure you can quote, drop it. A false positive costs more than a missed nit.
