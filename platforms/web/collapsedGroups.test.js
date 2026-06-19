import { describe, test, expect, beforeEach, vi } from 'vitest'

// The store loads localStorage at module-eval time, so each case gets a fresh
// module instance that reads the localStorage we set up first. This exercises
// the real load path without booting irrlicht.js or touching the DOM.
async function freshStore() {
  vi.resetModules()
  return import('./collapsedGroups.js')
}

const KEY = 'irrlicht_collapsedGroups'

describe('collapsedGroups store', () => {
  beforeEach(() => localStorage.clear())

  test('restores collapsed names persisted from a prior session', async () => {
    localStorage.setItem(KEY, JSON.stringify(['alpha', 'beta']))
    const store = await freshStore()
    expect(store.isGroupCollapsed('alpha')).toBe(true)
    expect(store.isGroupCollapsed('beta')).toBe(true)
    expect(store.isGroupCollapsed('gamma')).toBe(false)
  })

  test('toggle flips state and persists both directions', async () => {
    const store = await freshStore()
    expect(store.isGroupCollapsed('alpha')).toBe(false)

    store.toggleGroupCollapsed('alpha')
    expect(store.isGroupCollapsed('alpha')).toBe(true)
    expect(JSON.parse(localStorage.getItem(KEY))).toEqual(['alpha'])

    store.toggleGroupCollapsed('alpha')
    expect(store.isGroupCollapsed('alpha')).toBe(false)
    expect(JSON.parse(localStorage.getItem(KEY))).toEqual([])
  })

  test('ignores a malformed persisted value', async () => {
    localStorage.setItem(KEY, 'not json{')
    const store = await freshStore()
    expect(store.isGroupCollapsed('anything')).toBe(false)
    // still usable after a bad load
    store.toggleGroupCollapsed('x')
    expect(store.isGroupCollapsed('x')).toBe(true)
  })

  test('ignores valid JSON that is not an array', async () => {
    localStorage.setItem(KEY, JSON.stringify({ alpha: true }))
    const store = await freshStore()
    expect(store.isGroupCollapsed('alpha')).toBe(false)
  })

  test('drops non-string entries from a persisted array', async () => {
    localStorage.setItem(KEY, JSON.stringify(['ok', 42, null, 'fine']))
    const store = await freshStore()
    expect(store.isGroupCollapsed('ok')).toBe(true)
    expect(store.isGroupCollapsed('fine')).toBe(true)
    expect(store.isGroupCollapsed('42')).toBe(false)
  })
})
