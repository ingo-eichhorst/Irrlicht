# eta-research

A read-only replay harness that scores task-completion ETA estimators against
recorded sessions (issue #753). It replays each marker-bearing transcript
turn-by-turn, runs every candidate estimator at the turn's **transcript**
timestamp (never wall-clock — results are deterministic), and compares the
projected remaining time against the ground-truth completion.

## What it measures

For each estimator: accuracy (MAE / median / relative error / bias),
**time-to-first-estimate** (#753's headline — when a real number first
appears), and stability (turn-to-turn jitter). See `REPORT.md` for the latest
run.

The `production` row is not a candidate model but the **shipped seam itself** —
`session.ForecastTaskCompletion` called directly, with production's own
constants. Adding it (#977) also let the other estimators drop their verbatim
copy of the forecaster's rate measurement, which previously had to be kept in
sync by hand. If a change to the daemon's model doesn't move that row, the
change isn't reaching users.

The **last-mile** section (#977) is measured against a *different* ground truth
from everything above it: the accuracy table ends at the final marker, so it is
structurally blind to work that happens after the agent stops reporting rounds.
That section follows the transcript past the marker instead, and sweeps the
"how long a pause still counts as working" cutoff rather than assuming one —
the answer moves a lot with it.

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
  median collapses to the emission cadence (~4 s) rather than a true round.
  The corpus prior **drifts** — #753 measured ~70 s, #977 re-measured ~240 s on
  a rotated corpus — so the report prints the live gap between it and the
  shipped `session.TaskRoundPriorSeconds` on every run. That gap is not
  cosmetic: since #977 the prior is also the shrinkage target, so a stale one
  drags every estimate, not just the zero-round bootstrap.
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
