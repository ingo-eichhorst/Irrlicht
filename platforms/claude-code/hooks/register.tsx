// irrlicht-panel: a read-only Irrlicht panel inside Claude Code.
//
// It polls the Irrlicht daemon's GET /api/v1/sessions (the payload the
// dashboard and the menu bar read) and draws every session the daemon watches,
// across agents, plus the quota of the account this session bills against.
//
// What it is written never to do. `claude plugin validate .` lists what the
// module hooks and calls, which is how to check the first two:
//   - gate or hold up the agent: it hooks session.start, session.end, its own
//     /irrlicht command.run, and two ui.render sites (AbovePrompt, handed on
//     unchanged, and its own Pane); no tool, prompt or permission event. Its
//     session.start starts the poller first and then awaits only the
//     /irrlicht registration;
//   - write anything but its own `$.state`: validate lists no `$.fs.write`,
//     no `$.store` call and "env writes: nothing";
//   - throw out of a hook: the poller catches every failure and draws it as
//     the "not reachable" view, session.start catches a refused registration,
//     and the session.start and command.run registrations carry a `.catch`.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register, Timer } from 'claude-code'

import type { IrrlichtPanelRow, IrrlichtPanelView } from '../types'
import { hostPortOf, resolveDaemonAddress, sessionsURL } from '../lib/address.js'
import type { DaemonAddress } from '../lib/address.js'
import { fitRow, quotaBar } from '../lib/format.js'
import { unreachableView, viewFromResponse } from '../lib/view.js'

const PANE = 'irrlicht'
const TITLE = 'Irrlicht'
const POLL_MS = 3000

// `$.http.fetch` has no timeout or abort the plugin can set (HttpInit carries
// neither in this API), so a daemon that accepts a connection and never
// answers holds the request open until the connection ends. After this long
// the view says "no answer", but no second request is started beside the
// first: one that cannot be cancelled would stay open on the daemon, one more
// per stall. The poller waits for the outstanding request to settle, then
// resumes.
//
// How long that wait is, is the host's to decide. Measured on Claude Code
// 2.1.325 against a server that accepts and never answers (the debug log, and
// a BrokenPipe on the server when it later tried to answer): the host ended
// the request after about 30 s (GET at 11:44:23.519, the next request at
// 11:44:53.548). That is an observation of one build, not a guarantee, and
// nothing here relies on it.
const STALL_MS = 10_000

const view = atom({ plugin: 'irrlicht-panel', key: 'view' } as const, null)
const expanded = atom({ plugin: 'irrlicht-panel', key: 'expanded' } as const, [])

// Module state for the poller alone. Nothing drawn reads it (drawing reads
// `$.state`), and a hot reload, which drops it, also drops the timer it serves.
let timer: Timer | undefined
// The resolved address; cleared after any failed poll so the next one walks
// the ladder again (a daemon restarted on another port is found again).
let address: DaemonAddress | undefined
// The one outstanding request, if any: when it started, the daemon it asked,
// and whether its stall has been drawn.
let inFlight: { since: number; hostPort: string; isReported: boolean } | undefined
let isAutoOpenTried = false

// The reads the address ladder needs. `$.env.get` takes string literals only,
// which is why they are spelled out here and not in lib/address.js.
function addressIO($: EngineInterface) {
  return {
    bindAddr: () => $.env.get('IRRLICHT_BIND_ADDR'),
    irrlichtHome: () => $.env.get('IRRLICHT_HOME'),
    home: () => $.env.get('HOME'),
    stat: (path: string) => $.fs.stat(path),
    read: (path: string) => $.fs.read(path),
  }
}

// isPaneShown answers whether a poll has anyone to draw for: the panel's pane
// is open and placed. A session whose person never opened the panel costs the
// daemon nothing. When the engine cannot say, poll anyway.
async function isPaneShown($: EngineInterface): Promise<boolean> {
  try {
    const panes = await $.ui.panes()
    return panes.some(p => p.id === PANE && p.isPlaced)
  } catch {
    return true
  }
}

