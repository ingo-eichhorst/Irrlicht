# Prerequisites — codex

Maintainer-authored checklist of items that must be set up out-of-band
before `agent-onboard record` can drive Codex through scenarios.

Once every item below is complete, run:

```
touch .agent-onboarding/prereqs-codex.ok
```

…to acknowledge. See `tools/agent-onboarding/internal/preflight/prereqs.go`
for the gating logic.

## Items

- [ ] `codex` CLI installed and on PATH (`codex --version` works).
- [ ] OpenAI account auth complete (codex login flow done).
- [ ] tmux installed — required for pane / pipepane sensors.
- [ ] lsof installed — required for net sensor.

## Notes

- Codex's MCP boot has a typing-rhythm constraint (≥0.3s between text and
  Enter) that the existing `drive-codex-interactive.sh` already encodes.
- `cloud-background-agent` (Codex Cloud) requires a separate cloud account
  and is currently out of scope for local recording (see coverage matrix
  cell for codex/cloud-background-agent).
