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

## Get the binary

### From a release (Linux, amd64 + arm64)

`irrlichtrelay-linux-<arch>.tar.gz` is built by every release **from the next
one onward** — no published release carries it yet, so check the releases page
and use the from-source path below if it is not there. It carries the
dashboard alongside the binary. Extract it somewhere and keep the two
directories together — the relay finds its UI at `../Resources/web` relative to
the binary, so moving `bin/irrlichtrelay` out on its own leaves the dashboard
answering 503:

```bash
sudo mkdir -p /opt/irrlichtrelay
curl -fsSL -O https://github.com/ingo-eichhorst/Irrlicht/releases/latest/download/irrlichtrelay-linux-arm64.tar.gz
sudo tar -xzf irrlichtrelay-linux-arm64.tar.gz -C /opt/irrlichtrelay
/opt/irrlichtrelay/bin/irrlichtrelay --version
```

`arm64` is the Ampere A1 free tier; use `amd64` on the E2.1.Micro shape. Both
binaries are statically linked, so they also run on musl and distroless bases.

### From source

```bash
cd core
go build -trimpath -ldflags "-X main.Version=$(git describe --tags --always)" \
  -o /usr/local/bin/irrlichtrelay ./cmd/irrlichtrelay
irrlichtrelay --version
```

A from-source build installs no dashboard, and the binary at `/usr/local/bin`
has no `../Resources/web` to find — so either copy `platforms/web/` somewhere
and point `IRRLICHT_UI_DIR` at it, or accept a relay that serves the API and a
503 on `/`. The service files in this directory do the former.

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
# binary at /usr/local/bin/irrlichtrelay (see "Get the binary")
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

## Oracle Cloud (the reference deployment)

OCI's Always Free tier runs the relay at no cost, and its shape is the reason this section
exists: the free compute is **ARM** (`VM.Standard.A1.Flex`, aarch64), with an x86 fallback
(`VM.Standard.E2.1.Micro`) that operators routinely land on when A1 capacity is unavailable.
Both architectures are published, so match the binary to `uname -m` — `aarch64` on A1,
`x86_64` on micro. A wrong pick fails with `cannot execute binary file: Exec format error`
or a systemd unit dying instantly at `status=203/EXEC`.

> **Read this one first: OCI reclaims idle instances, and a relay is the idle profile.**
> Oracle's stated policy is that an Always Free instance is idle when, over a 7-day window,
> its 95th-percentile CPU is under 20% **and** network is under 20% **and** (A1 only) memory
> is under 20%. A relay holding a handful of long-lived WebSockets and posting the occasional
> 2 KiB push sits far below all three, so it is a candidate by construction — and the failure
> is silent: the instance stops and notifications simply cease. Converting the tenancy to Pay
> As You Go is reported to exempt it while usage within Always Free limits still bills at
> zero, but *we have not verified that* — confirm on Oracle's own FAQ before relying on it.
> What you can rely on is making the rebuild cheap, which the next paragraph is about.

**Design the deployment so a rebuild costs minutes and re-pairs nothing.** This is where the
relay's small state footprint pays: assign a **reserved public IP** (regional, survives
instance termination, reassignable to the replacement) and keep the DNS zone at your registrar
or Cloudflare rather than OCI DNS — which is not an Always Free resource. The hostname is then
independent of the instance, so a reclaimed or replaced VM does not change the origin. Restore
`tokens.json`, `vapid-keys.json`, `push-subscriptions.json` and `daemon-roster.json` onto the
new instance and every paired phone keeps working, because the origin and the VAPID identity
are exactly what a subscription is bound to. Without a reserved IP, a rebuild changes the
origin, and per the note above **that re-pairs every phone**.

### The firewall is two gates, and the console only shows you one

This is the single most common OCI failure, and it looks like a broken service: `curl
localhost:7839` works on the box, `ss -lntp` shows the process listening, and every request
from outside hangs. OCI platform images ship a **host firewall that permits only SSH**, on top
of the VCN security list you edited in the console. Open both.

