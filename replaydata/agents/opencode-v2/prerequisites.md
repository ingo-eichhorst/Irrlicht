# Prerequisites — opencode-v2

Maintainer-authored checklist for recording the `opencode-v2` column: OpenCode
2.x, replayed through the same `opencode` daemon adapter as the `opencode`
column (#2109). `of record prereq-check --agent opencode-v2` prints the short
form from `metadata.json`.

## What the rig records against

The column records sessions that **one running `opencode serve --service`**
hosts. The rig drives that service over its HTTP API
(`driver-interactive.sh`) and never runs an OpenCode binary itself:

- **It never starts, stops, restarts or reconfigures the service.** It refuses
  when `$XDG_STATE_HOME/opencode/service.json` is missing, when the pid in it
  is dead or is not `serve --service`, and when more than one
  `serve --service` runs (the daemon binds a service-hosted session only while
  exactly one runs: `core/adapters/inbound/agents/opencode/pid.go`
  `servicePIDFor`).
- **The CLI version is read from the store**, never with `--version`
  (`tools/onboarding-factory/scripts/lib/cli-version.sh`): on this host
  `opencode` on PATH is brew's 1.x, and a 2.x binary run outside its sandbox
  migrates the real `~/.local/share/opencode/opencode.db` in place.

On the development host the service is the sandbox's, under
`/Users/factory/.oc2sb/` (one shared service on the fixed port 49374, the local
Splash model at `127.0.0.1:8085`).

## The exports

Run `of record run` from a shell that exports exactly these, and nothing that
moves `HOME`:

```
export XDG_CONFIG_HOME=/Users/factory/.oc2sb/home/.config
export XDG_DATA_HOME=/Users/factory/.oc2sb/home/.local/share
export XDG_STATE_HOME=/Users/factory/.oc2sb/home/.local/state
unset OPENCODE_DB
IRRLICHT_ONBOARD_HOME=/Users/factory/.oc2sb/home/.local/share/irrlicht \
IRRLICHT_ONBOARD_BIND_ADDR=127.0.0.1:7843 \
  go run ./tools/onboarding-factory/cmd/of record run --agent opencode-v2 --scenario <scenario>
```

Why each one:

- **The three XDG variables** are the column's rig-home row
  (`tools/onboarding-factory/scripts/lib/agent-home.sh`, policy `optin`, all or
  nothing). `XDG_CONFIG_HOME` moves the plugin the daemon installs,
  `XDG_DATA_HOME` the store it watches, and `XDG_STATE_HOME` (driver-only) the
  `service.json` the driver authenticates with. They must be the **service's
  own** values: the driver reads the service's environment and refuses on any
  difference.
- **Not `HOME`, and not `source /Users/factory/.oc2sb/env.sh`.** A sandbox
  `HOME` would move every other adapter, and the Go build and module caches,
  into the sandbox. The rig never runs the 2.x binary, so it never needs
  `env.sh`.
- **`OPENCODE_DB` unset.** The service runs without it; set, it would point the
  daemon at a different database than the service writes. Both the version read
  and the driver refuse it.
- **`IRRLICHT_ONBOARD_HOME` is the service's `$HOME/.local/share/irrlicht`.**
  The 2.x plugin runs `irrlichd hook-post opencode` inside the service, and that
  beacon resolves the daemon from the service's environment
  (`core/pkg/daemonaddr`): with no `IRRLICHT_BIND_ADDR` or `IRRLICHT_HOME`
  there, it reads `$HOME/.local/share/irrlicht/irrlichd.addr`. The recording
  daemon publishes that file only when it runs with that directory as its home,
  and the driver refuses unless the file names the recording daemon's port.
- **A non-7837 bind address** — coexist mode refuses 7837 (production).

## Exclusivity

The service, the plugin file, and the addr slot are shared, so:

- Record **one cell at a time**, and run no other driver and no `qa-run.sh`
  against the service while recording.
- The rig refuses to start when a daemon already answers on
  `$IRRLICHT_ONBOARD_HOME/irrlichd.sock` (a `qa-run.sh` daemon uses the same
  directory).
- Before each cell it wipes the state stores the recording daemon owns under
  `$IRRLICHT_ONBOARD_HOME` (`instances/`, `sessions/`, `cost/`), so sessions an
  earlier daemon persisted are not restored into the recording.

## After a run

- The plugin file `$XDG_CONFIG_HOME/opencode/plugin/irrlicht.js` is restored to
  its prior state (absent, on the sandbox) by the managed-file snapshot.
- The driver leaves its sessions unarchived, so the recording ends on the
  session's real state; they stay in the service's store.
