# Deploying `irrlichtrelay`

Operator guide for running the standalone [`irrlichtrelay`](../../docs/relay-protocol.md) hub in
production — auth, TLS, systemd, and the cloned-replica gotcha. For the wire protocol itself, see
[`docs/relay-protocol.md`](../../docs/relay-protocol.md); for a one-command Docker run, the sibling
[`README.md`](./README.md).

```
  irrlichd ──ws push──▶  reverse proxy (wss://, TLS)  ──▶  irrlichtrelay :7839 (loopback)
 (any host, NAT-ok)                                              ▲
                                          macOS app / browser ───┘  read (Bearer token)
```

Daemons dial **out** to the relay, so only the relay needs a reachable port; the daemon side works
through NAT with no inbound port.

## Build the binary

Built from source — there is no published release yet:

```bash
cd core
go build -trimpath -ldflags "-X main.Version=$(git describe --tags --always)" \
  -o /usr/local/bin/irrlichtrelay ./cmd/irrlichtrelay
irrlichtrelay --version
```

Or run it in a container — see [`README.md`](./README.md) / [`Dockerfile`](./Dockerfile).

## Two postures

| | Trusted-LAN / dev | Production (exposed) |
|---|---|---|
| Bind | `127.0.0.1:7839` (default) | routable, behind TLS |
| Auth | `--auth off` (default) | `--auth tokens-file` |
| TLS | none (http/ws) | reverse proxy **or** `--tls-cert`/`--tls-key` |

```bash
irrlichtrelay serve                                   # trusted-LAN: loopback, no auth
irrlichtrelay serve --addr 127.0.0.1:7839 --auth tokens-file   # production, behind a TLS proxy
```

> ⚠️ A non-loopback bind **without `--auth` is wide open** — anyone who can reach it reads every session
> and can inject as a daemon. **TLS encrypts the wire but does not authenticate the peer**, so always pair
> exposure with `--auth`. The relay logs a loud warning if you bind a routable address with no auth.

## Bearer tokens

With `--auth tokens-file`, the relay verifies a hashed bearer token. Tokens live in
`$IRRLICHT_HOME/tokens.json` (mode `0600`, hashes only — plaintext is shown once) and are managed with the
`token` subcommand. Run it **as the same user/`IRRLICHT_HOME`** the relay uses, so both read one file:

```bash
export IRRLICHT_HOME=/var/lib/irrlichtrelay
irrlichtrelay token issue --label "ingo-laptop"   # prints the secret ONCE — store it now
irrlichtrelay token list                          # id  created  label
irrlichtrelay token revoke <id>                   # peer's next frame closes with WS 4401
```

The running relay polls the file every ~2s, so `issue`/`revoke` take effect **without a restart**.

Each daemon and client then presents the token:

- **daemon** (`irrlichd`): `IRRLICHT_RELAY_TOKEN=<token>` env, or `<dataDir>/relay-token.json`
  (`{"token":"…"}`, mode `0600`).
- **macOS / web**: enter it under **Settings → Sources** next to the relay URL.

## TLS

### Reverse-proxy termination (recommended)

Keep the relay on loopback and let a proxy terminate TLS and forward the WebSocket upgrade.

**nginx:**

```nginx
server {
    listen 443 ssl;
    server_name relay.example.com;
    ssl_certificate     /etc/letsencrypt/live/relay.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/relay.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:7839;
        proxy_http_version 1.1;
        proxy_set_header Upgrade    $http_upgrade;   # required for the WS upgrade
        proxy_set_header Connection "upgrade";
        proxy_set_header Host       $host;
        proxy_read_timeout 1h;                        # keep idle session streams open
    }
}
```

**Caddy** (auto-TLS):

```caddy
relay.example.com {
    reverse_proxy 127.0.0.1:7839
}
```

Daemons/clients then use `wss://relay.example.com` (the proxy handles `:443`).

### Native TLS

Skip the proxy and serve `wss://` directly — both flags are required together (still keep `--auth`):

```bash
irrlichtrelay serve --addr 0.0.0.0:7839 --auth tokens-file \
  --tls-cert /etc/irrlichtrelay/cert.pem --tls-key /etc/irrlichtrelay/key.pem
```

### Browser origin allowlist

For browser WS clients on a public origin, restrict who may connect:

```bash
irrlichtrelay serve … --origin-allowlist relay.example.com,dash.example.com
```

Empty (default) allows all origins — fine for loopback, not for a public bind.

## systemd

A ready-to-edit unit ships at [`irrlichtrelay.service`](./irrlichtrelay.service) (binds loopback + auth;
front it with one of the TLS proxies above). Install:

```bash
# binary at /usr/local/bin/irrlichtrelay (see "Build the binary")
useradd --system --no-create-home --home /var/lib/irrlichtrelay irrlichtrelay
cp examples/relay/irrlichtrelay.service /etc/systemd/system/

install -d -o irrlichtrelay -g irrlichtrelay /var/lib/irrlichtrelay
sudo -u irrlichtrelay IRRLICHT_HOME=/var/lib/irrlichtrelay \
  irrlichtrelay token issue --label "first-daemon"

systemctl daemon-reload
systemctl enable --now irrlichtrelay
journalctl -u irrlichtrelay -f
```

The unit sets `StateDirectory=irrlichtrelay` (`/var/lib/irrlichtrelay`) and `IRRLICHT_HOME` to match, so
`tokens.json` persists and the `token` CLI (run as `irrlichtrelay`) and the service share one file.

