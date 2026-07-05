import { describe, test, expect } from 'vitest'
import { paintStateBand, paintEventDots, paintTurns, paintExpectedLane } from './playbackView.js'

const bandEvents = [
  { session_id: 's1', kind: 'transcript_new', offset_ms: 0 },
  { session_id: 's1', kind: 'state_transition', new_state: 'working', offset_ms: 0 },
]

describe('paintStateBand', () => {
  test('renders one region per segment, carrying the tooltip', () => {
    const el = document.createElement('div')
    paintStateBand(el, bandEvents, 100)
    expect(el.children).toHaveLength(1)
    expect(el.children[0].getAttribute('data-tip')).toContain('working')
  })

  test('repaint clears the previous contents (no accumulation)', () => {
    const el = document.createElement('div')
    paintStateBand(el, bandEvents, 100)
    paintStateBand(el, bandEvents, 100)
    expect(el.children).toHaveLength(1)
    paintStateBand(el, [], 0) // no duration → cleared, empty
    expect(el.children).toHaveLength(0)
  })
})

describe('paintEventDots / paintTurns', () => {
  test('one dot per event', () => {
    const el = document.createElement('div')
    paintEventDots(el, [{ kind: 'transcript_new', session_id: 'proc-1', offset_ms: 50 }], 100)
    expect(el.children).toHaveLength(1)
    expect(el.children[0].getAttribute('data-tip')).toContain('session: proc-1')
  })

  test('one tick per turn', () => {
    const el = document.createElement('div')
    paintTurns(el, [{ role: 'user', offset_ms: 25, text: 'hi' }, { role: 'assistant', offset_ms: 75, text: 'yo' }], 100)
    expect(el.children).toHaveLength(2)
  })
})

describe('paintExpectedLane', () => {
  test('no expected.jsonl → a single grey "not configured" note', () => {
    const el = document.createElement('div')
    paintExpectedLane(el, null, 100)
    expect(el.children).toHaveLength(1)
    expect(el.children[0].textContent).toBe('expected: not configured')
  })

  test('no duration → nothing painted', () => {
    const el = document.createElement('div')
    paintExpectedLane(el, { phases: [{ phase: 'p', pass: true }] }, 0)
    expect(el.children).toHaveLength(0)
  })

  test('a matched phase → one positioned marker', () => {
    const el = document.createElement('div')
    paintExpectedLane(el, {
      recording_start: '2024-01-01T00:00:00.000Z',
      phases: [{ phase: 'p1', pass: true, matched_ts: '2024-01-01T00:00:00.500Z' }],
      definitions: [{ expected_state: 'working' }],
    }, 1000)
    expect(el.children).toHaveLength(1)
    expect(el.children[0].getAttribute('data-tip')).toContain('p1 — PASS')
  })
})