// publish writes a view only when it differs from the one drawn, so an
// unchanged daemon answer redraws nothing.
async function publish($: EngineInterface, next: IrrlichtPanelView): Promise<void> {
  const current = await read($, view)
  if (current !== null && JSON.stringify(current) === JSON.stringify(next)) return
  await update($, view, () => next)
}

async function poll($: EngineInterface): Promise<void> {
  try {
    if (!(await isPaneShown($))) return
    const now = await $.clock.now()
    if (inFlight) {
      // One request at a time (STALL_MS says why): report a stall once, and
      // otherwise leave the outstanding request to settle.
      if (!inFlight.isReported && now - inFlight.since >= STALL_MS) {
        inFlight.isReported = true
        await publish($, unreachableView(inFlight.hostPort, `no answer in ${STALL_MS / 1000} s`, true))
      }
      return
    }
    const request = { since: now, hostPort: address ? hostPortOf(address) : 'localhost', isReported: false }
    inFlight = request
    try {
      const target = address ?? (address = await resolveDaemonAddress(addressIO($)))
      request.hostPort = hostPortOf(target)
      let response: Awaited<ReturnType<EngineInterface['http']['fetch']>> | undefined
      try {
        response = await $.http.fetch(sessionsURL(target))
      } catch {
        // Rejected (a refused connection, a policy refusal): response stays
        // undefined, which the view reads as "no connection". A try/catch
        // rather than `.catch()`, so a fetch that throws synchronously lands
        // here too.
      }
      // Read per poll, never cached: after a /clear the process goes on under
      // a new session id, and no session.start follows to say so.
      const hereSessionId = await $.session.id().catch(() => undefined)
      const next = viewFromResponse({
        response, hostPort: hostPortOf(target), hereSessionId, nowMs: await $.clock.now(),
      })
      if (next.status !== 'ok') address = undefined
      await publish($, next)
    } finally {
      if (inFlight === request) inFlight = undefined
    }
  } catch {
    // Never out of a timer: the pane keeps its last view, the next tick tries
    // again from a fresh address.
    address = undefined
  }
}

