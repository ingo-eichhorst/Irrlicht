---
name: ir:exec
description: Execute the approved triage plan for one Irrlicht GitHub issue and produce a reviewed, verified, ready PR. Use for "/ir:exec <N>", "execute issue <N>", or "implement issue <N>". Require a current ready-for-agent triage comment. Do not plan, select a mode, or merge.
---

# Execute an Approved Issue Plan

Run:

```text
/ir:exec <N>
```

The command approves the latest `/ir:triage` design. Follow one path from that
design to a ready PR. Stop when a command fails. Do not invoke the Workflow
tool.

## 1. Validate the triage contract

Read the issue, all comments, and labels from `ingo-eichhorst/Irrlicht`. Select
the latest comment that starts with the triage disclaimer. Require all of these
items:

- the `ready-for-agent` label;
- `## High-level design` with content;
- `## Testing strategy` with content;
- `**Process:**` with investigation, review, and simplify values;
- `**Estimate:**` with agent-active time and human gates;
- `**Verdict:** ready-for-agent`.

If one item is absent, stop before a worktree or assignment exists. Report the
missing item and request `/ir:triage #<N>`. Do not infer a plan from the issue
body or accept a legacy triage comment.

## 2. Create the worktree

Check for work already in progress:

```bash
gh pr list --repo ingo-eichhorst/Irrlicht --state open --search "<N>" \
  --json number,headRefName,title,isDraft
git worktree list | grep -w "<N>"
git ls-remote --heads origin | grep -w "<N>"
```

On any hit, report the PR, branch, worktree, and WIP state. Stop. Do not create
a second implementation.

Create a branch from a frozen current base:

```bash
git -C <main-repo> fetch origin main
BASE_SHA=$(git -C <main-repo> rev-parse origin/main)
git -C <main-repo> worktree add -b feat/<N>-<slug> \
  .claude/worktrees/<N>-<slug> "$BASE_SHA"
git -C <main-repo> config --local \
  "branch.feat/<N>-<slug>.irExecBase" "$BASE_SHA"
```

Do all work in the new worktree. Never use `git stash`; worktrees share one
stash. Use a local commit for a checkpoint.

Compare the worktree copy of this skill with the main-checkout copy. If they
differ, re-read and follow the worktree copy. Do not pause.

When resuming an existing issue worktree, require a clean tree and record its
actual base:

```bash
BR=$(git rev-parse --abbrev-ref HEAD)
git config --local "branch.$BR.irExecBase" "$(git merge-base origin/main HEAD)"
```

## 3. Assign and execute

Assign the issue and verify the current login is present:

```bash
ME=$(gh api user -q .login)
gh issue edit <N> --repo ingo-eichhorst/Irrlicht --add-assignee @me
gh issue view <N> --repo ingo-eichhorst/Irrlicht --json assignees \
  -q "[.assignees[].login] | index(\"$ME\") != null"
```

Retry once if verification returns `false`. Stop if it still returns `false`.
Only remove the assignment on abort when this run added it.

Execute the triage process:

- Use the stated investigation depth.
- Use the high-level design as the strong default.
- Follow the testing strategy.
- Cover every frontend named in the design.
- Follow `AGENTS.md` and the surrounding code style.

Change the design only when repository evidence supports the change. Record
the evidence and the design deviation in the PR body. Do not silently replace
the design.

## 4. Prove and verify

For each defect test, run the test before the fix and require a failure. If it
passes, stop: the diagnosis or test is wrong. Identify lock tests separately;
they pass before the change by definition.

For a new guard, linter, schema rule, migration, rewriter, or contract check,
mutate what it protects and require the check to fail. Commit reusable mutation
fixtures when practical.

If the fix already exists, commit before mutating with
`git add -A && git commit -m wip`. Capture the checkpoint SHA and confirm
`git status --porcelain` is empty right after the commit. Restore *only* by
reading the checkpoint back with `git show <checkpoint-sha>:<path> > <path>`.
Never restore with `git checkout -- <file>`, `git restore --source=HEAD`,
`reset --hard`, or any command of the same shape. Never mutate a dirty tree.
Never keep the checkpoint in `/tmp` or a scratchpad instead of a commit.

