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
  → worktree → investigate → (auto resolves to plan or full here)
    → plan: HTML + ⛔ approval, or full: inline summary → assign issue
    → implement → verify
    → PR → /code-review (low) → fix → /simplify → recommendation [plan / full stop here]
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
would from there on:

```bash
gh issue view <N> --json labels -q '.labels[].name'                            # ready-for-agent / needs-info?
gh issue view <N> --comments | grep -o '\*\*Complexity:\*\*[^—]*' | tail -1     # value up to the em dash, if triaged
```

| Signal | Resolves to |
|---|---|
| `needs-info` label present | **Refuse.** Don't open a worktree — report the blockers (from the triage comment if present) and point at `/ir:triage <N>` or manual clarification. |
| No readiness label and no `/ir:triage` comment at all | `plan` — safest default when nothing has assessed the issue. |
| `ready-for-agent` label + Complexity Low or Medium | `full` |
| `ready-for-agent` label + Complexity High, or any value that doesn't cleanly parse as Low/Medium (e.g. a hybrid like "Medium-to-High") | `plan` — a visual plan is still worth a human's eyes before multi-package work starts, even though the label says ready. An ambiguous read of the signal should never resolve toward skipping the gate. |

If the issue was never triaged and has no readiness label, make the same
Low/Medium/High call yourself during Phase 2's investigation, reusing
`/ir:triage`'s calibration (one file/one concern = Low; 2–4 files one slice =
Medium; multi-package/schema/cross-adapter/multi-phase = High) rather than a
new one.

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
2. **Open a dedicated worktree** off the latest `main` (skip if already in a clean,
   issue-matching worktree — run `pwd` + `git status -sb` to check):
   ```bash
   git -C <main-repo> fetch origin main
   git -C <main-repo> worktree add -b feat/<N>-<slug> .claude/worktrees/<N>-<slug> origin/main
   ```
   `.claude/worktrees/` is gitignored. **Do all work via the worktree path** — editing
   the main checkout's files by absolute path touches the wrong tree.

## Phase 2 — Investigate & plan

3. **Fetch the issue and its comments** (comments often hold the real spec):
   ```bash
   gh issue view <N> --comments        # add --repo <owner/repo> for cross-repo
   ```
4. **Investigate the code.** Delegate to one or more `Explore` subagents (thoroughness
   "medium") with a prompt naming the issue's key terms — file names, symbols, error
   strings, feature names — asking where it lives, what touches it, current behavior.
   Don't grep manually first; the subagent protects the main context.
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
6. **If invoked as `auto`**, resolve the signal per the Auto mode table above and
   continue as whichever of `plan`/`full` it names — everything from here on follows
   that mode's path.

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
   gh issue edit <N> --add-assignee @me   # add --repo <owner/repo> for cross-repo
   gh issue view <N> --json assignees -q '.assignees | length'
   ```
   If the count comes back `0`, retry the `edit` once and re-check; if it's still
   `0`, **surface that and pause** rather than silently proceeding unassigned — the
   same idiom Phase 7 uses for an unmergeable PR.
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

## Phase 5 — PR, review, simplify

**Calibrate the depth of steps 13–14 to the diff you just produced** — a
one-line string edit and a multi-package refactor must not get identical
scrutiny (spending four `/simplify` subagents on a doc-string is the failure
this guards against). The diff exists as soon as Phase 4 is done; measure it
cheaply first:

```bash
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
    `/code-review` skill on the local diff, then fix every finding it surfaces
    in the worktree and push the fixes. A single review pass, not a fan-out.
14. **Simplify per the tier.** For **Medium/Large** diffs run the `/simplify`
    skill; for **Trivial/Small** diffs skip its 4-agent fan-out and do the
    reuse/simplification/efficiency/altitude review inline, stating what you
    checked. Push any cleanup.

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
- One worktree + one branch + one PR per issue.
- Scale Phase 5 to the diff (the tier table there): trivial changes get a `low`
  review and an inline simplify glance, not a `high` review and a four-agent
  fan-out. Depth follows the change, and never auto-escalates to `ultra`/Workflow.
- User-facing features ship on every applicable frontend (macOS + web), or the plan
  says explicitly why not (Phase 2). Verify each one directly rather than trusting an
  adjacent green test suite (Phase 4) — see the Activity Matrix incident above.
