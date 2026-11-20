import { describe, test, expect } from 'vitest'
import { json, notOk } from './viewerFetch.testutil.js'

// The recordings endpoint 500s a cell whose recording manifests it cannot
// read (an unparseable manifest, or one naming an unknown execution profile).
// Before #1889 the SPA swallowed any such failure into an empty array, which
// rendered as "No recordings yet." — an inability to look and an absence of
// recordings producing the same output. This file pins that they now differ.
//
// Its own file so viewer.js gets a fresh module registry: the bootstrap runs
// once per importing file.


const CELL = { agent: 'claudecode', subtree: 'scenarios', id: 'broken-cell' }
const CELL_PATH = '/api/scenarios/claudecode/scenarios/broken-cell'
const FAILURE = 'cannot read recording manifests: unknown execution profile "desktop"'

const serverError = (text) => notOk(500, text)

async function waitFor(selector, timeoutMs = 3000) {
  const start = Date.now()
  for (;;) {
    const el = document.querySelector(selector)
    if (el) return el
    if (Date.now() - start > timeoutMs) {
      throw new Error(`${selector} did not render within ${Date.now() - start}ms`)
    }
    await new Promise(r => setTimeout(r, 10))
  }
}

describe('an unreadable recording history is reported, not rendered as "none"', () => {
  test('the selector panel carries the server failure verbatim', async () => {
    global.fetch = (url) => {
      const path = url.split('?')[0]
      if (path.startsWith('/api/replay/')) return serverError('no replay in this fixture')
      if (path === '/api/scenarios') return json([CELL])
      if (path === '/api/catalog') return json({ agents: [], scenarios: [] })
      if (path === '/api/recipes') return json({ scenarios: [] })
      if (path === `${CELL_PATH}/recordings`) return serverError(FAILURE)
      if (path === CELL_PATH) {
        return json({ ...CELL, execution_profile: 'cli-local', degraded: true, transitions: [] })
      }
      return json(null)
    }
    location.hash = '#/recording/claudecode/scenarios/broken-cell'
    await import('./viewer.js')

    const banner = await waitFor('[data-testid=recordings-error]')
    expect(banner.textContent).toContain('Recording history unavailable')
    expect(banner.textContent).toContain('unknown execution profile')
    expect(banner.textContent).toContain('500')
  })
})
