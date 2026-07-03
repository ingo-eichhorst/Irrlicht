import { describe, test, expect } from 'vitest'

// #813: the cache-creation regression glyph (#374) was icon-only, requiring a
// hover to learn anything happened. The dashboard must now render an
// always-visible .row-cache-bloat badge with a short visible label (the
// daemon's version attribution, or a compact fallback), plus a longer
// plain-language explanation on hover/title.
//
// Own file so irrlicht.js loads fresh (per-file module isolation) against a
// payload crafted for this case.

const sessionsPayload = {
  groups: [
    {
      name: 'irrlicht',
      agents: [
        {
          session_id: 'attributed',
          state: 'working',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800000,
          metrics: { cache_bloat: true, cache_bloat_tooltip: 'claude-code 2.1.143 +14K cache tokens vs 2.1.98' },
        },
        {
          session_id: 'unattributed',
          state: 'working',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800100,
          metrics: { cache_bloat: true, cache_bloat_tooltip: '' },
        },
        {
          session_id: 'normal',
          state: 'working',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800200,
          metrics: {},
        },
      ],
      costs: {},
    },
  ],
  provider_costs: {},
}

const rowById = (id) =>
  [...document.querySelectorAll('#session-list .session-row')].find((r) => r.dataset.sessionId === id)
const cacheBloatBadge = (id) => rowById(id).querySelector('.row-cache-bloat')
const shown = (el) => el && el.style.display !== 'none'

describe('cache-creation regression badge (#813)', () => {
  test('badge is always visible with a short label and a longer hover explanation', async () => {
    global.fetch = (url) => {
      const u = String(url)
      if (u.includes('/api/v1/sessions')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(sessionsPayload) })
      }
      if (u.includes('/api/v1/agents')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
      }
      return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
    }

    await import('./irrlicht.js')
    await new Promise((r) => setTimeout(r, 0))

    expect(document.querySelectorAll('#session-list .session-row').length).toBe(3)

    // Attributed: visible text is the daemon's version-attribution string,
    // not just an icon; hover explanation folds the attribution in.
    const attributed = cacheBloatBadge('attributed')
    expect(shown(attributed)).toBe(true)
    expect(attributed.textContent).toBe('claude-code 2.1.143 +14K cache tokens vs 2.1.98')
    expect(attributed.title).toContain('claude-code 2.1.143 +14K cache tokens vs 2.1.98')
    expect(attributed.title).toContain('creating prompt-cache tokens well above normal')

    // Unattributed: compact fallback label, not the old generic sentence.
    const unattributed = cacheBloatBadge('unattributed')
    expect(shown(unattributed)).toBe(true)
    expect(unattributed.textContent).toBe('cache ↑')
    expect(unattributed.title).not.toContain('Likely tied to')
    expect(unattributed.title).toContain('creating prompt-cache tokens well above normal')

    // Ordinary session: no badge.
    expect(shown(cacheBloatBadge('normal'))).toBe(false)
  })
})
