import { describe, test, expect } from 'vitest'

// Regression test for the gastown agent-less group crash: a gastown
// orchestrator group whose agents all live in rig sub-groups serializes
// WITHOUT an `agents` field (json omitempty on AgentGroup.Agents,
// core/domain/session/grouped.go). rebuildIndex()/render() iterated
// g.agents unconditionally, so one such group in the /api/v1/sessions
// payload threw `g.agents is not iterable` inside the initial-load
// .then — render() never ran and the whole dashboard stayed blank,
// including every normal project group.
//
// Own test file: the payload is consumed at module load, and vitest's
// per-file isolation gives this file a fresh module instance.

const sessionsPayload = {
  groups: [
    {
      // No `agents` key — exactly what the daemon sends for this shape.
      name: 'Gas Town',
      type: 'gastown',
      groups: [
        {
          name: 'app',
          agents: [
            { session_id: 'proc-rig', state: 'working', project_name: 'app', metrics: {} },
          ],
        },
      ],
    },
    {
      name: 'webapp',
      agents: [
        {
          session_id: 'proc-app',
          state: 'ready',
          project_name: 'webapp',
          git_branch: 'main',
          metrics: { estimated_cost_usd: 0.05 },
        },
      ],
    },
  ],
  provider_costs: {},
}

describe('agent-less group in the sessions payload', () => {
  test('does not blank the dashboard — other groups still render', async () => {
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

    // Rows render despite the agent-less gastown group earlier in the
    // payload: the rig agent (nested sub-group, #559) and the normal
    // project group's agent.
    const rows = document.querySelectorAll('#session-list .session-row')
    expect(rows.length).toBe(2)
    expect(rows[0].dataset.sessionId).toBe('proc-rig')
    expect(rows[1].dataset.sessionId).toBe('proc-app')
    // Headers: gastown group + its rig sub-group + the project group.
    expect(document.querySelectorAll('#session-list .group-hdr').length).toBe(3)
    expect(document.getElementById('empty-state').style.display).toBe('none')
  })
})
