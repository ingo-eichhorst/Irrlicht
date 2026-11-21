import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest'
import { compoundSessionId } from './sessionIdentity.js'

// Click-to-focus inside the VS Code extension (#350). The dashboard is framed
// as <iframe src=".../?embed=vscode">, cross-origin to the webview, so the
// dashboard itself POSTs /api/v1/sessions/{id}/focus when a row is clicked.
// Embed mode is read once at module init, so each case sets location and
// re-imports a fresh irrlicht.js.

function payload(agents) {
  return {
    groups: [{ name: 'irrlicht', agents, costs: { day: 0.5 } }],
    provider_costs: {},
  }
}

const localAgent = (id) => ({
  session_id: id, state: 'working', project_name: 'irrlicht', git_branch: 'main',
  adapter: 'claude-code', first_seen: 1764800000,
  metrics: { estimated_cost_usd: 0.12, total_tokens: 2400 },
})

let calls
async function boot(search, agents) {
  history.replaceState(null, '', '/' + search)
  calls = []
  global.fetch = vi.fn((url, init) => {
    const u = String(url)
    calls.push([u, init])
    if (u.includes('/focus')) return Promise.resolve({ ok: true, status: 200 })
    if (u.includes('/api/v1/sessions')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(payload(agents)) })
    }
    if (u.includes('/api/v1/agents')) return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  })
  vi.resetModules()
  await import('./irrlicht.js')
  await new Promise((r) => setTimeout(r, 0))
}
const focusCalls = () => calls.filter(([u]) => u.includes('/focus'))

beforeEach(() => { localStorage.clear() })
afterEach(() => { history.replaceState(null, '', '/') })

describe('embed=vscode click-to-focus (#350)', () => {
  test('click on a row POSTs the focus endpoint once', async () => {
    await boot('?embed=vscode', [localAgent('sess/1')])
    const row = document.querySelector('#session-list .session-row')
    expect(row).not.toBeNull()
    row.click()
    expect(focusCalls()).toHaveLength(1)
    const [url, init] = focusCalls()[0]
    expect(url).toBe('/api/v1/sessions/' + encodeURIComponent('sess/1') + '/focus')
    expect(init).toMatchObject({ method: 'POST' })
  })

  test('click on the cost cell cycles cost mode and does NOT POST focus', async () => {
    await boot('?embed=vscode', [localAgent('sess-2')])
    document.querySelector('#session-list .session-row .row-cost').click()
    expect(focusCalls()).toHaveLength(0)
  })

  test('a rejected or non-2xx focus POST is swallowed with console.debug', async () => {
    await boot('?embed=vscode', [localAgent('sess-3')])
    const dbg = vi.spyOn(console, 'debug').mockImplementation(() => {})
    global.fetch = vi.fn(() => Promise.reject(new Error('boom')))
    document.querySelector('#session-list .session-row').click()
    await new Promise((r) => setTimeout(r, 0))
    expect(dbg.mock.calls.some((c) => String(c[0]).includes('focus'))).toBe(true)
    dbg.mockRestore()
  })

  test('relay-origin row makes no focus call', async () => {
    await boot('?embed=vscode', [localAgent(compoundSessionId('d1', 'sess-4'))])
    const row = document.querySelector('#session-list .session-row')
    expect(row).not.toBeNull()
    row.click()
    expect(focusCalls()).toHaveLength(0)
  })
})

describe('without embed (lock)', () => {
  test('a row click makes no focus request', async () => {
    await boot('', [localAgent('sess-5')])
    const row = document.querySelector('#session-list .session-row')
    expect(row).not.toBeNull()
    row.click()
    expect(focusCalls()).toHaveLength(0)
  })
})
