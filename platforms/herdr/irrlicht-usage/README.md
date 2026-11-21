# Irrlicht usage — a herdr plugin

One herdr plugin action, `usage`. Invoke it for a pane and it prints the token, cost and
rate-limit usage of the agent sessions Irrlicht has seen running in that pane, as one
JSON document in [Factory](https://github.com/ingo-eichhorst/factory)'s usage schema 1
(`crates/factory-agents/src/usage.rs`).

```sh
herdr plugin install ingo-eichhorst/Irrlicht/platforms/herdr/irrlicht-usage
```

Irrlicht does not install the plugin or touch herdr's configuration.

## Requirements

- **macOS** with the Irrlicht app running and `irrlicht-ls` installed (Irrlicht's
  settings, or the `.pkg` installer, links it into `/usr/local/bin`). The action finds it
  on `PATH`, then in `/usr/local/bin`, `/opt/homebrew/bin` and the app bundle. Set
  `IRRLICHT_LS` in herdr's environment to use a specific build.
- **herdr 0.8.0 or later.**
- **Irrlicht's "Terminal focus" permission** (launcher capture). Irrlicht learns which herdr
  pane a session runs in from that capture. Without it no session carries a pane, and the
  action answers every pane with `"sessions": []`. That answer is correct, but it is not
  informative.

## Which pane

herdr passes the pane the action is invoked for as `HERDR_PANE_ID`. Over herdr's socket
API, that is `plugin.action.invoke` with `context.focused_pane_id`, and Factory invokes it
that way. The action never asks herdr which pane has focus. Pane ids repeat across herdr
servers, so the action also matches `HERDR_SOCKET_PATH`, which herdr 0.8.0 passes to every
plugin action.

## What it prints

- **`sessions[]`** has one entry per agent session in the pane. A subagent is listed under
  its parent's `subagents[]`, not as a separate entry.
- **`tokens`** carries cumulative input, output, cache-read and cache-write counts.
- **`cost.usd`** is an API-equivalent cost. `cost.pricing_source` says where it came from:
  `litellm@<price table date>`, `provider-reported`, or both.
- **`elapsed_seconds`** is the time between the session's first and last observed event.
- **`rate_limit`** is the provider's own quota windows, when Irrlicht has seen any.

When Irrlicht cannot vouch for a number, the number is `null`, never `0`. The reason goes
under `unavailable`, keyed by the field's dotted path. For example:

- a model with no known price leaves `cost.usd` null;
- an agent whose turns report only a cost leaves `tokens.*` null;
- an agent adapter that has not declared which token types it counts leaves all its token
  counts null;
- a parent whose adapter has not established that its counts exclude its subagents leaves
  its own counts null.

## Subagents that have finished

The daemon removes a subagent's session once it is done, but it keeps that subagent's final
usage (tokens, cost and pricing source), and `subagents[]` still lists it.

- **Lifetime of the record:** it lasts as long as its root session is in Irrlicht's session list.
- **Root that is gone:** the records are kept for 7 more days, so a resumed session
  (`claude --continue`) still finds them.
- **Failed write:** the record is still served, but only until the daemon restarts. The daemon
  logs the failure.

## When it fails

If Irrlicht's daemon is unreachable or answers badly, the action exits non-zero with one
line on stderr and prints nothing. It also does this when the pane id matches sessions on
two herdr servers and no socket path tells them apart.

An `irrlicht-ls` from before this mode existed fails the same way, with its usage text. The
action never writes anything, and its failure is contained in herdr's plugin log.
