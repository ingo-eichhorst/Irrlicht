import { describe, test, expect } from 'vitest'

// Regression test for #564: the dashboard went blank — sessions present in the
// /api/v1/sessions payload but no .session-row rendered — because a persisted
// collapsed group (localStorage irrlicht_collapsedGroups) became the ONLY
// group. With a single group no header renders (showHeaders=false), so the
// collapse both hid every row and left no chevron to un-collapse it.
//
// This lives in its own test file (not irrlicht.test.js) because irrlicht.js
// reads the collapsed set from localStorage at module load, and vitest's
// per-file module isolation is what lets us seed localStorage BEFORE import.

// Representative /api/v1/sessions payload (wrapped shape, one group).
const sessionsPayload = {
  groups: [
    {
      name: 'irrlicht',
      agents: [
        {
          session_id: 'proc-1',
          state: 'working',
          project_name: 'irrlicht',
          git_branch: 'main',
          adapter: 'claude-code',
          first_seen: 1764800000,
          metrics: {
            model_name: 'claude-opus-4-8',
            estimated_cost_usd: 0.12,
            context_utilization_percentage: 45,
            pressure_level: 'low',
            total_tokens: 2400,
            elapsed_seconds: 120,
          },
        },
        {
          session_id: 'proc-2',
          state: 'ready',
          project_name: 'irrlicht',
          git_branch: 'feat/x',
          adapter: 'claude-code',
          first_seen: 1764800100,
          metrics: {},
        },
      ],
      costs: { day: 0.5 },
    },
  ],
  provider_costs: {},
}

describe('session row rendering (#564)', () => {
  test('initial payload renders rows even when the single group is persisted as collapsed', async () => {
    // The trap: groups collapsed earlier (when several projects were active)
    // survive in localStorage; later only one of them is left in the payload.
    localStorage.setItem('irrlicht_collapsedGroups', JSON.stringify(['irrlicht', 'articles']))

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
    // Let the initial Promise.all(fetch…).then(render) settle.
    await new Promise((r) => setTimeout(r, 0))

    const rows = document.querySelectorAll('#session-list .session-row')
    expect(rows.length).toBe(2)
    expect(rows[0].dataset.sessionId).toBe('proc-1')
    expect(rows[1].dataset.sessionId).toBe('proc-2')
    // Single group → no header rendered; the rows must show regardless of the
    // stale collapse entry.
    expect(document.querySelectorAll('#session-list .group-hdr').length).toBe(0)
    expect(document.getElementById('empty-state').style.display).toBe('none')
  })

  test('a second project restores headers and the persisted collapse', () => {
    // Same module instance as above (per-file isolation): a WS session_update
    // for another project brings the dashboard to two groups.
    const ws = global.lastMockWebSocket
    ws.simulateOpen()
    ws.simulateMessage({
      type: 'session_update',
      session: { session_id: 'proc-3', state: 'ready', project_name: 'webapp' },
    })

    // Headers are back, so the collapsed 'irrlicht' group legitimately hides
    // its rows again — collapse semantics unchanged when a header exists.
    expect(document.querySelectorAll('#session-list .group-hdr').length).toBe(2)
    const rows = document.querySelectorAll('#session-list .session-row')
    expect(rows.length).toBe(1)
    expect(rows[0].dataset.sessionId).toBe('proc-3')
  })
})
