import { describe, test, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { WEB_DIR, deriveShippedSet } from './shippedFiles.testutil.js'
import { SETUP_BODY } from './vitest.setup.js'

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
