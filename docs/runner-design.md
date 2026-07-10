# Runner & Remote Sessions — Design

Status: draft, accepted direction (2026-07-10)
Related: `docs/relay-protocol.md`, issue #724 (backchannel), epic #925 (phases #926–#930).

## Motivation

Irrlicht's identity is *observation*: irrlichd discovers agent sessions it does
not own and watches them. Control was bolted onto that observer: the
`BackchannelEngine` lives inside irrlichd and fires rules by injecting
keystrokes into terminals irrlicht merely discovered (tmux `send-keys`, kitty
remote control, AppleScript via the macOS app). Injection is inherently
best-effort — no ownership, no delivery guarantee, and iTerm/Terminal need the
Mac app as a prosthetic arm.

Two product goals force a rethink:

1. **Reliable control** of agents, decoupled from the observer.
2. **Mobility**: hand a running session over to a server at the press of a
   button, start brand-new sessions (or empty sandboxes) from a phone, and
   drive everything from Mac, iPhone, or other devices — with the session
   still *looking local* everywhere.

These are one requirement in disguise: you cannot hand over a session you do
not own. The component that owns the agent's PTY for reliable control is
exactly the unit that can run on a server instead of the Mac.

### Relationship to #724

This deliberately reverses the #724 pivot, where an owned-PTY `irrlicht run`
was rejected in favor of controlling discovered agents. The handover
requirement changes the calculus. Discovered-agent control (injection) remains
as the degraded tier for sessions the user did not launch through the runner.

## Architecture

Three roles, one job each:

| Role | Binary | Job |
|---|---|---|
| Observer | `irrlichd` (existing) | Discover, watch, emit the push stream. Pure again: rules engine and control dispatch move out over time. Runs on the Mac *and* on the server box. |
| Runner | `irrlicht-run` (new) | Own the agent: spawn under a PTY, mirror to the local terminal (or run headless), expose control (input/interrupt), host the backchannel rules engine for its sessions, implement checkpoint/handover. Agent-agnostic at the process level — it wraps *a command*; agent-specific smarts (lifecycle, resume, rules presets) layer on top when the command is a known agent. |
| Broker | `irrlichtrelay` (existing, extended) | Fan-out plus *placement*: which runner hosts which session. Gains spawn/attach/handover frames. Stays a dumb broker — no rules brain (standalone-relay principle holds). |

Clients (macOS app, web dashboard, iPhone app) speak only to the relay/daemon
streams. A handed-over or server-born session is just a session whose
placement says "server."

**Wrapping is explicit opt-in** (`irrlicht run claude`). Unwrapped sessions
keep today's injection-based control as a degraded tier. Server deployment v1
is a **self-hosted box** the user provisions once (Linux box/VM; credentials,
toolchains, clones set up out-of-band or interactively via a sandbox — see
below). The **rules engine lives in the runner**, so rules travel with the
session across handover with no extra plumbing.

## Interaction tiers

Escalating cost; higher tiers are strictly optional:

1. **Observe (always on, cheap).** The server box runs irrlichd too, tailing
   transcripts and forwarding the same push frames as today. Server sessions
   render identically to local ones in every client.
2. **Control (no PTY stream).** `input`/`interrupt` frames go
   client → relay → runner, fire-and-forget. Answering a waiting session works
   off observed state (the transcript-derived question), exactly like the Mac
   control popover today. Covers the bulk of remote steering.
3. **Attach (on-demand, heavy).** Full bidirectional PTY mirror, active only
   while at least one client is "in" the session: sandbox bootstrap,
   interactive logins, TUI dialogs, watching the raw terminal. Streaming over
   the relay starts on `attach` and stops when the last client detaches.

The runner's **local vt-state machine** (headless terminal emulator over its
own PTY) is always on — it is in-process and near-free. It provides:

- instant attach: screen **snapshot first, then deltas** (a late-joining
  client never sees garbage until the next redraw);
- continuous UI-detection (trust prompts, permission dialogs — the signals
  that never reach the transcript) folded into the observation stream,
  replacing today's `capture-pane` polling `TerminalObserver` for wrapped
  sessions.

Only human-eyeballs-on-terminal costs relay bandwidth. This also bounds the
flow-control problem: PTY bytes flow for a handful of attached sessions, not
the fleet. Note the relay's current drop-when-full policy is wrong for PTY
bytes (dropped bytes corrupt the screen); attachments need per-stream flow
control or snapshot-resync on overflow.

## Remote spawn & sandboxes

