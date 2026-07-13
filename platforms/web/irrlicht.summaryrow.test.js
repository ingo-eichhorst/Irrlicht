import { describe, test, expect } from 'vitest'

// Issue #979: the summary/question row had no dedicated test coverage at
// all before this — collapsedSummaries.test.js only covers the pure
// collapse-state store, never the rendered DOM. This pins the two
// behavioral changes: the row only ever renders for a waiting session
// (silence-by-default — a stale task_summary/last_assistant_text on a
// ready session must not resurrect it), and the daemon's headline/text is
// rendered verbatim with no further client-side truncation.
//
// Own file so irrlicht.js loads fresh (per-file module isolation) against a
// payload crafted for this case.

const longQuestion =
  'Should I resolve the merge conflict and patch the failing test cell now, ' +
  'or dig into the design decision for #905/#906 first?'

const sessionsPayload = {
  groups: [
    {
      name: 'irrlicht',
      agents: [
        {
          session_id: 'waiting-headline',
          state: 'waiting',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800000,
          metrics: {
            question_headline: longQuestion,
            last_assistant_text: 'a shorter, less complete raw fallback',
          },
        },
        {
          session_id: 'waiting-fallback',
          state: 'waiting',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800100,
          metrics: {
            last_assistant_text: longQuestion,
          },
        },
        {
          session_id: 'ready-had-summary',
          state: 'ready',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800200,
          metrics: {
            task_summary: 'Add OAuth login to the web dashboard',
            last_assistant_text: 'done, shipped as PR #710',
          },
        },
        {
          session_id: 'working-plain',
          state: 'working',
          project_name: 'irrlicht',
          adapter: 'claude-code',
          first_seen: 1764800300,
          metrics: {},
        },
      ],
      costs: {},
    },
  ],
  provider_costs: {},
}

const summaryRowFor = (id) =>
  [...document.querySelectorAll('#session-list .row-summary-row')].find((r) => r._sessionId === id)

describe('pending-question row (#979)', () => {
  test('renders only for waiting sessions, verbatim, headline preferred over raw text', async () => {
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

    expect(document.querySelectorAll('#session-list .session-row')).toHaveLength(4)

    // Waiting + a question_headline: the headline wins over the raw text,
    // rendered in full — no further client-side truncation on top of the
    // daemon's own (now generous) safety bound.
    const headlineRow = summaryRowFor('waiting-headline')
    expect(headlineRow).not.toBeUndefined()
    const headlineQuestion = headlineRow.querySelector('.summary-question')
    expect(headlineQuestion.textContent).toBe(longQuestion)
    expect(headlineQuestion.textContent.length).toBeGreaterThan(70)

    // Waiting with no headline: falls back to the full last_assistant_text,
    // still rendered verbatim.
    const fallbackRow = summaryRowFor('waiting-fallback')
    expect(fallbackRow).not.toBeUndefined()
    expect(fallbackRow.querySelector('.summary-question').textContent).toBe(longQuestion)

    // Ready with a leftover task_summary/last_assistant_text from its final
    // turn: no row at all. Silence-by-default — a finished session has
    // nothing left to report, and the old summary content must not
    // resurrect a row the way it did before #979.
    expect(summaryRowFor('ready-had-summary')).toBeUndefined()

    // Working, no content: no row either.
    expect(summaryRowFor('working-plain')).toBeUndefined()
  })
})
