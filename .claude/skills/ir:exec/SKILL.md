---
name: ir:exec
description: Investigate a GitHub issue and produce an implementation plan. Use at the start of a new task in a fresh worktree. Triggers on "/ir:exec", "plan issue", "plan this issue", "investigate issue", or when the user provides an issue number/URL and asks for a plan.
---

# Plan a GitHub Issue

Goal: read the issue, understand the affected code, then propose an implementation plan and enter plan mode.

## Inputs

Issue numer is provided from the git branch and/or worktree name. 
If there is no branch or worktree ask the user for a ticket.

## Workflow

1. **Confirm the worktree.** Run `pwd` and `git status -sb`. If not in a clean worktree, point it out — don't block.
2. **Fetch the issue** (and its comments — they often contain the real spec):
   ```bash
   gh issue view <N> --comments
   ```
   For cross-repo: `gh issue view <N> --repo <owner/repo> --comments`.
3. **Investigate the code.** Delegate to one or more `Explore` subagents with thoroughness "medium" — one prompt that names the issue's key terms (file names, symbols, error strings, feature names) and asks: where does this live, what touches it, what's the current behavior. Don't grep manually first; the subagent handles it and protects the main context.
4. **Synthesize a plan.** Distill into:
   - **Problem** — one sentence, what's broken or missing.
   - **Approach** — the chosen direction in 2–4 bullets, naming files/functions you'll touch.
   - **Steps** — ordered, concrete edits. Each step is one logical change.
   - **Risks / unknowns** — anything you'd want the user to weigh in on before coding.
5. **Enter plan mode** with `ExitPlanMode` and present the plan for approval. Do not start editing until approved.

## Notes

- Keep the plan tight. If it's longer than ~30 lines, you're over-planning — collapse steps.
- If the issue is ambiguous or under-specified, surface that in **Risks / unknowns** rather than guessing silently.
- Skip steps only when clearly redundant (e.g. user already pasted the issue body).
