# Recipe step grammar

The `script` array in a `by_adapter.<agent>` recipe is a list of step
objects, each `{"type": "...", ...}`. The interactive drivers
(`scripts/drive-<agent>-interactive.sh`) all dispatch on `type`. This is
the shared vocabulary — author recipes against it instead of reverse-
engineering the ~600-line driver. Stay inside it: an unknown `type`
**aborts the recording** — every driver logs it and exits non-zero
(`nonzero(2)`), so a typo'd step won't silently no-op.

| type            | extra fields            | semantics                                                                 | drivers                          |
|---              |---                      |---                                                                        |---                                |
| `send`          | `text`, `model` (opt)   | type `text` + Enter; bumps the expected-turn count. opencode-only `model` (`provider/model`) runs THIS turn on the named model (`opencode run -m`) — a per-turn switch | all interactive                  |
| `slash`         | `text`                  | alias of `send` for `/cmd` slash commands                                 | all except opencode              |
| `wait_turn`     | —                       | block until the agent finishes the current LLM round                      | all interactive                  |
| `sleep`         | `seconds` (default `1`) | pause N seconds (idle dwell, lazy-transcript settle)                      | all interactive                  |
| `interrupt`     | —                       | Escape (claudecode/codex/pi/opencode) or Ctrl-C (aider) mid-turn; un-bumps the turn count | all interactive            |
| `keys`          | `keys`                  | raw tmux key names, space-separated (e.g. `"Down Down Enter"`) for picker UIs like `/model` | claudecode, codex, opencode |
| `restart`       | —                       | kill current tmux, mint a new UUID + fresh cwd, re-init the session       | claudecode                       |
| `resume`        | —                       | kill current tmux, relaunch the SAME UUID + cwd with `--resume`           | claudecode, codex                |
| `reset_session` | —                       | in-app reset (`/clear`); keep the process alive, pick up the new UUID/transcript | claudecode, codex         |
| `fork`          | —                       | `/fork`; clone the conversation into a new rollout (eager — materializes at once) | codex                    |
| `sigkill`       | —                       | `kill -9` the agent process (forced termination)                          | claudecode                       |
| `exit_clean`    | —                       | Ctrl-D to the TUI for a graceful shutdown                                 | claudecode, codex                |
| `start_session` | `cwd` (optional)        | launch a concurrent session WITHOUT killing the current one (same cwd unless overridden) | claudecode, opencode |
| `session`       | `session` (slot N)      | switch focus to an existing session slot N (use after `start_session`)    | claudecode, opencode             |
| `mid_turn_send` | `text`                  | type `text` + Enter into the composer WHILE a turn is in flight; the TUI queues it and runs it as the NEXT turn — does NOT bump the turn count (a later `wait_turn` detects the queued turn) | opencode |

Notes:

- **Turn counting.** `send`/`slash` bump an expected-turn counter;
  `wait_turn` blocks until the agent has completed that many turns;
  `interrupt` decrements it (an interrupted turn never reaches `end_turn`).
  Always pair each `send` with a later `wait_turn` (or an `interrupt`).
- **Multi-variant / multi-session recordings.** `restart`, `sigkill`,
  `exit_clean`, `resume`, `reset_session`, `start_session` are how one
  recording chains several session lifetimes. The driver tracks every
  session UUID across these so the curator can pull them all.
- **Idle-only scenarios** (no prompts) use a single `sleep`-only script.
- **opencode** is a hybrid driver. Most cells run the deterministic
  headless path (`send`/`wait_turn`/`sleep`, plus `start_session`/`session`
  — a second `opencode run` chain in the same cwd is a second independent
  ses_-keyed arc, so multi-session-same-cwd needs no TUI); `slash`,
  `reset_session`, `interrupt`, `keys`, `restart`, `sigkill`, and
  `mid_turn_send` switch it to the live-TUI path
  (`run_live`, tmux) — `interrupt` fires a bare Escape mid-turn (opencode writes
  a step-finish reason=interrupted, which the parser maps to turn_done);
  `mid_turn_send` types a 2nd message into the composer DURING an in-flight turn
  (opencode silently queues it and runs it as the next turn — no turn-count bump
  at submit, a later `wait_turn` detects the queued turn). It does
  NOT implement `resume`/`exit_clean`.
  The `send` step accepts an OPTIONAL opencode-only `model`
  (`provider/model`, e.g. `lmstudio/google/gemma-4-26b-a4b`) that threads
  `opencode run -m` so that turn runs on the named model — the per-turn
  model-select primitive for `model-switch-midsession` (commit bbf82830).
  Omit it and the turn runs on the config default.
- Per-agent quirks (CLI flags, trust dialogs, exact key sequences for a
  picker) live in the driver; if a step you need isn't in this table, ask
  the maintainer rather than inventing a `type`.
