# VM pool — isolated agents in Irrlicht without sharing transcripts

One coding agent per container or micro-VM, a lead task handing work to
several of them, and nothing leaving the VM but a result. Irrlicht cannot see
such agents by itself, because their transcripts and processes live inside
the VMs. This example shows them anyway, through the
[worker list](../../site/docs/worker-list.html):

```
  host (macOS)                                   one VM / container per task
 ┌──────────────────────────────┐               ┌──────────────────────────────┐
 │ Irrlicht ◀── workerlist.json │               │ agent (claude, or the stub)  │
 │                ▲             │               │   hooks ──▶ report-state     │
 │ pool.sh supervise            │  /irrlicht-   │ entrypoint: heartbeat, exit  │
 │   reads 4 one-line files ◀───┼── state ──────┼── state heartbeat ended exit │
 └──────────────────────────────┘   (mount)     └──────────────────────────────┘
```

- Each **lead task** is a team and each **task** a worker in it.
- A running task is **busy**. While its agent waits for a person (a permission
  prompt, a question), it is **waiting**: the row turns orange and Irrlicht
  notifies, as for a waiting session. A stopped task is **free**, with
  `failed · exit N` when it exited non-zero. A guest that stops reporting
  turns **stale**.
- **What crosses the boundary:** four one-line files the guest writes into
  the mounted `/irrlicht-state` directory: a state word (`busy`, `waiting`,
  `done`), a heartbeat timestamp, an end timestamp and an exit code.
  `pool.sh` reads nothing else, so no transcript, prompt or output reaches the
  host or Irrlicht. With Docker, `guest/` is mounted read-only.

## Run it without credentials

The stub agent works for a while, asks a question, waits for the answer,
works again and exits. Requirements: Docker (or Apple's `container`, below),
`jq`, and Irrlicht.

1. In Irrlicht's permissions, grant **Worker list**.
2. Start some tasks and the supervisor:

   ```bash
   cd examples/vm-pool
   ./pool.sh run lead-T100 T101
   STUB_EXIT=3 ./pool.sh run lead-T100 T102 --env STUB_EXIT
   ./pool.sh run lead-T200 T201
   ./pool.sh supervise          # writes the worker list every 5 s; Ctrl-C stops
   ```

3. After about 15 s every task turns **waiting** and you get a notification
   for each one (with "waiting" notifications switched on in Irrlicht's
   settings). Answer one from another terminal:

   ```bash
   ./pool.sh answer lead-T100 T102     # busy again, then exits 3 → failed
   ```

4. Clean up: `./pool.sh down` stops every task and removes the worker list it
   wrote.

`./pool.sh status` prints the worker list without writing it.

## Run a real agent

Any image with a POSIX `sh` works; the task command is wrapped by
`guest/entrypoint`. For Claude Code, the guest reports state through hooks.
`guest/claude-settings.json` maps:

| Claude Code hook | State |
| --- | --- |
| `PermissionRequest`, `Notification` (`permission_prompt`, elicitation dialogs), `PreToolUse` on `AskUserQuestion` / `ExitPlanMode` | `waiting` |
| `PostToolUse`, `UserPromptSubmit` | `busy` |

Install it as the guest's Claude settings: copy it to `~/.claude/settings.json`
inside the image, or pass `--settings /opt/irrlicht-guest/claude-settings.json`
to `claude`. Credentials come from your environment **by name**, so the
value never appears on a command line:

```bash
export CLAUDE_CODE_OAUTH_TOKEN=…     # from `claude setup-token`
./pool.sh run lead-T300 T301 --image <an image with claude> --env CLAUDE_CODE_OAUTH_TOKEN \
  -- claude --settings /opt/irrlicht-guest/claude-settings.json -p "Fix the failing test"
```

A headless run (`claude -p`) ends the container when the task is done, so the
slot turns free with its exit code. An interactive session that ends a turn
stays busy, since it still holds its VM.

For Mistral Vibe, `guest/vibe-hooks.toml` maps:

| Vibe hook | State |
| --- | --- |
| `notification` (`approval_requested`, `question_asked`) | `waiting` |
| `pre_agent`, `post_tool` | `busy` |
| `session_end` | `done` |

Copy it to `~/.vibe/hooks.toml` inside the image. A project-level
`.vibe/hooks.toml` only loads in a trusted folder, so the user level is the
safer place in a pool image. `post_tool` also fires when the user denies an
approval, so the state returns to `busy` then. `session_end` does not run
after a crash or a kill; the heartbeat marks such a task stale. A headless run
(`vibe -p --auto-approve`) never asks, so it reports `busy` and `done` only.

## Backends

| `--backend` | Isolation | Notes |
| --- | --- | --- |
| `docker` (default) | containers; on macOS they share Docker Desktop's Linux VM | `VM_POOL_DOCKER` overrides the CLI path |
| `container` | Apple's [container](https://github.com/apple/container) CLI: one lightweight VM per task (macOS 26 or later) | **Not yet run against the real CLI** — the run/inspect/stop/delete calls are written from its documented interface. `VM_POOL_CONTAINER_CLI` overrides the CLI path. `guest/` is mounted read-write. A guest killed before it wrote its exit code shows as free with no last run (the Docker backend reads the code from the runtime instead) |

## Reference

```
pool.sh run <team> <task> [--backend docker|container] [--image IMAGE]
                          [--env NAME]... [--work-item TEXT] [-- command...]
pool.sh answer <team> <task>
pool.sh supervise [--once] [--interval S]
pool.sh status
pool.sh rm <team> <task>
pool.sh down
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `VM_POOL_DIR` | `~/.local/state/irrlicht-vm-pool` | one directory per task, mounted into its guest |
| `VM_POOL_NAME` | `VM pool` | the group header in Irrlicht |
| `VM_POOL_BACKEND` / `VM_POOL_IMAGE` | `docker` / `alpine:latest` | defaults for `run` |
| `IRRLICHT_HEARTBEAT_SECONDS` | `20` | guest heartbeat; a task is stale after three missed beats |
| `IRRLICHT_WORKERLIST_FILE`, `IRRLICHT_HOME` | the daemon's own default | where the worker list goes, resolved as the daemon does |
| `STUB_WORK_SECONDS`, `STUB_EXIT` | `15`, `0` | the stub agent's work time and exit code (pass with `--env`) |

Tests: `tools/lib/vm-pool_test.sh` (no container runtime needed; the output is
decoded by the daemon's own parser) and `tools/lib/vm-pool-mutations_test.sh`.
