# Prerequisites — opencode

Maintainer-authored checklist for `agent-onboard record` against OpenCode.

Once every item below is complete, run:

```
touch .agent-onboarding/prereqs-opencode.ok
```

## Items

- [ ] `opencode` (or whatever the binary is named locally) installed and on PATH.
- [ ] Model provider configured per OpenCode's docs.
- [ ] tmux + lsof — required for pane / net sensors.

## Notes

- OpenCode persists sessions in SQLite (`~/.local/share/opencode/storage.db`
  or platform-equivalent). Irrlicht's adapter reads the DB directly — no
  PID-binding step is needed, so `Long-idle live session` is observed via
  DB-watcher signal rather than process liveness.
- `Session reset` / `Checkpoint rewind` / `Cloud / background agent`
  support is currently `unknown` — Phase 0 survey output should clarify.
