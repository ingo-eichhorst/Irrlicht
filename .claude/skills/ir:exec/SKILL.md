---
name: ir:exec
description: End-to-end execution of a GitHub issue via `/ir:exec [mode] <N>` — open a worktree, investigate, then either present a visual HTML plan for approval or proceed straight to implementation (mode-dependent), open a PR, review it with /code-review, fix findings, simplify, hand back the PR link with a test-or-merge recommendation, and land the merge on request. `mode` defaults to `auto`, which picks `plan` or `full` from the issue's `/ir:triage` signals. Triggers on "/ir:exec", "exec issue", "implement issue", "plan issue", or when the user gives an issue number/URL and asks to plan, implement, or land it.
---

# Execute a GitHub Issue, End to End

Take an issue from a number to a review-clean, landed PR:

```
/ir:exec [mode] <N>
```

Four modes share one flow; `mode` defaults to `auto` when omitted.
`ir:exec` always works in its own worktree (Phase 1), so there's no separate
"runs in a worktree" naming convention to invoke — the mode is just an
argument.

```
/ir:exec [mode] <N>
  → not-already-claimed? → worktree → investigate → (auto resolves to plan or full here)
    → plan: HTML + ⛔ approval, or full: inline summary → assign issue
    → implement → verify
    → PR → /code-review (effort scaled to diff) → fix → /simplify (or inline) → recommendation [plan / full stop here]
    → land: confirm mergeable → squash-merge → remove worktree   [close]
```

## Modes

| Mode | Invocation | Gate? | Stops after |
|---|---|---|---|
| `auto` (default) | `/ir:exec <N>` or `/ir:exec auto <N>` | Decided from the issue's `/ir:triage` signals — see Auto mode below | Whatever the resolved mode (`plan` or `full`) stops after |
| `plan` | `/ir:exec plan <N>` | Yes — render HTML plan, end turn, wait for explicit approval | Phase 6 (hand-back), once approved |
| `full` | `/ir:exec full <N>` | No — skip the wait | Phase 6 (hand-back) |
| `close` | `/ir:exec close [N]` (or replying "merge" to Phase 6) | n/a | Phase 7 (land) |

`full` still follows every other rule in this skill — worktree isolation, one
branch/PR per issue, AGENTS.md conventions — only the approval wait and the
HTML plan artifact are dropped.

### Auto mode

`auto` never invents a new strategy — it picks between `plan` and `full` using
signals `/ir:triage` already computes, then proceeds exactly as that mode
would from there on.

`auto` issues no `gh` reads of its own: the readiness label and triage comment
are read in **Phase 2 step 3**, which *every* mode runs. `auto` only interprets
what that read already returned.

Read the tiers **in order** and stop at the first that yields a value — the
richer signal wins, and the legacy tier is explicit rather than an accident of
a loose regex:

| Tier | Signal | Resolves to |
|---|---|---|
| — | `needs-info` label present | **Refuse.** Implement nothing — report the blockers (from the triage comment if present) and point at `/ir:triage <N>` or manual clarification. Refusing is `auto`-only: it is what "the user named no mode" resolves to. A named mode does **not** refuse — it surfaces the label and continues (Phase 2 step 3a). |
| 1 | A `**Run plan:**` line | Take its mode (`full` / `plan`) verbatim, plus its investigation depth, review effort and simplify depth — `/ir:triage` already did this arithmetic. |
| 2 | A numeric `**Complexity:**` — e.g. `6 (base 5, openness +1)` | `full` at effective level ≤ 6, `plan` at 7–8. A level of 9–10 should not have reached `ready-for-agent`; if one has, treat it as `plan` and say so. |
| 3 | A legacy `**Complexity:** Low` / `Medium` / `High` | `full` for Low or Medium, `plan` for High. Pre-dates the effective-level scale and stays supported — issues triaged before it cannot be migrated. |
| 4 | No readiness label and no `/ir:triage` comment at all | `plan` — safest default when nothing has assessed the issue. |

Anything that parses cleanly at none of these tiers (a hybrid like
"Medium-to-High", a malformed line) resolves to `plan`. An ambiguous read of
the signal should never resolve toward skipping the gate.

