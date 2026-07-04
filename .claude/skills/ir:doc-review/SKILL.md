---
name: ir:doc-review
description: Audit all project documentation for understandability, completeness, and validity using objective, binary criteria, then file one GitHub issue per documentation surface with exact, agent-ready fixes. Every finding is anchored to a quoted location and stamped with a stable ID so independent runs converge. Triggers on "/ir:doc-review", "audit docs", "review documentation", "check the docs", "doc audit". Supports a --report-only dry run and a --fix mode that applies high-confidence corrections directly instead of filing an issue for them.
---

# ir:doc-review — Objective Documentation Audit

Goal: audit **every** in-scope documentation surface against three axes —
**understandability (U)**, **completeness (C)**, **validity (V)** — and emit **one GitHub
issue per surface** containing exact, agent-ready fixes.

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

- `--report-only` — run the full audit and print per-surface findings, but file nothing. Use
  this first, and for the convergence check.
- `--fix` — after scoring (step 5), apply direct edits for the auto-fix-eligible findings
  defined in step 6b instead of filing an issue for them; every other finding still gets filed
  as an issue in step 7. This is what `ir:release` Step 4b runs on every release (#834), so
  drift can't silently accumulate the way the stale "no hooks" claim did — that claim's fix
  (correcting existing prose to match a code-derived fact, no new content authored) is exactly
  the auto-fix-eligible shape. Combine with `--report-only` to preview which findings would be
  auto-fixed vs. filed, without touching any file or opening any issue.
- An optional path or glob — limit the audit to matching surfaces (default: all in-scope).

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

### 6. Build one issue body per surface
For each surface with ≥1 finding:
- **Title:** `docs(<surface>): <N> findings — <c> Critical / <m> Major / <n> Minor / <k> Nit`
  (omit zero buckets). `<surface>` is the repo-relative path.
- **Body:**
  - First line: `> *Generated by AI (ir:doc-review). Verify before acting.*`
  - A findings table with columns: ID · Axis+Criterion · Severity · Location.
  - Then one `###` section per finding, in this exact order:
    - **Location** — `file:line` (or HTML anchor) + a verbatim quote of the offending text.
    - **What's wrong** — one sentence, tied to the named criterion.
    - **Exact fix** — the specific replacement text / precise addition / reference to correct.
      Agent-ready imperative, no "consider" or "maybe".
    - **Verification** — the concrete re-check (e.g. "the path resolves", "the grid lists all 7
      adapters", "the count matches `All()`").
  - Last line — the hidden reconciliation marker (one line, exactly):
    `<!-- ir:doc-review surface=<path> findings=<id1,id2,...> -->`

### 6b. Apply auto-fix-eligible findings directly (only with `--fix`)
Skip this step entirely unless `--fix` was passed — without it, every finding built in step 6
falls straight through to step 7 unchanged.

For each finding, decide whether it is **auto-fix eligible** before step 7 runs:

- **Eligible criteria:** V1, V2, V4, V5, V6, C2. These are exactly the findings where an
  existing claim is directly contradicted by a code-derived inventory/fact
  (`inventory-sources.md`), and step 6's own "Exact fix" text is a direct in-place correction or
  removal — no new section, paragraph, or example needs authoring.
- **Never eligible — always falls through to step 7:** U1–U6 (wording/ordering/audience are
  judgment calls), C1/C3/C4 (missing mention, missing surface, missing example — these need new
  content authored, not a correction), and V3 (link/anchor liveness — never blocking per the
  severity rubric anyway).
- Even within an eligible criterion, if the "Exact fix" isn't unambiguous from the inventory
  alone — the claim could be resolved two different ways and picking one is a judgment call —
  file it in step 7 instead of guessing.

For each eligible finding: re-locate the anchor by its verbatim quote (don't trust the original
line number — an earlier edit in the same file may have shifted it), apply the "Exact fix" text
from step 6 verbatim, then immediately run that finding's own "Verification" check. If the quote
no longer matches the live file, or verification fails after the edit, **revert the edit** and
leave the finding in the surface's draft issue body for step 7 — never leave a half-applied or
unverified edit in a doc, and never fuzzy-match a quote that's moved. When a surface has more
than one eligible finding, apply them bottom-of-file-upward so an earlier edit can't shift a
later finding's anchor off target.

Remove every successfully auto-fixed finding from its surface's draft issue body (built in step
6) before step 7 runs, and record it (surface + finding-ID) for the step 8 summary.

### 7. Dedupe and file (skip entirely if --report-only)
For each surface with findings remaining after step 6b (all findings, if `--fix` was not passed):
1. `gh issue list --repo ingo-eichhorst/Irrlicht --state open --search "in:title docs(<surface>)" --json number,body,title`.
2. **No match →** `gh issue create` with the title/body above and labels: `documentation`, the
   matching `scope:*` (mapping below), and `needs-triage`.
3. **Match →** read the existing body's marker and compare its finding-ID set to the new one.
   If the sets differ, rebuild title+body and `gh issue edit <N> --body-file … --title …` in a
   single call. If identical, leave the issue untouched.

Never open a second issue for a surface that already has one. Leave priority and
`ready-for-agent` to `ir:triage`. Create no new labels.

Scope-label mapping:
- `site/**` → `scope:site`
- `tools/**`, `.claude/skills/**` → `scope:tooling`
- `docs/relay-protocol.md`, `events.md` → `scope:daemon`
- anything else (`README.md`, `CONTRIBUTING.md`, …) → no scope label

### 8. Print the run summary
Always print: surfaces scanned, a findings-by-axis-and-severity table, and — unless
`--report-only` — the issues created / updated / unchanged with their URLs. When `--fix` was
set, also print the findings auto-fixed directly (surface + finding-ID, from step 6b) separately
from the findings that still got filed as issues.

## Conventions

- `gh` always targets `ingo-eichhorst/Irrlicht`.
- Reuse existing labels only; never create new labels.
- Without `--fix`, the skill **only reports** — it never edits documentation. With `--fix`, it
  edits documentation directly, but only for the auto-fix-eligible findings defined in step 6b;
  everything else still only gets filed as an issue, same as without the flag.
- `--fix` never commits or opens a PR itself — it leaves auto-fixed files as uncommitted
  working-tree changes for whatever invoked it (a release commit via `ir:release` Step 4b, or
  the user's own review when run standalone) to pick up.
- External-link liveness is reported but never blocks (see V3).
- When uncertain whether something is a finding, re-read the criterion: if it isn't a binary
  failure you can quote, drop it. A false positive costs more than a missed nit.
