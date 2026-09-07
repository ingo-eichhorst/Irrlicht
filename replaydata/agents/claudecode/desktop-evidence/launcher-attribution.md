# The rig contaminates the app it is about to drive

Measured 2026-09-07, while recording cell 2-8 through the slash step.

## What happened

Three consecutive runs of 2-8 drove their recipe correctly — the composer took
`/goal`, the popup offered it, Return accepted it, the arguments landed, Send
submitted, and the agent ran a turn — and then all three spent their entire
12-minute budget on:

```
Irrlicht session state working or waiting was not observed before its deadline
(Irrlicht has not attributed the session's launcher yet (host bundle ID is empty))
```

The driver refuses a session whose `host_bundle_id` is not
`com.anthropic.claudefordesktop`, because a session it cannot attribute to
Claude Desktop might belong to somebody else's Claude Code.

## What it was

Queried from the run's own daemon while the run was still in flight:

```json
{
  "session_id": "e5fe2a63-1cc4-4aef-b609-ac17491defc2",
  "state": "ready",
  "pid": 90370,
  "launcher": { "term_program": "vscode" },
  "host_bundle_id": null
}
```

The session was attributed to **VS Code**. Claude Desktop's own process says why:

```sh
ps -p "$(pgrep -x Claude)" -Eo command= | tr ' ' '\n' \
  | grep -E '^(TERM_PROGRAM|CLAUDECODE|CLAUDE_CODE_|VSCODE_)'
```

```
TERM_PROGRAM=vscode
CLAUDECODE=1
CLAUDE_CODE_MESSAGING_TOKEN=…
CLAUDE_CODE_SSE_PORT=48021
VSCODE_GIT_ASKPASS_NODE=/Applications/Visual…
```

Those are not a stale operator environment. `CLAUDECODE=1` and
`CLAUDE_CODE_SSE_PORT` belong to **the Claude Code session that was doing the
recording**. Claude Desktop had been started at 20:22:43 that evening, in the
middle of the run, and it inherited the driving session's whole environment.
Claude Code then inherits it again from Desktop, and Irrlicht's launcher
attribution reads `TERM_PROGRAM` and reports a terminal instead of the host app.

The rig activates Claude Desktop before each run
(`osascript -e 'tell application "Claude" to activate'`). When the app is
already running that is harmless. When it is NOT running, that call launches it —
from the driving shell, with the driving shell's environment.

The control is worth stating, because it is what rules out a reading error:
Finder and Google Chrome, running on the same machine at the same moment, carry
no `TERM_PROGRAM` at all.

## Why it is not a driver defect, and not a daemon defect either

The daemon reported what the process said about itself. The driver refused a
session it could not attribute to Claude Desktop, which is the correct refusal —
loosening it would let a run adopt a session it does not own.

Cell 2-12 recorded successfully at 19:58 the same evening against Claude Desktop
**pid 23588**. These runs failed against **pid 27220**, the process the rig
itself started an hour later.

## The check to run before spending a recording budget

```sh
ps -p "$(pgrep -x Claude)" -Eo command= | tr ' ' '\n' \
  | grep -qE '^(TERM_PROGRAM|CLAUDECODE)=' \
  && echo "REFUSE TO RECORD: Claude Desktop carries the driving session's environment"
```

To clear it, quit the app and let launchd start it, so it takes the login
environment rather than a shell's:

```sh
osascript -e 'tell application "Claude" to quit'
sleep 3
open -a Claude          # LaunchServices, not the calling shell
```

A run must never be the thing that starts Claude Desktop. Starting it is what
decides the environment every session it spawns will carry, and a run that
starts it is measuring its own contamination.
