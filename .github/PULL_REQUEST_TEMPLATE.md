<!--
Thanks for the PR! A few notes before you submit:

  - Run the full test suite locally (see AGENTS.md#testing). A change is only done when every layer passes.
  - Keep PRs focused — one concern per PR merges fastest.
  - For UI changes, include a screenshot or short screen recording.
  - Reference related issues with "Fixes #123" or "Refs #123".
-->

## Summary

<!-- What does this PR do, and *why*? One short paragraph is enough. -->

## Changes

<!-- Bullet list of the main things touched. Keep it terse. -->

-
-

## Test plan

<!-- How you verified the change. Be concrete. -->

- [ ] Full test suite passes locally (see [AGENTS.md](../AGENTS.md#testing))
- [ ] Added or updated tests covering the change
- [ ] Manually exercised the relevant flow (describe below)

<!-- If UI: attach screenshots or a short screen recording. -->

## Related issues

<!-- Fixes #123, Refs #456 -->

## Checklist

- [ ] Follows the conventions in [AGENTS.md](../AGENTS.md) and [CONTRIBUTING.md](../CONTRIBUTING.md)
- [ ] Commit messages use [Conventional Commits](https://www.conventionalcommits.org/)
- [ ] No new abstractions added ahead of need
- [ ] Documentation updated if behavior changed (README, `site/docs/`, or `events.md`)
- [ ] No `cancelled` session state introduced — three states only: `working`, `waiting`, `ready`
