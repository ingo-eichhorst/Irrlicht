# Irrlicht for Herdr.app

A [Herdr.app](https://herdr.dev) mod that shows how full each agent's context
window is. It lists Herdr's agents in Herdr's own order, and next to each one
shows the fill Irrlicht measured, for example `4.8%`, with a small bar coloured
by pressure. Clicking a row focuses that agent's pane.

![Irrlicht in Herdr](preview.png)

It is read-only. It reads the local Irrlicht daemon (`GET /api/v1/sessions`,
the endpoint the dashboard uses) and changes nothing in Irrlicht or Herdr.

This is for `Herdr.app` (`~/.config/herdr-app`), not the older terminal `herdr`
(`~/.config/herdr`).

## Install

You need Irrlicht running (the menu bar app or `irrlichd`) and Herdr.app.

Copy this folder, or link it from a clone, to `~/.config/herdr-app/mods/irrlicht`:

```sh
ln -s "$PWD/platforms/herdr/mods/irrlicht" ~/.config/herdr-app/mods/irrlicht
herdr-app check ~/.config/herdr-app/mods/irrlicht
```

Herdr can also install it from this folder's GitHub URL: give your Herdr agent
the link and ask it to add the mod.

## Show it

- As a floating window, over every tab. Run the same command again to close it:

  ```sh
  herdr-app do mod.open --name irrlicht
  ```

- As a pane beside the terminals of the current tab:

  ```sh
  herdr-app do pane.split --direction right --mod irrlicht
  ```

`mod.json` asks for a place on the right, but Herdr 0.1.16 did not dock the
mod into its shipped `main` layout by itself: not for a linked folder, not for
a copied one, and not after a restart. Both commands above were tested on
0.1.16.

To focus an agent from the keyboard, `irrlicht.focus-<n>` runs the click of
row `n`, counting from 1. Bind it in `keys.json`, or run it from a shell:

```sh
herdr-app do irrlicht.focus-2
```

## What a row shows

| The row shows | When |
|---|---|
| `4.8%` and a bar | Irrlicht knows the model's context window. The bar takes the theme's `idle` colour below 60%, `working` from 60%, and `blocked` from 80%. These are Irrlicht's pressure levels. |
| `31K tok` | Irrlicht could not resolve the context window (`context_window_unknown`). It shows the token count, never a misleading `0%`. |
| `–` | No Irrlicht session matched this agent, or the session has no figure yet. |

Hover over a row to see the exact figure, the model, and how the session was
matched.

The status line under the header says which daemon the panel reads, for example
`Irrlicht at localhost:7837`. It says *not reachable* when nothing answers
there, and *not answering* when a request has hung for 10 seconds (a daemon
that accepts the connection but never replies). In both cases the gauges turn
to dashes. It says *panel error* when the mod itself failed, so a broken panel
never looks like a stopped daemon.

## Which daemon it reads

It uses the same address order as Irrlicht's other clients
(`daemonaddr.ClientURL`):

1. `IRRLICHT_BIND_ADDR`, if Herdr.app's own environment has it.
2. The address the daemon published in `$IRRLICHT_HOME/irrlichd.addr`, else in
   `~/.local/share/irrlicht/irrlichd.addr`. A symlink is not read.
3. `localhost:7837`.

Herdr's own docs say a Finder launch loads the login shell's `PATH`. Whether
other variables from a shell profile reach Herdr.app was not checked, so expect
the address file and the default port to be what applies. The mod reads the
environment once, when it starts.

## How agents are matched to sessions

- **By pid first.** This uses the pid of the terminal's foreground process,
  then the pid of the terminal itself. Claude Code 2.1.330 matched this way.
- **Then by agent and folder.** This needs Herdr's agent name (`codex`) and the
  folder to be shared by exactly one Herdr agent and exactly one Irrlicht
  session. Codex 0.163.0 installed with npm matched this way. Herdr reports its
  `node` launcher as the foreground process, and Irrlicht reports the native
  binary that `node` starts, so the pids never match.

Two sessions of the same agent in one folder therefore get no gauge, unless
their pids match. They never trade numbers. Sessions Irrlicht sees outside
Herdr are not listed. An agent on another machine (`vps/t7`) gets no gauge:
the daemon only sees this machine's sessions.

## What it runs

Herdr asks you to check this before you add a mod:

- **Network:** one `GET http://localhost:<port>/api/v1/sessions` every 3
  seconds.
- **Programs:** `/usr/bin/printenv` twice at start, for the two environment
  variables, because a mod has no `process.env`. Also `/bin/sh -c '[ -f "$1" ]
  && [ ! -L "$1" ]'` on the address file, because a mod has no `lstat`. This
  test runs when the panel starts, and at most every 30 seconds while the
  daemon does not answer.
- **Files:** it reads only the address file above.

## Develop

The decisions live in `lib/`, which imports nothing from Herdr. The tests in
`test/` run under vitest:

```sh
cd platforms/herdr && npm ci --ignore-scripts && npm test
```

`test/address.parity.test.js` also runs the Claude Code panel's copy of the
address ladder (`platforms/claude-code/lib`) through the same table of Go's
answers, so it runs only inside this repository. `main.js` is the only file
that uses Herdr's runtime. Check it with
`herdr-app check`, which needs Herdr.app installed and is not part of CI.
Herdr writes `jsconfig.json` and `gpui-kit.d.ts` into a mod folder it loads.
`.gitignore` keeps them out of the repository. In one test on Herdr 0.1.16, an
open panel window kept running the old code after a save in the linked folder.
Closing and reopening it (`mod.open` twice) loaded the new code.
