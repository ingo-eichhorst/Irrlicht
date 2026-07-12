import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { scenes, FIXED_NOW_SEC } from './fixtures.js'
import { serializeSessionList } from './serialize.js'
import { SETUP_BODY } from '../vitest.setup.js'

// Tier 1 of the web visual artifact (issue #757): drive each scene through the
// REAL irrlicht.js in jsdom, assert the rows render (so a broken render fails
// `npm test` — this is the CI gate), and emit a self-contained HTML per scene
// under snapshots/out/ for an agent to read via the Artifact tool / WebFetch.
//
// jsdom can't paint pixels, so this is structural HTML + inlined CSS, not a PNG;
// the true-PNG path is the opt-in snapshot.png.mjs (Tier 2).

const here = dirname(fileURLToPath(import.meta.url))
const outDir = join(here, 'out')

// SETUP_BODY (the DOM scaffold irrlicht.js queries at module load) is imported
// from vitest.setup.js — the single source of truth — and re-applied per scene
// so each fresh import renders into a clean #session-list.

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
      expect(rows).toHaveLength(scene.expectedRows)

      const html = serializeSessionList(document, {
        theme: scene.theme,
        title: `irrlicht — ${scene.name}`,
      })
      expect(html).toContain('session-row')
      writeFileSync(join(outDir, `${scene.name}.html`), html)
    })
  }
})
