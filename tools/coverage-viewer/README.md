# coverage-viewer

A small dev-only HTTP server that renders the agent × scenario coverage matrix, a per-cell pipeline drilldown, and a per-session swim-lane timeline. Reads canonical files (`replaydata/agents/*`, `.claude/skills/ir:onboard-agent/scenarios.json`) in place — no cache, no DB.

## Run

```sh
cd tools/coverage-viewer
go run .
open http://127.0.0.1:7838/
```

The server walks up from CWD looking for `go.work` (or `.git`) to find the repo root. Override with `--root /path/to/repo` and `--addr 127.0.0.1:9999` if needed.

## What it shows

- **Matrix** — every (adapter × scenario) cell, color-coded as `covered` / `staged-only` / `missing-prompt` / `n/a`. Click a cell to open the drilldown.
- **Drilldown** — the 6-step `run-cell` pipeline (precheck → daemon → driver → curate → replay → verify) with GitHub permalinks pinned to the current `git rev-parse HEAD`, plus the live `by_adapter` prompt/settings and `verify` block from `scenarios.json`.
- **Timeline** — for any scenario with a committed fixture, a swim-lane view (driver / agent / tool result / hook / daemon state / subagent) merged from `events.jsonl` + `transcript.jsonl` (normalized via the per-adapter parser in `core/adapters/inbound/agents/<adapter>/parser.go`). Click any block for the raw payload.

## Known limits

- **Driver lane** only shows what's visible in committed fixtures (the dispatched prompt). Literal driver script keystrokes/tmux send-keys aren't captured in committed fixtures, only in fresh local `.build/refresh/` runs.
- **Aider transcripts are markdown**, not JSONL. Aider timelines show daemon-side events only with a "transcript not parsed" note.
- **Replay-report mismatch markers** are not yet rendered — `replaydata/agents/_reports/` is empty today. Will land once a report producer exists.
