// irrlicht-panel: a read-only Irrlicht panel inside Claude Code.
//
// It polls the Irrlicht daemon's GET /api/v1/sessions (the payload the
// dashboard and the menu bar read) and draws every session the daemon watches,
// across agents, plus the quota of the account this session bills against.
//
// What it never does, by construction rather than by care:
//   - gate anything: it hooks no tool, prompt or permission event, so it
//     cannot slow down or block the agent;
//   - write anything but its own `$.state` (no daemon writes, no settings,
//     no `$.store`, no files);
//   - throw out of a hook: every failure becomes the "not reachable" view.

import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register, Timer } from 'claude-code'

import type { IrrlichtPanelRow, IrrlichtPanelView } from '../types'
import { hostPortOf, resolveDaemonAddress, sessionsURL } from '../lib/address.js'
import { fitRow, quotaBar } from '../lib/format.js'
import { unreachableView, viewFromResponse } from '../lib/view.js'

const PANE = 'irrlicht'
const TITLE = 'Irrlicht'
const POLL_MS = 3000

// `$.http.fetch` has no timeout or abort of its own (HttpInit carries neither
// in this API), so a daemon that accepts a connection and never answers would
// hold one request forever. A request older than this is abandoned: the view
// says so, its late answer is ignored, and the next tick asks again.
const STALL_MS = 10_000

const view = atom({ plugin: 'irrlicht-panel', key: 'view' } as const, null)
const expanded = atom({ plugin: 'irrlicht-panel', key: 'expanded' } as const, [])

type Address = Awaited<ReturnType<typeof resolveDaemonAddress>>

// Module state for the poller alone. Nothing drawn reads it (drawing reads
// `$.state`), and a hot reload, which drops it, also drops the timer it serves.
let timer: Timer | undefined
// The resolved address; cleared after any failed poll so the next one walks
// the ladder again (a daemon restarted on another port is found again).
let address: Address | undefined
let inFlight: { generation: number; since: number } | undefined
let generation = 0
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
    if (inFlight && now - inFlight.since < STALL_MS) return
    if (inFlight) {
      const stalled = address
      inFlight = undefined
      address = undefined
      if (stalled) await publish($, unreachableView(hostPortOf(stalled), `no answer in ${STALL_MS / 1000} s`))
    }
    const mine = ++generation
    inFlight = { generation: mine, since: now }
    try {
      const target = address ?? (address = await resolveDaemonAddress(addressIO($)))
      let response: Awaited<ReturnType<EngineInterface['http']['fetch']>> | undefined
      let error: unknown
      try {
        response = await $.http.fetch(sessionsURL(target))
      } catch (err) {
        error = err ?? 'rejected'
      }
      if (mine !== generation) return // abandoned as stalled meanwhile
      // Read per poll, never cached: after a /clear the process goes on under
      // a new session id, and no session.start follows to say so.
      const hereSessionId = await $.session.id().catch(() => undefined)
      const next = viewFromResponse({
        response, error, hostPort: hostPortOf(target), hereSessionId, nowMs: await $.clock.now(),
      })
      if (next.status !== 'ok') address = undefined
      await publish($, next)
    } finally {
      if (inFlight?.generation === mine) inFlight = undefined
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
    await $.command.register({
      name: 'irrlicht',
      description: 'Open the Irrlicht panel: every agent session Irrlicht watches, and your quota',
    })
    timer?.cancel()
    void poll($)
    timer = $.clock.every(POLL_MS, () => {
      void poll($)
    })
    return next(e)
  })

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
  // per load; the engine still holds an unasked pane back below 144 columns.
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
            {current.reason}; trying again every {POLL_MS / 1000} s.
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
              {section.name} ({section.count})
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
