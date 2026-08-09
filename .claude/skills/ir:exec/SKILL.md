---
name: ir:exec
description: End-to-end execution of a GitHub issue via `/ir:exec [mode] <N>` — open a worktree, investigate, then either present a visual HTML plan for approval or proceed straight to implementation (mode-dependent), open a PR, review it with a dedicated review subagent, fix findings, simplify, hand back the PR link with a test-or-merge recommendation, and land the merge on request. `mode` defaults to `auto`, which picks `plan` or `full` from the issue's `/ir:triage` signals. Triggers on "/ir:exec", "exec issue", "implement issue", "plan issue", or when the user gives an issue number/URL and asks to plan, implement, or land it.
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
    → implement → verify → commit
    → PR → review subagent (effort scaled to diff) → fix → /simplify (or inline) → recommendation [plan / full stop here]
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
   deliberately. Never silently reimplement. **Report the PR's WIP state with it**
   — a `WIP:` title or `isDraft: true` (step 12) means an agent is mid-run and the
   branch is still moving; a cleared marker means the work reached Phase 6 and is
   waiting on a human. That distinction is the whole of what the human needs to
   pick between those three options, and it is the one thing an issue number
   alone never tells you. (Real incident: #1178 had an open,
   CI-green PR — #1207 — *and* a live worktree with uncommitted WIP. `ir:exec`
   opened a second branch anyway, ran a complete independent implementation that
   converged on the same design, and only tripped over the duplicate at Phase 5
   while grepping for something unrelated. The entire branch was discarded.)
2. **Open a dedicated worktree** off the latest `main` (skip if already in a clean,
   issue-matching worktree — run `pwd` + `git status -sb` to check):
   ```bash
   git -C <main-repo> fetch origin main
   BASE_SHA=$(git -C <main-repo> rev-parse origin/main)   # freeze the branch point
   git -C <main-repo> worktree add -b feat/<N>-<slug> .claude/worktrees/<N>-<slug> "$BASE_SHA"
   git -C <main-repo> config --local "branch.feat/<N>-<slug>.irExecBase" "$BASE_SHA"
   ```
   Keep the `git -C <main-repo>` prefix on the `config` line too, and use the literal
   branch name. A bare `cd` into the new worktree is not equivalent: nothing here runs
   under `set -e`, so a `cd` that fails leaves the next command resolving
   `--abbrev-ref HEAD` against **whatever repo the shell happens to be in** and
   stamping this run's SHA onto *another branch's* key in the shared `.git/config`.

   `.claude/worktrees/` is gitignored. **Do all work via the worktree path** — editing
   the main checkout's files by absolute path touches the wrong tree.
2a. **Confirm you are running the current playbook.** The skill text you are
   executing was loaded into this session from the **main checkout** at startup;
   the worktree you just created is off fresh `origin/main`. Those disagree
   whenever the main checkout is behind, and this file is among the most
   frequently changed in the repo — so from here on you would be editing current
   code while following superseded instructions.
   ```bash
   # Both paths absolute on purpose: written relative to cwd, a run from the
   # main checkout compares the file to itself and always reports "current" —
   # a false negative in the one check whose job is catching a silent pass.
   diff -q <worktree>/.claude/skills/ir:exec/SKILL.md \
           <main-repo>/.claude/skills/ir:exec/SKILL.md
   ```
   On a difference, **re-read the worktree's copy and follow that**, and say so in
   one line of response text. Don't reconcile the two by judgment: the worktree
   copy is authoritative because it is what every agent working from current
   `main` is running.

   Unlike the other Phase 1 checks this one does **not** pause — it is a
   correction, not a conflict, and the corrected instructions are right there.

   (Real incident, #1275: a run started from a checkout 59 commits behind
   followed a step 13 that said to invoke `/code-review` — which is
   `disable-model-invocation` and unreachable. #1217 had already replaced it
   with the subagent delegation below. The review gate degraded to an improvised
   inline pass, and the run's final report proposed, as its top improvement, a
   fix that had shipped days earlier. Note which guards did *not* catch this:
   step 13's `test -f` confirms the **reviewer** exists, never that the
   **instructions** are current — a stale playbook passes every check it
   contains, because those checks are stale too.)
