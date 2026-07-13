import { describe, test, expect, beforeEach, vi } from 'vitest'

// Mirrors collapsedGroups.test.js: each case gets a fresh module instance that
// reads the localStorage we set up first, exercising the real load path
// without booting irrlicht.js or touching the DOM.
async function freshStore() {
  vi.resetModules()
  return import('./collapsedSummaries.js')
}

const OVERRIDE_KEY = 'irrlicht_summaryOverrides'
const MODE_KEY = 'irrlicht_summaryMode'

describe('collapsedSummaries store', () => {
  beforeEach(() => localStorage.clear())

  test('defaults to waiting mode with no manual overrides', async () => {
    const store = await freshStore()
    expect(store.getSummaryMode()).toBe('waiting')
    expect(store.isSummaryCollapsed('sess-a', 'waiting')).toBe(false)
    expect(store.isSummaryCollapsed('sess-a', 'working')).toBe(true)
    expect(store.isSummaryCollapsed('sess-a', 'ready')).toBe(true)
  })

  test('restores a persisted mode', async () => {
    localStorage.setItem(MODE_KEY, 'collapsed')
    const store = await freshStore()
    expect(store.getSummaryMode()).toBe('collapsed')
    // collapsed mode hides the block regardless of session state.
    expect(store.isSummaryCollapsed('sess-a', 'waiting')).toBe(true)
    expect(store.isSummaryCollapsed('sess-a', 'working')).toBe(true)
  })

  test('ignores a malformed persisted mode', async () => {
    localStorage.setItem(MODE_KEY, 'expanded')
    const store = await freshStore()
    expect(store.getSummaryMode()).toBe('waiting')
  })

  test('toggleSummaryMode flips between collapsed and waiting and persists it', async () => {
    const store = await freshStore()
    expect(store.getSummaryMode()).toBe('waiting')

    store.toggleSummaryMode()
    expect(store.getSummaryMode()).toBe('collapsed')
    expect(localStorage.getItem(MODE_KEY)).toBe('collapsed')

    store.toggleSummaryMode()
    expect(store.getSummaryMode()).toBe('waiting')
    expect(localStorage.getItem(MODE_KEY)).toBe('waiting')
  })

  test('a session popping into waiting mid-mode shows live, not snapshotted', async () => {
    const store = await freshStore()
    // waiting mode, session currently working — collapsed.
    expect(store.isSummaryCollapsed('sess-a', 'working')).toBe(true)
    // same session transitions to waiting — pops open with no extra call.
    expect(store.isSummaryCollapsed('sess-a', 'waiting')).toBe(false)
    // answered and back to ready — collapses again.
    expect(store.isSummaryCollapsed('sess-a', 'ready')).toBe(true)
  })

  test('manual override flips a single row regardless of mode', async () => {
    const store = await freshStore()
    // waiting mode: a working session is collapsed by default...
    expect(store.isSummaryCollapsed('sess-a', 'working')).toBe(true)
    store.toggleSummaryCollapsed('sess-a')
    // ...manually expanded via the per-row override, glancing at it anyway.
    expect(store.isSummaryCollapsed('sess-a', 'working')).toBe(false)
    // a waiting session defaults to expanded...
    expect(store.isSummaryCollapsed('sess-b', 'waiting')).toBe(false)
    store.toggleSummaryCollapsed('sess-b')
    // ...manually collapsed despite being in the mode that would show it.
    expect(store.isSummaryCollapsed('sess-b', 'waiting')).toBe(true)

    // toggling back restores the mode's baseline for both.
    store.toggleSummaryCollapsed('sess-a')
    expect(store.isSummaryCollapsed('sess-a', 'working')).toBe(true)
    store.toggleSummaryCollapsed('sess-b')
    expect(store.isSummaryCollapsed('sess-b', 'waiting')).toBe(false)
  })

  test('manual overrides persist across reloads, keyed independently of mode', async () => {
    let store = await freshStore()
    store.toggleSummaryCollapsed('sess-a')
    expect(JSON.parse(localStorage.getItem(OVERRIDE_KEY))).toEqual(['sess-a'])

    store = await freshStore()
    expect(store.isSummaryCollapsed('sess-a', 'working')).toBe(false)
  })

  test('ignores a malformed persisted override set', async () => {
    localStorage.setItem(OVERRIDE_KEY, 'not json{')
    const store = await freshStore()
    expect(store.isSummaryCollapsed('anything', 'working')).toBe(true)
    store.toggleSummaryCollapsed('x')
    expect(store.isSummaryCollapsed('x', 'working')).toBe(false)
  })
})
