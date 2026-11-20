import { describe, test, expect, beforeAll, afterAll, afterEach, vi } from 'vitest'

// Fire-and-forget promises land in the console, not nowhere (#2147). These
// cases drive three fire-and-forget wrappers (historyTab.js fetchHistory,
// permissionsWizard.js refreshPermissionsInBackground, irrlicht.js
// renderElfdansPanel) through their real entry points and make the work
// behind them fail.
//
// Each failure is a REJECTION, not a swallowed fetch error: every callee here
// already catches its own fetch and resolves null, so the case breaks the step
// AFTER the fetch (a render, or initElfdans itself). That is the path the
// wrappers exist for. Without the `.catch` the label never reaches
// console.error, which is what each case asserts. For the history and
// permissions cases vitest additionally reports the rejection as unhandled and
// fails the run. The Elfdans case gets only the assertion: its rejection comes
// from a vi.fn, and in a run against the unfixed code vitest reported no
// unhandled rejection for it, so the label is the whole signal there.

const mocks = vi.hoisted(() => ({
  initElfdans: vi.fn(async () => { throw new Error('initElfdans exploded') }),
}))

// elfdans.js's own wiring has its own suites; here only initElfdans is
// replaced, so the dashboard's render of the Elfdans panel can be made to fail.
vi.mock('./elfdans.js', async (importOriginal) => ({ ...(await importOriginal()), ...mocks }))

let errSpy
let irr
let historyTab
let realFetch

const okJson = (body) => Promise.resolve({ ok: true, json: () => Promise.resolve(body) })

beforeAll(async () => {
  realFetch = globalThis.fetch
  // Installed before the import so the panel render irrlicht.js performs at
  // module load (which rejects too, with the mock above) stays out of the test
  // output. The calls recorded here do not reach the tests: this vitest clears
  // mock calls between beforeAll and each test (checked: a spy that saw one
  // call in beforeAll reported zero inside the test), so every case below
  // triggers its own failure.
  errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  // The Settings → Sources token input is not in vitest.setup.js's scaffold,
  // and its change handler is attached while irrlicht.js is imported, so it
  // has to exist before the import.
  document.body.insertAdjacentHTML('beforeend', '<input id="t-relay-token" type="password" data-setting="relayToken">')
  irr = await import('./irrlicht.js')
  historyTab = await import('./historyTab.js')
})

afterEach(() => { globalThis.fetch = realFetch })
afterAll(() => { errSpy.mockRestore() })

describe('a rejected fire-and-forget promise is logged with its label (#2147)', () => {
  test('renderElfdansPanel: initElfdans rejecting is logged, not left unhandled', async () => {
    // Entering a relay token re-renders the Elfdans panel.
    const input = document.getElementById('t-relay-token')
    input.value = 'a-client-token'
    input.dispatchEvent(new Event('change'))
    expect(mocks.initElfdans).toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(errSpy).toHaveBeenCalledWith('irrlicht: elfdans panel render failed', expect.any(Error))
    }, { timeout: 2000, interval: 10 })
  })

  test('fetchHistory: a render step that throws after the fetch is logged', async () => {
    const host = document.createElement('div')
    host.innerHTML = '<input type="checkbox" id="history-forecast">'
    document.body.append(host)
    try {
      historyTab.initHistoryTab()
      // `top_contributors` is truthy but not iterable, so loadHistory's
      // post-fetch `.then` throws a TypeError while growing the project
      // vocabulary: the fetch itself succeeded, the step after it did not.
      globalThis.fetch = () => okJson({ group: 'project', top_contributors: 7 })
      const box = document.getElementById('history-forecast')
      box.checked = true
      box.dispatchEvent(new Event('change'))
      await vi.waitFor(() => {
        expect(errSpy).toHaveBeenCalledWith('irrlicht: history refresh failed', expect.any(TypeError))
      }, { timeout: 2000, interval: 10 })
    } finally {
      host.remove()
    }
  })

  test('refreshPermissionsInBackground: a banner render that throws is logged', async () => {
    const banner = document.createElement('div')
    banner.id = 'permission-apply-banner'
    banner.hidden = true
    document.body.append(banner)
    try {
      // A null entry in unapplied_grants makes renderUnappliedGrantsBanner
      // read `.agent` off null — again after a fetch that succeeded.
      globalThis.fetch = () => okJson({ mode: 'ask', agents: [], unapplied_grants: [null] })
      // The daemon's "consent changed" push: irrlicht.js answers it with a
      // background permissions refresh.
      irr.dispatchRawFrame({ type: 'permissions_updated' })
      await vi.waitFor(() => {
        expect(errSpy).toHaveBeenCalledWith('irrlicht: permissions refresh failed', expect.any(TypeError))
      }, { timeout: 2000, interval: 10 })
    } finally {
      banner.remove()
    }
  })
})
