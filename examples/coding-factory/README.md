# Coding factory — 3× codex over an authenticated relay

A multi-agent testbed: **three** containers, each running the OpenAI **codex**
CLI driven via **tmux**, with a co-located [`irrlichd`](../../docs/relay-protocol.md)
that forwards **out** to **one auth-enabled** [`irrlichtrelay`](../relay/). All
three surface live on the macOS app and the web dashboard as **three distinct
rows** — exercising the relay's bearer-token auth, multi-daemon fan-in,
compound `(daemon_id, session_id)` keying, and the remote-origin glyph in one
demo. Builds on the single-agent [`../roundtrip/`](../roundtrip/) (1× claude).

```
  codex-1  ─ irrlichd ─┐
  codex-2  ─ irrlichd ─┼─ ws push (Bearer token) ─▶  irrlichtrelay :7839  ◀─ macOS app / browser
  codex-3  ─ irrlichd ─┘        (internal network)     (auth on, loopback)      (token in Settings)
```

Each agent dials *out*, so this works through NAT; only the relay needs a
reachable port. Each container mints its **own** `daemon_id`, so the three never
collapse into one row.

## Run

You need an `OPENAI_API_KEY` (set up out-of-band — use a small/cheap model to
keep it inexpensive). The one-command bootstrap brings up the relay, issues a
bearer token, and starts the three agents sharing it:

```bash
cd examples/coding-factory
OPENAI_API_KEY=sk-... ./up.sh        # or put OPENAI_API_KEY in .env first
```

Then on the **Mac** (Settings → Sources) or **web**, add the relay as a source
with the token `up.sh` printed:

```
Relay URL:  ws://localhost:7839
Token:      <printed by up.sh>
```

…or just open the relay-served dashboard at **http://localhost:7839** (it will
prompt for the token).

### Manual bootstrap (what `up.sh` automates)

```bash
cp .env.example .env && $EDITOR .env          # set OPENAI_API_KEY (+ optional CODEX_MODEL)
docker compose -f docker-compose.yml up -d --build relay
docker compose -f docker-compose.yml exec relay \
  sh -c 'IRRLICHT_HOME=/var/lib/irrlichtrelay irrlichtrelay token issue --label coding-factory'
# copy the printed token into IRRLICHT_RELAY_TOKEN in .env, then:
docker compose -f docker-compose.yml up -d --build codex-1 codex-2 codex-3
```

## What you should see

- **Three rows** labelled `codex-1`, `codex-2`, `codex-3`, each with the **cloud
  origin glyph** (they arrived over the relay, not a local socket), cycling
  **working → ready** as the tmux auto-driver feeds each codex a small task on a
  loop. The connection-status tooltip lists the three connected daemons.
- A `waiting` dip if codex holds for a tool-use approval (the `curl`
  PermissionRequest hook, #488 — `curl` ships in the image).
- **Kill one agent** (`docker compose … stop codex-2`) → its rows **fade** in
  place rather than vanishing (#540); they restore when you start it again.

### Drive one by hand

The agents auto-drive, but you can attach and take over any codex TUI:

```bash
docker compose -f docker-compose.yml exec -it codex-1 tmux attach -t codex
```

## Notes

- **Auth is on.** The relay runs `--auth tokens-file`; the agents present the
  token via `IRRLICHT_RELAY_TOKEN`. `token revoke <id>` takes effect within ~2s
  without a restart. To expose the dashboard beyond loopback, front it with TLS
  — see [`../relay/DEPLOY.md`](../relay/DEPLOY.md).
- **Fresh identity per agent.** Agent state is ephemeral (no volume) and the
  entrypoint deletes `relay-identity.json`, so every container mints a new
  `daemon_id` → three distinct daemons. Never bake an already-connected
  daemon's identity into an image (the cloned-VM gotcha — `DEPLOY.md`).
- **Cheap model.** Set `CODEX_MODEL` in `.env` to a small model your account
  has; the agents write it to `~/.codex/config.toml` at start.
- **`down` vs `down -v`.** `down` keeps the relay's token volume; `down -v`
  wipes it (you'll re-issue a token next `up`).
- **Baked dashboard.** The relay image bakes a snapshot of `platforms/web/`;
  dashboard edits need an image rebuild (`--build`).

## Stop

```bash
docker compose -f examples/coding-factory/docker-compose.yml down
```

To stop without removing containers (preserves state for a quick restart):

```bash
docker compose -f examples/coding-factory/docker-compose.yml stop
```

→ Simpler single-agent demo: [`../roundtrip/`](../roundtrip/) ·
Protocol reference: [`../../docs/relay-protocol.md`](../../docs/relay-protocol.md) ·
Deploy/ops: [`../relay/DEPLOY.md`](../relay/DEPLOY.md)