2b. **The branch point is a literal SHA, and `origin/main` is shared mutable state.**
   Worktrees share the parent repo's `.git`, so *another agent's* `git fetch` advances
   `origin/main` under a run that never fetched (AGENTS.md bans `git stash` for the
   same reason). Every later step that means *"what did I branch from"* reads the
   frozen SHA, never the ref — a SHA cannot move under a concurrent fetch. Step 2
   branches from `"$BASE_SHA"` rather than from `origin/main` for the same reason: it
   closes the window between the `rev-parse` and the `worktree add`.

   **Store it in git config, not in a scratch file.** The key is namespaced by branch
   name, and step 1a guarantees one branch per issue, so concurrent agents cannot
   collide; `.git/config` is shared across worktrees, so any turn of the run can read
   it back. It is also outside the working tree, so step 11b's `git add -A` can't
   stage it.

   Do **not** park the SHA in `/tmp` or an agent scratchpad. Those directories are
   shared and recycled: while implementing this very fix, a `/tmp` scratch file
   holding the base SHA was silently overwritten by a concurrent agent within
   minutes, and the run read back *another ticket's* commit — the same shared-mutable-
   state bug this step exists to fix, one layer down. Git config and git itself are
   the only stores here that are actually per-branch.

   **Record it on every entry into the worktree, not only when you created it.**
   Phase 7 keeps the remote branch, so a second `/ir:exec` on the same issue resumes
   onto an existing branch, takes step 2's skip, and finds the key *present but
   stale* — pointing several merged commits back, which no "missing key" check
   catches. Overwrite it rather than inheriting it:
   ```bash
   BR=$(git rev-parse --abbrev-ref HEAD)   # resumed into an existing worktree/branch
   git config --local "branch.$BR.irExecBase" "$(git merge-base origin/main HEAD)"
   ```

   **Re-derive `BASE_SHA` in every new shell.** The variable does not survive between
   tool calls, and an empty one is *silently wrong* rather than loud: `git log
   ""..origin/main` is exactly `git log HEAD..origin/main` and exits 0 — the
   self-defeating probe Phase 5 exists to replace. Prepend this to any block that
   consumes it:
   ```bash
   BR=$(git rev-parse --abbrev-ref HEAD)
   BASE_SHA=$(git config --local --get "branch.$BR.irExecBase") \
     || BASE_SHA=$(git merge-base origin/main HEAD)   # honest branch point; NOT origin/main
   [ -n "$BASE_SHA" ] || { echo "no recorded base — see step 2b"; exit 1; }
   ```
   `git merge-base origin/main HEAD` is a safe reconstruction because a merge base
   resolves backwards to where the branch actually diverged; `git rev-parse
   origin/main` is not, because by then it is a different commit than the one you
   branched from. Its one blind spot is a reset that **already** happened — after a
   bad `reset --soft origin/main` the merge base *is* `origin/main`. To check that
   case, read the branch's creation entry:
   ```bash
   git reflog show "$BR" | tail -1     # "branch: Created from <sha>"; column 1 is the SHA
   ```
   The reflog is authoritative **only until Phase 5's first rebase**. After a rebase
   the branch point legitimately moved and the two disagree by design: the git-config
   value wins, and the reflog's creation entry is stale. So **re-record as part of the
   rebase**, immediately after it succeeds:
   ```bash
   git config --local "branch.$BR.irExecBase" "$(git rev-parse origin/main)"
   ```
   (A worktree created by the pre-#1419 playbook reads `Created from origin/main`
   rather than a SHA — the message echoes the literal argument — but column 1 still
   carries the commit. `ir:release` re-records after its own rebase for the same
   reason, at its step 7b-guard; note it keeps its SHA in `/tmp`, which this step
   deliberately does not.)

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
   ⚠ #977 is needs-info (blockers: pick a direction; state a target). Proceeding per `full`; assuming <X>, <Y>.
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
11b. **Commit the implementation** before leaving Phase 4. Everything downstream
    assumes you did, and no step before this one tells you to — the only
    `git commit` above this line is step 11a's scaffold.

    **Fold in the 11a checkpoint first, if you made one.** After 11a's
    revert-and-restore dance the implementation is sitting in a commit literally
    titled `wip` and the tree is *clean* — so a blind `git commit` here fails with
    `nothing to commit` while the porcelain check still passes, and the run walks
    on to step 12 with `wip` as the branch's only commit. `--fill` then takes the
    PR title from it and step 19's squash lands **`wip` on `main`**. Amend it
    (`git commit --amend`), or `git reset --soft "$BASE_SHA"` and recommit.

    ⚠ **Reset to `"$BASE_SHA"` (step 2b), never to `origin/main`.** This is the one
    idiom in this skill that can *destroy landed work*, and it survives every gate.
    `git reset --soft` moves `HEAD` but leaves the index and working tree alone, so
    resetting onto a ref that advanced mid-run re-parents **your stale tree** onto
    **someone else's newer commit**. The next `git commit` then records their merged
    files as *deleted by you*. Nothing objects: the result is a coherent **older**
    tree, so it compiles, `go test` and `preflight.sh` pass, and CI certifies the
    revert — the reverted code was self-consistent before it existed. Reset to the
    frozen SHA and none of it starts, because `HEAD`'s parent is the commit you
    actually branched from.

    Re-derive `BASE_SHA` first using step 2b's snippet — it does not survive between
    tool calls. Here an unset one at least fails loudly (`git reset --soft ""` is
    `fatal: ambiguous argument ''`, exit 128), but the tempting repair is the banned
    spelling, so derive it rather than reaching for `origin/main`.

    ```bash
    git status --short                                # see what you're about to stage
    git add -A
    git commit -m "<type>(<scope>): <what changed>"   # conventional commits; reference #<N>
    git status --porcelain                            # must print nothing
    git log --oneline origin/main..HEAD               # ≥1 commit, and no `wip` subjects
    ```

    The `log` line keeps the **live** ref deliberately — do not "fix" it to
    `"$BASE_SHA"..HEAD` for symmetry with the reset two lines above. It asks *"what
    will this PR contribute"*, and two-dot `origin/main..HEAD` excludes whatever main
    already has, which is the right answer at both ends of Phase 5. Re-anchored to the
    branch point it would list other agents' merged commits as this branch's own once
    Phase 5 rebases (measured: 2 commits vs 1, #1419). Two lines of the same step want
    opposite readings of `origin/main`; see the table in Phase 5.

    Both gates are needed and they catch opposite failures: porcelain proves
    nothing was *left behind*, the `log` proves something is actually *there* and
    isn't still a checkpoint. Read `git status --short` before the `add` rather
    than trusting `-A` blindly — it stages anything untracked the repo's
    `.gitignore` doesn't cover, and per-machine excludes don't travel.

    Reaching Phase 5 without a real commit fails in three different places
    depending on how much got committed, and **the two quiet ones look like a
    clean gate**:
    - *Quiet.* Phase 5's `origin/main...HEAD` diffs are commit-to-commit and
      cannot see the working tree, so the tier table measures an **empty diff** and
      step 13's reviewer is handed nothing to review — which reads as a clean gate
      rather than a skipped one, and Phase 6 then leans *merge* on a review that
      never saw a line of code.
    - *Quiet.* A leftover `wip` checkpoint passes every check in this step, as
      above, and ships as the squash subject.
    - *Loud.* `git rebase` aborts (`cannot rebase: You have unstaged changes`) and
      `gh pr create` refuses with `No commits between main and …`. Both stop the
      run, which is the good outcome — but `git stash` is not the way out of the
      abort: AGENTS.md forbids it in a worktree, since the stash stack is shared
      repo-wide and a concurrent agent can pop your WIP.

## Phase 5 — PR, review, simplify

**Re-check the base before you push.** Phase 1 fetched `origin/main` at most
once, to create the worktree — and not at all when step 2's "already in a clean,
issue-matching worktree" skip fired. The branch point has been frozen ever since,
while every step that reasons about "what changed" diffs against a
remote-tracking ref that other agents sharing this `.git` dir advance
underneath it. On a run of any length — investigation, an approval gate,
implementation — `main` moves, and nothing in this skill looks again until the
pre-push hook or the PR itself, by which point the work is committed onto a
stale base:

```bash
BR=$(git rev-parse --abbrev-ref HEAD)                # re-derive: the variable does not
BASE_SHA=$(git config --local --get "branch.$BR.irExecBase") \
  || BASE_SHA=$(git merge-base origin/main HEAD)     #   survive between tool calls (step 2b)
[ -n "$BASE_SHA" ] || { echo "no recorded base — see step 2b"; exit 1; }

git status --porcelain                               # MUST be empty first — commit the work before rebasing
git fetch origin main                                # also refreshes the ref steps 13–14 diff against
git log --oneline "$BASE_SHA"..origin/main           # did main move since you branched?  empty ⇒ base current
git diff --name-only -z origin/main...HEAD \
  | xargs -0 -r git log --oneline "$BASE_SHA"..origin/main --   # did it move *here*?
```

The `[ -n … ]` guard is not defensive clutter: an **empty** `$BASE_SHA` does not error,
it silently means `HEAD`. `git log ""..origin/main` is byte-identical to `git log
HEAD..origin/main` and exits 0 (measured, #1419) — so a run that forgot to re-derive
would quietly get back the exact self-defeating probe the next paragraph forbids,
with no signal at all.

The porcelain line is a precondition, not decoration: step 11b should have left
the tree clean, and if it didn't, the collision probe below reads empty no matter
what you changed — see 11b for why. Go back and commit rather than working
around it.

**Anchor both probes to `"$BASE_SHA"` (step 2b), not to `HEAD`.** The natural
spelling `git log HEAD..origin/main` is **self-defeating**: it asks "is anything on
main missing from me", and `git reset --soft origin/main` makes `origin/main` a
parent of `HEAD`, so it answers *empty* — "base current" — at the precise moment a
reset onto a moved ref has just reverted someone's merged PR. `git merge-base
--is-ancestor origin/main HEAD` is **the same trap in a different spelling** and is
no substitute: the reset genuinely does make `origin/main` an ancestor, so it too
reports "up to date" (both measured against a constructed revert, #1419). Only an
endpoint that cannot move — the recorded branch point — keeps the question
meaningful, which is why step 2 records it (step 2b).

Use the `-z`/`xargs -0 -r` form rather than an unquoted `$(git diff --name-only …)`.
Both of its failure modes are silent and both point the wrong way: unquoted
command substitution word-splits a path containing spaces (this repo tracks one)
into fragments that match nothing, so a real collision reports as clean — the exact
false negative this block exists to prevent; and on an empty file list the
substitution leaves a bare trailing `--`, which git reads as *no pathspec at all*,
so every unrelated commit reports as a collision. `xargs -0 -r` handles both: NUL
delimiting survives spaces, and `-r` skips the command entirely on empty input
(GNU honours it; BSD/macOS xargs accepts it and already skips by default).

**After any successful rebase below, re-record the base** —
`git config --local "branch.$BR.irExecBase" "$(git rev-parse origin/main)"` — the
branch point genuinely moved, and a later reset to the pre-rebase value would undo
the rebase (step 2b).

- **Nothing from the first `log`** — the base is current; continue.
- **`main` moved, but not into this branch's files** — `git rebase origin/main`,
  note it in one line, continue.
- **`main` moved into files this branch touched** — `git rebase origin/main`, then
  **surface the collision by name**: which commits, which overlapping files. Do not
  let a clean rebase end the matter. A textual auto-merge is the *dangerous*
  outcome, not the conflicted one — two branches editing adjacent prose, or
  adjacent rows of the same table, merge without complaint and produce a document
  that contradicts itself, and by then you have already reported the work done.
  Read the merged region on both sides and confirm it still says one coherent
  thing. Where the reconciliation is semantic rather than textual and you can't
  verify it yourself, **surface it and pause** — the idiom step 1a, step 9, and
  step 18 use.
- **The rebase stops with a conflict** — expected on this path, not an anomaly;
  the incident below hit exactly that. Resolve each conflicted hunk on its merits
  (both sides deliberate, so neither `--ours` nor `--theirs` wholesale is an
  answer), `git add` and `git rebase --continue`, then apply the semantic re-read
  above. If you can't resolve it confidently, `git rebase --abort` — which returns
  the branch intact to its pre-rebase state — and **surface it and pause**. Never
  walk on to step 12 from inside a stopped rebase: `HEAD` is detached mid-replay,
  so the `--shortstat` below mis-tiers steps 13–14 and the `git push -u` pushes
  the wrong ref.

  (Real incident: during #1199 / PR #1204, `origin/main` advanced twice mid-run,
  and PR #1201 landed edits to *both* files that run was in the middle of
  rewriting. It was caught only because an unrelated line-count check didn't
  reconcile. The rebase then produced a real conflict in `ir:triage`'s axis table
  — #1201 edited the Verifiability row while the branch edited the adjacent
  Specification row — plus two semantic reconciliations a textual auto-merge would
  have gotten silently wrong.)

**Deletion tripwire — run it after the rebase, before the push.** Everything above
prevents the *known* route to a silent revert. This catches the outcome itself, by
whatever route it arrived, and it is one command:

```bash
git diff --diff-filter=D --name-only origin/main...HEAD   # expected: EMPTY
```

Non-empty means this branch **removes files that `main` currently has**. For almost
every ticket that is a bug, not a feature. Treat any output as a stop: read the list,
and either name the deletion as deliberate in the PR body (retiring a fixture,
`git mv`) or find out whose merged work you are about to revert. Do not push past it
silently — that is the one step the incident below had no defence against.

**Diff against `origin/main`, not against `"$BASE_SHA"` — this is the one check that
inverts the rule.** Every *other* "what did I change" question wants the frozen branch
point; this one wants the live ref, because it must ask the question GitHub's PR diff
will ask. The reverted file was added to `main` **after** you branched, so it never
existed at your branch point and `git diff --diff-filter=D "$BASE_SHA"...HEAD` reports
**empty** — a tripwire that cannot fire on the very incident it was written for
(measured, #1419: `"$BASE_SHA"...HEAD` silent, `origin/main...HEAD` names the file).
Three-dot is correct here and is not the same as the two-dot form: after the bad reset
the merge base *is* `origin/main`, so the deletions surface; on a healthy branch the
merge base is your branch point, so your own work never reads as a deletion.

**Which `origin/main` does a step mean?** They are different needs currently spelled
the same way, and two of them sit two lines apart inside step 11b:

| Step | Reads | Why |
|---|---|---|
| 2 — `worktree add` | **current**, then frozen | Branch from the newest main; record it as `BASE_SHA` in the same breath. |
| 11b — `git reset --soft` | **original** (`"$BASE_SHA"`) | "Undo *my* commit." A moved ref re-parents a stale tree and reverts merged work. |
| 11b — `git log …..HEAD` | **current** | "What will this PR contribute?" Excludes what main already has; correct at both ends of Phase 5. |
| Phase 5 — freshness probes | **original** vs **current** | The comparison *is* the question; a probe with `HEAD` as an endpoint goes vacuous after a reset. |
| Phase 5 — `git rebase` | **current** | Legitimately wants the newest main so the PR merges cleanly. |
| Phase 5 — deletion tripwire | **current** | Must match the PR's own diff; the recorded SHA cannot see a file added to main after you branched. |
| 13/14/15 — review + simplify bases | **current**, three-dot | `origin/main...HEAD` resolves back to the merge base on its own. |
| 12 — `gh pr create --base main` | **current** | The PR's target branch, not a diff base. |

(Real incident, #1419: a run branched, `origin/main` advanced mid-run when a
concurrent agent's fetch pulled in a merged PR, and `git reset --soft origin/main`
re-parented the stale tree onto it — deleting that PR's files. Every gate passed,
because the tree was coherent, and `git log HEAD..origin/main` was empty *by
construction*. It was caught only by a human eyeballing the file list. Four safety
nets fail simultaneously here: it compiles, tests pass, CI certifies, and the natural
staleness probe answers "no" exactly when the damage was just done — so the tripwire
above is deliberately a check on the *outcome*, not on the cause.)

**Then calibrate the depth of steps 13–14 to the diff you just produced** — a
one-line string edit and a multi-package refactor must not get identical
scrutiny (spending four `/simplify` subagents on a doc-string is the failure
this guards against). `origin/main` is fresh from the check above, so measure
against it — never against local `main`, which is not updated when other PRs
merge:

```bash
git diff --shortstat origin/main...HEAD   # files + lines; origin/main, not local (stale-ref footgun)
```

…then glance at *what* changed (docs/strings/tests only? new control flow? how
many packages?) and read it into one of four tiers. These are calibration
anchors, not hard gates — use judgment at the boundaries:

| Diff tier | Looks like | Step 13 review effort | Step 14 simplify |
|---|---|---|---|
| **Trivial** | docs / comments / string-constants / config only, or ≤~30 non-test lines in 1 file | `low` | **skip the fan-out** — do an inline reuse/simplification/efficiency/altitude glance and say so |
| **Small** | 1–3 files, one concern, no new logic, <~150 lines | `low` | inline glance, no 4-agent fan-out |
| **Medium** | 2–5 files / one slice / some new logic | `medium` | run `/simplify` (fan-out is fine) |
| **Large / risky** | multi-package, schema, cross-adapter, logic-heavy, >~400 lines | `high` | run `/simplify` (fan-out) |

**The two columns are read independently.** A diff can legitimately land on
different rows for review and for simplify, and forcing one row on both is a
misread, not consistency. Take each column from whichever row describes that
axis best:

- **Review effort** follows how much *new behaviour or procedure* the diff
  introduces — how much there is to get wrong.
- **Simplify depth** follows what *kind of material* changed — a 4-agent
  code-simplification fan-out returns nothing on prose, config, or fixtures
  no matter how long the diff is.

Worked example (this skill's own #1205 PR): docs-only markdown, which is the
**Trivial** row's decisive clause for simplify — but two files of substantive
new procedure, so **Medium** for review. Split that way, the `medium` review
found a real defect that a `low` pass would have missed, while a `/simplify`
fan-out would have had nothing to chew on. **Say which row you took each
column from** whenever they differ, so the split is a visible decision rather
than a silent inconsistency.

**These tiers are the post-hoc correction.** Triage's run plan set a *predicted*
review effort before any code existed; this table measures what the change
actually turned out to be. When the two disagree, **this table wins** — a
predicted level is a budget, a real diff is evidence. A large gap in either
direction is worth one line in the Phase 6 hand-back: it is the only feedback
the levelling scale ever gets.

**Guardrails (unconditional):** the built-in `/code-review` is human-only at
*every* tier (it is `disable-model-invocation`), so no run auto-selects it —
least of all `ultra`, the cloud, billed path. Step 13 is bounded to **one**
review subagent at **low↔high** effort; `xhigh`/`max` and anything reaching for
the built-in stay human-triggered, and the Workflow tool is never used for
either step.

12. **Open the PR** against `main`, **marked WIP** — the work is not finished
    here: review (13), fixes, and simplification (14) all still push onto this
    branch, and everything the PR says about itself is provisional until Phase 6.
    ```bash
    git push -u origin feat/<N>-<slug>
    gh pr create --base main --draft --title "WIP: <type>(<scope>): <what changed>" \
      --body "..."                    # reference "Closes #<N>"
    ```
    End the PR body with the `🤖 Generated with [Claude Code]` line.

    **Both markers, not one.** The `--draft` flag is what GitHub's UI and
    `gh pr view --json isDraft` see; the `WIP:` title prefix is what a human
    scanning `gh pr list` and step 1a's `gh pr list --search "<N>"` see — that
    search prints `title`, not draft state, so a draft without the prefix reads
    to the next agent as a finished PR waiting to be reviewed. Marking is the
    whole point: step 1a's incident (#1178) was a collision with a branch whose
    state nobody could tell from outside.

    `--fill` is not usable with a WIP title (it takes the title from the commit),
    so write the title and body. Drafts are not exempt from CI — nothing in
    `.github/workflows/` filters on draft state, so the checks step 18 reads
    still run.
13. **Review the diff** at the **calibrated effort** — by delegating to a
    single review subagent, not by reviewing it yourself (the mind that just
    wrote the code is the weakest available reviewer of it).

    **Confirm the reviewer exists first.** This step's entire substance lives
    in another file, so a moved or renamed skill leaves the subagent pointed at
    a dead path, reviewing from nothing — the same silent degradation #1205 was
    about, wearing a different mask:
    ```bash
    test -f .claude/skills/ir:code-review/SKILL.md && echo OK || echo MISSING
    ```
    On `MISSING`, **surface it and pause** — don't improvise a review, don't
    fall through to step 14 — the idiom step 9 uses for a failed self-assign
    and step 18 for an unmergeable PR.

    Then spawn one `Agent` (`subagent_type: general-purpose`,
    `run_in_background: false` so Phase 5 can't race past its own gate) whose
    prompt names:
    - the worktree path, the issue number, and one line of intent — a fresh
      reviewer knows nothing about the plan, so a deliberate decision left
      unstated comes back as a finding;
    - the **effort** from the tier table above;
    - the **explicit base** `origin/main...HEAD`;
    - the instruction to follow `.claude/skills/ir:code-review/SKILL.md` —
      point at it *by path*, which works whether or not skill invocation is
      available inside a subagent;
    - an explicit **"return the findings themselves as your final text"**. A
      subagent's tool calls never reach you — only its final message does — so
      a reviewer that files its findings through a tool and replies "3 findings
      reported" hands you nothing, and an empty-looking return reads as a clean
      gate at step 15.

    The reviewer reports; **you** fix. Apply every surviving finding in the
    worktree, push, and state in the transcript what came back — including an
    explicit "no findings", so a clean gate stays distinguishable from a
    skipped one. **One subagent, never a fan-out**, and never the Workflow tool.

    Pass the base explicitly because in this worktree *both* of the convenient
    defaults mislead: the local `main` ref is stale (never updated when other
    PRs merge, so `main...HEAD` drags in already-merged hunks) and after step
    12's `git push -u`, `@{upstream}...HEAD` is ~empty (upstream now points at
    the pushed branch, so it reviews nothing).

    ⚠ **Neither built-in review skill is a substitute.** `/code-review` is
    `disable-model-invocation`: the `Skill` tool refuses it and it cannot be
    reached from `Bash` either — don't attempt it. `/review` is *not* a
    fallback either; it parses its first argument as a PR number, so
    `/review low origin/main...HEAD` runs `gh pr view low` and reviews a PR
    that doesn't exist. A *human* can still run `/code-review` — see step 15.
14. **Simplify per the tier.** For **Medium/Large** diffs run the `/simplify`
    skill with the same explicit base (`/simplify origin/main...HEAD`, for the
    reason in step 13); for **Trivial/Small** diffs skip its 4-agent fan-out
    and do the reuse/simplification/efficiency/altitude review inline, stating
    what you checked. Push any cleanup.
14a. **Clear the WIP marker** — active work on this branch is over, and the PR
    should stop saying otherwise before you hand it to a human. Do this only
    once step 13's findings are applied and step 14's cleanup is pushed; a run
    that pauses earlier (a `MISSING` reviewer, a failing suite, an unanswered
    question) leaves the PR WIP on purpose, because that is exactly what it is.
    ```bash
    gh pr ready <PR>                                  # undraft
    gh pr edit <PR> --title "<type>(<scope>): <what changed>"   # drop the "WIP: " prefix
    gh pr view <PR> --json isDraft,title              # isDraft:false, title has no WIP:
    ```
    **Step 19's squash takes its commit subject from the PR title**, so a
    leftover prefix lands `WIP: …` on `main` — the same failure step 11b
    describes for a leftover `wip` commit, arriving by a different route. The
    read-back is there because both commands are easy to skip when the run is
    already narrating "done".

## Phase 6 — Hand back

15. **Present the final PR link** and ask whether the user wants to **test** or **merge**.
    Make a recommendation, and let your **confidence** decide which you lead with:
    - **Lean merge** when: the review subagent came back clean (no unresolved
      findings), all relevant suites are green, and the diff is small/low-risk and
      fully covered by tests. Suggested: proceed to Phase 7 (land), or `/ir:exec close`.
    - **Lean test-first** when: review raised non-trivial findings, tests are
      failing/flaky/absent for the behavior, the diff is large or risky, or the change
      is user-visible and only confirmable by running it. Point at `/verify`, or
      `/ir:test-mac` for macOS-app changes. For a **large or risky** diff, also say
      plainly that step 13's subagent is the weaker reviewer and suggest the user run
      `/code-review <tier> origin/main...HEAD` themselves before merging — that path
      is human-only, so offering it is the only way it ever happens.
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
18. **Confirm the PR is mergeable**: `gh pr view <N> --json mergeable,state,isDraft,title`.
    If checks are pending or failing, **surface that and pause** rather than forcing
    the merge.

    A still-draft or still-`WIP:`-titled PR means step 14a never ran — this
    phase is self-sufficient and may be entered standalone, so it cannot assume
    it did. Don't merge past it: either the work is genuinely unfinished
    (surface and pause), or it finished and the marker was left behind, in which
    case run step 14a's two commands now. `--squash` would otherwise write the
    title verbatim as `main`'s commit subject, and GitHub refuses to merge a
    draft at all.
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
- A PR is **WIP for as long as this run is still pushing to it** — opened draft
  with a `WIP:` title (step 12), cleared only after review and simplify are done
  (step 14a). Concurrent agents share this repo; the marker is how they, and the
  human reading `gh pr list`, tell a branch that is still moving from one that is
  waiting on them.
- One worktree + one branch + one PR per issue. Phase 1 step 1a is what enforces
  that against *other* agents' work, not just your own — run it before
  `worktree add`, every time.
- `origin/main` is **shared mutable state** — concurrent agents' fetches advance it
  mid-run. Phase 1 step 2 freezes the branch point as `BASE_SHA` (step 2b); step 11b resets to
  that SHA and never to the ref, because `reset --soft` onto a moved ref silently
  reverts merged work past every gate. Phase 5's deletion tripwire
  (`git diff --diff-filter=D --name-only origin/main...HEAD`, expected empty) is the
  backstop that catches the outcome by any route — see the "Which `origin/main`" table
  there for which steps want the frozen SHA and which want the live ref.
- Phase 5 re-fetches `origin/main` and rebases onto it before the push. Phase 1's
  branch point is not a base check you can rely on — step 2 skips its fetch when
  you resume inside an existing worktree — and a run long enough to be worth
  automating is long enough for `main` to move under it either way.
- Scale Phase 5 to the diff (the tier table there): trivial changes get a `low`
  review and an inline simplify glance, not a `high` review and a four-agent
  fan-out. Depth follows the change. Step 13 always delegates to exactly one
  review subagent running `.claude/skills/ir:code-review/SKILL.md` — it never
  reaches for the built-in `/code-review` (human-only) and never uses Workflow.
- User-facing features ship on every applicable frontend (macOS + web), or the plan
  says explicitly why not (Phase 2). Verify each one directly rather than trusting an
  adjacent green test suite (Phase 4) — see the Activity Matrix incident above.
- A defect test proves nothing until it has been seen red (Phase 4 step 11a). This
  binds regardless of where the test came from — the issue, `/ir:triage`, or your
  own diagnosis; a green that was never red is the failure mode, not the author.
