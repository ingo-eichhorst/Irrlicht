// The mod against the engine, under `claude plugin test .`: its hooks loaded
// from this folder as a session loads them, and everything beneath them (the
// daemon's HTTP answer, the environment, the addr file, the clock, this
// session's id) answered by the hooks each test registers. Every UI body runs
// on both the terminal and the desktop surface, so the panel is shown not to
// depend on one.
//
// The payload is the same synthetic fixture the vitest suite shapes
// (lib/testdata/sessions.fixture.js); no real daemon answer is used.

import { expect, mock, test } from 'claude-code/testing'
import type { ElementQuery, Mounted } from 'claude-code/testing'
import type { On } from 'claude-code'

import { HERE_ID, NOW_MS, SESSIONS_PAYLOAD } from '../lib/testdata/sessions.fixture.js'

const PLUGIN = 'irrlicht-panel'
const PANE = 'irrlicht'
const SURFACES = ['terminal', 'desktop'] as const
const PANE_PROPS = {
  title: 'Irrlicht',
  isFocused: false,
  bodyColumns: 60,
  placement: 'dock' as const,
  scroll: { offset: 0, bodyRows: 40 },
  view: {},
}
const BAND_PROPS = {
  hasSurvey: false,
  isCollapsed: false,
  isWorking: false,
  maxRows: 10,
  bodyColumns: 150,
  scroll: { offset: 0, bodyRows: 10 },
  view: {},
}

type Answer = { status: number; ok: boolean; text: string }
type World = {
  env?: Record<string, string>
  /** Refuse `$.command.register`, as another mod or an organization's guard may. */
  denyRegister?: boolean
  hereId?: string
  paneOpen?: boolean
  files?: Record<string, string>
  answer?: (url: string) => Answer | Promise<Answer> | 'refuse'
}

const okAnswer = (): Answer => ({ status: 200, ok: true, text: JSON.stringify(SESSIONS_PAYLOAD) })

// world answers everything the plugin asks beneath it, and records the URLs
// it fetched and the panes it opened, so a test asserts on what happened
// rather than on what did not show.
function world(on: On, w: World = {}) {
  const fetched: string[] = []
  const opened: string[] = []
  const clock = mock.clock(on, { now: NOW_MS })
  mock.env(on, w.env ?? { HOME: '/home/test' })
  on('session.start', (_$, e) => ({ cwd: e.cwd }))
  on('session.end', (_$, e) => ({ sessionId: e.sessionId }))
  on('command.register', (_$, e) =>
    w.denyRegister ? { deny: 'command registration refused by policy' } : { value: { command: e.name } })
  on('session.id', () => ({ value: w.hereId ?? HERE_ID }))
  on('ui.panes', () => ({
    value: w.paneOpen === false ? [] : [{ id: PANE, title: 'Irrlicht', isShown: true, isFocused: false, isPlaced: true }],
  }))
  on('ui.open', (_$, e) => {
    opened.push(e.id)
    return { value: { isPlaced: true as const } }
  })
  on('fs.stat', (_$, e) => {
    if (w.files && Object.hasOwn(w.files, e.path)) return { value: { kind: 'file' as const, size: 20, mtimeMs: 0, isLink: false } }
    return { deny: 'ENOENT: no such file' }
  })
  on('fs.read', (_$, e) => {
    if (w.files && Object.hasOwn(w.files, e.path)) return { value: w.files[e.path] as string }
    return { deny: 'ENOENT: no such file' }
  })
  on('http.fetch', async (_$, e) => {
    fetched.push(e.url)
    const a = await (w.answer ?? okAnswer)(e.url)
    if (a === 'refuse') return { deny: 'connection refused' }
    return { value: { ...a, headers: {} } }
  })
  return { clock, fetched, opened }
}

// The reads and the press the shared assertions below make on a mounted Pane.
type Drawing = Pick<Mounted, 'find' | 'press'>

// textOf answers the text of the first element matching `query`, or
// undefined when none matches (which then fails the assertion it feeds).
async function textOf(ui: Drawing, query: ElementQuery): Promise<string | undefined> {
  return (await ui.find(query))?.text
}

// The quota of the here session's own account, its two windows as bars.
async function expectHereQuota(ui: Drawing) {
  expect(await ui.find({ type: 'Text', text: 'Claude Max · …0001' })).toBeDefined()
  expect(await textOf(ui, { type: 'Text', text: /^5h / })).toContain('11%')
  expect(await textOf(ui, { type: 'Text', text: /^7d / })).toContain('83%')
}

