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
- v0 is deliberately thin: **no persistence, single node, one relay per
  daemon.** Auth and TLS are supported but off by default (see
  [Auth, TLS, and origins](#auth-tls-and-origins)). Relay state is in-memory
  and rebuilt from each daemon's reconnect `daemon_snapshot`.

## Versioning

`hello.protocol_version` is the wire version (currently `1`); the relay echoes
the negotiated value in `hello_ack.accepted_version`. Bump only on a breaking
change to the frames below.

## Frames

Every frame is a JSON text message with a `type` tag.

### `hello` (peer → relay, both roles)

```jsonc
{ "type": "hello", "protocol_version": 1, "role": "daemon" | "client",
  "daemon_id": "uuid", "daemon_label": "laptop",     // daemon_* set by daemons only
  "token": "…" }                                      // both roles, when --auth is on
```

A daemon mints `daemon_id` once and persists it at
`~/.local/share/irrlicht/relay-identity.json` (relocated by `IRRLICHT_HOME`), so
the id is stable across restarts; clients dedupe by it. `daemon_label` defaults
to the hostname. The relay **refuses** a daemon `hello` with an empty
`daemon_id`. A client `hello` omits the daemon fields.

`token` carries the bearer token, sent by **both** roles. It is omitted against
a `--auth off` relay (the trusted-LAN default) and required against an
auth-enabled one (see [Auth, TLS, and origins](#auth-tls-and-origins)). The
token rides in the `hello` rather than an HTTP header because a browser cannot
set headers on a WebSocket — one channel serves daemon, macOS, and web alike.

### `hello_ack` (relay → peer)

```jsonc
{ "type": "hello_ack", "accepted_version": 1 }
```

### `daemon_snapshot` (daemon → relay, after the ack)

```jsonc
{ "type": "daemon_snapshot",
  "sessions": [ <SessionState>, ... ],   // the daemon's current sessions
  "agents":   [ <AgentInfo>, ... ] }     // its **adapter** registry (the daemon's per-agent-type integrations; /api/v1/agents shape)
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

### `control` (client → relay → daemon, upstream)

```jsonc
{ "type": "control", "target_daemon": "uuid", "session_id": "proc-1234",
  "action": "input" | "interrupt", "data": "…" }                    // input only
```

The first client→relay→daemon frame in an otherwise one-way protocol (issue
#724): a client asks a specific daemon to inject input or an interrupt into
one of its sessions. The relay routes the frame to the addressed daemon
within the client's own token-derived workspace, then drops
`target_daemon`; the daemon re-gates it through the same consent path
(backchannel toggle, per-agent consent, controllability) as a local request
before forwarding to its `InputService`.

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

Against an auth-enabled relay a client includes its `token` in that `hello`. A
WS close with code **4401** means the token was missing, invalid, or revoked;
clients treat it as "auth failed" and stop reconnecting rather than looping.

## HTTP

The relay re-serves the daemon's read API from its cache so clients render
without an empty first paint, and serves the `platforms/web/` dashboard:

| Endpoint              | Body                                                            |
| --------------------- | -------------------------------------------------------------- |
| `GET /api/v1/sessions` | `{ "groups": [...] }` — `BuildDashboard` over the cached sessions (grouped by project; no **orchestrator** (Gas Town) state in v0). |
| `GET /api/v1/agents`   | union of every connected daemon's registry, deduped by `name`. |
| `GET /api/v1/version`  | the relay's own build version.                                 |
| `GET /` (+ assets)     | the dashboard, served from disk (`IRRLICHT_UI_DIR` override, else dev/bundle lookup). |

WebSocket `CheckOrigin` is permissive by default (the dashboard served from a
different port is cross-origin). `serve --origin-allowlist host1,host2`
restricts which browser `Origin`s may open the socket; non-browser peers send
no `Origin` and are always admitted, with auth still gating them.

## Auth, TLS, and origins

The relay is safe to expose once these are in place.

**TLS (`wss://`).** Two supported patterns:

- *Reverse-proxy termination (recommended).* Front the relay with
  Caddy/nginx/traefik terminating TLS and passing the WS upgrade through to the
  relay on loopback. The relay itself binds `127.0.0.1`.
- *Native TLS.* `irrlichtrelay serve --tls-cert cert.pem --tls-key key.pem`
  serves `wss://` directly (`ListenAndServeTLS`); both flags are required
  together. The daemon dialer already upgrades `https://`→`wss://`.

**Bearer tokens (`--auth`).** `off` (default) accepts any `hello` — the
trusted-LAN posture. `tokens-file[:PATH]` verifies the `hello`'s `token` for
both roles against a store hashed at rest (SHA-256); default path
`<data-dir>/tokens.json` (`$IRRLICHT_HOME` or `~/.local/share/irrlicht`).

```
irrlichtrelay token issue --label "ingo-laptop"             # prints the plaintext ONCE
irrlichtrelay token issue --label "acme" --workspace acme   # scoped to a tenant
irrlichtrelay token list                                    # id, created, workspace, label
irrlichtrelay token revoke <id>                             # next frame closed with 4401
```

A serving relay re-reads the tokens file when it changes, so `revoke` takes
effect without a restart: the revoked peer's next frame is closed with WS code
**4401** (`relay.CloseRevoked`), the same code that rejects a missing/invalid
token at handshake. The daemon presents its token via `IRRLICHT_RELAY_TOKEN` or
a `<data-dir>/relay-token.json` `{"token":"…"}` file (mode 0600 — a different
basename from the relay server's hashed `tokens.json` so the two never collide
under a shared `$IRRLICHT_HOME`); macOS uses the Keychain (URL/enabled stay in
UserDefaults); web keeps it in `localStorage`.

When `--auth` is on the gate also covers the read endpoints `GET
/api/v1/sessions` and `/api/v1/agents` — they carry the same session content as
the WS stream, so a token is required via `Authorization: Bearer <t>` or a
`?token=<t>` query param. `GET /api/v1/version` stays open as a health check.
`/api/v1/tokens` REST management is deferred — CLI-only for now.

**Workspaces (multi-tenant isolation).** A token may be scoped to a *workspace*
(`token issue --workspace <id>`); the workspace is stored with the token's hash
and is **server-derived at every handshake** — it is never read from the wire,
so it cannot be spoofed (the `hello` frame has no workspace field, and daemons
need no change). The relay partitions all cached state by
`(workspace, daemon_id, session_id)` and scopes both the WS fan-out and the
HTTP reads to the connection's own workspace, so a peer only ever sees its own
tenant's sessions — even if a daemon in another workspace claims a colliding
`daemon_id`. Isolation is on routing metadata only; payloads stay unread. A
token issued without `--workspace` (and every connection on a `--auth off`
relay) lives in the default workspace `""`, i.e. single-tenant — the behavior of
every relay before workspaces existed.

## Running it

```sh
irrlichd                                          # daemon on :7837 (its default)
IRRLICHT_RELAY_URL=ws://localhost:7839 irrlichd   # …also forwards to the relay
irrlichtrelay serve                               # the relay on 127.0.0.1:7839
```

> **Bind:** the relay listens on **`127.0.0.1:7839`** by default. With no
> `--auth` and no TLS it must stay loopback-only — anyone who can reach an
> exposed address can read every session and inject as a daemon. To expose it,
> bind a routable address *and* enable auth + TLS (or front it with a
> TLS-terminating reverse proxy), e.g. `irrlichtrelay serve --addr 0.0.0.0:7839
> --tls-cert cert.pem --tls-key key.pem --auth tokens-file`. This mirrors the
> daemon's own loopback-by-default posture.

> **Port choice:** irrlicht uses three contiguous ports — `7837` (production
> daemon), `7838` (dev-daemon coexist via `IRRLICHT_DAEMON_PORT`/`BIND_ADDR`),
> and `7839` (relay default). All three sit in the `7836–7850` band, which has
> no IANA-notable service and is effectively never seen open in the wild (per
> nmap-services frequency data), so collisions are unlikely.

Then in **Settings → Sources** (macOS or web) enable **Local** and/or enter the
**Relay server URL**; both clients show the union of sessions, live, and the
connection-status tooltip lists the connected daemon(s).

## Reserved (named, not built)

- `seq` (per-source sequence numbers) and `resume` (reconnect cursor) for
  gap-free reconnection.
- `/api/v1/tokens` REST management (POST/DELETE); token management is CLI-only.
- Multiple relays per daemon; multi-node relays; persistence.
- Row-level source badges and "fade, don't delete" on daemon-offline (v0 deletes
  a disconnected daemon's rows).
- History re-hydration of late-joining relay clients (bars fill from live ticks;
  session state *is* replayed, history snapshots are not).
