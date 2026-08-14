import { describe, test, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { WEB_DIR, deriveShippedSet } from './shippedFiles.testutil.js'

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

  test('dashboard on a non-push origin: zero register calls, no Beacon section', async () => {
    const register = vi.fn(() => Promise.resolve({}))
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { register, getRegistration: vi.fn(() => Promise.resolve(null)) },
    })
    // Every endpoint answers 404 — the daemon-served (or old-relay) origin.
    global.fetch = () => Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve(null) })

    await import('./irrlicht.js')
    // Let initBeacon's feature-detection fetch settle.
    await new Promise((r) => setTimeout(r, 0))
    await new Promise((r) => setTimeout(r, 0))

    expect(register).not.toHaveBeenCalled()
    const section = document.getElementById('beacon-section')
    expect(section.hidden).toBe(true)
    expect(section.children).toHaveLength(0)
  })
})
