import { describe, test, expect } from 'vitest'
import { inferDriverLabel } from './viewer.js'

// Regression test for #432: renderRecipePanel always showed "Headless one-shot"
// even for tmux-REPL scenarios because it checked recipe.driver/recipe.interactive
// instead of the actual recipe.script array.
describe('inferDriverLabel', () => {
  test('non-empty script array → Interactive (tmux REPL)', () => {
    expect(inferDriverLabel({ script: ['claude --print "hello"', 'assert output'] }))
      .toBe('Interactive (tmux REPL)')
  })

  test('single-element script array → Interactive (tmux REPL)', () => {
    expect(inferDriverLabel({ script: ['step'] }))
      .toBe('Interactive (tmux REPL)')
  })

  test('empty script array → Headless one-shot', () => {
    expect(inferDriverLabel({ script: [] }))
      .toBe('Headless one-shot')
  })

  test('prompt-only entry → Headless one-shot', () => {
    expect(inferDriverLabel({ prompt: 'Reply with exactly the word: ok', timeout_seconds: 60 }))
      .toBe('Headless one-shot')
  })

  test('null → Headless one-shot', () => {
    expect(inferDriverLabel(null)).toBe('Headless one-shot')
  })

  test('undefined → Headless one-shot', () => {
    expect(inferDriverLabel(undefined)).toBe('Headless one-shot')
  })

  test('empty object → Headless one-shot', () => {
    expect(inferDriverLabel({})).toBe('Headless one-shot')
  })
})
