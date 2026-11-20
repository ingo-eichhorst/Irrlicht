import { describe, test, expect, vi } from 'vitest'

// Fire-and-forget promises land in the console, not nowhere (#2147).
//
// SonarCloud's javascript:S9383 flagged the viewer's un-awaited async calls.
// Each now ends in a `.catch` that logs with a label; the recording dropdown's
// two calls share one non-promise-returning wrapper (refreshSelection). These
// cases make the work behind two of those calls reject and assert the label
// reaches console.error. Without the `.catch` the rejection is unhandled: the
// label assertion fails, and vitest also fails the run on the unhandled
// rejection.
//
// Its own file (like viewer.profile.test.js) so vitest gives viewer.js a fresh
// module registry: viewer.js's bootstrap runs once per importing file and
// reads location.hash at import time.

// jsdom implements no scrolling, and viewer.js scrolls the active sidebar
// button into view on every navigation.
if (typeof Element.prototype.scrollIntoView !== 'function') {
  Element.prototype.scrollIntoView = function scrollIntoViewStub() {}
}

const CELL = { agent: 'claudecode', subtree: 'scenarios', id: 'cell' }
const BROKEN = { agent: 'claudecode', subtree: 'scenarios', id: 'broken' }
const CELL_PATH = '/api/scenarios/claudecode/scenarios/cell'
const BROKEN_PATH = '/api/scenarios/claudecode/scenarios/broken'
const NEWEST = '2026-05-02_new'
const OLDER = '2026-05-01_old'

function json(body) {
  return Promise.resolve({
    ok: true,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
    headers: { get: () => null },
  })
}

function unavailable() {
  return Promise.resolve({
    ok: false,
    status: 404,
    json: () => Promise.resolve(null),
    text: () => Promise.resolve('no replay in this fixture'),
    headers: { get: () => null },
  })
}

function installFetchStub() {
  global.fetch = (url) => {
    const path = url.split('?')[0]
    if (path.startsWith('/api/replay/')) return unavailable()
    if (path === '/api/scenarios') return json([CELL, BROKEN])
    if (path === '/api/catalog') return json({ agents: [], scenarios: [] })
    if (path === '/api/recipes') return json({ scenarios: [] })
    if (path === CELL_PATH) {
      return json({ ...CELL, latest_recording: NEWEST, expected: { pass: true, summary: '1/1 phases' }, transitions: [] })
    }
    if (path === `${CELL_PATH}/recordings`) return json([{ name: NEWEST }, { name: OLDER }])
    // The older archive's detail is the fetch the dropdown makes on selection;
    // the cell page itself rendered, so this is the selection failing alone.
    if (path === `${CELL_PATH}/recordings/${OLDER}`) return Promise.reject(new Error('archive detail unreachable'))
    // The whole cell detail failing: loadScenario rejects before it renders.
    if (path === BROKEN_PATH) return Promise.reject(new Error('cell detail unreachable'))
    return json(null)
  }
}

const waitForLabel = (spy, label) => vi.waitFor(() => {
  expect(spy).toHaveBeenCalledWith(label, expect.any(Error))
}, { timeout: 3000, interval: 10 })

describe('a rejected fire-and-forget promise is logged with its label (#2147)', () => {
  test('refreshSelection: a deep link to an older archive whose fetch rejects is logged', async () => {
    // Created inside the test: this vitest clears mock calls between
    // beforeAll and each test, and the import below is what triggers the call.
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      installFetchStub()
      location.hash = `#/recording/claudecode/scenarios/cell/${OLDER}`
      await import('./viewer.js')
      await waitForLabel(errSpy, 'recording selection failed')
    } finally {
      errSpy.mockRestore()
    }
  })

  test('route → loadScenario: a cell whose detail fetch rejects is logged', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      installFetchStub()
      // A no-op when the case above already imported viewer.js; otherwise this
      // bootstraps it, so the case also runs alone under `-t`. Either way the
      // hashchange listener is registered once the import resolves, and it
      // routes the hash set below.
      await import('./viewer.js')
      location.hash = '#/recording/claudecode/scenarios/broken'
      await waitForLabel(errSpy, 'route: loadScenario failed')
    } finally {
      errSpy.mockRestore()
    }
  })
})