If the issue was never triaged, make the call yourself during Phase 2's
investigation using `/ir:triage`'s own base-level ladder (1–2 one file; 3–4 one
function; 5–6 one slice across 2–4 files; 7–8 cross-cutting or schema; 9–10
subsystem) rather than a new calibration.

`auto` only ever resolves to `plan` or `full` — never to `close` (landing is a
step only a human would ask for directly) — and it must never itself invoke
the Workflow tool no matter how high the detected complexity; that AGENTS.md
rule is unconditional.

## Inputs

`<N>` is the issue number — from what the user typed, or from the branch/
worktree name. `close` additionally resolves it from `pwd` / `git status -sb` /
`gh pr view` if omitted (see Phase 7). If unresolvable, ask before continuing.

## Phase 1 — Worktree

1. **Resolve the issue number** and a short kebab slug from its title.
1a. **Confirm nobody is already on it** — a precondition of `worktree add`, not a
   courtesy. Concurrent `ir:exec` agents are the normal mode of operation here,
   and a collision is invisible from both sides: an issue number on its own says
   nothing about work already in flight.
   ```bash
   gh pr list --state open --search "<N>" --json number,headRefName,title
   git worktree list | grep -w "<N>"
   git ls-remote --heads origin | grep -w "<N>"
   ```
   Step 2's own "skip if already in a clean, issue-matching worktree" guard does
   not cover this: it inspects `pwd`, so it only ever sees the worktree this
   session is standing in — never one another agent opened.

   On any hit, **surface it and pause before creating anything** — the same idiom
   step 9 and Phase 7 step 18 use. Report what you found and let the human choose:
   continue the existing branch, review the open PR, or open a second one
   deliberately. Never silently reimplement. (Real incident: #1178 had an open,
   CI-green PR — #1207 — *and* a live worktree with uncommitted WIP. `ir:exec`
   opened a second branch anyway, ran a complete independent implementation that
   converged on the same design, and only tripped over the duplicate at Phase 5
   while grepping for something unrelated. The entire branch was discarded.)
2. **Open a dedicated worktree** off the latest `main` (skip if already in a clean,
   issue-matching worktree — run `pwd` + `git status -sb` to check):
   ```bash
   git -C <main-repo> fetch origin main
   git -C <main-repo> worktree add -b feat/<N>-<slug> .claude/worktrees/<N>-<slug> origin/main
   ```
   `.claude/worktrees/` is gitignored. **Do all work via the worktree path** — editing
   the main checkout's files by absolute path touches the wrong tree.

## Phase 2 — Investigate & plan

3. **Fetch the issue, its comments, and its readiness signals** — one read, run by
   **every mode**, not just `auto` (comments often hold the real spec):
   ```bash
   C=$(gh issue view <N> --comments); echo "$C"            # the spec (--repo <owner/repo> for cross-repo)
   gh issue view <N> --json labels -q '.labels[].name'     # ready-for-agent / needs-info?
   grep -o '\*\*Run plan:\*\*[^—]*'   <<<"$C" | tail -1    # tier 1 — current triage output
   grep -o '\*\*Complexity:\*\*[^—]*' <<<"$C" | tail -1    # tiers 2/3 — numeric level, or legacy Low/Medium/High
   ```
   These are the reads the Auto mode table consumes, hoisted out of that branch so a
   named mode sees the same signals `auto` would have. `auto` resolves its mode from
   them in step 6; a named mode uses them for step 3a and for step 4's fan-out depth.
3a. **Surface a `needs-info` label — then continue.** If the readiness label is
   `needs-info` and the mode was named explicitly (`plan` / `full`), say so in **one
   line** of response text *before editing any file*: the label, the blockers from the
   triage comment, and the assumption you are making in place of each. Then proceed
   normally.
   ```
   ⚠ #977 is needs-info (blockers: pick a direction; state a target).
     Proceeding per `full`; assuming <X> and <Y>.
   ```
   Naming a mode is the user's decision to skip the gate, so this **never** becomes a
   second gate: don't refuse, don't stop the turn, don't ask for confirmation. The
   point is only that a blocker triage deliberately reserved for the maintainer — and
   the judgment call you substituted for it — is visible up front rather than buried in
   the Phase 6 hand-back ~800 lines later. In `plan` mode, carry the same blockers into
   the plan's Risks & Unknowns; the approval gate then covers them. (Real incident:
   `/ir:exec full 977` invented answers to both of #977's open blockers and disclosed
   it only at hand-back — PR #1211.)