// Grouped by project, nested groups under their parent, all adapters.
async function expectSessionRows(ui: Drawing) {
  for (const heading of ['orchard (3)', 'lighthouse (1)', 'fleet / rig-a (1)']) {
    expect(await ui.find({ type: 'Text', text: heading }), heading).toBeDefined()
  }
  const here = await ui.find({ key: 'row:' + HERE_ID })
  expect(here?.type).toBe('Button')
  expect(here?.text).toContain('main 177K 17.7% opus-5.5 ← here')
  expect(await textOf(ui, { key: 'row:sess-pi-0004' })).toContain('[pi]')
  expect(await textOf(ui, { key: 'row:sess-child-0002' })).toContain('↳')
  expect(await ui.find({ type: 'Text', text: /24h \$16\.50/ })).toBeDefined()
}

// Press: the detail block appears; press again: it goes.
async function expectRowPressTogglesDetail(ui: Drawing) {
  expect(await ui.find({ type: 'Text', text: /Refactored the loader/ })).toBeUndefined()
  await ui.press({ key: 'row:' + HERE_ID })
  expect(await textOf(ui, { type: 'Text', text: /Refactored the loader/ }))
    .toBe('› Refactored the loader into two passes.')
  expect(await textOf(ui, { type: 'Text', text: /^working · / })).toBe('working · 10m5s · $1.23')
  await ui.press({ key: 'row:sess-other-0003' })
  expect(await textOf(ui, { type: 'Text', text: /staging database/ }))
    .toBe('? Apply the migration to the staging database?')
  await ui.press({ key: 'row:' + HERE_ID })
  await ui.press({ key: 'row:sess-other-0003' })
  expect(await ui.find({ type: 'Text', text: /Refactored the loader/ })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: /staging database/ })).toBeUndefined()
}

test('sessions render on both surfaces, and a row press shows its detail', async ($, on) => {
  const { clock, fetched } = world(on)
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  expect(fetched[0]).toBe('http://localhost:7837/api/v1/sessions')

  for (const surface of SURFACES) {
    const ui = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', requestId: PANE, props: PANE_PROPS })
    await expectHereQuota(ui)
    await expectSessionRows(ui)
    await expectRowPressTogglesDetail(ui)
    await ui.unmount()
  }
})

test('"← here" is drawn only on an exact session-id match', async ($, on) => {
  // The id is read per poll, so one world with a changing id serves; that
  // is also what a /clear looks like to the panel.
  const w: World = { hereId: HERE_ID }
  const { clock } = world(on, w)
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  const cases: ReadonlyArray<readonly [string, readonly string[]]> = [
    [HERE_ID, [HERE_ID]],
    ['sess-pi-0004', ['sess-pi-0004']],
    ['sess-here', []],
    ['not-listed-by-the-daemon', []],
  ]
  for (const [hereId, expected] of cases) {
    w.hereId = hereId
    await clock.advance(3000)
    for (const surface of SURFACES) {
      const ui = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', requestId: PANE, props: PANE_PROPS })
      const rows = await ui.findAll({ type: 'Button' })
      expect(rows.length, 'rows drawn').toBe(5)
      const marked = rows.filter(r => r.text.includes('← here')).map(r => r.key)
      expect(marked, `${surface}, here = ${hereId}`).toEqual(expected.map(id => 'row:' + id))
      await ui.unmount()
    }
  }
})

test('every failure draws "not reachable at <host:port>", on both surfaces', async ($, on) => {
  let answer: World['answer'] = () => 'refuse'
  const { clock } = world(on, { answer: url => answer!(url) })
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()

  const outcomes: ReadonlyArray<readonly [string, World['answer'], RegExp]> = [
    ['refused', () => 'refuse', /no connection/],
    ['HTTP 503', () => ({ status: 503, ok: false, text: 'unavailable' }), /HTTP 503/],
    ['not JSON', () => ({ status: 200, ok: true, text: '<html>captive portal</html>' }), /not a JSON answer/],
  ]
  for (const [name, next, reason] of outcomes) {
    answer = next
    await clock.advance(3000)
    for (const surface of SURFACES) {
      const ui = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', requestId: PANE, props: PANE_PROPS })
      expect(await ui.find({ type: 'Text', text: 'Irrlicht daemon not reachable at localhost:7837' }), name).toBeDefined()
      expect((await ui.find({ type: 'Text', text: reason }))?.text, name).toMatch(/; trying again every 3 s\.$/)
      expect(await ui.findAll({ type: 'Button' }), name).toHaveLength(0)
      await ui.unmount()
    }
  }

  // And back: the next good answer replaces the not-reachable view.
  answer = okAnswer
  await clock.advance(3000)
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'Pane', requestId: PANE, props: PANE_PROPS })
  expect(await ui.find({ type: 'Text', text: 'orchard (3)' })).toBeDefined()
  await ui.unmount()
})

