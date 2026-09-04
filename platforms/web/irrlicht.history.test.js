import { describe, test, expect, beforeEach } from 'vitest'

import { readMacosTokens } from './snapshots/serialize.js'
import {
  historyPriorityForState,
  decodeHistoryBuckets,
  historyStateForCode,
  dispatchRawFrame,
  historyByGranularity,
  normalizeSourcedFrame,
  HISTORY_MSG,
  compoundSessionId,
  HISTORY_BAR_COLORS,
} from './irrlicht.js'

// The history strip's wire format and decode path (#1805), split out of
// irrlicht.test.js so the strip's tests live together and that file stops
// growing. Everything below the first describe is new: before #1805 this whole
// path — the code table, the decoder, the dispatch that guards it, and the
// relay id-folding — had no coverage at all, while hardcoding a byte width, a
// bucket count and a `& 0x3` mask in three places.

describe('historyPriorityForState', () => {
  test('error has highest priority (3)', () => {
    expect(historyPriorityForState('error')).toBe(3)
  })

  test('waiting is 2, working is 1, ready is 0, unknown is -1', () => {
    expect(historyPriorityForState('waiting')).toBe(2)
    expect(historyPriorityForState('working')).toBe(1)
    expect(historyPriorityForState('ready')).toBe(0)
    expect(historyPriorityForState('unknown')).toBe(-1)
  })
})

const b64 = (bytes) => btoa(String.fromCharCode(...bytes))
const bytes = (n, fill) => new Array(n).fill(fill)

// THE SHARED VECTOR. Front-padded with the no-data sentinel, ladder in the last
// four slots — the shape encodePriorities actually emits for a partly-filled
// ring. A test that front-loaded the ladder would assert a payload the daemon
// never sends.
//
// The same 60 bytes are decoded by HistoryWireFormatTests.swift (`encoded`) and
// produced by history_wire_test.go's TestHistoryTracker_EncodeByteLayout. Three
// independent hand-written decoders, one vector.
const SHARED_VECTOR = [...bytes(56, 255), 0, 1, 2, 3]

describe('historyStateForCode', () => {
  test('the ladder maps to its four states in priority order', () => {
    expect(historyStateForCode(0)).toBe('ready')
    expect(historyStateForCode(1)).toBe('working')
    expect(historyStateForCode(2)).toBe('waiting')
    expect(historyStateForCode(3)).toBe('error')
  })

  test('the no-data sentinel is blank, never a state', () => {
    expect(historyStateForCode(255)).toBe('')
  })

  // The masking bug this replaced, pinned from both sides. `code & 0x3` folded
  // any off-ladder code onto a real state: 4 became `ready` (green, #1807) and
  // an older daemon's no-data 3 would now become `error` (red for a failure
  // that never happened). Blank is the only honest answer for a code this
  // build cannot name.
  test('a code from a newer daemon is blank, not folded onto a state', () => {
    for (const code of [4, 5, 7, 8, 64, 254]) {
      expect(historyStateForCode(code)).toBe('')
    }
  })
})

describe('decodeHistoryBuckets', () => {
  test('decodes one byte per bucket, oldest to newest', () => {
    const out = decodeHistoryBuckets(b64(SHARED_VECTOR))
    expect(out).not.toBeNull()
    expect(out.length).toBe(60)
    expect(out.slice(-4)).toEqual(['ready', 'working', 'waiting', 'error'])
    expect(out[0]).toBe('')
  })

  // The length check is the compatibility boundary, so it is asserted, not
  // assumed. 15 bytes is exactly what a pre-#1805 daemon ships (60 buckets x 2
  // bits). Dropping the whole message leaves the strip blank; a partial decode
  // would invent buckets.
  test('rejects an older daemon 15-byte packed payload outright', () => {
    expect(decodeHistoryBuckets(b64(bytes(15, 0xff)))).toBeNull()
  })

  test('rejects any other wrong length, and unparseable base64', () => {
    expect(decodeHistoryBuckets(b64(bytes(59, 0)))).toBeNull()
    expect(decodeHistoryBuckets(b64(bytes(61, 0)))).toBeNull()
    expect(decodeHistoryBuckets('!!!not base64!!!')).toBeNull()
  })
})