4. **Investigate the code, at the depth the run plan names.** Delegate to `Explore`
   subagents with a prompt naming the issue's key terms — file names, symbols, error
   strings, feature names — asking where it lives, what touches it, current behavior.
   Don't grep manually first; the subagent protects the main context.

   **Scale the fan-out to the band** (from the triage comment's `**Run plan:**` line,
   or from the effective level): **1–2** read the named file directly, no subagent at
   all; **3–4** one `Explore` at "medium"; **5–6** one or two at "medium"; **7–8** two
   or three at "very thorough". Dispatching a subagent fleet at a one-line fix is the
   waste this scaling exists to prevent — and skipping investigation on a cross-cutting
   change is the failure on the other side.
5. **Synthesize the plan**: Problem (one sentence), Approach/Design (the chosen
   direction, naming files/functions), Steps (ordered, concrete, one logical change
   each), Files touched (new/mod/del), Risks/unknowns. **For a user-facing feature,
   identify which frontends this repo ships (`platforms/macos/` Swift app,
   `platforms/web/` dashboard) and scope Approach + Steps to implement it in every
   frontend the capability applies to — not just whichever is easiest to reach
   first. If one is deliberately excluded, say so explicitly under Risks/unknowns
   with the reason; never let a single-frontend implementation land silently as if
   it shipped everywhere.** (Real incident: the Activity Matrix History chart
   landed web-only, its changelog entry didn't say so — unlike the DORA entry
   right above it, which explicitly said "on both macOS and web" — and a later
   from-scratch QA pass on the macOS app couldn't find the feature at all.)
6. **If invoked as `auto`**, resolve the signals **already read in step 3** per the Auto
   mode table above and continue as whichever of `plan`/`full` it names — everything
   from here on follows that mode's path. (Re-running the `gh` reads here is the
   duplication step 3 exists to avoid.)

## Phase 3 — Present the plan (branches by mode)

### `plan` mode (gated)