test('the address follows the client ladder and is re-resolved after a failure', async ($, on) => {
  const addrFile = '/home/test/.local/share/irrlicht/irrlichd.addr'
  const w: World = { env: { HOME: '/home/test' }, files: { [addrFile]: '127.0.0.1:7901\n' }, answer: () => 'refuse' }
  const { clock, fetched } = world(on, w)
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  expect(fetched).toEqual(['http://localhost:7901/api/v1/sessions'])

  // The daemon came back on another port and republished its address: the
  // failed poll dropped the resolved address, so the next one reads the file
  // again.
  w.files = { [addrFile]: '127.0.0.1:7902\n' }
  w.answer = okAnswer
  await clock.advance(3000)
  expect(fetched).toEqual(['http://localhost:7901/api/v1/sessions', 'http://localhost:7902/api/v1/sessions'])

  // A good answer keeps the address: the file is not consulted per poll.
  w.files = { [addrFile]: '127.0.0.1:7903\n' }
  await clock.advance(3000)
  expect(fetched.at(-1)).toBe('http://localhost:7902/api/v1/sessions')
})

test('IRRLICHT_BIND_ADDR with a fixed port wins over the addr file', async ($, on) => {
  const addrFile = '/home/test/.local/share/irrlicht/irrlichd.addr'
  const { clock, fetched } = world(on, {
    env: { HOME: '/home/test', IRRLICHT_BIND_ADDR: '0.0.0.0:7838' },
    files: { [addrFile]: '127.0.0.1:7901\n' },
  })
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  expect(fetched).toEqual(['http://localhost:7838/api/v1/sessions'])
})

test('no poll reaches the daemon while the pane is not shown', async ($, on) => {
  const { clock, fetched } = world(on, { paneOpen: false })
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.advance(9000)
  expect(fetched).toEqual([])
})

test('/irrlicht opens the pane and polls at once', async ($, on) => {
  const { clock, fetched, opened } = world(on)
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  const before = fetched.length
  const ran = await $.command.run({
    command: 'irrlicht', args: '', origin: { kind: 'composer' }, presentation: { isFullscreen: false, columns: 80 },
  })
  await clock.settle()
  expect(ran.text).toBe('Irrlicht panel opened.')
  expect(opened).toEqual([PANE])
  expect(fetched.length).toBe(before + 1)
})

test('unasked, the pane opens only where the surface docks it, once', async ($, on) => {
  const { clock, opened } = world(on)
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  expect(opened).toEqual([]) // session.start alone opens nothing

  for (const surface of SURFACES) {
    // The main screen (and a surface that has not said): no unasked pane.
    for (const viewport of [{ columns: 160, rows: 50, isFullscreen: false }, { columns: 160, rows: 50 }]) {
      const band = await $.ui.mount({ plugin: PLUGIN, surface, component: 'AbovePrompt', props: BAND_PROPS, viewport })
      expect(await band.keepsBeneath()).toBe(true)
      await band.unmount()
    }
  }
  expect(opened).toEqual([])

  for (const surface of SURFACES) {
    const band = await $.ui.mount({
      plugin: PLUGIN, surface, component: 'AbovePrompt', props: BAND_PROPS,
      viewport: { columns: 160, rows: 50, isFullscreen: true },
    })
    expect(await band.keepsBeneath()).toBe(true)
    await band.unmount()
  }
  await clock.settle()
  expect(opened).toEqual([PANE]) // one try, not one per drawing
})

