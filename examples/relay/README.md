# Relay in Docker

A standalone [`irrlichtrelay`](../../docs/relay-protocol.md) — the cross-host hub.
Daemons (`irrlichd`) **forward out** to it; clients (the macOS app or a browser)
read from it. Because daemons dial *out*, this works through NAT with no inbound
port on the daemon side — the relay is the only thing that needs a reachable port.

```
  irrlichd  ──ws push──▶  irrlichtrelay :7839  ◀── macOS app / browser
 (any host)                  (this container)
```

## Run

```bash
docker compose -f examples/relay/docker-compose.yml up -d --build
open http://localhost:7839          # relay-served dashboard
```

Then point a daemon at it:

```bash
IRRLICHT_RELAY_URL=ws://<relay-host>:7839 irrlichd
```

…or add `ws://localhost:7839` as a **Source** in the macOS app
(Settings → Sources).

## Notes

- **Auth & TLS are off by default** — fine for this loopback-published demo. To
  expose the relay, enable a bearer token and front it with TLS:

  ```bash
  docker compose -f examples/relay/docker-compose.yml exec relay \
    irrlichtrelay token issue --label laptop      # secret shown once
  ```

  then uncomment the `--auth tokens-file` block in `docker-compose.yml` and put
  a TLS-terminating proxy in front. Full recipes (auth, TLS, systemd, the
  cloned-VM identity gotcha) → **[DEPLOY.md](./DEPLOY.md)**.
- For a non-container deploy, a system unit ships at
  [`irrlichtrelay.service`](./irrlichtrelay.service).
- The image **bakes a snapshot** of `platforms/web/`. Dashboard edits need an
  image rebuild (`--build`) — unlike the daemon, which serves the dashboard
  live from disk.
- Built from source in this repo (multi-stage Go build); no published release
  required.

For a full **live cross-host demo** that drives a real `claude` through a Linux
daemon into this relay and onto a Mac, see [`../roundtrip/`](../roundtrip/).