const TONE_COLOR = { green: 'success', yellow: 'warning', orange: 'error' } as const

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    // The poller first, so nothing after it can keep it from running.
    timer?.cancel()
    timer = $.clock.every(POLL_MS, () => {
      void poll($)
    })
    void poll($)
    try {
      await $.command.register({
        name: 'irrlicht',
        description: 'Open the Irrlicht panel: every agent session Irrlicht watches, and your quota',
      })
    } catch {
      // Another mod or an organization's guard refused /irrlicht. The panel
      // still polls; without the command, only the full-screen auto-open
      // (below) opens the pane.
    }
    return next(e)
  }).catch(($, e, next) => next(e))

  on('session.end', ($, e, next) => {
    // A /clear or a resume ends the conversation, not the process: "after a
    // /clear or a resume the process goes on under another" id, and no
    // session.start follows a /clear (SessionEndInput's own doc in the API's
    // types). So polling goes on; the next tick reads the new id. Every other
    // reason (the person left, logout, a signal) stops the timer.
    if (e.reason !== 'clear' && e.reason !== 'resume') {
      timer?.cancel()
      timer = undefined
    }
    return next(e)
  })

  // `/irrlicht` is the way in everywhere: the person asked, so the engine
  // places the pane at any width (docked in fullscreen, inline otherwise).
  on('command.run', { command: 'irrlicht' }, async $ => {
    const opened = await $.ui.open({ id: PANE, title: TITLE })
    void poll($)
    return { text: opened.isPlaced ? 'Irrlicht panel opened.' : 'Irrlicht panel waits: ' + opened.reason }
  }).catch(() => ({ text: 'The Irrlicht panel could not be opened.' }))

  // Unasked, the pane opens only where it docks as a sidebar. session.start
  // cannot tell (its input has no viewport); a render's `e.viewport.isFullscreen`
  // can, which is where the API's own example opens an unasked pane. This hook
  // draws nothing of its own: it answers what is beneath, unchanged. One try
  // per load. The engine then applies its own width floor to an unasked pane,
  // measured on Claude Code 2.1.325 (tmux, CLAUDE_CODE_NO_FLICKER=1, the
  // `--debug-file` log's `ui.open` line): 144 columns for a pane id the person
  // has never opened (120: "waits unplaced (unasked below 144 columns …)",
  // 150: "placed"), 110 once they have opened it (120: "placed", 100: "waits
  // unplaced (unasked below 110 columns (100 now, an id the person opened
  // before) …)"), as the API's UiOpenResult doc describes.
  on('ui.render', { component: 'AbovePrompt' }, ($, e, next) => {
    if (!isAutoOpenTried && e.viewport?.isFullscreen === true) {
      isAutoOpenTried = true
      void $.ui.open({ id: PANE, title: TITLE }).then(
        () => poll($),
        () => undefined,
      )
    }
    return next(e)
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const { Box, Button, Text } = $.ui.resolve(e)
    const current = await read($, view)
    const open = await read($, expanded)
    const width = Math.max(16, e.props.bodyColumns)

    if (current === null) {
      return <Text dimColor>Asking the Irrlicht daemon…</Text>
    }

    if (current.status !== 'ok') {
      return (
        <Box flexDirection="column">
          <Text color="warning" wrap="wrap">
            Irrlicht daemon not reachable at {current.hostPort}
          </Text>
          <Text dimColor wrap="wrap">
            {current.reason};{' '}
            {current.isAwaitingAnswer ? 'waiting for it to end.' : `trying again every ${POLL_MS / 1000} s.`}
          </Text>
        </Box>
      )
    }

    const toggle = (id: string) =>
      update($, expanded, list => (list.includes(id) ? list.filter(x => x !== id) : [...list, id]))

    const barWidth = Math.min(10, Math.max(4, width - 22))
    const quota = current.quota

    const row = (r: IrrlichtPanelRow) => {
      const indent = r.depth > 0 ? '  '.repeat(r.depth - 1) + '↳ ' : ''
      const isOpen = open.includes(r.id)
      const d = r.detail
      return (
        <Box flexDirection="column">
          <Button key={'row:' + r.id} plain onPress={() => toggle(r.id)}>
            {indent}
            <Text color={r.color}>●</Text> {fitRow(r, width - indent.length)}
          </Button>
          {isOpen && (
            <Box flexDirection="column" paddingLeft={indent.length + 2}>
              <Text dimColor wrap="wrap">
                {[d.state, d.elapsed, d.cost].filter(Boolean).join(' · ')}
              </Text>
              {d.text !== '' && (
                <Text wrap="wrap">
                  {d.textKind === 'question' ? '? ' : '› '}
                  {d.text}
                </Text>
              )}
            </Box>
          )}
        </Box>
      )
    }

    return (
      <Box flexDirection="column">
        {quota !== null && (
          <Box flexDirection="column" marginBottom={1}>
            <Text dimColor wrap="truncate-end">
              {quota.title}
              {quota.isStale ? ' (stale)' : ''}
            </Text>
            {quota.windows.map(w => (
              <Text wrap="truncate-end">
                {w.label.padEnd(3)} <Text color={TONE_COLOR[w.tone]}>{quotaBar(w.usedPercent, barWidth)}</Text>{' '}
                {(w.usedPercent + '%').padStart(4)}
                {w.resetsIn !== '' ? '  ↻ ' + w.resetsIn : ''}
              </Text>
            ))}
            {quota.windows.length === 0 && quota.retrievalFailure !== '' && (
              <Text dimColor wrap="wrap">
                quota unavailable: {quota.retrievalFailure}
              </Text>
            )}
          </Box>
        )}
        {current.sections.length === 0 && <Text dimColor>No sessions.</Text>}
        {current.sections.map(section => (
          <Box flexDirection="column" marginBottom={1}>
            <Text bold wrap="truncate-end">
              {section.name} ({section.rows.length})
            </Text>
            {section.rows.map(row)}
          </Box>
        ))}
        <Text dimColor wrap="truncate-end">
          24h {current.dayCost}
          {width >= 40 ? ' · press a session for details' : ''}
        </Text>
      </Box>
    )
  })
}