```bash
# Layer 1 — VCN. Prefer a Network Security Group over editing the default security list,
# so the rule travels with the instance.  (protocol 6 = TCP; leave rules stateful)
oci network nsg rules add --nsg-id <nsg-ocid> --security-rules '[{"direction":"INGRESS",
  "protocol":"6","source":"0.0.0.0/0","sourceType":"CIDR_BLOCK","isStateless":false,
  "tcpOptions":{"destinationPortRange":{"min":443,"max":443}}}]'

# Layer 2a — Oracle Linux (firewalld)
sudo firewall-cmd --zone=public --permanent --add-port=443/tcp && sudo firewall-cmd --reload

# Layer 2b — Ubuntu (raw iptables, persisted in /etc/iptables/rules.v4)
sudo iptables -I INPUT 6 -p tcp --dport 443 -j ACCEPT   # INSERT above the REJECT, do not append
sudo netfilter-persistent save
```

The Ubuntu ruleset ends in `-A INPUT -j REJECT --reject-with icmp-host-prohibited`, so a rule
**appended** after it never takes effect — insert above it. Two things to never do: Oracle
states that using `ufw` on an Ubuntu OCI image "might cause an instance not to boot", and
flushing iptables wholesale removes the rules protecting the iSCSI endpoints
(`169.254.0.2:3260`, `169.254.2.0/24:3260`) that serve the boot volume — the instance dies at
the next reboot. Add rules; never replace the ruleset.

Outbound needs no configuration at all: a new VCN's default security list allows all egress,
so the relay reaches Apple/Google and Let's Encrypt without a rule.

### TLS and the hostname

Caddy on the instance, terminating TLS with automatic Let's Encrypt and reverse-proxying to
the relay on loopback (the config is in [TLS](#tls) above). ACME works normally here; there is
nothing OCI-specific about it. Two traps:

- **Do not put the Always Free Load Balancer in front of it.** Its HTTP listener has a
  60-second idle timeout that kills long-lived WebSockets, and the send and receive timers are
  independent — a one-directional server heartbeat does not hold the connection open. For a
  single small relay it adds a failure mode and buys nothing.
- Ports below 1024 are privileged. Grant the capability rather than running Caddy as root:
  `AmbientCapabilities=CAP_NET_BIND_SERVICE` in the unit. An ACME failure from a
  `bind: permission denied` looks exactly like a blocked port and sends you back to re-check
  firewall rules that were already correct.

### Sizing and the free-tier ledger

The A1 Always Free allocation is **2 OCPUs and 12 GB** — metered as 1,500 OCPU hours and 9,000
GB hours per month. Note what that arithmetic means: one 2-OCPU instance running 24/7 through a
31-day month consumes 1,488 of the 1,500 hours. The allowance is sized for exactly one
always-on instance with about 1% of headroom, so terminate before replacing rather than after,
or size the overlap window at 1 OCPU. (Many older guides still quote 4 OCPU / 24 GB; that is
out of date.) Outbound transfer is 10 TB/month — irrelevant for push.

Region is chosen once: Always Free instances must live in the tenancy's **home region**, which
is fixed at signup and cannot be changed without deleting the tenancy. A1 capacity varies by
region and "Out of host capacity" is common, so decide the region before signing up. For a
relay with a handful of connections the micro shape's 1 GB and ~50 Mbps of public bandwidth is
genuinely sufficient — the capacity fight is usually not worth having.

## Phone notifications (Irrlicht Elfdans)

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

Nothing to configure. In the dashboard the relay serves, **Settings → Irrlicht Elfdans** mints a one-time code; the
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
| `vapid-keys.json` | the relay's signing identity | **every phone must be paired again** — a phone whose subscription and relay record both still exist never notices the key changed, so nothing self-heals. This is the file to back up |
| `push-subscriptions.json` | one delivery address per paired phone | phones re-register on next open |
| `daemon-roster.json` | which daemons exist, and when each was last seen | the watchdog forgets a daemon that was already offline, so its disconnect goes unreported until it returns |

Notification payloads are stored **nowhere** — there is no outbox. Apple and Google are the queue, and
each message's TTL bounds it (a `waiting` notification stays true for an hour, a stale `ready` is noise
after ten minutes).

Delivery health — when each phone was last reached, and why the last attempt failed — is kept in
memory only, so after a restart it honestly reads "unknown" rather than reporting a stale success.

**Backup / host migration:** copy those files and keep the hostname; every phone survives the move
untouched. On a host that can be reclaimed under you (see [Oracle Cloud](#oracle-cloud-the-reference-deployment)),
this is the whole disaster-recovery story — four small files and a DNS record that never changed. A corrupt `vapid-keys.json` is refused at startup rather than silently regenerated — the
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
