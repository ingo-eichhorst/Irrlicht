# Irrlicht

A small panel that lists Herdr's agents, each with how full its context window
is, as measured by the local [Irrlicht](https://github.com/ingo-eichhorst/Irrlicht)
daemon. A Claude Code row reads `4.8%` with a thin bar, and a Codex row reads
`3.6%`. Clicking a row focuses that agent.

![Irrlicht](preview.png)

## What you see

- A column on the theme's `chrome` background, laid out top to bottom:
  - A header reading "Context" (11.5px, medium weight, `dim`), 41px tall with
    16px of padding at the sides.
  - A status line (11px). When the daemon answers it is `dim` and reads
    `Irrlicht at localhost:<port>`. When nothing answers it is `dim` and reads
    `Irrlicht daemon not reachable at localhost:<port>`. When a request has
    gone 10 s without an answer it is `dim` and reads
    `Irrlicht daemon at localhost:<port> is not answering`. When something else
    answers it uses `blocked` and reads
    `Irrlicht at localhost:<port> sent an unexpected answer`. When the mod
    itself threw it uses `blocked` and reads `Irrlicht panel error: <message>`.
  - One 30px row per agent, in `agents()` order, with 8px rounded corners.
    `selected` marks the focused agent and `hover` shows on hover. Each row has,
    left to right:
    - the program icon (16px);
    - `space · title` in `foreground`, truncated, where the title is the
      agent's own with any status glyph removed, else the tab's name;
    - the gauge;
    - a 7px dot in the colour of the agent's Herdr status.
- The gauge is one of three things:
  - A 36×4px bar in `border` whose fill is the context share in the pressure
    colour, followed by the percentage to one decimal (11.5px).
  - `<n>K tok` in `muted`, when the context window is unknown.
  - A `dim` en dash, when there is no figure.
- With no agents, the column reads "No agents yet. Agents show up here when
  they report their status to Herdr."

## How it behaves

- **Agent status:** working, blocked, done and idle each show only as the dot's
  colour (`working`, `blocked`, `done`, `idle`). The panel does not reorder rows
  by status.
- **Pressure colour:** Irrlicht's `pressure_level` picks the theme colour. `safe`
  (below 60%) uses `idle`, `caution` (60%) uses `working`, and `warning` (80%)
  and `critical` (90%) use `blocked`. The percentage uses the same colour,
  except `safe`, which uses `muted`.
- **Clicking a row** runs `pane.focus {terminal: <address>}`. The mod command
  `irrlicht.focus-<n>` does the same for row n, from 1.
- **Hover** shows a tooltip such as
  `1. 4.8% of 1.0M context, safe · claude-opus-5-5 · matched by pid`.
- **Polling:** every 3 s it GETs `/api/v1/sessions` and redraws only when a
  status line, gauge or tooltip changed. It sends one request at a time.
  Herdr's `fetch` has no timeout, so a request still unanswered after 10 s
  turns the status line to "not answering" and the gauges to dashes.

## Built from

- **One mod:** `mods/irrlicht/`. `mod.json` sets `place: right`, size 320, and an
  `overlay` of kind `window` at 380×240, which `mod.open` uses.
- **Herdr functions:** `agents()`, `terminals()` (`pid`, `foreground.pid`,
  `foreground.cwd`), `theme()`, `run("pane.focus")`, `exec`, `home()`,
  `readTextFile`, `checking()`, `nextCommand()`, and the global `fetch`.
- **Programs:** `/usr/bin/printenv IRRLICHT_BIND_ADDR` and
  `/usr/bin/printenv IRRLICHT_HOME`, run once each, because a mod has no
  `process.env`. Also `/bin/sh -c '[ -f "$1" ] && [ ! -L "$1" ]'` on the address
  file, because a mod has no `lstat`.
- **Network:** `http://localhost:<port>/api/v1/sessions` only.
- **Address order**, as Irrlicht's `daemonaddr.ClientURL`:
  1. `IRRLICHT_BIND_ADDR` with a port from 1 to 65535.
  2. The first 64 bytes of `$IRRLICHT_HOME/irrlichd.addr`, else of
     `~/.local/share/irrlicht/irrlichd.addr`, trimmed. It must be a regular
     file and not a symlink.
  3. Port 7837.

  The address is kept while the daemon answers. Once a poll has failed, it
  is looked up again at most every 30 s.
- **Matching an agent to a session:**
  1. A session whose `pid` equals the terminal's `foreground.pid`, else the
     terminal's `pid`. Two sessions on one pid get no gauge.
  2. Otherwise, the session whose `adapter` equals the agent's `app` and whose
     `cwd` equals `foreground.cwd`. This applies only when exactly one agent and
     exactly one session share that pair, and no row already holds that
     session.

  Child sessions (subagents) and agents on other machines (`vps/t7`) are
  never matched.
- **Code layout:** the decisions are in `lib/` (address, sessions, match,
  gauge, poll, rows), with no Herdr imports, and are tested with vitest.
  `main.js` reads Herdr and draws.

## Watch out

- The mod's runtime has no `TextEncoder`, `process.env` or `lstat`, and
  `readTextFile` follows symlinks. All four were measured in a live mod.
- `foreground.cwd` is the resolved path (`/private/tmp/...`) and matches
  Irrlicht's `cwd`. The terminal's own `cwd` is the path as typed (`/tmp/...`)
  and does not.
- Codex installed with npm runs as `node` → native binary. Herdr's foreground
  pid is the `node` process and Irrlicht's pid is the binary, so Codex is
  matched by folder only.
- A Herdr.app started from inside a Claude Code session passes
  `CLAUDE_CODE_CHILD_SESSION` to every terminal. Claude Code then saves no
  transcript, and Irrlicht cannot see that session.
- Herdr 0.1.16 did not dock the mod by `place` into the shipped `main` layout.
  Open it with `mod.open` or `pane.split {mod: "irrlicht"}`.
- `herdr-app check` renders the first frame only. The poll, the address
  lookup and the matching run later and are covered by the vitest suite.

## Done when

- `herdr-app check mods/irrlicht` passes.
- With two agents of different kinds running in Herdr, each row's percentage
  equals the daemon's `context_utilization_percentage` for that session, to
  one decimal.
- Stopping the daemon turns the status line to "not reachable" and the gauges
  to dashes, while the rows stay. Starting it again restores the gauges within
  one poll.
- Pausing the daemon (`kill -STOP`) turns the status line to "not answering"
  within about 13 s, and resuming it (`kill -CONT`) restores the gauges.
- Clicking a row, or running `herdr-app do irrlicht.focus-<n>`, focuses that
  agent's pane.
