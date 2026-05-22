# Prerequisites — claudecode

Maintainer-authored checklist of items that must be set up out-of-band
before `agent-onboard record` can drive Claude Code through scenarios.

Once every item below is complete, run:

```
touch .agent-onboarding/prereqs-claudecode.ok
```

…to acknowledge. The recorder refuses to start until that file exists and
is newer than this manifest. If you update this manifest later, the recorder
will fail again until the .ok file is re-touched.

## Items

- [ ] `claude` CLI installed and on PATH (`claude --version` works).
- [ ] Anthropic subscription login complete (`claude` runs without auth errors).
- [ ] Optional: API key set if you want to exercise scenarios that bypass the subscription path.
- [ ] tmux installed and on PATH (`tmux -V` works) — required for pane / pipepane sensors.
- [ ] lsof installed — required for net sensor. Pre-installed on macOS; `apt install lsof` on Debian/Ubuntu.

## Notes

- Hooks-based scenarios (`permission-hook-denial`, `subagent-spawn`) need
  the Claude Code hook integration installed; the skill's
  `install-instructions.md` covers that.
- The Phase 0 survey (`/ir:onboard-agent survey claudecode`) proposes
  additional per-scenario prereqs in its output. Review and copy any that
  apply into this manifest.
