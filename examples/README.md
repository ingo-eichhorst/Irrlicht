# Examples

Copy-paste deployment recipes for irrlicht. (For the hermetic CI Linux gate,
see [`docker/linux-replay.Dockerfile`](../docker/) instead — that's a test
fixture, not a deployment.)

| Example | What it is |
| --- | --- |
| [`relay/`](relay/) | Standalone `irrlichtrelay` in Docker — the cross-host hub daemons forward to and clients read from. |
| [`roundtrip/`](roundtrip/) | The full live cross-host demo: a Linux `irrlichd` + real `claude` → relay → the macOS app on your host. Builds on `relay/`. |

Both build `irrlichd` / `irrlichtrelay` from source in this repo; no published
release is required.
