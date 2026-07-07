import { describe, test, expect } from 'vitest'

// #744: a Claude Code Agent View background agent (kind:bg) keeps running
// detached in the daemon pool after its window closes. The daemon flags it with
// a `background` object ({name, detached}); the dashboard must render a
// .row-bg-badge for it (and only it), with .is-detached when no window owns it.
//
// Own file so irrlicht.js loads fresh (per-file module isolation) against a
// payload crafted for this case.

const sessionsPayload = {
  groups: [
    {
      name: 'irrlicht',
      agents: [
        {
          session_id: 'bg-detached',
          state: 'ready',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800000,
          metrics: {},
          background: { name: 'Add guiding colors to quest cards', detached: true },
        },
        {
          session_id: 'bg-attached',
          state: 'working',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800100,
          metrics: {},
          background: { name: 'Tidy imports' },
        },
        {
          session_id: 'normal',
          state: 'ready',
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
const bgBadge = (id) => rowById(id).querySelector('.row-bg-badge')
const shown = (el) => el && el.style.display !== 'none'

describe('background-agent badge (#744)', () => {
  test('badge shows for kind:bg sessions and emphasizes detached', async () => {
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

    expect(document.querySelectorAll('#session-list .session-row')).toHaveLength(3)

    // Detached bg agent: badge shown, amber-emphasized, descriptive tooltip.
    const detached = bgBadge('bg-detached')
    expect(shown(detached)).toBe(true)
    expect(detached.classList.contains('is-detached')).toBe(true)
    expect(detached.title).toContain('Detached background agent')
    expect(detached.title).toContain('Add guiding colors to quest cards')

    // Attached bg agent: badge shown, NOT detached.
    const attached = bgBadge('bg-attached')
    expect(shown(attached)).toBe(true)
    expect(attached.classList.contains('is-detached')).toBe(false)
    expect(attached.title).toBe('Background agent (Tidy imports)')

    // Ordinary session: no badge.
    expect(shown(bgBadge('normal'))).toBe(false)

    // Branch column shrinks for bg rows (has-bg) so columns stay aligned;
    // ordinary rows keep the full-width branch.
    expect(rowById('bg-detached').classList.contains('has-bg')).toBe(true)
    expect(rowById('bg-attached').classList.contains('has-bg')).toBe(true)
    expect(rowById('normal').classList.contains('has-bg')).toBe(false)
  })
})