describe('dispatchRawFrame: history compatibility across daemon versions', () => {
  const SID = 'sess-1805'

  beforeEach(() => {
    for (const g of [1, 10, 60]) delete historyByGranularity[g][SID]
  })

  test('a current daemon snapshot fills the strip and error paints', () => {
    dispatchRawFrame({
      type: 'history_snapshot_v2',
      session_id: SID,
      history: { 1: b64([...bytes(59, 255), 3]) },
    })
    expect(historyByGranularity[1][SID][59]).toBe('error')
  })

  // THE REGRESSION THIS DESIGN EXISTS TO PREVENT (#1805).
  //
  // Wire code 3 means no-data to a pre-#1805 daemon and error to this build.
  // A new client reading an old daemon therefore reads "nothing happened" as
  // "the session failed" and paints a red bucket for an error that never
  // occurred — and an old daemon emits 3 continuously, since tick() maps its
  // negative sentinel through wireCode for every unfilled bucket.
  //
  // Renaming the message types is what removes the collision: this build never
  // reads an old daemon's frame at all. Run this test against the un-renamed
  // types and it fails with 'error' — that is how it was proven red.
  test('an older daemon tick cannot invent an error from its no-data code', () => {
    dispatchRawFrame({
      type: 'history_tick',
      granularity_sec: 1,
      buckets: { [SID]: 3 },
    })
    expect(historyByGranularity[1][SID]).toBeUndefined()
  })

  // Deliberately a payload this build WOULD accept (60 bytes, valid codes), so
  // the only thing that can reject it is the type name. Using an authentic
  // 15-byte packed payload here would pass for the wrong reason — the length
  // check would catch it, and the type guard would go untested. The 15-byte
  // case is covered on its own in decodeHistoryBuckets above.
  test('the type name alone rejects an older daemon snapshot and upgrade', () => {
    dispatchRawFrame({
      type: 'history_snapshot',
      session_id: SID,
      history: { 1: b64(bytes(60, 1)) },
    })
    dispatchRawFrame({ type: 'history_upgrade', session_id: SID, priority: 3 })
    expect(historyByGranularity[1][SID]).toBeUndefined()
  })
})


// --- #1805 follow-up: the relay's session-id folding --------------------------
//
// normalizeSourcedFrame folds the daemon id into a relayed frame's session
// identity (#537) so two daemons sharing a bare session_id stay distinct. It
// matches history frames BY TYPE NAME, and the #1805 rename updated
// dispatchRawFrame's copy of those names while leaving this one behind — so
// every relayed history frame skipped the fold, wrote under a bare id, and left
// the row (keyed by the compound id) permanently blank. Nothing went red,
// because this function had no test.
describe('normalizeSourcedFrame: relayed history frames', () => {
  const SRC = 'daemon-a'
  const BARE = 'sess-99'

  test('folds the source into a relayed snapshot session_id', () => {
    const out = normalizeSourcedFrame(SRC, { type: HISTORY_MSG.snapshot, session_id: BARE, history: {} })
    expect(out.session_id).toBe(compoundSessionId(SRC, BARE))
  })

  test('folds the source into a relayed upgrade session_id', () => {
    const out = normalizeSourcedFrame(SRC, { type: HISTORY_MSG.upgrade, session_id: BARE, priority: 3 })
    expect(out.session_id).toBe(compoundSessionId(SRC, BARE))
  })

  test('folds the source into every key of a relayed tick', () => {
    const out = normalizeSourcedFrame(SRC, {
      type: HISTORY_MSG.tick,
      granularity_sec: 1,
      buckets: { [BARE]: 3 },
      bucket_generations: { [BARE]: 7 },
    })
    const key = compoundSessionId(SRC, BARE)
    expect(Object.keys(out.buckets)).toEqual([key])
    expect(Object.keys(out.bucket_generations)).toEqual([key])
  })

  // The regression above in one assertion: every type dispatchRawFrame accepts
  // must also be folded. It holds by construction now that normalizeSourcedFrame
  // matches on FIELD SHAPE rather than type name — so read it as a LOCK on that
  // design, not as red-first proof. It still bites if anyone reverts the folding
  // to a type-name list, which is exactly how #1805 broke it.
  test('every dispatched history type is also a folded type', () => {
    for (const type of Object.values(HISTORY_MSG)) {
      const frame = { type, session_id: BARE, buckets: { [BARE]: 0 }, bucket_generations: {} }
      const out = normalizeSourcedFrame(SRC, frame)
      const folded = out.session_id === compoundSessionId(SRC, BARE) ||
        Object.keys(out.buckets)[0] === compoundSessionId(SRC, BARE)
      expect(folded, `${type} was not folded — relay strips will render blank`).toBe(true)
    }
  })

  test('a frame with no source passes through untouched', () => {
    const out = normalizeSourcedFrame('', { type: HISTORY_MSG.snapshot, session_id: BARE })
    expect(out.session_id).toBe(BARE)
  })
})

// The strip's `error` bucket and the macOS bar must be the same red. The web
// palette is hardcoded on purpose — paintRowHistory runs at canvas-paint time
// and must not read computed styles — so nothing but this check stops it
// drifting from the Swift token. Loud in both directions (#1797's rule): the
// extraction is asserted before the comparison, so a DELETED IrrHex.error fails
// here rather than silently comparing against nothing.
describe('history bar error colour agrees with macOS', () => {
  test('HISTORY_BAR_COLORS.error equals IrrHex.error', () => {
    const swiftDecl = readMacosTokens().match(/static let error\s*=\s*"(#[0-9A-Fa-f]{6})"/)
    expect(swiftDecl, 'no IrrHex.error found in platforms/macos/Irrlicht/Theme/Tokens.swift').not.toBeNull()
    expect(HISTORY_BAR_COLORS.error.toUpperCase()).toBe(swiftDecl[1].toUpperCase())
  })
})
