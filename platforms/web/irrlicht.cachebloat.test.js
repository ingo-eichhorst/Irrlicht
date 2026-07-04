import { describe, test, expect } from 'vitest'

// #813: the cache-creation regression glyph (#374) was icon-only, requiring a
// hover to learn anything happened. The dashboard must now render an
// always-visible .row-cache-bloat badge with a short visible label (the
// daemon's version attribution, or a compact fallback), plus a longer
// plain-language explanation on hover/title. The badge lives in its own
// .row-cache-bloat-row beneath the parent (not inline in the session row's
// fixed-width icon slots) since the attribution string can be a full
// sentence — inline would squeeze .row-branch/.row-model on that row only.
//
// #827: the hover explanation is composed daemon-side (cache_bloat_explanation)
// and rendered verbatim, so the fixture supplies it exactly as the detector
// would rather than letting the client re-derive it from the tooltip.
//
// Own file so irrlicht.js loads fresh (per-file module isolation) against a
// payload crafted for this case.

const attributedExplanation =
  "This session is creating prompt-cache tokens well above normal for this project — it's getting less benefit from caching and costing more per turn." +
  ' Likely tied to claude-code 2.1.143 +14K cache tokens vs 2.1.98.' +
  ' Common causes: an agent update that changed context construction, large or varying pasted content each turn, or frequent context resets (e.g. /clear).'

const unattributedExplanation =
  "This session is creating prompt-cache tokens well above normal for this project — it's getting less benefit from caching and costing more per turn." +
  ' Common causes: an agent update that changed context construction, large or varying pasted content each turn, or frequent context resets (e.g. /clear).'

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
          metrics: {
            cache_bloat: true,
            cache_bloat_tooltip: 'claude-code 2.1.143 +14K cache tokens vs 2.1.98',
            cache_bloat_explanation: attributedExplanation,
          },
        },
        {
          session_id: 'unattributed',
          state: 'working',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800100,
          metrics: { cache_bloat: true, cache_bloat_tooltip: '', cache_bloat_explanation: unattributedExplanation },
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
const cacheBloatRow = (id) =>
  [...document.querySelectorAll('#session-list .row-cache-bloat-row')].find((r) => r.dataset.sessionId === id)
const cacheBloatBadge = (id) => {
  const row = cacheBloatRow(id)
  return row ? row.querySelector('.row-cache-bloat') : null
}

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
    // not just an icon; hover explanation is the daemon-composed string,
    // rendered verbatim (issue #827 — no client-side re-derivation).
    const attributed = cacheBloatBadge('attributed')
    expect(attributed).not.toBeNull()
    expect(attributed.textContent).toBe('claude-code 2.1.143 +14K cache tokens vs 2.1.98')
    expect(attributed.title).toBe(attributedExplanation)

    // Unattributed: compact fallback label, not the old generic sentence.
    const unattributed = cacheBloatBadge('unattributed')
    expect(unattributed).not.toBeNull()
    expect(unattributed.textContent).toBe('cache ↑')
    expect(unattributed.title).toBe(unattributedExplanation)
    expect(unattributed.title).not.toContain('Likely tied to')

    // Ordinary session: no badge row at all.
    expect(cacheBloatRow('normal')).toBeUndefined()

    // The badge is its own row beneath the parent, NOT inline in the session
    // row's fixed-width icon slots — a long attribution string must not
    // squeeze .row-branch/.row-model on that row.
    expect(rowById('attributed').querySelector('.row-cache-bloat')).toBeNull()
  })
})
