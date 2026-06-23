import { describe, test, expect, beforeEach, vi } from 'vitest'

// Mirrors collapsedGroups.test.js: each case gets a fresh module instance that
// reads the localStorage we set up first, exercising the real load path
// without booting irrlicht.js or touching the DOM.
async function freshStore() {
  vi.resetModules()
  return import('./collapsedSummaries.js')
}

const KEY = 'irrlicht_collapsedSummaries'

describe('collapsedSummaries store', () => {
  beforeEach(() => localStorage.clear())

  test('restores collapsed session ids persisted from a prior session', async () => {
    localStorage.setItem(KEY, JSON.stringify(['sess-a', 'sess-b']))
    const store = await freshStore()
    expect(store.isSummaryCollapsed('sess-a')).toBe(true)
    expect(store.isSummaryCollapsed('sess-b')).toBe(true)
    expect(store.isSummaryCollapsed('sess-c')).toBe(false)
  })

  test('toggle flips state and persists both directions', async () => {
    const store = await freshStore()
    expect(store.isSummaryCollapsed('sess-a')).toBe(false)

    store.toggleSummaryCollapsed('sess-a')
    expect(store.isSummaryCollapsed('sess-a')).toBe(true)
    expect(store.anySummaryCollapsed()).toBe(true)
    expect(JSON.parse(localStorage.getItem(KEY))).toEqual(['sess-a'])

    store.toggleSummaryCollapsed('sess-a')
    expect(store.isSummaryCollapsed('sess-a')).toBe(false)
    expect(store.anySummaryCollapsed()).toBe(false)
  })

  test('collapseAll then expandAll', async () => {
    const store = await freshStore()
    store.collapseAllSummaries(['s1', 's2', 's3'])
    expect(store.isSummaryCollapsed('s1')).toBe(true)
    expect(store.isSummaryCollapsed('s3')).toBe(true)
    expect(store.anySummaryCollapsed()).toBe(true)

    store.expandAllSummaries()
    expect(store.isSummaryCollapsed('s1')).toBe(false)
    expect(store.anySummaryCollapsed()).toBe(false)
    expect(JSON.parse(localStorage.getItem(KEY))).toEqual([])
  })

  test('ignores a malformed persisted value', async () => {
    localStorage.setItem(KEY, 'not json{')
    const store = await freshStore()
    expect(store.isSummaryCollapsed('anything')).toBe(false)
    store.toggleSummaryCollapsed('x')
    expect(store.isSummaryCollapsed('x')).toBe(true)
  })
})
