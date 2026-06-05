import { describe, test, expect } from 'vitest'
import { agentsPerPage, inferDriverLabel, paginateAgents, renderMarkdown } from './viewer.js'

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

// Thresholds derive from AGENT_COL_PX=220 + MATRIX_RESERVED_PX=240:
// n columns fit at width ≥ 240 + 220·n (2→680, 3→900, 4→1120).
describe('agentsPerPage', () => {
  test('clamps to 2 on very narrow panes', () => {
    expect(agentsPerPage(0)).toBe(2)
    expect(agentsPerPage(400)).toBe(2)
    expect(agentsPerPage(679)).toBe(2)
  })

  test('steps 2 → 3 → 4 with width', () => {
    expect(agentsPerPage(680)).toBe(2)
    expect(agentsPerPage(899)).toBe(2)
    expect(agentsPerPage(900)).toBe(3)
    expect(agentsPerPage(1119)).toBe(3)
    expect(agentsPerPage(1120)).toBe(4)
  })

  test('clamps to 4 on wide panes', () => {
    expect(agentsPerPage(2000)).toBe(4)
    expect(agentsPerPage(10000)).toBe(4)
  })
})

describe('paginateAgents', () => {
  const six = ['claudecode', 'codex', 'pi', 'aider', 'opencode', 'kiro-cli']

  test('first page of 6 agents at perPage 4', () => {
    expect(paginateAgents(six, 0, 4)).toEqual({
      visible: ['claudecode', 'codex', 'pi', 'aider'],
      page: 0, pages: 2, start: 0, end: 4,
    })
  })

  test('last page holds the remainder', () => {
    expect(paginateAgents(six, 1, 4)).toEqual({
      visible: ['opencode', 'kiro-cli'],
      page: 1, pages: 2, start: 4, end: 6,
    })
  })

  test('out-of-range page clamps to the last page', () => {
    expect(paginateAgents(six, 9, 4).page).toBe(1)
    expect(paginateAgents(six, 9, 4).visible).toEqual(['opencode', 'kiro-cli'])
  })

  test('negative page clamps to the first page', () => {
    expect(paginateAgents(six, -1, 4).page).toBe(0)
  })

  test('agent count ≤ perPage → a single page with everything visible', () => {
    expect(paginateAgents(['a', 'b', 'c'], 0, 4)).toEqual({
      visible: ['a', 'b', 'c'],
      page: 0, pages: 1, start: 0, end: 3,
    })
  })

  test('empty agent list → one empty page', () => {
    expect(paginateAgents([], 3, 4)).toEqual({
      visible: [], page: 0, pages: 1, start: 0, end: 0,
    })
  })

  test('perPage change re-windows correctly (resize path)', () => {
    expect(paginateAgents(six, 2, 2)).toEqual({
      visible: ['opencode', 'kiro-cli'],
      page: 2, pages: 3, start: 4, end: 6,
    })
  })
})
