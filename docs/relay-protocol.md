# Relay wire protocol (v0)

The relay link lets an `irrlichd` daemon push its session events to a standalone
`irrlichtrelay` server, which fans them out to macOS and web clients. The daemon
pushes **out** to the relay, so it needs no inbound reachability (works behind
NAT). Hub-mode inside `irrlichd` was rejected — the relay is always its own
binary. See the [Relay-Server wiki](https://github.com/ingo-eichhorst/Irrlicht/wiki/Relay-Server).

This document covers **only what v0 builds**. Fields marked _reserved_ are named
here so the shape is stable, but they are neither sent nor honored yet.

## Topology

```
irrlichd ──(daemon role)──▶ irrlichtrelay ◀──(client role)── macOS app / web dashboard
```

- One WebSocket endpoint, `GET /api/v1/sessions/stream`, carries both roles; the
  opening `hello.role` selects the path.
- A client may connect to the local daemon **and/or** a relay at once and merges
  the sessions into one list. The daemon speaks **raw** `PushMessage` frames (no
  handshake); the relay speaks the **envelope** below.
- v0 is deliberately thin: **no auth, no TLS, no persistence, single node, one
  relay per daemon.** Relay state is in-memory and rebuilt from each daemon's
  reconnect `daemon_snapshot`.

## Versioning

`hello.protocol_version` is the wire version (currently `1`); the relay echoes
the negotiated value in `hello_ack.accepted_version`. Bump only on a breaking
change to the frames below.

## Frames

Every frame is a JSON text message with a `type` tag.

### `hello` (peer → relay, both roles)

```jsonc
{ "type": "hello", "protocol_version": 1, "role": "daemon" | "client",
  "daemon_id": "uuid", "daemon_label": "laptop" }   // daemon_* set by daemons only
```

A daemon mints `daemon_id` once and persists it at
`~/.local/share/irrlicht/relay-identity.json` (relocated by `IRRLICHT_HOME`), so
the id is stable across restarts; clients dedupe by it. `daemon_label` defaults
to the hostname. The relay **refuses** a daemon `hello` with an empty
`daemon_id`. A client `hello` omits the daemon fields.

### `hello_ack` (relay → peer)

```jsonc
{ "type": "hello_ack", "accepted_version": 1 }
```

### `daemon_snapshot` (daemon → relay, after the ack)

```jsonc
{ "type": "daemon_snapshot",
  "sessions": [ <SessionState>, ... ],   // the daemon's current sessions
  "agents":   [ <AgentInfo>, ... ] }     // its adapter registry (/api/v1/agents shape)
```

Sent once per (re)connect to reconcile the relay's cache. The relay replaces
that daemon's cached sessions with the snapshot and reconciles connected
clients: a `session_updated` push for each session present, a `session_deleted`
for each that vanished since the prior snapshot.

### `snapshot` (relay → client, after the ack)

```jsonc
{ "type": "snapshot",
  "daemons": [ { "daemon_id": "uuid", "daemon_label": "laptop", "status": "connected" }, ... ] }
```

The identity + status list that seeds the client's connection-status tooltip.

### `daemon_status` (relay → clients, on daemon connect/disconnect)

```jsonc
{ "type": "daemon_status", "daemon_id": "uuid", "daemon_label": "laptop",
  "status": "connected" | "disconnected", "since": 1778800000 }
```

Keeps the tooltip live without a full re-`snapshot`.

### `push` (daemon → relay → client; the live channel)

```jsonc
{ "type": "push", "source": "daemon-uuid", "ts": 1778800000,
  "msg": { ...existing outbound.PushMessage, UNCHANGED... } }
```

The daemon wraps each `PushMessage` it would send on its own WebSocket. The relay
stamps `source` with the originating `daemon_id` (authoritative — it does not
trust the daemon's stamp) and re-emits to every client. **Clients read `.msg`
and process it exactly as a raw daemon frame.** `focus_requested` is filtered at
the forwarder (host-local; meaningless cross-host, wiki §5.4).

## Client behavior

A single client handler covers both source kinds: it always sends a client
`hello` (a daemon ignores unexpected frames; a relay requires it) and dispatches
by frame type — `push` unwraps `.msg`, `snapshot`/`daemon_status` feed the
tooltip, and anything else is a raw daemon frame. On WS connect the relay also
**replays its cached session state** to that client as `session_updated` pushes,
so a client that connects after a daemon hydrates over the socket alone (no
cross-origin HTTP needed).

Sessions are keyed by `session_id`. The same daemon reached over both the local
socket and a relay collapses to one row automatically. **Caveat:** two
*different* daemons that share a `session_id` (e.g. `proc-<pid>`) would merge —
per-source keying and row-level source badges are deferred.

## HTTP

The relay re-serves the daemon's read API from its cache so clients render
without an empty first paint, and serves the `platforms/web/` dashboard:

| Endpoint              | Body                                                            |
| --------------------- | -------------------------------------------------------------- |
| `GET /api/v1/sessions` | `{ "groups": [...] }` — `BuildDashboard` over the cached sessions (grouped by project; no orchestrator state in v0). |
| `GET /api/v1/agents`   | union of every connected daemon's registry, deduped by `name`. |
| `GET /api/v1/version`  | the relay's own build version.                                 |
| `GET /` (+ assets)     | the dashboard, served from disk (`IRRLICHT_UI_DIR` override, else dev/bundle lookup). |

WebSocket `CheckOrigin` is permissive in v0 (localhost-only, no auth; the
dashboard served from a different port is cross-origin). Tighten alongside auth.

## Running it

```sh
irrlichd                                    # daemon on :7837 (its default)
IRRLICHT_RELAY_URL=ws://localhost:7839 irrlichd   # …also forwards to the relay
irrlichtrelay serve --addr :7839            # the relay
```

> **Port choice:** irrlicht uses three contiguous ports — `7837` (production
> daemon), `7838` (dev-daemon coexist via `IRRLICHT_DAEMON_PORT`/`BIND_ADDR`),
> and `7839` (relay default). All three sit in the `7836–7850` band, which has
> no IANA-notable service and is effectively never seen open in the wild (per
> nmap-services frequency data), so collisions are unlikely. Override any of
> them — `--addr` for the relay — if your environment already uses one.

Then in **Settings → Sources** (macOS or web) enable **Local** and/or enter the
**Relay server URL**; both clients show the union of sessions, live, and the
connection-status tooltip lists the connected daemon(s).

## Reserved (named, not built)

- `auth` / bearer tokens, `token issue|list|revoke` CLI.
- `seq` (per-source sequence numbers) and `resume` (reconnect cursor) for
  gap-free reconnection.
- Multiple relays per daemon; multi-node relays; persistence; TLS.
- Row-level source badges and "fade, don't delete" on daemon-offline (v0 deletes
  a disconnected daemon's rows).
- History re-hydration of late-joining relay clients (bars fill from live ticks;
  session state *is* replayed, history snapshots are not).
