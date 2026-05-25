# Live cross-host round-trip

The first **real cross-host round-trip against a real agent**: a Linux
`irrlichd` co-located with `claude` (Claude Code) in one container, forwarding
out to a standalone `irrlichtrelay` in a second container, surfaced **live on
the macOS app running on the host Mac**.

You bring the two containers up, add the relay as a Source in the Mac app, then
`docker compose exec` into the agent and drive `claude` by hand — and watch the
Linux session appear and transition `working → ready` on your Mac, with live
cost/tokens.

```
  ┌──────────────── docker compose network ────────────────┐
  │  service: agent (hostname: linux-dev)   service: relay  │
  │  ┌───────────────────────────┐         ┌─────────────┐  │
  │  │ claude  (driven via exec)  │  ws push │ irrlichtrelay│ │
  │  │ irrlichd (/proc+pidfd)  ───┼─────────▶│  :7839      │  │
  │  └───────────────────────────┘ ws://relay└──────┬──────┘  │
  └────────────────────────────────────────────────│ 7839:7839┘
                                                    ▼
                          host Mac:  Irrlicht.app → Sources → ws://localhost:7839
                                     (or open http://localhost:7839)
```

The daemon dials **out** to the relay, so the only published port is the
relay's `7839`. The container daemon's `127.0.0.1:7837` is never exposed.

## Prerequisites

- Docker with BuildKit (Docker Desktop on macOS is fine). Target arch is
  `linux/arm64` (Apple Silicon); amd64 works too.
- The macOS app from a build that includes the relay **Sources** feature
  (the daemon Forwarder + relay client, #479).
- Both images build from source in this repo — no published Linux release
  needed.

## The demo

```bash
# from the repo root
docker compose -f examples/roundtrip/docker-compose.yml up -d --build
```

**On the Mac:** Irrlicht.app → Settings → Sources → add `ws://localhost:7839`
and enable it. (Or just open <http://localhost:7839> for the relay-served
dashboard.)

```bash
# Drive claude interactively. First run triggers login (see below).
docker compose -f examples/roundtrip/docker-compose.yml exec -it agent claude
#   …inside claude, ask it to do real work — edit README.md, run a command —
#   a genuine turn. Approve its first tool use when prompted.
```

**Expected on the Mac, live:**

- a new session appears under daemon **`linux-dev`**, project **`work`**;
- it transitions **`working → waiting → working → ready`** as the turn runs:
  it dips to **`waiting`** while a tool-use **permission prompt** is held open
  ("Do you want to proceed?"), returns to `working` on approval, and settles
  `ready` when the turn ends — with live **cost/tokens** (a one-edit turn ran
  ~$0.14 / 25.8k tokens, model `claude-opus-4-7`);
- hovering the connection-status indicator shows **`linux-dev — connected`**.

> **Permission prompts and `curl`.** The `waiting` dip on a permission prompt
> is driven by Claude Code's `PermissionRequest` hook, which the daemon
> auto-installs as a `curl … || true` POST to itself. The agent image
> therefore **must** ship `curl` — without it the hook silently no-ops and
> prompts stay `working` (this was #488). On any host where the daemon
> observes `claude` but `curl` is absent, the daemon logs a startup warning
> and falls back to a transcript heuristic that still flags a held file-edit
> prompt (`Edit`/`Write`/`MultiEdit`/`NotebookEdit`) as `waiting`.

```bash
# Relay restart: the daemon reconnects (backoff) and the Mac re-hydrates,
# no app restart needed.
docker compose -f examples/roundtrip/docker-compose.yml restart relay

# Stop. The credential/transcript volume PERSISTS for next time.
docker compose -f examples/roundtrip/docker-compose.yml down
```

> `tools/roundtrip-up.sh` is a thin wrapper for `up -d --build`.

## One-time login

The first `exec -it … claude` walks you through Claude Code's login. Use the
**subscription** path: claude prints a URL — open it on the Mac, complete the
OAuth, and paste the returned code back into the terminal. (`claude
setup-token` works too; an `ANTHROPIC_API_KEY` in the `agent` environment is a
fallback.) Credentials land in the `claude-home` volume and survive
`down`/`up`.

## Notes & gotchas

- **`down -v` wipes the login volume** — you'll have to log in again. Plain
  `down` keeps it.
- **Baked-in dashboard.** The relay serves a *snapshot* of `platforms/web/`;
  web edits need an image rebuild (`--build`). The daemon, by contrast, serves
  the dashboard live from disk.
- **Port `7838` vs `7839`.** A local dev `irrlichd` uses `7838`; the relay uses
  `7839`, so they don't collide on the Mac.
- **`depends_on` is start-ordering, not readiness** — the daemon's reconnect
  backoff handles a relay that isn't up yet, so no brittle healthcheck.
- **Daemon liveness.** `irrlichd` logs to the container's stdout:
  `docker compose -f examples/roundtrip/docker-compose.yml logs -f agent`.
  If it exits, the entrypoint logs a warning rather than idling silently.
- **Process matching on Linux.** Claude Code 2.x ships a **native binary**
  (`claude.exe`); a live process exposes `comm="claude"` / `argv0="claude"`, so
  the daemon's `ExactName{"claude"}` matcher works directly (verified — the
  session is keyed `proc-<pid>` before its transcript UUID is even known). PID
  discovery also has a name-independent path via the transcript +
  `~/.claude/sessions/<pid>.json`. (The old worry that `/proc/<pid>/comm` reads
  `node` applied to the previous Node-*script* distribution.)
