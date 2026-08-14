import { describe, test, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { WEB_DIR, deriveShippedSet } from './shippedFiles.testutil.js'
import { SETUP_BODY } from './vitest.setup.js'
import { BEACON_MESSAGES } from './beacon.js'

// The §5.2 additivity-contract tripwire (docs/mobile-notifications-arc42.md
// §5.2, §8.7). platforms/web is one shared surface served by the localhost
// daemon AND by relays, so "the Beacon feature changes nothing for plain
// dashboard usage" is enforced, not intended:
//   (a) sw.js handles push + notificationclick ONLY — a handler for the fetch
//       event could interpose on asset loading and cache the dashboard stale.
//   (b) the service worker is registered lazily from the pairing flow only,
//       so exactly one shipped module (beacon.js) may contain the
//       registration call.
//   (c) behaviorally: a dashboard whose origin 404s /api/v1/push/info makes
//       zero registration calls and renders no Beacon section.

const swSource = readFileSync(join(WEB_DIR, 'sw.js'), 'utf8')

describe('service worker contract (arc42 §5.2)', () => {
  test('sw.js registers no fetch handler', () => {
    expect(swSource).not.toMatch(/addEventListener\(\s*['"`]fetch['"`]/)
    expect(swSource).not.toMatch(/\bonfetch\b/)
  })

  test('sw.js does register push and notificationclick (vacuity guard)', () => {
    // The positive arm: if these stopped matching, the negative arm above
    // would be reading a file that no longer expresses handlers this way.
    expect(swSource).toMatch(/addEventListener\(\s*['"`]push['"`]/)
    expect(swSource).toMatch(/addEventListener\(\s*['"`]notificationclick['"`]/)
  })

  test('exactly one shipped module contains the service-worker registration call: beacon.js', () => {
    const { files } = deriveShippedSet()
    const registering = [...files]
      .filter((f) => f.endsWith('.js'))
      .filter((f) => /serviceWorker\s*\.\s*register\s*\(/.test(readFileSync(join(WEB_DIR, f), 'utf8')))
      .sort()
    expect(registering).toEqual(['beacon.js'])
  })

  // The two copies of the page↔worker vocabulary (arc42 §8.5). sw.js is a
  // classic script with nothing to import from — its header says why — so it
  // spells as literals what beacon.js exports. A rename on one side would
  // leave the other silently unheard: the app would publish live sessions
  // nobody folds, and a tap would postMessage a type nobody listens for, both
  // answering exactly like a healthy install. This is the third §5.2-shaped
  // tripwire and lives here for the same reason as the other two.
  //
  // Read off the SOURCE rather than by executing sw.js: the point is that the
  // literals in that file match, and a check that ran the file could only
  // observe values it had already been handed.
  function workerMessageLiterals(source) {
    return [...source.matchAll(/^const (?:MSG_[A-Z_]+|SESSION_HASH_KEY) = '([^']+)';$/gm)]
      .map((m) => m[1])
      .sort()
  }

  test('sw.js spells exactly the message names beacon.js exports', () => {
    expect(workerMessageLiterals(swSource)).toEqual(Object.values(BEACON_MESSAGES).sort())
  })

  test('and reports the opposite for a worker whose copy drifted', () => {
    // Committed mutation evidence (AGENTS.md "Testing"): the arm above passes
    // by construction against a correct pair, so it is also driven against a
    // worker with one name renamed — the shape of the drift it exists to
    // catch. A no-op edit throws rather than reading as a pass.
    const drifted = swSource.replace("const MSG_LIVE_SESSIONS = 'beacon-live-sessions';", "const MSG_LIVE_SESSIONS = 'beacon-live-session';")
    expect(drifted, 'stale mutation: sw.js no longer declares MSG_LIVE_SESSIONS this way').not.toBe(swSource)
    expect(workerMessageLiterals(drifted)).not.toEqual(Object.values(BEACON_MESSAGES).sort())
  })

  test('a worker that stopped declaring them at all fails loudly, not quietly', () => {
    // The regex is the weak point: a spelling it cannot see would return a
    // short list, and a short list must never read as agreement.
    expect(workerMessageLiterals('// no constants here')).toEqual([])
    expect(Object.values(BEACON_MESSAGES)).toHaveLength(4)
  })

  // Boots the dashboard the way index.html does: a fresh module registry, so
  // each arm re-runs irrlicht.js's top-level wiring instead of inspecting the
  // module the previous arm already executed.
  async function bootDashboard(routes) {
    vi.resetModules()
    localStorage.clear()
    document.body.innerHTML = SETUP_BODY
    const register = vi.fn(() => Promise.resolve({}))
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        register,
        getRegistration: vi.fn(() => Promise.resolve(null)),
        ready: new Promise(() => {}),
      },
    })
    global.fetch = (url) => {
      const route = routes[String(url)]
      if (!route) return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve(null) })
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(route) })
    }
    await import('./irrlicht.js')
    // Let initBeacon's feature-detection fetch and its render settle.
    for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0))
    return { register, section: document.getElementById('beacon-section') }
  }

  test('dashboard on a non-push origin: zero register calls, no Beacon section', async () => {
    // Every endpoint answers 404 — the daemon-served (or old-relay) origin.
    const { register, section } = await bootDashboard({})
    expect(register).not.toHaveBeenCalled()
    expect(section.hidden).toBe(true)
    expect(section.children).toHaveLength(0)
  })

  test('push-capable origin, phone not paired: the section renders and STILL no worker is installed', async () => {
    // The other half of §5.2's "registered lazily from the pairing flow only".
    // The arm above is satisfied by an initBeacon nobody calls; this one is
    // not — a section that renders is proof irrlicht.js reaches the module —
    // and it is the state most users' phones sit in before they ever pair.
    const { register, section } = await bootDashboard({
      'api/v1/push/info': { enabled: true, vapid_public_key: 'AQID' },
    })
    expect(section.hidden).toBe(false)
    expect(section.querySelector('h2').textContent).toBe('Irrlicht Beacon')
    expect(document.getElementById('beacon-code-input'), 'no pairing entry rendered').toBeTruthy()
    expect(register, 'a service worker was installed by merely opening the dashboard').not.toHaveBeenCalled()
  })
})
