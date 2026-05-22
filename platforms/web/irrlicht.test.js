import { describe, test, expect, beforeEach } from 'vitest'
import {
  resolvedTheme,
  rowLabel,
  maybeNotifyOnUpdate,
  formatCost,
  pressureClass,
  historyPriorityForState,
} from './irrlicht.js'

describe('resolvedTheme', () => {
  beforeEach(() => localStorage.clear())

  test('returns stored preference when set', () => {
    localStorage.setItem('irrlicht_theme', 'light')
    expect(resolvedTheme()).toBe('light')
  })

  test('falls back to dark when no stored pref and matchMedia reports dark', () => {
    // setup stub returns matches:false for light → resolves to 'dark'
    expect(resolvedTheme()).toBe('dark')
  })
})

describe('rowLabel', () => {
  test('formats project · branch when both present', () => {
    expect(rowLabel({ project_name: 'myapp', git_branch: 'feat/x' }))
      .toBe('myapp · feat/x')
  })

  test('falls back to project name when branch is absent', () => {
    expect(rowLabel({ project_name: 'myapp' })).toBe('myapp')
  })

  test('falls back to branch when project is absent', () => {
    expect(rowLabel({ git_branch: 'main' })).toBe('main')
  })

  test('falls back to truncated session_id when both absent', () => {
    expect(rowLabel({ session_id: 'abcdef123456' })).toBe('abcdef12')
  })

  test('returns "session" when all fields are absent', () => {
    expect(rowLabel({})).toBe('session')
  })
})

describe('maybeNotifyOnUpdate', () => {
  test('does nothing when next is null', () => {
    // should not throw
    maybeNotifyOnUpdate(null, null)
  })

  test('handles state transition from working to ready without throwing', () => {
    const prev = { state: 'working', session_id: 'abc', project_name: 'p' }
    const next = { state: 'ready',   session_id: 'abc', project_name: 'p' }
    maybeNotifyOnUpdate(prev, next)
  })
})

describe('formatCost', () => {
  test('returns empty string for zero or falsy', () => {
    expect(formatCost(0)).toBe('')
    expect(formatCost(null)).toBe('')
  })

  test('formats positive values with dollar sign and two decimals', () => {
    expect(formatCost(1.5)).toBe('$1.50')
    expect(formatCost(0.123)).toBe('$0.12')
  })
})

describe('pressureClass', () => {
  test('maps critical to "critical"', () => {
    expect(pressureClass('critical')).toBe('critical')
  })

  test('maps warning and high to "high"', () => {
    expect(pressureClass('warning')).toBe('high')
    expect(pressureClass('high')).toBe('high')
  })

  test('maps caution and medium to "medium"', () => {
    expect(pressureClass('caution')).toBe('medium')
    expect(pressureClass('medium')).toBe('medium')
  })

  test('returns empty string for unknown levels', () => {
    expect(pressureClass('low')).toBe('')
    expect(pressureClass('')).toBe('')
  })
})

describe('historyPriorityForState', () => {
  test('waiting has highest priority (2)', () => {
    expect(historyPriorityForState('waiting')).toBe(2)
  })

  test('working is 1, ready is 0, unknown is -1', () => {
    expect(historyPriorityForState('working')).toBe(1)
    expect(historyPriorityForState('ready')).toBe(0)
    expect(historyPriorityForState('unknown')).toBe(-1)
  })
})
