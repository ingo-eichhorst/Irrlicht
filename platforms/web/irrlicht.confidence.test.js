import { describe, test, expect, beforeAll } from 'vitest'

import { executionConfidenceChip } from './formatters.js'
import { readCss } from './snapshots/serialize.js'
import {
  contrastRatio, composite, readToken, DARK_BLOCK, LIGHT_MEDIA_BLOCK, LIGHT_THEME_BLOCK,
} from './snapshots/contrast.mjs'

// #737 Phase 2: the low execution-confidence chip in the session row.
//
// The daemon decides everything (core/domain/session/execution_confidence.go):
// `execution_confidence` is the 0–100 score, `execution_confidence_low` its
// verdict against its own threshold (omitted from the JSON when false), and
// `execution_confidence_tooltip` the hover text. The dashboard renders a chip
// only when the flag is set, and shows the tooltip verbatim — the same
// "daemon composes, client renders" contract as cache_bloat_explanation (#827,
// irrlicht.cachebloat.test.js).
//
// Own file so irrlicht.js loads fresh (per-file module isolation) against a
// payload crafted for this case.

// The daemon's tooltip, written out the way executionConfidenceTooltip
// composes it, so a client that rewrote or re-derived it would not match.
const tooltip = (score) =>
  `Execution confidence ${score}/100 — scored from hedging and uncertainty language in the agent's recent messages, latest weighted most (100 = decisive; below 50 is low).`

const agent = (id, metrics, firstSeen) => ({
  session_id: id,
  state: 'working',
  project_name: 'irrlicht',
  adapter: 'claude-code',
  first_seen: firstSeen,
  metrics,
})

const initial = [
  agent('low', { execution_confidence: 42, execution_confidence_low: true, execution_confidence_tooltip: tooltip(42) }, 1764800000),
  // The real wire shape of a confident session: the daemon omits `_low` when false.
  agent('confident', { execution_confidence: 80, execution_confidence_tooltip: tooltip(80) }, 1764800100),
  // An explicit false must read the same as an omitted flag.
  agent('explicit-false', { execution_confidence: 55, execution_confidence_low: false, execution_confidence_tooltip: tooltip(55) }, 1764800200),
  // No score yet (nil until the first scored assistant message): no fields at all.
  agent('absent', {}, 1764800300),
  // The lowest score there is. A truthiness check on the score hides exactly this one.
  agent('zero', { execution_confidence: 0, execution_confidence_low: true, execution_confidence_tooltip: tooltip(0) }, 1764800400),
]

const sessionsPayload = { groups: [{ name: 'irrlicht', agents: initial, costs: {} }], provider_costs: {} }

const rowById = (id) =>
  [...document.querySelectorAll('#session-list .session-row')].find((r) => r.dataset.sessionId === id)
const chip = (id) => rowById(id).querySelector('.row-confidence')
const shown = (el) => !!el && el.style.display !== 'none'
const tick = () => new Promise((r) => setTimeout(r, 0))

let ws = null
const update = async (a) => {
  ws.simulateMessage({ type: 'session_update', session: a })
  await tick()
}

beforeAll(async () => {
  const bodyFor = (u) => (u.includes('/api/v1/sessions') ? sessionsPayload : u.includes('/api/v1/agents') ? [] : null)
  global.fetch = (url) => {
    const body = bodyFor(String(url))
    return Promise.resolve({ ok: body !== null, json: () => Promise.resolve(body) })
  }
  await import('./irrlicht.js')
  await tick()
  ws = global.lastMockWebSocket
  expect(ws, 'no websocket was opened — the update tests below could not drive anything').toBeTruthy()
  ws.simulateOpen()
  await tick()
})

describe('low execution-confidence chip (#737)', () => {
  test('every session renders a row', () => {
    expect(document.querySelectorAll('#session-list .session-row')).toHaveLength(initial.length)
  })

  test('shown when the daemon flags the session low: compact score, daemon tooltip verbatim', () => {
    const el = chip('low')
    expect(shown(el)).toBe(true)
    expect(el.textContent).toBe('? 42')
    expect(el.title).toBe(tooltip(42))
  })

  test('hidden when the flag is absent, even though a score is present', () => {
    expect(shown(chip('confident'))).toBe(false)
  })

  test('hidden when the flag is explicitly false', () => {
    expect(shown(chip('explicit-false'))).toBe(false)
  })

  test('hidden when the session carries no confidence fields at all', () => {
    expect(shown(chip('absent'))).toBe(false)
    expect(chip('absent').textContent).toBe('')
  })

  test('a score of 0 is still shown — the gate is the flag, not the number', () => {
    const el = chip('zero')
    expect(shown(el)).toBe(true)
    expect(el.textContent).toBe('? 0')
    expect(el.title).toBe(tooltip(0))
  })

  test('updates in place: value change, appear, disappear — the row element is never re-created', async () => {
    const lowRow = rowById('low')
    const confidentRow = rowById('confident')

    // Value change while still low.
    await update(agent('low', { execution_confidence: 17, execution_confidence_low: true, execution_confidence_tooltip: tooltip(17) }, 1764800000))
    expect(rowById('low')).toBe(lowRow)
    expect(chip('low').textContent).toBe('? 17')
    expect(chip('low').title).toBe(tooltip(17))

    // Recovers: the daemon drops the flag (omitted when false) → chip goes away.
    await update(agent('low', { execution_confidence: 63, execution_confidence_tooltip: tooltip(63) }, 1764800000))
    expect(rowById('low')).toBe(lowRow)
    expect(shown(chip('low'))).toBe(false)
    expect(chip('low').title).toBe('')

    // A confident session turns low → chip appears on the existing row.
    await update(agent('confident', { execution_confidence: 31, execution_confidence_low: true, execution_confidence_tooltip: tooltip(31) }, 1764800100))
    expect(rowById('confident')).toBe(confidentRow)
    expect(shown(chip('confident'))).toBe(true)
    expect(chip('confident').textContent).toBe('? 31')
    expect(chip('confident').title).toBe(tooltip(31))
  })
})

