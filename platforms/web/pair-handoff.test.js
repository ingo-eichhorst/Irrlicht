import { describe, expect, test, vi } from 'vitest'
import { pairingCodeFromPath, isInstalledApp, continueInstalledPairing } from './pair-handoff.js'

describe('pairing install handoff', () => {
  test('extracts only a code-specific handoff path', () => {
    expect(pairingCodeFromPath('/pair/ABCD-EFGH/')).toBe('ABCD-EFGH')
    expect(pairingCodeFromPath('/pair/ABCD-EFGH/extra')).toBe('')
    expect(pairingCodeFromPath('/pair/not-a-code/')).toBe('')
  })

  test('detects manifest and legacy standalone modes', () => {
    expect(isInstalledApp(() => ({ matches: true }), false)).toBe(true)
    expect(isInstalledApp(() => ({ matches: false }), true)).toBe(true)
    expect(isInstalledApp(() => ({ matches: false }), false)).toBe(false)
  })

  test('an installed launch carries the code to the root app', () => {
    const locationObject = { pathname: '/pair/ABCD-EFGH/', replace: vi.fn() }
    const original = window.matchMedia
    window.matchMedia = () => ({ matches: true })
    expect(continueInstalledPairing(locationObject)).toBe(true)
    expect(locationObject.replace).toHaveBeenCalledWith('/?pair=ABCD-EFGH')
    window.matchMedia = original
  })
})
