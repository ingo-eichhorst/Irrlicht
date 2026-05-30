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
