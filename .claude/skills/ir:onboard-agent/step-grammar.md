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
| `send`          | `text`                  | type `text` + Enter; bumps the expected-turn count                        | all interactive                  |
| `slash`         | `text`                  | alias of `send` for `/cmd` slash commands                                 | all except opencode              |
| `wait_turn`     | —                       | block until the agent finishes the current LLM round                      | all interactive                  |
| `sleep`         | `seconds` (default `1`) | pause N seconds (idle dwell, lazy-transcript settle)                      | all interactive                  |
| `interrupt`     | —                       | Escape (claudecode/codex/pi) or Ctrl-C (aider) mid-turn; un-bumps the turn count | all except opencode        |
| `keys`          | `keys`                  | raw tmux key names, space-separated (e.g. `"Down Down Enter"`) for picker UIs like `/model` | claudecode, codex   |
| `restart`       | —                       | kill current tmux, mint a new UUID + fresh cwd, re-init the session       | claudecode                       |
| `resume`        | —                       | kill current tmux, relaunch the SAME UUID + cwd with `--resume`           | claudecode, codex                |
| `reset_session` | —                       | in-app reset (`/clear`); keep the process alive, pick up the new UUID/transcript | claudecode, codex         |
| `fork`          | —                       | `/fork`; clone the conversation into a new rollout (eager — materializes at once) | codex                    |
| `sigkill`       | —                       | `kill -9` the agent process (forced termination)                          | claudecode                       |
| `exit_clean`    | —                       | Ctrl-D to the TUI for a graceful shutdown                                 | claudecode, codex                |
| `start_session` | `cwd` (optional)        | launch a concurrent session WITHOUT killing the current one (same cwd unless overridden) | claudecode        |
| `session`       | `session` (slot N)      | switch focus to an existing session slot N (use after `start_session`)    | claudecode                       |

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
- **opencode** has the narrowest driver — it implements only `send`,
  `wait_turn`, and `sleep`. Any other step type aborts the recording, so
  opencode recipes can't use interrupts, slash commands, or multi-session
  steps.
- Per-agent quirks (CLI flags, trust dialogs, exact key sequences for a
  picker) live in the driver; if a step you need isn't in this table, ask
  the maintainer rather than inventing a `type`.