7. **Render the plan to HTML.** Read `templates/plan.html` (next to this file). Copy it
   to `/tmp/ir-exec-plan-<N>.html` and fill it in. The page reads outside-in — a
   stranger to the codebase should understand the top half:

   **Section roster (order in the template):**
   - **TL;DR** — `{{TLDR}}`, 2–3 sentences: the problem + the intent. The most-read line.
   - **High-level design** — `{{HLD_INTRO}}` + `REPEAT:hld` bullets. **Code-free**: no
     file or function names here (those belong to the technical Approach below).
   - **Visual** — pick exactly one archetype (see next bullet), or delete all three.
   - **Approach & Design (technical)** — the file/function-level direction (`REPEAT:approach`).
   - **Steps** — `REPEAT:step`; card = title + one-line summary + one or more `chip`
     spans for the file(s) it touches; the deep rationale/edge-cases go in the step's
     `<template>` (click-to-reveal).
   - **Files Touched** — `REPEAT:file`, `badge` class `new`|`mod`|`del`; per-file "what
     changes and why" in its `<template>`.
   - **Risks & Unknowns** — `REPEAT:risk` (native `<details>`).

   **Fill primitives:** replace every `{{TOKEN}}`; duplicate each
   `REPEAT:x`…`/REPEAT:x` region per item and delete the leftover example; delete any
   unused `OPTIONAL:x` region whole. Then **strip every `REPEAT:`/`OPTIONAL:` marker
   comment** from the file — they are scaffolding, not output (a kept marker is
   harmless but clutters the source).

   **Pick ONE visual archetype** matching the dominant kind of work (delete the others;
   delete all three if no visual adds signal — don't ship an empty box):
   | Issue kind | Keep block | What to author |
   |---|---|---|
   | Frontend / UI | `OPTIONAL:ui` | a real screenshot ("Today") + a hand-authored SVG wireframe ("Proposed"); mark each new region `<g data-detail="ui-N">` |
   | Data processing | `OPTIONAL:dataflow` | node-and-arrow flow; each node `data-detail` reveals its transform |
   | Vertical slice / many components | `OPTIONAL:components` | impact-map nodes, `data-impact` = `changed`\|`adjacent`\|`untouched` — show the blast radius AND what's left alone |

   **UI screenshot policy** (the `ui` archetype embeds the *real* current UI). Obtain the
   capture **before** rendering — never ship the page with an unfilled
   `{{UI_SCREENSHOT_DATA_URI}}`:
   - **Web UI (a URL exists):** use the `claude-in-chrome` tools to open the running UI
     and screenshot the relevant viewport (not a giant full-page capture). The capture
     comes back as a file path — **base64-encode it** and embed as the "Today"
     `<img src="data:image/png;base64,…">`. Never put a `file://`, `http(s)://`, or raw
     path in `src` (that is an external/broken reference, not self-contained).
   - **Non-URL UI (macOS app, CLI) or no clean capture:** there's no reliable capture at
     plan time — either **ask the user for a screenshot** (and wait for it before
     rendering), **hand-model the current screen as SVG** in place of the `<img>`, or
     **delete the `OPTIONAL:ui` block**. **Never invent a UI you haven't seen**, and never
     render with the image token still unfilled.

   **Interactivity:** to make anything click-to-reveal, give it `data-detail="<id>"`
   (the id must be **unique within the page**) and
   put the detail in a sibling `<template data-detail-body="<id>">`. **Do not write event
   handlers** — the template's inline engine handles it.

   **Self-containment & hazards:**
   - Self-contained = **no EXTERNAL resources** (no URLs, CDN scripts, or web fonts).
     The inline `<style>` block and the inline `<script>` engine are part of the
     template — **keep both byte-for-byte**, add no others. `data:` URIs (the screenshot)
     are fine.
   - Never write a comment-close sequence inside a comment's text, and never write a
     closing `</template>` or `</script>` inside a detail body.
   - Before presenting, verify **no `{{TOKEN}}` is left behind** (the page also shows a
     warning banner at load time if any slip through).
   - One visual archetype max; Steps ≤ 8–10.
8. **Present the link, then end your turn.** Give the user the
   `file:///tmp/ir-exec-plan-<N>.html` link plus a 2–3 line summary, and **stop the
   response there** — do not present the link and keep working in the same turn. The
   next user message is the gate: treat only an explicit approval as go. An ambiguous or
   partial reply ("looks good, but…") is a change request — revise the plan + HTML and
   re-present. Do not edit a single implementation file until the user approves.

### `full` mode (gate-skipped)

Nobody is gating on the plan, so skip the HTML artifact and the wait entirely:

7. **Post a short inline plan summary** in the response text — Problem, Approach,
   Steps, Files touched, each 1 line — no separate render step, no `/tmp` file.
8. **Proceed straight into Phase 4 in the same turn.** No stop, no waiting for a reply.

## Phase 4 — Implement

9. **Assign the issue** — a gated precondition of starting Phase 4, not a step to
   fire-and-forget:
   ```bash
   ME="$(gh api user -q .login)"
   gh issue view <N> --json assignees -q '[.assignees[].login]'   # before: did @me already own it?
   gh issue edit <N> --add-assignee @me   # add --repo <owner/repo> for cross-repo
   gh issue view <N> --json assignees -q "[.assignees[].login] | index(\"$ME\") != null"
   ```
   Verify by **login, not by count**. `.assignees | length` is `>= 1` whenever
   *anyone* is assigned, so it reports success even when the `--add-assignee` did
   nothing at all — which is exactly what happens when `@me` resolves to an account
   that already owned the issue. If the check comes back `false`, retry the `edit`
   once and re-check; if it's still `false`, **surface that and pause** rather than
   silently proceeding unassigned — the same idiom Phase 7 uses for an unmergeable
   PR.

   The "before" read is what makes an abort safe. If the run is later abandoned,
   **do not blanket-`gh issue edit <N> --remove-assignee @me`** on the way out:
   when `@me` was already in that list, the removal strips a pre-existing human
   assignment that was never yours to clear. Remove only what this step actually
   added.
10. **Push through the implementation** in the worktree.
    - If the work is complex/multi-part, break it into tasks with `TaskCreate` and work
      them in order (as you naturally would). For a small change, just implement it.
    - Follow the repo's conventions (AGENTS.md): surgical changes, match surrounding
      style, three-state model, hexagonal layering, etc.
11. **Verify** before declaring done: run the test suites relevant to what you touched
    (per AGENTS.md — `go test ./core/... -race -count=1`, the factory/web suites, replay
    fixtures, `swift build`/`swift test`, as applicable). Fix what you broke. **For a
    UI-facing change, also confirm it's actually reachable in every frontend the plan
    scoped it to** — open the macOS app (`/ir:test-mac`) and/or the web dashboard and
    look. A passing test suite in the same area is not evidence the specific feature
    exists on a specific platform — name the exact test that covers the claim, or look.
11a. **Prove red-first.** For every test asserting a claimed defect, run it
    **before** the fix exists and confirm it **fails**. Paste the failure — in
    the PR body, or the issue if you're reporting back there. Do this during
    step 10: write the test first, before the fix exists. If the fix is already
    in your worktree, checkpoint it (`git commit -m wip`), revert the fix, run
    the test, then restore — never `git stash`, whose stack is shared across
    worktrees (AGENTS.md).

    A test that **passes** on `main` is not a regression test. Either the
    diagnosis is wrong, or the test doesn't reach the defect — e.g. it exercises
    a stub that is blind to the field it asserts on. **STOP and report.** Do not
    proceed on a green that proves nothing.

    **Locks** — tests pinning behavior that must *not* change — pass on `main` by
    construction. Say which tests those are explicitly, rather than letting their
    green read as a red-first proof.

    This applies to test code the *issue itself* pasted. A code block in an issue
    is a proposal, not evidence: `/ir:triage` marks such a test **unproven** on
    its Verifiability axis, and an untriaged one owes you the same run. (Real
    incident: #1076 shipped seven ACs with complete test code aimed at
    `core/pkg/tailer`, whose `handleTestSystemEvent` stub never reads the field
    under test — the literal AC would have passed on `main` while the bug
    shipped. Three of six issues in that batch specified tests that pass on
    `main`; nothing in this skill caught it.)

## Phase 5 — PR, review, simplify

**Calibrate the depth of steps 13–14 to the diff you just produced** — a
one-line string edit and a multi-package refactor must not get identical
scrutiny (spending four `/simplify` subagents on a doc-string is the failure
this guards against). The diff exists as soon as Phase 4 is done; measure it
cheaply first:

```bash
git fetch origin main                     # refresh origin/main first; steps 13–14 diff against it, not stale local main
git diff --shortstat origin/main...HEAD   # files + lines; origin/main, not local (stale-ref footgun)
```

…then glance at *what* changed (docs/strings/tests only? new control flow? how
many packages?) and read it into one of four tiers. These are calibration
anchors, not hard gates — use judgment at the boundaries:

| Diff tier | Looks like | Step 13 review | Step 14 simplify |
|---|---|---|---|
| **Trivial** | docs / comments / string-constants / config only, or ≤~30 non-test lines in 1 file | `low` | **skip the fan-out** — do an inline reuse/simplification/efficiency/altitude glance and say so |
| **Small** | 1–3 files, one concern, no new logic, <~150 lines | `low` | inline glance, no 4-agent fan-out |
| **Medium** | 2–5 files / one slice / some new logic | `medium` | run `/simplify` (fan-out is fine) |
| **Large / risky** | multi-package, schema, cross-adapter, logic-heavy, >~400 lines | `high` | run `/simplify` (fan-out) |

**These tiers are the post-hoc correction.** Triage's run plan set a *predicted*
review effort before any code existed; this table measures what the change
actually turned out to be. When the two disagree, **this table wins** — a
predicted level is a budget, a real diff is evidence. A large gap in either
direction is worth one line in the Phase 6 hand-back: it is the only feedback
the levelling scale ever gets.

**Guardrails (unconditional):** never auto-select `/code-review ultra` (the
cloud, billed, human-only path) and never use the Workflow tool for either
step. The auto range is bounded **low↔high**; `max`/`ultra` stay
human-triggered.

12. **Open the PR** against `main`:
    ```bash
    git push -u origin feat/<N>-<slug>
    gh pr create --base main --fill   # or a written title/body; reference "Closes #<N>"
    ```
    End the PR body with the `🤖 Generated with [Claude Code]` line.
13. **Review the diff** at the **calibrated effort** (`low` for trivial/small,
    up to `high` for large/risky — never `ultra`/Workflow). Run the
    `/code-review` skill with the **explicit base** `origin/main...HEAD` (e.g.
    `/code-review low origin/main...HEAD`), then fix every finding it surfaces
    in the worktree and push the fixes. A single review pass, not a fan-out.
    Pass the base explicitly because in this worktree *both* of the skill's own
    defaults mislead: the local `main` ref is stale (never updated when other
    PRs merge, so `main...HEAD` drags in already-merged hunks) and after step
    12's `git push -u`, `@{upstream}...HEAD` is ~empty (upstream now points at
    the pushed branch, so it reviews nothing).
