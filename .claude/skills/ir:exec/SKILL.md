---
name: ir:exec
description: End-to-end execution of a GitHub issue — open a worktree, investigate, present a visual HTML plan for approval (or skip the gate and proceed inline), implement, open a PR, review it with /code-review, fix findings, simplify, hand back the PR link with a test-or-merge recommendation, and land the merge on request. Triggers on "/ir:exec", "exec issue", "implement issue", "plan issue", "wt+plan", "wt+exec", "wt+full", "wt+close", or when the user gives an issue number/URL and asks to plan, implement, or land it.
---

# Execute a GitHub Issue, End to End

Take an issue from a number to a review-clean, landed PR. One flow, four modes —
see below for which stop early and which skip the approval gate.

```
worktree → investigate → plan (HTML + ⛔ approval, or inline if gate-skipped) → assign issue
  → implement → verify                                        [wt+exec stops here]
  → PR → /code-review (low) → fix → /simplify → recommendation [wt+plan / wt+full stop here]
  → land: confirm mergeable → squash-merge → remove worktree   [wt+close / "merge" reply]
```

## Modes

The four `wt+*` shortcuts (and plain `/ir:exec`) are all this one skill — they
differ on exactly two axes: whether the approval gate is skipped, and how far
past implementation the flow continues.

| Invocation | Gate? | Stops after |
|---|---|---|
| `/ir:exec #N`, "wt+plan #N" (default) | Yes — render HTML plan, end turn, wait for explicit approval | Phase 6 (hand-back), once approved |
| "wt+exec #N" | No — skip the wait | Phase 4 (implement + verify) |
| "wt+full #N" | No — skip the wait | Phase 6 (hand-back) |
| "wt+close" (or replying "merge" to Phase 6) | n/a | Phase 7 (land) |

Gate-skipped modes (`wt+exec`, `wt+full`) still follow every other rule in this
skill — worktree isolation, one branch/PR per issue, AGENTS.md conventions —
only the approval wait and the HTML plan artifact are dropped.

## Inputs

The issue number comes from the branch or worktree name, or from what the user
typed. If none is resolvable, ask for an issue number before continuing.
Phase 7 (land) additionally resolves standalone from `pwd` / `git status -sb` /
`gh pr view` — see that phase.

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
   each), Files touched (new/mod/del), Risks/unknowns.

## Phase 3 — Present the plan (branches by gate)

### Gated path — default `/ir:exec`, "wt+plan"

6. **Render the plan to HTML.** Read `templates/plan.html` (next to this file). Copy it
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
7. **Present the link, then end your turn.** Give the user the
   `file:///tmp/ir-exec-plan-<N>.html` link plus a 2–3 line summary, and **stop the
   response there** — do not present the link and keep working in the same turn. The
   next user message is the gate: treat only an explicit approval as go. An ambiguous or
   partial reply ("looks good, but…") is a change request — revise the plan + HTML and
   re-present. Do not edit a single implementation file until the user approves.

### Gate-skipped path — "wt+exec", "wt+full"

Nobody is gating on the plan, so skip the HTML artifact and the wait entirely:

6. **Post a short inline plan summary** in the response text — Problem, Approach,
   Steps, Files touched, each 1 line — no separate render step, no `/tmp` file.
7. **Proceed straight into Phase 4 in the same turn.** No stop, no waiting for a reply.

## Phase 4 — Implement

8. **Mark the issue in progress.** Now that work is actually starting, assign the issue
   to the current GitHub user so others can see it's being worked:
   ```bash
   gh issue edit <N> --add-assignee @me   # add --repo <owner/repo> for cross-repo
   ```
9. **Push through the implementation** in the worktree.
   - If the work is complex/multi-part, break it into tasks with `TaskCreate` and work
     them in order (as you naturally would). For a small change, just implement it.
   - Follow the repo's conventions (AGENTS.md): surgical changes, match surrounding
     style, three-state model, hexagonal layering, etc.
10. **Verify** before declaring done: run the test suites relevant to what you touched
    (per AGENTS.md — `go test ./core/... -race -count=1`, the factory/web suites, replay
    fixtures, `swift build`/`swift test`, as applicable). Fix what you broke.

**"wt+exec" stops here.** Report what was implemented and verified, and note that
opening a PR (or re-invoking as "wt+full") continues the flow. Do not auto-open a PR
for this shortcut.

## Phase 5 — PR, review, simplify

11. **Open the PR** against `main`:
    ```bash
    git push -u origin feat/<N>-<slug>
    gh pr create --base main --fill   # or a written title/body; reference "Closes #<N>"
    ```
    End the PR body with the `🤖 Generated with [Claude Code]` line.
12. **Review the diff.** Run the `/code-review` skill at **low** effort on the local
    diff, then fix every finding it surfaces in the worktree and push the fixes.
    - **IMPORTANT: do NOT use the Workflow tool / multi-agent orchestration for the
      review — it is too expensive.** A single review pass, not a fan-out.
13. **Run the `/simplify` skill** on the change to clean up reuse/complexity, then push.

## Phase 6 — Hand back

14. **Present the final PR link** and ask whether the user wants to **test** or **merge**.
    Make a recommendation, and let your **confidence** decide which you lead with:
    - **Lean merge** when: `/code-review` came back clean (no unresolved findings), all
      relevant suites are green, and the diff is small/low-risk and fully covered by
      tests. Suggested: proceed to Phase 7 (land), or "wt+close".
    - **Lean test-first** when: review raised non-trivial findings, tests are
      failing/flaky/absent for the behavior, the diff is large or risky, or the change
      is user-visible and only confirmable by running it. Point at `/verify`, or
      `/ir:test-mac` for macOS-app changes.
    State the recommendation in one line with the reason; the merge decision is the
    user's. A reply of "merge" (or "wt+close") moves into Phase 7.

## Phase 7 — Land ("wt+close")

Self-sufficient: this phase resolves the issue/PR itself rather than assuming
continuity from earlier phases, so it works standalone or as a continuation of
Phase 6.

15. **Resolve the worktree, branch, and PR** if not already known from context —
    `pwd` / `git status -sb` for the worktree and branch, `gh pr view` for the PR —
    the same way Phase 1 resolves the issue number.
16. **Confirm the worktree is clean and pushed**: `git status -sb` shows nothing
    outstanding and the branch is up to date with its remote.
17. **Confirm the PR is mergeable**: `gh pr view <N> --json mergeable,state`. If
    checks are pending or failing, **surface that and pause** rather than forcing
    the merge.
18. **Merge**: `gh pr merge --squash` (no `--delete-branch` — keep the remote
    branch, per existing repo convention).
19. **Clean up the local worktree**: `git -C <main-repo> worktree remove <path>`,
    and move the session back to the main repo.
20. **Confirm final state** (`git worktree list`) and report the merged PR link.

## Notes

- The approval gate (Phase 3, gated path) is real — never start editing before the
  user accepts.
- Keep the plan tight: if Steps run past ~8–10 entries, you're over-planning; collapse.
- If the issue is ambiguous, surface it under Risks/unknowns in the plan rather than
  guessing — that's what the approval gate is for.
- One worktree + one branch + one PR per issue.