Run the repository checks in the foreground. To chunk: run each command as a
separate call. Do not background a gate or end a turn while waiting for one.

```bash
tools/preflight.sh --only go
tools/preflight.sh --only web
tools/preflight.sh --only arch
tools/preflight.sh --only tools
tools/preflight.sh --only skills
tools/preflight.sh --only posix
tools/preflight.sh --only bash
tools/preflight.sh --only security
tools/preflight.sh --only swift
```

Run `--only swift` only for `platforms/macos/` changes. Keep `--only linux`
opt-in unless the change is Linux-specific. Perform the manual or UI checks
named by triage. A broad readiness signal is not proof that the target behavior
ran; poll the target condition to a deadline.

## 5. Commit and refresh the base

Fold any `wip` checkpoint into a conventional commit. Re-read the stored base;
never substitute the moving `origin/main` ref when undoing a checkpoint:

```bash
BR=$(git rev-parse --abbrev-ref HEAD)
BASE_SHA=$(git config --local --get "branch.$BR.irExecBase") \
  || BASE_SHA=$(git merge-base origin/main HEAD)
[ -n "$BASE_SHA" ] || { echo "no recorded base"; exit 1; }
git status --short
git add -A
git commit -m "<type>(<scope>): <change> (#<N>)"
git status --porcelain
git log --oneline origin/main..HEAD
```

Require a clean tree, at least one commit, and no `wip` subject.

Refresh `main` before the push:

```bash
git fetch origin main
git log --oneline "$BASE_SHA"..origin/main
git diff --name-only -z origin/main...HEAD |
  xargs -0 -r git log --oneline "$BASE_SHA"..origin/main --
git rebase origin/main &&
  git config --local "branch.$BR.irExecBase" "$(git rev-parse origin/main)"
```

If new commits touch the same files, read the merged regions and the colliding
PR. Resolve semantic conflicts. If confidence is insufficient, abort the
rebase and stop. After a conflict, run:

```bash
git diff --name-only -z origin/main...HEAD |
  xargs -0 -r bash tools/lib/rebase-conflict-check.sh
```

Then run the deletion tripwire:

```bash
git diff --diff-filter=D --name-only origin/main...HEAD
```

Stop on an unexplained deletion. Name each intentional deletion in the PR.

## 6. Open and review the PR

Open a draft PR with both WIP markers:

```bash
git push -u origin feat/<N>-<slug>
gh pr create --base main --draft \
  --title "WIP: <type>(<scope>): <change>" --body "..."
```

Reference `Closes #<N>`. Include the test evidence, design deviations, and the
`🤖 Generated with [Claude Code]` footer.

Start with the review effort from triage. Measure the real diff against
`origin/main...HEAD`. Increase or reduce the effort when the real risk differs
from the estimate. Record the change and reason in the hand-back.

Require a clean tree. Delegate one review to a general-purpose Agent. Give it
the worktree, issue intent, selected effort, explicit base
`origin/main...HEAD`, and `.claude/skills/ir:code-review/SKILL.md`. Require the
findings in its final text. Never use the built-in `/code-review`, `/review`,
or the Workflow tool here. Fix valid findings, commit, and push.

Use the simplify method from triage. For `/simplify`, pass
`origin/main...HEAD`. For an inline pass, inspect the same four angles. Confirm
each by name: reuse, simplification, efficiency, altitude. If one angle is
silent, surface it and pause. Commit and push any cleanup.

Verify the PR head, merge state, and every expected check:

```bash
gh pr view <PR> --json mergeable,mergeStateStatus,headRefOid
gh pr checks <PR>
```

An absent check is not a passing check. Rebase and repeat the relevant checks
when the PR is conflicting or an expected check did not run on the current
head.

When review, simplification, and checks are complete, clear both WIP markers:

```bash
gh pr ready <PR>
gh pr edit <PR> --title "<type>(<scope>): <change>"
gh pr view <PR> --json isDraft,title
```

## 7. Hand back

Return the ready PR link. Report:

- the tests and checks that passed;
- red-first, lock, and mutation evidence;
- review findings and fixes;
- any design deviation;
- any review-effort change;
- remaining risks or human gates from triage.

Do not merge. Do not recommend a merge or another execution mode.
