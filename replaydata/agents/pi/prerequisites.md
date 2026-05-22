# Prerequisites — pi

Maintainer-authored checklist for `agent-onboard record` against Pi (Inflection / now part of the broader Pi product line).

Once every item below is complete, run:

```
touch .agent-onboarding/prereqs-pi.ok
```

## Items

- [ ] `pi` CLI installed and on PATH (`pi --version` works).
- [ ] Pi account auth complete.
- [ ] tmux + lsof — required for pane / net sensors.

## Notes

- Pi has no subagent feature; `foreground-subagent` / `background-subagent`
  scenarios surface as `agent_supports: no` in the coverage matrix.
- `checkpoint-rewind` / `session-resume` support is currently unknown for
  Pi — see Phase 0 survey output for an evidence-based verdict.