14. **Simplify per the tier.** For **Medium/Large** diffs run the `/simplify`
    skill with the same explicit base (`/simplify origin/main...HEAD`, for the
    reason in step 13); for **Trivial/Small** diffs skip its 4-agent fan-out
    and do the reuse/simplification/efficiency/altitude review inline, stating
    what you checked. Push any cleanup.

## Phase 6 — Hand back

15. **Present the final PR link** and ask whether the user wants to **test** or **merge**.
    Make a recommendation, and let your **confidence** decide which you lead with:
    - **Lean merge** when: `/code-review` came back clean (no unresolved findings), all
      relevant suites are green, and the diff is small/low-risk and fully covered by
      tests. Suggested: proceed to Phase 7 (land), or `/ir:exec close`.
    - **Lean test-first** when: review raised non-trivial findings, tests are
      failing/flaky/absent for the behavior, the diff is large or risky, or the change
      is user-visible and only confirmable by running it. Point at `/verify`, or
      `/ir:test-mac` for macOS-app changes.
    State the recommendation in one line with the reason; the merge decision is the
    user's. A reply of "merge" (or `/ir:exec close`) moves into Phase 7.

## Phase 7 — Land (`close` mode)

Self-sufficient: this phase resolves the issue/PR itself rather than assuming
continuity from earlier phases, so it works standalone or as a continuation of
Phase 6.

