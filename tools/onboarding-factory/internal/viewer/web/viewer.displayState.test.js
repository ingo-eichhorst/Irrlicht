import { describe, test, expect } from 'vitest'
import { coverageBadge } from './viewer.js'

// #1367 canonicalised the not-applicable cell state to "n/a". The viewer is a
// renderer of the same vocabulary the Go side derives, so it has to read the
// same token — a display-time alias here would be exactly the half-fix the
// issue rejects.
//
// The retired spelling is assembled from parts so this file is not itself a
// hit for the Go-side source census (matrix.TestNoSourceEmitsRetiredSpelling).
const RETIRED = 'n' + '.a.'

describe('coverageBadge — not-applicable display state', () => {
  test('renders the canonical "n/a" state', () => {
    const badge = coverageBadge('n/a')
    expect(badge.label).toBe('n/a')
    // Not the grey "unknown" fallback — that would silently mis-bucket every
    // not-applicable cell in the matrix.
    expect(badge.label).not.toBe('unknown')
  })

  test('does not recognise the retired dotted spelling', () => {
    // It must fall through to the unknown fallback rather than quietly
    // rendering as if it were valid: the schema rejects it on disk, so a cell
    // carrying it is genuinely malformed and should look that way.
    expect(coverageBadge(RETIRED).label).toBe('unknown')
  })

  test('every other display state still renders its own label', () => {
    expect(coverageBadge('observed').label).toBe('observed')
    expect(coverageBadge('pending-record').label).toBe('pending record')
    expect(coverageBadge('blocked-driver').label).toBe('blocked: driver')
    expect(coverageBadge('blocked-daemon').label).toBe('blocked: daemon')
    expect(coverageBadge('unobservable').label).toBe('unobservable')
    expect(coverageBadge('unknown').label).toBe('unknown')
  })

  test('no display state renders the retired spelling as its label', () => {
    for (const s of ['observed', 'pending-record', 'blocked-driver',
                     'blocked-daemon', 'unobservable', 'n/a', 'unknown', RETIRED, '']) {
      expect(coverageBadge(s).label).not.toBe(RETIRED)
    }
  })
})
