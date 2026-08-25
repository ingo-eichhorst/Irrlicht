import { describe, test, expect } from 'vitest'

// Reachability for #1801, and the counterpart of sessionError.test.js: every
// assertion in that file calls sessionErrorText / renderDaemonErrorBanner
// DIRECTLY, so deleting the `sessionErrorItem` push inside emitAgentRowItems,
// or the `renderDaemonErrorBanner` call inside ingestInitialSessions, would
// leave all thirty of them green. That is the exact hole
// permissionsBanner.test.js records finding by mutation in review. This file
// drives the REAL path — fetch → ingestInitialSessions → render — which is the
// one a refactor can break.
//
// Its own file, not a second describe in sessionError.test.js, because
// irrlicht.js does its top-level wiring at module load: only vitest's per-file
// module isolation lets the fetch stub be installed BEFORE the import, the
// same reason irrlicht.render.test.js is separate.

const ERROR_MESSAGE = 'API Error: 429 rate limited'

const sessionsPayload = {
  groups: [
    {
      name: 'irrlicht',
      agents: [
        {
          session_id: 'sess-red',
          state: 'error',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800000,
          metrics: {
            session_error: {
              phase: 'retrying',
              class: 'rate_limit',
              message: ERROR_MESSAGE,
              http_status: 429,
              attempt: 3,
              max_attempts: 10,
              retry_in_ms: 616.4520045919932,
            },
          },
        },
        {
          session_id: 'sess-green',
          state: 'ready',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800100,
          metrics: {},
        },
      ],
      costs: {},
    },
  ],
  provider_costs: {},
  daemon_errors: [
    {
      kind: 'hook_channel_silent',
      scope: 'claude-code',
      message: 'Irrlicht is receiving no hook events from claude-code — its sessions have fallen back to slower transcript-only detection.',
      detail: '9 completed turns with no receipt',
    },
  ],
}

describe('the dashboard actually wires the error state up (#1801)', () => {
  test('an errored session in the initial payload renders red, with its own line', async () => {
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

    // The banner slot lives in index.html, not in vitest.setup.js's SETUP_BODY
    // — add it before the module wires itself up, the way #1385's suite does.
    document.body.innerHTML +=
      '<div id="daemon-error-banner" role="alert" aria-live="polite" hidden></div>'

    await import('./irrlicht.js')
    await new Promise((r) => setTimeout(r, 0))

    // Guard the guard: if the list did not render at all, every "is the error
    // row there" assertion below would be reporting on an empty document —
    // inability to look reading as a clean pass.
    const rows = document.querySelectorAll('#session-list .session-row')
    expect(rows, 'the session list did not render — nothing below would mean anything').toHaveLength(2)

    // The row itself: the state icon is the error glyph, not the ready one.
    const red = document.querySelector('#session-list .session-row[data-session-id="sess-red"]')
    expect(red).not.toBeNull()
    expect(red.dataset.state).toBe('error')
    expect(red.querySelector('.row-state-icon svg.state-error')).not.toBeNull()

    // ...and the healthy session beside it is untouched.
    const green = document.querySelector('#session-list .session-row[data-session-id="sess-green"]')
    expect(green.querySelector('.row-state-icon svg.state-error')).toBeNull()

    // The red line beneath it, carrying the agent's own words and the ladder.
    const errRows = document.querySelectorAll('#session-list .row-error-row')
    expect(errRows, 'no .row-error-row was emitted for the errored session').toHaveLength(1)
    expect(errRows[0].dataset.sessionId).toBe('sess-red')
    expect(errRows[0].textContent).toContain(ERROR_MESSAGE)
    expect(errRows[0].textContent).toContain('attempt 3 of 10')
    expect(errRows[0].querySelector('.row-error')).not.toBeNull()

    // It sits directly beneath its own session row, not at the end of the list.
    expect(errRows[0].previousElementSibling).toBe(red)

    // And the daemon-wide banner is up.
    const banner = document.getElementById('daemon-error-banner')
    expect(banner.hidden, 'daemon_errors was in the payload but the banner stayed hidden').toBe(false)
    expect(banner.textContent).toContain('claude-code')
    expect(banner.textContent).toContain('9 completed turns with no receipt')
  })

  test('a websocket update clearing the error removes the red line', () => {
    // Same module instance (per-file isolation). This is the path that matters
    // most in practice: the error arrives and clears over the websocket, not
    // over a refetch, and the row must disappear when it does — the daemon
    // clears session_error rather than sending a "no longer failing" signal.
    const ws = global.lastMockWebSocket
    ws.simulateOpen()
    ws.simulateMessage({
      type: 'session_update',
      session: {
        session_id: 'sess-red',
        state: 'working',
        project_name: 'irrlicht',
        adapter: 'claude-code',
        metrics: {},
      },
    })

    expect(document.querySelectorAll('#session-list .row-error-row')).toHaveLength(0)
    const row = document.querySelector('#session-list .session-row[data-session-id="sess-red"]')
    expect(row.dataset.state).toBe('working')
  })

  test('an error arriving over the websocket adds the red line', () => {
    // The inverse, so the removal above cannot pass by the row never having
    // been reachable on the websocket path in the first place.
    const ws = global.lastMockWebSocket
    ws.simulateMessage({
      type: 'session_update',
      session: {
        session_id: 'sess-red',
        state: 'error',
        project_name: 'irrlicht',
        adapter: 'claude-code',
        metrics: {
          session_error: { phase: 'terminal', class: 'auth', message: 'credentials rejected' },
        },
      },
    })

    const errRows = document.querySelectorAll('#session-list .row-error-row')
    expect(errRows).toHaveLength(1)
    expect(errRows[0].textContent).toContain('credentials rejected')
    // Terminal: no retry clause invented from counters the agent never sent.
    expect(errRows[0].textContent).not.toMatch(/attempt/)
  })
})