16. **Resolve the worktree, branch, and PR** if not already known from context —
    `pwd` / `git status -sb` for the worktree and branch, `gh pr view` for the PR —
    the same way Phase 1 resolves the issue number.
17. **Confirm the worktree is clean and pushed**: `git status -sb` shows nothing
    outstanding and the branch is up to date with its remote.
18. **Confirm the PR is mergeable**: `gh pr view <N> --json mergeable,state`. If
    checks are pending or failing, **surface that and pause** rather than forcing
    the merge.
19. **Merge**: `gh pr merge --squash` (no `--delete-branch` — keep the remote
    branch, per existing repo convention).
20. **Clean up the local worktree**: `git -C <main-repo> worktree remove <path>`,
    and move the session back to the main repo.
21. **Confirm final state** (`git worktree list`) and report the merged PR link.

## Notes

- The approval gate (`plan` mode) is real — never start editing before the user
  accepts.
- Keep the plan tight: if Steps run past ~8–10 entries, you're over-planning; collapse.
- If the issue is ambiguous, surface it under Risks/unknowns in the plan rather than
  guessing — that's what the approval gate is for.
- One worktree + one branch + one PR per issue. Phase 1 step 1a is what enforces
  that against *other* agents' work, not just your own — run it before
  `worktree add`, every time.
- Scale Phase 5 to the diff (the tier table there): trivial changes get a `low`
  review and an inline simplify glance, not a `high` review and a four-agent
  fan-out. Depth follows the change, and never auto-escalates to `ultra`/Workflow.
- User-facing features ship on every applicable frontend (macOS + web), or the plan
  says explicitly why not (Phase 2). Verify each one directly rather than trusting an
  adjacent green test suite (Phase 4) — see the Activity Matrix incident above.
- A defect test proves nothing until it has been seen red (Phase 4 step 11a). This
  binds regardless of where the test came from — the issue, `/ir:triage`, or your
  own diagnosis; a green that was never red is the failure mode, not the author.