describe('executionConfidenceChip — the gate, and committed in-language mutants of it', () => {
  test('null unless the daemon flags the session low', () => {
    expect(executionConfidenceChip(undefined)).toBeNull()
    expect(executionConfidenceChip({})).toBeNull()
    expect(executionConfidenceChip({ execution_confidence: 80 })).toBeNull()
    expect(executionConfidenceChip({ execution_confidence: 10, execution_confidence_low: false })).toBeNull()
    expect(executionConfidenceChip({ execution_confidence: 10, execution_confidence_low: true, execution_confidence_tooltip: 't' }))
      .toEqual({ text: '? 10', title: 't' })
  })

  // COMMITTED IN-LANGUAGE MUTANTS (the idiom of
  // irrlicht.history.autonomy.measurement.test.js). Each is a plausible wrong
  // gate that passes at least one assertion above on its own; production must
  // differ from it on the input that tells them apart. The same mutations were
  // also applied to formatters.js itself with tools/mutate.sh and turned the
  // row tests above red — that run is recorded in the PR body.
  test('production differs from a gate that ignores the flag', () => {
    // Drops the `_low` check: any scored session shows a chip.
    const flagBlind = (m) => (m && m.execution_confidence != null ? { text: '? ' + m.execution_confidence, title: '' } : null)
    const confident = { execution_confidence: 80 }
    expect(flagBlind(confident)).not.toBeNull()
    expect(executionConfidenceChip(confident)).toBeNull()
  })

  test('production differs from a gate keyed on the score\'s truthiness', () => {
    const truthy = (m) => (m && m.execution_confidence ? { text: '? ' + m.execution_confidence, title: '' } : null)
    const zero = { execution_confidence: 0, execution_confidence_low: true }
    expect(truthy(zero)).toBeNull()
    expect(executionConfidenceChip(zero)).not.toBeNull()
  })

  test('production differs from a client that re-derives the threshold itself', () => {
    // The daemon's threshold is its own business; a session it calls low at 55
    // (a future threshold) must still show, and one it does not flag at 10
    // must not.
    const rederived = (m) => (m && m.execution_confidence < 50 ? { text: '? ' + m.execution_confidence, title: '' } : null)
    expect(rederived({ execution_confidence: 55, execution_confidence_low: true })).toBeNull()
    expect(executionConfidenceChip({ execution_confidence: 55, execution_confidence_low: true })).not.toBeNull()
    expect(rederived({ execution_confidence: 10 })).not.toBeNull()
    expect(executionConfidenceChip({ execution_confidence: 10 })).toBeNull()
  })
})

describe('chip colours clear WCAG AA in both themes', () => {
  test('--waiting on its own --waiting-dim wash is >= 4.5:1', () => {
    // The chip borrows this pair (irrlicht.css .row-confidence). Reproduce the
    // full table with: node platforms/web/snapshots/contrast.mjs
    // The wash is composited from --waiting-dim's own rgba() as declared in
    // each block, so a re-tuned alpha or hue is measured rather than assumed.
    // Both light declarations are checked: the OS-preference @media block and
    // the explicit [data-theme="light"] block can drift apart.
    const css = readCss()
    const blocks = [['dark', DARK_BLOCK], ['light (media)', LIGHT_MEDIA_BLOCK], ['light (data-theme)', LIGHT_THEME_BLOCK]]
    for (const [name, block] of blocks) {
      const hex = readToken(css, block, 'waiting')
      const surface = readToken(css, block, 'surface')
      const dim = readToken(css, block, 'waiting-dim')
      const m = dim.match(/rgba\((\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\)/)
      expect(m, `${name} --waiting-dim ${dim} is not an rgba() quadruple`).not.toBeNull()
      const washHex = '#' + [1, 2, 3].map((i) => Number(m[i]).toString(16).padStart(2, '0')).join('')
      const alpha = Number(m[4])
      const ratio = contrastRatio(hex, composite(washHex, alpha, surface))
      expect(ratio, `${name} --waiting ${hex} on --waiting-dim ${dim} over ${surface} is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5)
    }
  })

  test('.row-confidence actually uses that pair', () => {
    const rule = readCss().match(/\.row-confidence \{[^}]*\}/)
    expect(rule, '.row-confidence rule not found in irrlicht.css').not.toBeNull()
    expect(rule[0]).toContain('background: var(--waiting-dim)')
    expect(rule[0]).toContain('color: var(--waiting)')
  })
})
