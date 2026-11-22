import { describe, test, expect, beforeAll } from 'vitest'

// Issue #2163: turning a quota account off in Settings removes its chip from
// the header strip, and turning it back on restores it, with no reload. This
// drives the real strip (renderHeaderTitle) and the real Settings rows
// (refreshProviderSettings) over sessions the dashboard loaded itself, rather
// than restating the strip's derivation the way quotaChips.test.js does.
//
// Own file so irrlicht.js loads fresh (per-file module isolation) against a
// payload crafted for this case.

const future = Math.floor(Date.now() / 1000) + 3600
const agent = (id, ref, planType, pct) => ({
  session_id: id, state: 'working', project_name: 'irrlicht', adapter: 'claude-code', first_seen: 1764800000,
  metrics: { rate_limit: {
    provider: 'anthropic', attribution_quality: 'confirmed', confirmed_account_ref: ref, plan_type: planType,
    sampled_at: 1, windows: [{ used_percent: pct, window_minutes: 300, resets_at: future }],
  } },
})
const payload = {
  groups: [{ name: 'irrlicht', agents: [agent('s1', 'acct-AAAA', 'max', 20), agent('s2', 'acct-BBBB', 'team', 80)], costs: {} }],
  provider_costs: {},
}

const KEY = 'hiddenQuotaAccounts'
const chipCount = () => document.querySelectorAll('#quota-chips .quota-chip').length
const accountBox = (key) => document.querySelector(`#settings-providers input[data-quota-account="${key}"]`)
const flip = (box, checked) => { box.checked = checked; box.dispatchEvent(new Event('change')) }

let quotaChips

beforeAll(async () => {
  global.fetch = (url) => {
    const u = String(url)
    if (u.includes('/api/v1/sessions')) return Promise.resolve({ ok: true, json: () => Promise.resolve(structuredClone(payload)) })
    if (u.includes('/api/v1/agents')) return Promise.resolve({ ok: true, json: () => Promise.resolve([]) })
    return Promise.resolve({ ok: false, json: () => Promise.resolve(null) })
  }
  // irrlicht.js first: it is the page's entry, and the two modules import
  // each other (see quota.parity.test.js).
  await import('./irrlicht.js')
  quotaChips = await import('./quotaChips.js')
  for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0))
})

describe('hiding a quota account from Settings (#2163)', () => {
  test('red-first: the strip drops the chip at once and brings it back at once', () => {
    expect(chipCount(), 'vacuity: both accounts must draw a chip before anything is hidden').toBe(2)
    quotaChips.refreshProviderSettings()
    const box = accountBox('anthropic|acct-BBBB')
    expect(box, 'Settings rendered no row for the reporting account').not.toBeNull()
    expect(box.checked).toBe(true)

    flip(box, false)
    expect(chipCount()).toBe(1)
    // Hiding a reporting account stores its live tier label.
    expect(JSON.parse(localStorage.getItem(KEY))).toEqual({ 'anthropic|acct-BBBB': 'Team' })
    // Still reporting, so still listed, now off.
    const again = accountBox('anthropic|acct-BBBB')
    expect(again.checked).toBe(false)

    flip(again, true)
    expect(chipCount()).toBe(2)
    expect(localStorage.getItem(KEY)).toBeNull()
  })
})
