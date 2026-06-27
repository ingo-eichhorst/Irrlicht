import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scenes, FIXED_NOW_SEC } from './fixtures.js'
import { serializeSessionList } from './serialize.js'

// Tier 1 of the web visual artifact (issue #757): drive each scene through the
// REAL irrlicht.js in jsdom, assert the rows render (so a broken render fails
// `npm test` — this is the CI gate), and emit a self-contained HTML per scene
// under snapshots/out/ for an agent to read via the Artifact tool / WebFetch.
//
// jsdom can't paint pixels, so this is structural HTML + inlined CSS, not a PNG;
// the true-PNG path is the opt-in snapshot.png.mjs (Tier 2).

const here = dirname(fileURLToPath(import.meta.url))
const outDir = join(here, 'out')

// The DOM scaffold irrlicht.js queries at module load (mirrors vitest.setup.js).
// Re-applied per scene so each fresh import renders into a clean #session-list.
const SETUP_BODY = `
  <header></header>
  <button id="theme-toggle"></button>
  <button id="view-mode-cycle">Context</button>
  <button id="summary-collapse-all"></button>
  <button id="settings-toggle"></button>
  <button id="settings-close"></button>
  <div id="settings-backdrop"></div>
  <div id="settings-providers"></div>
  <div id="session-list"></div>
  <div id="app-version"></div>
  <div id="empty-state"></div>
  <div id="ws-dot" class="ws-dot"></div>
  <span id="ws-label"></span>
  <div id="quota-chips"></div>
  <div id="app-state-icons"></div>
  <div id="gt-container" style="display:none"></div>
  <div id="connection-banner"></div>
  <div id="settings-perm-note"></div>
  <button id="settings-review-permissions"></button>
  <div id="permissions-backdrop">
    <h2 id="permissions-title"></h2>
    <p id="permissions-intro"></p>
    <div id="permissions-body"></div>
    <button id="permissions-apply"></button>
  </div>
`

function makeFetch(sessionsPayload) {
  return (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(sessionsPayload) })
    }
    if (u.includes('/api/v1/agents')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    }
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

beforeEach(() => {
  vi.resetModules()
  document.body.innerHTML = SETUP_BODY
  localStorage.clear()
  // Freeze ONLY Date: active-session elapsed and the ETA chip read Date.now(),
  // so a fixed clock makes the artifact deterministic. setTimeout stays real so
  // the post-import flush tick still fires (faking all timers would deadlock on
  // irrlicht.js's reconnect/elapsed setInterval loops).
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(FIXED_NOW_SEC * 1000)
})

afterEach(() => {
  vi.useRealTimers()
})

mkdirSync(outDir, { recursive: true })

describe('web session-list HTML snapshot artifacts (#757)', () => {
  for (const scene of scenes) {
    test(scene.name, async () => {
      localStorage.setItem('irrlicht_theme', scene.theme)
      global.fetch = makeFetch(scene.sessions)

      await import('../irrlicht.js')
      await tick() // let the initial Promise.all(fetch…).then(render) settle

      const ws = global.lastMockWebSocket
      if (ws) {
        ws.simulateOpen()
        for (const delta of scene.deltas) ws.simulateMessage(delta)
      }
      await tick()

      // CI gate: a broken render (no rows) fails `npm test`.
      const rows = document.querySelectorAll('#session-list .session-row')
      expect(rows.length).toBe(scene.expectedRows)

      const html = serializeSessionList(document, {
        theme: scene.theme,
        title: `irrlicht — ${scene.name}`,
      })
      expect(html).toContain('session-row')
      writeFileSync(join(outDir, `${scene.name}.html`), html)
    })
  }
})
