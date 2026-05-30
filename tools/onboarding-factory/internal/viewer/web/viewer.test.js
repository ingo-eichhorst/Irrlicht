import { describe, test, expect } from 'vitest'
import { inferDriverLabel, renderMarkdown } from './viewer.js'

describe('renderMarkdown', () => {
  test('## / ### headings → h4 / h5', () => {
    expect(renderMarkdown('## Verdict')).toBe('<h4>Verdict</h4>')
    expect(renderMarkdown('### Reasoning')).toBe('<h5>Reasoning</h5>')
  })

  test('**bold** and `code` inline', () => {
    expect(renderMarkdown('never enters **waiting**')).toBe('<p>never enters <strong>waiting</strong></p>')
    expect(renderMarkdown('the `update_plan` tool')).toBe('<p>the <code>update_plan</code> tool</p>')
  })

  test('- bullets and 1. numbered lists', () => {
    expect(renderMarkdown('- one\n- two')).toBe('<ul><li>one</li><li>two</li></ul>')
    expect(renderMarkdown('1. first\n2. second')).toBe('<ol><li>first</li><li>second</li></ol>')
  })

  test('escapes HTML before rendering (no injection)', () => {
    expect(renderMarkdown('a <script>x</script> & b')).toBe('<p>a &lt;script&gt;x&lt;/script&gt; &amp; b</p>')
  })

  test('plain " 0 " in prose is NOT turned into a code span', () => {
    expect(renderMarkdown('Rule 0 fires')).toBe('<p>Rule 0 fires</p>')
  })

  test('** inside a code span stays literal', () => {
    expect(renderMarkdown('`a ** b`')).toBe('<p><code>a ** b</code></p>')
  })

  test('blank line separates paragraphs', () => {
    expect(renderMarkdown('one\n\ntwo')).toBe('<p>one</p>\n<p>two</p>')
  })

  test('empty / nullish input → empty string', () => {
    expect(renderMarkdown('')).toBe('')
    expect(renderMarkdown(null)).toBe('')
  })
})

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
