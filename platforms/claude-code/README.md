# Irrlicht panel for Claude Code

`irrlicht-panel` is a Claude Code mod that shows Irrlicht's view beside the chat, for when the menu bar is out of sight: a full-screen terminal, an SSH session with the daemon's port forwarded, or another monitor.

It is a **read-only client** of the local Irrlicht daemon's `GET /api/v1/sessions`, the same endpoint the menu bar and the web dashboard read. It needs a running Irrlicht daemon, and it changes nothing: no daemon writes, no settings, no files.

```
Claude Max · …0001
5h  █░░░░░░░░░  11%  ↻ 2h 14m
7d  ████████░░  83%  ↻ 3d 4h

orchard (3)
● main 177K 17.7% opus-5.5 ← here
↳ ● main 4.2K 2.1% haiku-4.5
● feature/a-ver… 66K 6.6% sonnet-4.5

lighthouse (1)
● dev 40K 4.0% gpt-5.4 [pi]

24h $16.50 · press a session for details
```

## Install

At the prompt of a Claude Code terminal session:

```
/plugin install irrlicht-panel --marketplace ingo-eichhorst/Irrlicht
```

Answer `y` to add the marketplace, then pick a scope. From a shell, the same is `claude plugin install irrlicht-panel --marketplace ingo-eichhorst/Irrlicht`. In the desktop app, where `/plugin` does not run: click + next to the prompt box, then Plugins, Add marketplace with `ingo-eichhorst/Irrlicht`, then Add plugin.

## Open it

- Type `/irrlicht`. That opens the pane at any width: docked beside the transcript in the full-screen layout, inline above the prompt otherwise.
- In the full-screen layout the pane also opens on its own, as a sidebar, once the terminal is wide enough for one. Claude Code places a pane nobody asked for only from 144 columns (110 after you have opened it once). On the main screen (tmux's default, and `CLAUDE_CODE_NO_FLICKER=0`) it never opens on its own, so it never takes over the space above your prompt unasked.
- Close it with its `✕` or ctrl+x x.

## What it shows

- **Quota**: the windows (5h, 7d, …) of one quota bucket as bars, with the time to each reset. The bucket is the one the current session's own rate-limit sample lands in; without one, the most recently sampled bucket. Buckets follow the dashboard's and the menu bar's rule exactly (`<provider>|<confirmed account>`, or one `unconfirmed` bucket per provider), so all three show the same numbers. `lib/quota.parity.test.js` checks the rule against the dashboard's code.
- **Sessions**: every session the daemon watches, from every agent it supports, grouped by project. Each row shows a state dot (working, waiting, ready, error, in the dashboard's colours), the branch, tokens, context fill and model. The agent's name follows in brackets for sessions that are not Claude Code's, such as `[pi]` or `[codex]`, and subagents are indented under their parent.
- **← here** marks the session the pane runs in, and only on an exact match of this session's id with the id the daemon lists. A session's id reaches the daemon with its transcript, so a brand-new session that has not written one yet shows no marker. The panel never guesses.
- **Press a row** for its state, running time, estimated cost, and the question it is waiting on (or its last reply). Press it again to close it.
- **24h $X**: the spend of the trailing 24 hours across every provider, unattributed included. This is the daemon's rolling `day` window, not the calendar day.
- If the daemon cannot be reached, or answers with something that is not a sessions payload, the pane says `Irrlicht daemon not reachable at localhost:<port>` and why, then tries again every 3 s.

## Which daemon it asks

The same daemon `irrlicht-focus` and other Irrlicht clients find (`daemonaddr.ClientURL` in `core/pkg/daemonaddr`), always on `localhost`:

1. `IRRLICHT_BIND_ADDR`, when it names a fixed port (1 to 65535). The host part is ignored.
2. The address the running daemon published in `$IRRLICHT_HOME/irrlichd.addr`, or else in `~/.local/share/irrlicht/irrlichd.addr`. Only a regular file is read (not a symlink), and only its first 64 bytes.
3. Port 7837.

After a failed request the address is worked out again, so a daemon restarted on another port is found.

## What it never does

- It registers no hook on tool calls, prompts or permissions, so it cannot slow down or block the agent.
- It polls every 3 s, and only while its pane is open. A request that has not been answered after 10 s is abandoned.
- It writes nothing outside its own `$.state` in Claude Code.

## Develop

Run a session with the mod loaded from this folder. It hot-reloads on save:

```sh
claude --plugin-dir platforms/claude-code
```

The folder holds two test suites, run by two runners:

```sh
cd platforms/claude-code
npm ci && npm test                       # vitest: lib/**/*.test.js
claude plugin test platforms/claude-code # the mod against the engine: tests/*.test.tsx
claude plugin validate platforms/claude-code
```

- `lib/` holds the data shaping as plain ES modules: the address ladder, session rows, the quota rule and formatting. The hooks module and vitest both import them. The `.d.ts` files beside them only type them for the hooks module.
- `hooks/register.tsx` is the hooks module: polling, the `/irrlicht` command, and drawing the pane.
- `types/index.d.ts` is the mod's `$.state` contract.
- `tests/panel.test.tsx` mounts the pane on the `terminal` and `desktop` surfaces through `claude-code/testing`.

Claude Code writes its API's type declarations to `.claude-plugin/types/` each time it loads the mod from this folder. That folder ignores itself in git. `tsconfig.json` extends it, so `npx -p typescript tsc -p platforms/claude-code` type-checks the mod after one `claude --plugin-dir` run.

Never run `npm install` in this tree, because it rewrites `package-lock.json`. Use `npm ci`.
