# Examples

Copy-paste deployment recipes for irrlicht. (For the hermetic CI Linux gate,
see [`tools/linux-replay.Dockerfile`](../tools/linux-replay.Dockerfile) instead — that's a test
fixture, not a deployment.)

| Example | What it is |
| --- | --- |
| [`relay/`](relay/) | Standalone `irrlichtrelay` in Docker — the cross-host hub daemons forward to and clients read from. |
| [`roundtrip/`](roundtrip/) | The full live cross-host demo: a Linux `irrlichd` + real `claude` → relay → the macOS app on your host. Builds on `relay/`. |
| [`coding-factory/`](coding-factory/) | Three `codex` containers, each with its own `irrlichd`, forwarding to one auth-enabled relay: three rows on the macOS app and the web dashboard. Builds on `roundtrip/`. |
| [`vm-pool/`](vm-pool/) | Coding agents one per container or micro-VM (Docker or Apple `container`), shown through the worker list — busy, waiting with a notification, failed, stale — without any transcript leaving the VM. |

`relay/`, `roundtrip/` and `coding-factory/` build `irrlichd` / `irrlichtrelay`
from source in this repo; `vm-pool/` reports to the Irrlicht already running on
your host. None of them needs a published release.