// Red-first for the review request to turn the auto-open off: with the
// manifest's `autoOpen` field declared and set to false, the module opened the
// pane all the same on the first full-screen render. The default (the test
// above, which passes no options) is its lock: unset, the pane still opens.
test('with autoOpen off, no pane opens unasked, and /irrlicht still opens it', { options: { autoOpen: false } }, async ($, on) => {
  const { clock, opened } = world(on)
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  for (const surface of SURFACES) {
    const band = await $.ui.mount({
      plugin: PLUGIN, surface, component: 'AbovePrompt', props: BAND_PROPS,
      viewport: { columns: 160, rows: 50, isFullscreen: true },
    })
    expect(await band.keepsBeneath()).toBe(true)
    await band.unmount()
  }
  await clock.settle()
  expect(opened, 'panes opened unasked').toEqual([])

  const ran = await $.command.run({
    command: 'irrlicht', args: '', origin: { kind: 'composer' }, presentation: { isFullscreen: true, columns: 160 },
  })
  expect(ran.text).toBe('Irrlicht panel opened.')
  expect(opened).toEqual([PANE])
})

// Red-first for the review finding that abandoned requests piled up: an
// unanswered `$.http.fetch` cannot be aborted (HttpInit has no signal), so
// starting a new request every 10 s left one more open connection on the
// daemon each time (11 after two minutes). One request at a time, always.
test('one request at a time: an unanswered one is reported, never piled on', async ($, on) => {
  let release: () => void = () => undefined
  const gate = new Promise<void>(resolve => {
    release = resolve
  })
  let isFirst = true
  const { clock, fetched } = world(on, {
    answer: async () => {
      if (isFirst) {
        isFirst = false
        await gate
      }
      return okAnswer()
    },
  })
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  await clock.advance(120_000) // two minutes, forty ticks, the first request still open
  expect(fetched, 'requests issued while the first never answered').toHaveLength(1)
  for (const surface of SURFACES) {
    const ui = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', requestId: PANE, props: PANE_PROPS })
    expect(await ui.find({ type: 'Text', text: 'Irrlicht daemon not reachable at localhost:7837' })).toBeDefined()
    // It waits for the outstanding request, so it must not claim a retry
    // every 3 s, which it no longer does while one is open.
    expect((await ui.find({ type: 'Text', text: /no answer in 10 s/ }))?.text)
      .toBe('no answer in 10 s; waiting for it to end.')
    expect(await ui.find({ type: 'Text', text: /trying again/ })).toBeUndefined()
    await ui.unmount()
  }

  // The request settles at last (a restarted daemon drops the connection, or
  // answers): its answer is drawn, and polling resumes at the usual pace.
  release()
  await clock.settle()
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'Pane', requestId: PANE, props: PANE_PROPS })
  expect(await ui.find({ type: 'Text', text: 'orchard (3)' })).toBeDefined()
  await ui.unmount()
  await clock.advance(3000)
  expect(fetched).toHaveLength(2)
})

// Red-first for the review finding that session.start awaited
// `$.command.register` before starting the timer: a registration another mod
// or an organization's guard refuses made the hook throw, so the panel never
// polled and sat on "Asking the Irrlicht daemon…" for good.
test('a refused /irrlicht registration still polls and keeps the view current', async ($, on) => {
  let answer: () => Answer = okAnswer
  const { clock, fetched } = world(on, { denyRegister: true, answer: () => answer() })
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  expect(fetched, 'the first poll').toHaveLength(1)
  for (const surface of SURFACES) {
    const ui = await $.ui.mount({ plugin: PLUGIN, surface, component: 'Pane', requestId: PANE, props: PANE_PROPS })
    expect(await ui.find({ type: 'Text', text: 'orchard (3)' })).toBeDefined()
    await ui.unmount()
  }

  answer = () => ({ status: 503, ok: false, text: 'unavailable' })
  await clock.advance(3000)
  expect(fetched, 'the timer runs').toHaveLength(2)
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'Pane', requestId: PANE, props: PANE_PROPS })
  expect(await ui.find({ type: 'Text', text: /HTTP 503/ })).toBeDefined()
  await ui.unmount()
})

test('a /clear keeps polling; an exit stops it', async ($, on) => {
  const { clock, fetched } = world(on)
  await $.session.start({ cwd: '/home/test/src/orchard', surface: 'terminal', isInteractive: true })
  await clock.settle()
  await $.session.end({ reason: 'clear', sessionId: HERE_ID, resume: { id: HERE_ID } })
  await clock.advance(3000)
  const afterClear = fetched.length
  expect(afterClear).toBe(2)
  await $.session.end({ reason: 'prompt_input_exit', sessionId: HERE_ID, resume: { id: HERE_ID } })
  await clock.advance(9000)
  expect(fetched.length).toBe(afterClear)
})