The relay gains a `spawn` frame:
`{target_runner, kind: agent|sandbox, agent?, workspace?, initial_prompt?, options}`.
A supervisor mode of the runner on the server listens for spawns the way the
relay `Forwarder` listens for control frames today.

- **Catalog spawn:** the server runner advertises available workspaces and
  installed agents in its hello/snapshot
  (`workspaces: [{repo, path, branch}], agents: [...]`); clients render a
  picker. Provisioning a repo onto the box once makes it appear.
- **Empty sandbox:** the degenerate case — fresh directory (later optionally a
  container), plain shell (`zsh`), no agent. Via the attached terminal the
  user runs `gh auth login`, `git clone`, agent login (OAuth device-code
  flows: CLI prints a URL, opened in the phone's browser), and launches the
  agent from inside. A box can therefore bootstrap itself entirely from a
  phone; the catalog is a convenience, not a requirement.

A phone-born session has no "home" Mac: its identity and lifecycle are owned
by runner + relay, never derived from a local irrlichd observation. This is
the forcing function that keeps the runner self-sufficient — which handover
needs anyway.

Sandbox isolation v1 is a bare directory on the box; container-per-sandbox is
a later hardening step, deliberately deferred until the UX is proven.

## Handover (the button)

Checkpoint + resume, **not** live process migration (CRIU does not cross
OS/arch; Mac→Linux is the common case).

1. Drain: wait for a turn boundary (`waiting`/`ready`), or interrupt to force
   one. No handover mid-tool-call.
2. Sync three things: **workspace** (wip commit pushed to the server-side
   clone — git is the transport), **session state** (agent transcript +
   session files, shipped through the relay), **environment** (provisioned
   once out-of-band; no per-handover secret syncing).
3. Server runner checks out the branch, resumes (`claude --resume <id>` for
   Claude Code; per-agent resume adapters follow the capability-matrix
   pattern, `requires: [resumable]`).
4. Relay flips placement; every client's view continues seamlessly.
5. Pull-back is symmetric.

Known limits, surfaced honestly in v1: live local state does not survive
resume — background shells, MCP servers with local state. Detect and warn
rather than pretend.

## Terminal clients

- macOS / iOS: SwiftTerm rendering; `attach`/`pty` frames over the existing
  relay WS. Keystrokes return through the input path, so consent gating
  applies unchanged.
- Web dashboard: xterm.js (the dashboard gains its first control
  affordances).
- Mac terminal: `irrlicht attach <session>` — a thin PTY client speaking the
  same frames.
- Resize v1: PTY sizes to the most recent attacher (tmux aggressive-resize
  semantics); renegotiate on rotate.

## Consent & gating

Nothing changes philosophically: every control path continues to flow through
the `InputService` gate chain (master toggle → per-adapter `control` consent →
controllability), and relay-borne control keeps its double gate (relay-control
toggle + gate chain). The runner adds a fourth call-site shape for
`contracttesting.AssertPermissionGated`.

## Phases

1. **Extraction** — carve `BackchannelEngine` + control dispatch out of
   irrlichd's service layer into a hostable `control` package; behavior
   unchanged; existing HTTP endpoints keep serving unwrapped sessions.
2. **Local runner** — `irrlicht run <agent>`: owned PTY, local terminal
   mirroring, vt-state machine + snapshot/attach machinery (built here, on one
   machine, where it is debuggable), embedded rules engine, registration with
   irrlichd so a wrapped session appears exactly once (no double discovery).
3. **Remote spawn + terminal streaming** — headless runner + supervisor mode,
   relay `spawn`/`attach`/`pty`/placement frames, sandbox spawn, workspace
   catalog, server-box provisioning doc. End state: phone/web starts an empty
   sandbox or a session and drives it through a live terminal.
4. **Handover** — checkpoint/resume machinery, local⇄server both directions;
   prove checkpoint→kill→resume on one machine first.
5. **Devices** — iPhone app (session list, terminal view, spawn screen), web
   control affordances, `irrlicht attach`.

Phase 3 lands the full "on the go" story without handover; phase 4 turns
handover into an enhancement ("take this session with me") rather than a
prerequisite for mobility.

## Risks

- **PTY flow control over the relay** — the riskiest protocol piece; needs a
  sketch before building (per-attachment credit or snapshot-resync).
- **Runner↔irrlichd identity handshake** — a wrapped session must not show up
  twice.
- **Resume fidelity** — background shells / stateful MCP servers; detect and
  warn.
- **Credential/toolchain parity on the server** — mitigated by
  provision-once + interactive sandbox bootstrap.
