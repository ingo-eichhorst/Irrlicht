import { describe, test, expect, vi } from 'vitest'
import { json, notOk } from './viewerFetch.testutil.js'

// Fire-and-forget promises land in the console, not nowhere (#2147).
//
// SonarCloud's javascript:S9383 flagged the viewer's un-awaited async calls;
// they now go through logRejection, which logs a rejection under a label.
// These cases make two of those calls reject and assert the label reaches
// console.error. Without the `.catch` the label assertion fails, and vitest
// also fails the run on the unhandled rejection.
//
// Its own file (like viewer.profile.test.js) so vitest gives viewer.js a fresh
// module registry: viewer.js's bootstrap runs once per importing file and
// reads location.hash at import time.


const CELL = { agent: 'claudecode', subtree: 'scenarios', id: 'cell' }
const BROKEN = { agent: 'claudecode', subtree: 'scenarios', id: 'broken' }
const CELL_PATH = '/api/scenarios/claudecode/scenarios/cell'
const BROKEN_PATH = '/api/scenarios/claudecode/scenarios/broken'
const NEWEST = '2026-05-02_new'
const OLDER = '2026-05-01_old'

// What each fetch path answers. Anything unlisted answers 200 with null.
const RESPONSES = {
  '/api/scenarios': () => json([CELL, BROKEN]),
  '/api/catalog': () => json({ agents: [], scenarios: [] }),
  '/api/recipes': () => json({ scenarios: [] }),
  [CELL_PATH]: () => json({ ...CELL, latest_recording: NEWEST, expected: { pass: true, summary: '1/1 phases' }, transitions: [] }),
  [`${CELL_PATH}/recordings`]: () => json([{ name: NEWEST }, { name: OLDER }]),
  // The older archive's detail is the fetch the dropdown makes on selection;
  // the cell page itself rendered, so this is the selection failing alone.
  [`${CELL_PATH}/recordings/${OLDER}`]: () => Promise.reject(new Error('archive detail unreachable')),
  // The whole cell detail failing: loadScenario rejects before it renders.
  [BROKEN_PATH]: () => Promise.reject(new Error('cell detail unreachable')),
}

function installFetchStub() {
  global.fetch = (url) => {
    const path = url.split('?')[0]
    // The Playback panel auto-starts a replay; this fixture has none to play.
    if (path.startsWith('/api/replay/')) return notOk(404, 'no replay in this fixture')
    const answer = RESPONSES[path]
    return answer ? answer() : json(null)
  }
}

const waitForLabel = (spy, label) => vi.waitFor(() => {
  expect(spy).toHaveBeenCalledWith(label, expect.any(Error))
}, { timeout: 3000, interval: 10 })

describe('a rejected fire-and-forget promise is logged with its label (#2147)', () => {
  test('selectionChanged: a deep link to an older archive whose fetch rejects is logged', async () => {
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
