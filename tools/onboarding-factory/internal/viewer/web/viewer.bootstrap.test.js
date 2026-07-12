import { describe, test, expect } from 'vitest'

// Separate test FILE (not a case inside viewer.test.js) so vitest gives it its
// own module registry: viewer.js's top-level bootstrap runs once per file that
// imports it, and viewer.test.js already imports it statically (with
// vitest.setup.js's empty-catalog fetch stub) before this file's test body
// could install a different mock — a dynamic import here, in a fresh file,
// lets us feed a realistic, non-empty /api/catalog response instead.

// Realistic coverage cell shape (captured from a live /api/catalog response).
// measurement.status: "fail" makes _computeDriftKind return "regression" for
// every cell using this fixture, regardless of the daemon/driver values —
// see viewer.js's _computeDriftKind.
const driftedCell = {
  agent_supports: "yes",
  applicable: true,
  confidence: 0.85,
  daemon_capability: "full",
  display_state: "observed",
  driver_capability: "ready",
  measurement: { status: "fail", summary: "drift regression fixture" },
  notes: "",
  pipeline: {
    recipe: { authored: true, step_count: 3 },
    recordings: { archive_count: 1, latest: true },
    spec: { authored: true, phase_count: 2 },
  },
}

const catalogFixture = {
  agents: [
    { id: "aider", onboarded: true },
    { id: "mistral-vibe", onboarded: true },
  ],
  scenarios: [
    {
      id: "session-start",
      code: "1.1",
      coverage: { aider: driftedCell, "mistral-vibe": driftedCell },
    },
  ],
}

describe('viewer bootstrap — coverage matrix with a drifted cell', () => {
  test('renders the coverage table without throwing (regression for #_DRIFT_STYLES TDZ)', async () => {
    global.fetch = (url) => {
      if (url === '/api/scenarios') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
      }
      if (url === '/api/catalog') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve(catalogFixture),
          headers: { get: (h) => (h === 'X-Catalog-Source' ? 'coverage' : null) },
        })
      }
      return Promise.resolve({ ok: false, json: () => Promise.resolve(null), headers: { get: () => null } })
    }

    // If the bootstrap's initial render throws (e.g. a TDZ access on a
    // top-level const declared after the bootstrap block), the exception
    // propagates through this dynamic import's returned promise.
    await import('./viewer.js')

    const detail = document.getElementById('detail')
    expect(detail.querySelectorAll('td').length).toBeGreaterThan(0)
  })
})
