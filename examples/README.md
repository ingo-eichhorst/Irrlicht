# Examples

Copy-paste deployment recipes for irrlicht. (For the hermetic CI Linux gate,
see [`tools/linux-replay.Dockerfile`](../tools/linux-replay.Dockerfile) instead — that's a test
fixture, not a deployment.)

| Example | What it is |
| --- | --- |
| [`relay/`](relay/) | Standalone `irrlichtrelay` in Docker — the cross-host hub daemons forward to and clients read from. |
| [`roundtrip/`](roundtrip/) | The full live cross-host demo: a Linux `irrlichd` + real `claude` → relay → the macOS app on your host. Builds on `relay/`. |
| [`vm-pool/`](vm-pool/) | Coding agents one per container or micro-VM (Docker or Apple `container`), shown through the worker list — busy, waiting with a notification, failed, stale — without any transcript leaving the VM. |

Both build `irrlichd` / `irrlichtrelay` from source in this repo; no published
release is required.
