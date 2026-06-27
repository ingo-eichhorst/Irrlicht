---
name: ir:exec
description: End-to-end execution of a GitHub issue — open a worktree, investigate, present a visual HTML plan for approval, then (on accept) implement, open a PR, review it with /review, fix findings, simplify, and hand back the PR link with a test-or-merge recommendation. Triggers on "/ir:exec", "exec issue", "implement issue", "plan issue", or when the user gives an issue number/URL and asks to plan or implement it.
---

# Execute a GitHub Issue, End to End

Take an issue from a number to a review-clean PR. The flow has a hard gate in the
middle: **plan → user approves → implement**. Nothing is edited before approval.

```
worktree → investigate → HTML plan (/tmp) → ⛔ APPROVAL → implement → PR → /review → fix → /simplify → PR link + recommendation
```

## Inputs

The issue number comes from the branch or worktree name, or from what the user
typed. If none is resolvable, ask for an issue number before continuing.

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

## Phase 3 — Visual HTML plan + approval gate

6. **Render the plan to HTML.** Read `templates/plan.html` (next to this file). Copy it
   to `/tmp/ir-exec-plan-<N>.html` and fill it in. The page reads outside-in — a
   stranger to the codebase should understand the top half:

   **Section roster (order in the template):**
   - **TL;DR** — `{{TLDR}}`, 2–3 sentences: the problem + the intent. The most-read line.
   - **High-level design** — `{{HLD_INTRO}}` + `REPEAT:hld` bullets. **Code-free**: no
     file or function names here (those belong to the technical Approach below).
   - **Visual** — pick exactly one archetype (see next bullet), or delete all three.
   - **Approach & Design (technical)** — the file/function-level direction (`REPEAT:approach`).
   - **Steps** — `REPEAT:step`; card = title + one-line summary + file chips; the deep
     rationale/edge-cases go in the step's `<template>` (click-to-reveal).
   - **Files Touched** — `REPEAT:file`, `badge` class `new`|`mod`|`del`; per-file "what
     changes and why" in its `<template>`.
   - **Risks & Unknowns** — `REPEAT:risk` (native `<details>`).

   **Fill primitives:** replace every `{{TOKEN}}`; duplicate each
   `REPEAT:x`…`/REPEAT:x` region per item and delete the leftover example; delete any
   unused `OPTIONAL:x` region whole.

   **Pick ONE visual archetype** matching the dominant kind of work (delete the others;
   delete all three if no visual adds signal — don't ship an empty box):
   | Issue kind | Keep block | What to author |
   |---|---|---|
   | Frontend / UI | `OPTIONAL:ui` | a real screenshot ("Today") + a hand-authored SVG wireframe ("Proposed"); mark each new region `<g data-detail="ui-N">` |
   | Data processing | `OPTIONAL:dataflow` | node-and-arrow flow; each node `data-detail` reveals its transform |
   | Vertical slice / many components | `OPTIONAL:components` | impact-map nodes, `data-impact` = `changed`\|`adjacent`\|`untouched` — show the blast radius AND what's left alone |

   **UI screenshot policy** (the `ui` archetype embeds the *real* current UI):
   - **Web UI (a URL exists):** use the `claude-in-chrome` tools to open the running UI,
     screenshot the relevant viewport (not a giant full-page capture — keep the data-URI
     reasonable), and embed it as the "Today" `<img src="data:image/png;base64,…">`.
   - **Non-URL UI (macOS app, CLI):** there's no reliable capture at plan time — **ask
     the user for a screenshot**, or hand-model the current screen as SVG from code/asset
     inspection. **Never invent a UI you haven't seen.**

   **Interactivity:** to make anything click-to-reveal, give it `data-detail="<id>"` and
   put the detail in a sibling `<template data-detail-body="<id>">`. **Do not write event
   handlers** — the template's inline engine handles it.

   **Self-containment & hazards:**
   - Self-contained = **no EXTERNAL resources** (no URLs, CDN scripts, or web fonts).
     The inline `<style>` block and the inline `<script>` engine are part of the
     template — **keep both byte-for-byte**, add no others. `data:` URIs (the screenshot)
     are fine.
   - Never write a comment-close sequence inside a comment's text, and never write a
     closing `</template>` or `</script>` inside a detail body.
   - Before presenting, verify **no `{{` and no `REPEAT:`/`OPTIONAL:` markers are left
     behind** (the page also shows a warning banner at load time if any slip through).
   - One visual archetype max; Steps ≤ 8–10.
7. **Present the link and stop.** Give the user the `file:///tmp/ir-exec-plan-<N>.html`
   link plus a 2–3 line summary, then **wait**. Do not edit a single file until the
   user replies with approval. If they request changes, revise the plan + HTML and
   re-present.

## Phase 4 — Implement (only after approval)

8. **Push through the implementation** in the worktree.
   - If the work is complex/multi-part, break it into tasks with `TaskCreate` and work
     them in order (as you naturally would). For a small change, just implement it.
   - Follow the repo's conventions (AGENTS.md): surgical changes, match surrounding
     style, three-state model, hexagonal layering, etc.
9. **Verify** before declaring done: run the test suites relevant to what you touched
   (per AGENTS.md — `go test ./core/... -race -count=1`, the factory/web suites, replay
   fixtures, `swift build`/`swift test`, as applicable). Fix what you broke.

## Phase 5 — PR, review, simplify

10. **Open the PR** against `main`:
    ```bash
    git push -u origin feat/<N>-<slug>
    gh pr create --base main --fill   # or a written title/body; reference "Closes #<N>"
    ```
    End the PR body with the `🤖 Generated with [Claude Code]` line.
11. **Review with the `/review` skill on the PR.** Then fix every issue it surfaces, in
    the worktree, and push the fixes.
    - **IMPORTANT: do NOT use the Workflow tool / multi-agent orchestration for the
      review — it is too expensive.** Invoke the `/review` skill directly (use
      `/code-review` on the local diff if you prefer not to round-trip the PR). A single
      review pass, not a fan-out.
12. **Run the `/simplify` skill** on the change to clean up reuse/complexity, then push.

## Phase 6 — Hand back

13. **Present the final PR link** and ask whether the user wants to **test** or **merge**.
    Make a recommendation, and let your **confidence** decide which you lead with:
    - **Lean merge** when: `/review` came back clean (no unresolved findings), all
      relevant suites are green, and the diff is small/low-risk and fully covered by
      tests. Suggested: `gh pr merge --squash` (keep the branch — don't pass
      `--delete-branch`).
    - **Lean test-first** when: review raised non-trivial findings, tests are
      failing/flaky/absent for the behavior, the diff is large or risky, or the change
      is user-visible and only confirmable by running it. Point at `/verify`, or
      `/ir:test-mac` for macOS-app changes.
    State the recommendation in one line with the reason; the merge decision is the
    user's.

## Notes

- The approval gate (Phase 3) is real — never start editing before the user accepts.
- Keep the plan tight: if Steps run past ~8–10 entries, you're over-planning; collapse.
- If the issue is ambiguous, surface it under Risks/unknowns in the plan rather than
  guessing — that's what the approval gate is for.
- One worktree + one branch + one PR per issue.
