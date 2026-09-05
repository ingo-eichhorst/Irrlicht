import { describe, test, expect } from 'vitest'

// Wiring test for the execution-profile split (#1889). The pure rendering
// rules are covered in profileView.test.js; this file proves the SPA actually
// carries the selected profile through to the API and back into the DOM —
// the seam where a correct renderer still shows the wrong evidence.
//
// Its own file (like viewer.bootstrap.test.js) so vitest gives viewer.js a
// fresh module registry: viewer.js's bootstrap runs once per importing file
// and reads location.hash at import time.

// jsdom implements no scrolling, and viewer.js scrolls the active sidebar
// button into view on every navigation. Stub it rather than guarding the
// production call: the absence is the test environment's, not the SPA's.
if (typeof Element.prototype.scrollIntoView !== 'function') {
  Element.prototype.scrollIntoView = function scrollIntoViewStub() {}
}

const CELL = { agent: 'claudecode', subtree: 'scenarios', id: 'desktop-cell' }
const CELL_PATH = '/api/scenarios/claudecode/scenarios/desktop-cell'

const PROFILES = [
  { id: 'cli-local', label: 'Claude Code CLI Local', selectable: true, recordings: 1, has_result: false },
  { id: 'desktop-local', label: 'Claude Desktop Local', selectable: true, recordings: 1, has_result: true },
]

const DETAIL_BY_PROFILE = {
  'cli-local': {
    ...CELL,
    execution_profile: 'cli-local',
    latest_recording: '2026-05-01_cli',
    expected: { pass: true, summary: '6/6 phases' },
    latest_manifest: { name: '2026-05-01_cli', daemon_version: '0.6.1', agent_cli_version: '2.1.100' },
    profiles: PROFILES,
    transitions: [],
  },
  'desktop-local': {
    ...CELL,
    execution_profile: 'desktop-local',
    latest_recording: '2026-06-02_desktop',
    expected: { pass: true, summary: '4/4 phases' },
    latest_manifest: {
      name: '2026-06-02_desktop', daemon_version: '0.6.2',
      agent_cli_version: '2.1.258', desktop_app_version: '1.44121.4',
    },
    profiles: PROFILES,
    transitions: [],
    desktop_result: {
      scenario_id: 'desktop-cell',
      outcome: 'observed-passing',
      recording: '2026-06-02_desktop',
      recording_profile: 'desktop-local',
      versions: { desktop_app: '1.44121.4', agent_cli: '2.1.258', irrlicht: '0.6.2' },
      evidence: [{ field: 'transcript.jsonl', file: 'transcript.jsonl', present: true }],
    },
  },
}

const RECORDINGS_BY_PROFILE = {
  'cli-local': [{ name: '2026-05-01_cli', execution_profile: 'cli-local', daemon_version: '0.6.1' }],
  'desktop-local': [{ name: '2026-06-02_desktop', execution_profile: 'desktop-local', daemon_version: '0.6.2' }],
}

// requested records every URL the SPA fetched, so the test can assert the
// profile really travelled on the wire rather than only reaching the renderer.
const requested = []

function profileOf(url) {
  const q = url.indexOf('?')
  if (q < 0) return 'cli-local'
  return new URLSearchParams(url.slice(q + 1)).get('profile') || 'cli-local'
}

function json(body) {
  return Promise.resolve({
    ok: true,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
    headers: { get: () => null },
  })
}

function unavailable() {
  return Promise.resolve({
    ok: false,
    status: 404,
    json: () => Promise.resolve(null),
    text: () => Promise.resolve('no replay in this fixture'),
    headers: { get: () => null },
  })
}

function installFetchStub() {
  requested.length = 0
  global.fetch = (url) => {
    requested.push(url)
    const path = url.split('?')[0]
    // The Playback panel auto-starts a replay for the selected recording.
    // This fixture is about profile plumbing, not playback, so the replay
    // endpoints answer "nothing to play" — the path viewer.js handles by
    // logging and bailing, exactly as for an un-recorded deep link.
    if (path.startsWith('/api/replay/')) return unavailable()
    if (path === '/api/scenarios') return json([CELL])
    if (path === '/api/catalog') return json({ agents: [], scenarios: [] })
    if (path === '/api/recipes') return json({ scenarios: [] })
    if (path === CELL_PATH) return json(DETAIL_BY_PROFILE[profileOf(url)])
    if (path === `${CELL_PATH}/recordings`) return json(RECORDINGS_BY_PROFILE[profileOf(url)])
    return json(null)
  }
}

// waitForProfile polls (never sleeps) until the profile panel for `want` is in
// the DOM, and fails with the elapsed time and what it actually saw. The panel
// is appended from an un-awaited async route(), so a fixed delay would either
// flake or hide a regression.
async function waitForProfile(want, timeoutMs = 3000) {
  const start = Date.now()
  for (;;) {
    const box = document.querySelector('[data-testid=profile-evidence]')
    if (box && box.dataset.profile === want) return box
    if (Date.now() - start > timeoutMs) {
      throw new Error(
        `profile panel for ${want} did not render within ${Date.now() - start}ms ` +
        `(saw ${box ? box.dataset.profile : 'no panel'})`)
    }
    await new Promise(r => setTimeout(r, 10))
  }
}

function statusText(box) {
  return box.querySelector('[data-testid=profile-status]').textContent
}

function versionText(box, key) {
  return box.querySelector(`[data-version=${key}]`).textContent
}

function recordingHref(box) {
  return box.querySelector('[data-testid=profile-recording-link]').getAttribute('href')
}

describe('viewer wiring — the selected profile reaches the API and the DOM', () => {
  test('a desktop-local deep link fetches and renders Desktop evidence, and switching back gives CLI evidence', async () => {
    installFetchStub()
    location.hash = '#/recording/claudecode/scenarios/desktop-cell?profile=desktop-local'
    await import('./viewer.js')

    const desktop = await waitForProfile('desktop-local')
    // Every cell fetch carried the profile — not just the detail one, or the
    // recording history and the matrix measurement would describe CLI Local.
    expect(requested).toContain(`${CELL_PATH}?profile=desktop-local`)
    expect(requested).toContain(`${CELL_PATH}/recordings?profile=desktop-local`)
    expect(requested).toContain('/api/catalog?profile=desktop-local')

    expect(statusText(desktop)).toContain('observed-passing')
    expect(versionText(desktop, 'desktop_app')).toBe('1.44121.4')
    expect(versionText(desktop, 'agent_cli')).toBe('2.1.258')
    expect(recordingHref(desktop)).toBe(
      '#/recording/claudecode/scenarios/desktop-cell/2026-06-02_desktop?profile=desktop-local')

    // Switching the selector must change all three. Driving the real <select>
    // exercises the navigate() → hashchange → route() → re-fetch path.
    const select = document.querySelector('[data-testid=profile-select]')
    expect(select.value).toBe('desktop-local')
    select.value = 'cli-local'
    select.dispatchEvent(new Event('change'))

    const cli = await waitForProfile('cli-local')
    // The CLI Local default sends no query at all — the pre-#1889 request.
    expect(requested).toContain(CELL_PATH)
    expect(statusText(cli)).toContain('pass')
    expect(statusText(cli)).not.toContain('observed-passing')
    expect(versionText(cli, 'desktop_app')).toBe('—')
    expect(versionText(cli, 'agent_cli')).toBe('2.1.100')
    expect(recordingHref(cli)).toBe('#/recording/claudecode/scenarios/desktop-cell/2026-05-01_cli')
  })
})
