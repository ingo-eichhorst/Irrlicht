# eta-research

A read-only replay harness that scores task-completion ETA estimators against
recorded sessions (issue #753). It replays each marker-bearing transcript
turn-by-turn, runs every candidate estimator at the turn's **transcript**
timestamp (never wall-clock — results are deterministic), and compares the
projected remaining time against the ground-truth completion.

## What it measures

For each estimator: accuracy (MAE / median / relative error / bias),
**time-to-first-estimate** (the issue's headline — when a real number first
appears), and stability (turn-to-turn jitter). See `REPORT.md` for the latest
run.

## Methodology notes

- **Episode** = one task within a session, segmented by the transcript
  **tailer**'s (`core/pkg/tailer`, the daemon component that watches `.jsonl`
  transcripts) own task anchor (the rate base re-anchors on a new task / user
  message).
- **Ground truth = the last marker**, not the working→waiting/ready transition.
  The issue named the transition as the candidate; replaying the real corpus
  showed it is idle-contaminated (it fires when the user next returns — a median
  3.5 min and up to ~20 h after the agent actually stopped). The last marker is
  the agent's final progress report and lands when the work stops.
- **Prior** = the median *per-episode* average round duration. Per episode, not
  per consecutive marker delta: markers are emitted in bursts, so a per-delta
  median collapses to the emission cadence (~4 s) rather than a true round
  (~70 s).
- Accuracy is scored only on episodes that reached `completed==total` (where the
  last marker is genuinely the completion).

## Running

```sh
# Committed report (real numbers) — needs a local corpus:
go run ./cmd/eta-research \
    -fixtures ../../replaydata/agents/claudecode \
    -local "$HOME/.claude/projects" \
    -out ./REPORT.md

# $IRRLICHT_ETA_CORPUS is the default for -local.
```

The committed fixtures alone are single-round-per-turn scenarios and don't form
multi-round episodes, so the trustworthy accuracy numbers require a local corpus
of real transcripts (never committed). A local test (`go test ./...` from `tools/eta-research/`) exercises
the scorer and estimators on synthetic episodes — no corpus required.
(Not currently wired into CI.)