## macOS: launchd

A relay on the Mac itself needs no extra hardware, and a tailnet name gives it the one thing Web Push
requires — an origin that survives every network the Mac joins. A ready-to-edit LaunchAgent ships at
[`io.irrlicht.relay.plist`](./io.irrlicht.relay.plist) (loopback + auth, its own state dir, replace
`/Users/YOU`):

```bash
mkdir -p ~/Library/Application\ Support/Irrlicht/relay
IRRLICHT_HOME=~/Library/Application\ Support/Irrlicht/relay \
  irrlichtrelay token issue --label "this-mac"
cp examples/relay/io.irrlicht.relay.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/io.irrlicht.relay.plist

tailscale serve --bg 7839     # tailnet-only  → https://<mac>.<tailnet>.ts.net
tailscale funnel --bg 7839    # public        → same name, reachable anywhere
```

Give the relay its own `IRRLICHT_HOME`, separate from the daemon's — two processes, two state dirs,
nothing shared to reason about.

## Phone notifications (Irrlicht Beacon)

The relay can push `waiting` / `ready` transitions to a paired phone as Web Push notifications
(architecture: [`docs/mobile-notifications-arc42.md`](../../docs/mobile-notifications-arc42.md)). It is
off until a phone is paired, and there is no project-run service anywhere in the path — the relay signs
with its own VAPID key and posts directly to Apple's/Google's push service.

**Push requires `--auth`.** A push-capable relay is by definition reachable, and every push endpoint is
an abuse without an identity, so the relay refuses to serve them under `--auth off`: no VAPID key is
generated, `GET /api/v1/push/info` answers `{"enabled":false}` with the reason, and every other push
route answers `403` naming the fix. One security model, not two — this holds on a tailnet as well, where
the network already gates access.

**It also needs a stable HTTPS origin.** Browser push subscriptions and the installed web app are both
bound to their origin (a `*.ts.net` name or your own domain, either is fine). Renaming it re-pairs every
phone, so pick the name once.

### Pairing, from the operator's side

Nothing to configure. In the dashboard the relay serves, **Settings → Beacon** mints a one-time code; the
phone opens the same URL, adds it to the home screen, and types the code inside the installed app (iOS
keeps browser-tab storage and installed-app storage separate, which is why the last step happens there).
The code is single-use, expires in 10 minutes, and repeated wrong guesses are rate-limited.

Redeeming a code issues an ordinary bearer token for that phone, so it shows up in `token list` and
`token revoke <id>` is the whole un-pairing story — the relay drops the phone's delivery address within
the same tick its stream access dies.

### What lands on disk

Session content is **never** written to the relay host — before this feature and after it. Push adds two
files beside `tokens.json`, both mode `0600`:

| File | Holds | If lost |
|---|---|---|
| `tokens.json` | token hashes (existing) | every daemon, client and phone must be re-issued |
| `vapid-keys.json` | the relay's signing identity | no phone is pushable until its app is next opened, which re-subscribes against the new key |
| `push-subscriptions.json` | one delivery address per paired phone | phones re-register on next open |

Notification payloads are stored **nowhere** — there is no outbox. Apple and Google are the queue, and
each message's TTL bounds it (a `waiting` notification stays true for an hour, a stale `ready` is noise
after ten minutes).

**Backup / host migration:** copy those files and keep the hostname; every phone survives the move
untouched. A corrupt `vapid-keys.json` is refused at startup rather than silently regenerated — the
error names the file and your two options — because minting a fresh identity would orphan every paired
phone.

> **Disk compromise:** an attacker who reads the relay's state dir gets no session content and no usable
> bearer tokens (only hashes) — but `vapid-keys.json` plus `push-subscriptions.json` together let them
> send arbitrary notifications to paired phones until you re-pair, which rotates everything.

## Cloned VMs / replicas: `relay-identity.json` collision

This is the most common multi-instance footgun, and it concerns the **daemons that forward in**, not the
relay host.

Each daemon mints a stable `daemon_id` (a UUID) **once** and persists it at
`$IRRLICHT_HOME/relay-identity.json` (default `~/.local/share/irrlicht/relay-identity.json`). The relay
keys daemons by that id. If you build a VM/container image with a daemon that has **already connected
once**, every clone ships the **same** `relay-identity.json` → every replica announces the **same
`daemon_id`** → the relay merges them all into **one** daemon, and their sessions clobber each other.

**Fix — give each replica a fresh identity before its first relay connect:**

```bash
rm -f "${IRRLICHT_HOME:-$HOME/.local/share/irrlicht}/relay-identity.json"
```

The daemon mints a new UUID on next start. Do this in the image's first-boot hook (cloud-init, an
ENTRYPOINT step, a `systemd` `ExecStartPre`, etc.), or simply **never bake a daemon that has already run**
into the golden image. Critical for any autoscaled or multi-container deploy (and for the cross-host demo
in [`../roundtrip/`](../roundtrip/)).

---

→ Protocol reference: [`docs/relay-protocol.md`](../../docs/relay-protocol.md)
([Auth, TLS, and origins](../../docs/relay-protocol.md#auth-tls-and-origins)) ·
Live cross-host demo: [`../roundtrip/`](../roundtrip/) ·
One-command Docker run: [`README.md`](./README.md)
