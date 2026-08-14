import { describe, test, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { WEB_DIR } from './shippedFiles.testutil.js'

// On-device composition + ledger fold (docs/mobile-notifications-arc42.md
// §8.2, §8.4, §8.5). sw.js is a classic script structured as pure functions
// on `self`, so these tests evaluate the file against a stubbed `self` and
// drive the functions (and the registered handlers) directly.

const swSource = readFileSync(join(WEB_DIR, 'sw.js'), 'utf8')

function loadWorkerFrom(source) {
  const self = {
    listeners: Object.create(null),
    addEventListener(type, fn) {
      ;(this.listeners[type] ||= []).push(fn)
    },
    registration: { showNotification: vi.fn(() => Promise.resolve()) },
    clients: {
      matchAll: vi.fn(() => Promise.resolve([])),
      openWindow: vi.fn(() => Promise.resolve(null)),
    },
  }
  new Function('self', source)(self)
  return self
}

function loadWorker() {
  return loadWorkerFrom(swSource)
}

// Committed mutation evidence (AGENTS.md "Testing"): the two structural
// properties below hold by construction against a correct sw.js, so each is
// also driven against a copy of the source with the property deliberately
// removed — the arm that must report the opposite verdict. A mutation that
// stopped applying would silently turn both arms into the same arm, so a
// no-op edit throws rather than returning the source unchanged.
function mutate(source, from, to) {
  const out = source.replace(from, to)
  if (out === source) {
    throw new Error('stale mutation: sw.js no longer contains ' + JSON.stringify(from))
  }
  return out
}

function pushEvent(data) {
  const ev = {
    data,
    chain: null,
    waitUntil(p) {
      this.chain = p
    },
  }
  return ev
}

// The injected ledger backend the fold logic is driven against (jsdom has no
// IndexedDB and the repo takes no new deps). It answers the same four-method
// shape idbLedgerBackend does, including `all()`'s {key, value} rows — the
// badge reads the KEY space, so a backend that returned bare values would
// make every badge arm pass for the wrong reason. `readsNull` reproduces the
// unreadable ledger, which arc42 §8.3 keeps distinct from an empty one.
function mapBackend({ seed = {}, readsNull = false } = {}) {
  const rows = new Map(Object.entries(seed))
  const puts = []
  const removes = []
  return {
    rows,
    puts,
    removes,
    put(key, value) {
      puts.push([key, value])
      rows.set(key, value)
      return Promise.resolve()
    },
    get(key) {
      return Promise.resolve(rows.has(key) ? rows.get(key) : null)
    },
    all() {
      if (readsNull) return Promise.resolve(null)
      return Promise.resolve([...rows].map(([key, value]) => ({ key, value })))
    },
    remove(key) {
      removes.push(key)
      rows.delete(key)
      return Promise.resolve()
    },
  }
}

// A `self.navigator` carrying the Badging API, or deliberately missing it.
// Absent is the common case, not the exotic one — every desktop Firefox and
// Safari outside an installed app — so it is a fixture rather than an aside.
function badgingNavigator({ absent = false, throws = false, rejects = false } = {}) {
  const nav = { set: [], cleared: 0 }
  if (absent) return nav
  nav.setAppBadge = vi.fn((n) => {
    nav.set.push(n)
    if (throws) throw new TypeError('badging is not available')
    return rejects ? Promise.reject(new Error('denied')) : Promise.resolve()
  })
  nav.clearAppBadge = vi.fn(() => {
    nav.cleared++
    if (throws) throw new TypeError('badging is not available')
    return rejects ? Promise.reject(new Error('denied')) : Promise.resolve()
  })
  return nav
}

describe('composeNotification (arc42 §8.2/§8.4)', () => {
  const sw = loadWorker()

  test('session waiting → "<label> needs input", body=project, tag=session id', () => {
    const n = sw.composeNotification({
      v: 1, kind: 'session', session_id: 's-1', label: 'claude-code',
      project: 'irrlicht', state: 'waiting', at: 1755000000, renotify: false,
    })
    expect(n.title).toBe('claude-code needs input')
    expect(n.body).toBe('irrlicht')
    expect(n.tag).toBe('s-1')
    expect(n.renotify).toBe(false)
  })

  test('session ready → "<label> is ready", renotify carried from the payload', () => {
    const n = sw.composeNotification({
      v: 1, kind: 'session', session_id: 's-2', label: 'codex',
      project: 'webapp', state: 'ready', at: 1755000000, renotify: true,
    })
    expect(n.title).toBe('codex is ready')
    expect(n.body).toBe('webapp')
    expect(n.tag).toBe('s-2')
    expect(n.renotify).toBe(true)
  })

  test('summary → "N agents need attention" + member labels, tag "summary"', () => {
    const n = sw.composeNotification({
      v: 1, kind: 'summary', count: 3, sessions: ['claude-code', 'codex', 'aider'],
      at: 1755000000, renotify: true,
    })
    expect(n.title).toBe('3 agents need attention')
    expect(n.body).toBe('claude-code, codex, aider')
    expect(n.tag).toBe('summary')
    expect(n.renotify).toBe(true)
  })

  test('daemon_down → "Mac <label> disconnected", tag "daemon:<id>"', () => {
    const n = sw.composeNotification({
      v: 1, kind: 'daemon_down', daemon_id: 'd-9', daemon_label: 'laptop',
      at: 1755000000, renotify: false,
    })
    expect(n.title).toBe('Mac laptop disconnected')
    expect(n.tag).toBe('daemon:d-9')
    expect(n.renotify).toBe(false)
  })

  test('daemon_up → "Mac <label> reconnected", tag "daemon:<id>"', () => {
    const n = sw.composeNotification({
      v: 1, kind: 'daemon_up', daemon_id: 'd-9', daemon_label: 'laptop',
      at: 1755000000, renotify: true,
    })
    expect(n.title).toBe('Mac laptop reconnected')
    expect(n.tag).toBe('daemon:d-9')
    expect(n.renotify).toBe(true)
  })

  test('an absent daemon_id still collapses onto the relay\'s own key, and never reads "Mac undefined"', () => {
    // daemon_id is `omitempty` on the wire (Payload in
    // core/domain/notify/notify.go), so an empty id simply is not sent — while
    // the relay's collapse key stays daemonTopic("") = "daemon:" (engine.go).
    // The tag must equal it byte for byte or a later daemon notification stops
    // replacing the earlier one (arc42 §8.4, R3).
    const down = sw.composeNotification({ v: 1, kind: 'daemon_down', at: 1755000000, renotify: false })
    expect(down.tag).toBe('daemon:')
    expect(down.title).not.toContain('undefined')
    const up = sw.composeNotification({ v: 1, kind: 'daemon_up', at: 1755000000, renotify: true })
    expect(up.tag).toBe('daemon:')
    expect(up.title).not.toContain('undefined')
  })

  test('a daemon payload with an id but no label falls back to the id, as the session kind does', () => {
    const n = sw.composeNotification({ v: 1, kind: 'daemon_down', daemon_id: 'd-9', at: 1755000000 })
    expect(n.title).toBe('Mac d-9 disconnected')
    expect(n.tag).toBe('daemon:d-9')
  })

  test('v:2 → generic fallback, never a mis-render of unknown fields', () => {
    const n = sw.composeNotification({
      v: 2, kind: 'session', session_id: 's-3', label: 'future', state: 'waiting',
    })
    expect(n.title).toBe('Irrlicht')
    expect(n.body).toBe('Update the app page')
    expect(n.tag).toBe('irrlicht-update')
  })

  test('unknown kind → generic fallback', () => {
    const n = sw.composeNotification({ v: 1, kind: 'hologram', at: 1755000000 })
    expect(n.title).toBe('Irrlicht')
    expect(n.body).toBe('Update the app page')
  })
})

describe('push handler (arc42 §6.2)', () => {
  test('shows the composed notification with tag + renotify in the options', async () => {
    const sw = loadWorker()
    const ev = pushEvent({
      json: () => ({
        v: 1, kind: 'session', session_id: 's-7', label: 'claude-code',
        project: 'irrlicht', state: 'ready', at: 1755000000, renotify: true,
      }),
    })
    sw.listeners.push[0](ev)
    await ev.chain
    expect(sw.registration.showNotification).toHaveBeenCalledTimes(1)
    const [title, options] = sw.registration.showNotification.mock.calls[0]
    expect(title).toBe('claude-code is ready')
    expect(options.tag).toBe('s-7')
    expect(options.renotify).toBe(true)
  })

  test('malformed JSON → generic fallback, no throw', async () => {
    const sw = loadWorker()
    const ev = pushEvent({
      json: () => {
        throw new SyntaxError('unexpected token')
      },
    })
    sw.listeners.push[0](ev)
    await ev.chain
    const [title, options] = sw.registration.showNotification.mock.calls[0]
    expect(title).toBe('Irrlicht')
    expect(options.body).toBe('Update the app page')
  })
})

describe('ledger fold (arc42 §8.5)', () => {
  test('a session payload writes {state,label,project,at} under the session id', async () => {
    const sw = loadWorker()
    const backend = mapBackend()
    await sw.ledgerStore(backend).update({
      v: 1, kind: 'session', session_id: 's-1', label: 'claude-code',
      project: 'irrlicht', state: 'waiting', at: 1755000000, renotify: false,
    })
    expect(backend.puts).toEqual([
      ['s-1', { state: 'waiting', label: 'claude-code', project: 'irrlicht', at: 1755000000 }],
    ])
  })

  test('non-session and unknown-version payloads write nothing', async () => {
    const sw = loadWorker()
    const backend = mapBackend()
    await sw.ledgerStore(backend).update({ v: 1, kind: 'summary', count: 2, sessions: ['a', 'b'] })
    await sw.ledgerStore(backend).update({ v: 2, kind: 'session', session_id: 's-1', state: 'ready' })
    await sw.ledgerStore(backend).update(null)
    expect(backend.puts).toEqual([])
  })
})

describe('ledger read path (arc42 §8.5)', () => {
  test('what the push fold wrote reads back under the same key', async () => {
    const sw = loadWorker()
    const store = sw.ledgerStore(mapBackend())
    await store.update({
      v: 1, kind: 'session', session_id: 's-1', label: 'claude-code',
      project: 'irrlicht', state: 'waiting', at: 1755000000,
    })
    expect(await store.get('s-1')).toEqual({
      state: 'waiting', label: 'claude-code', project: 'irrlicht', at: 1755000000,
    })
    expect(await store.get('s-nobody')).toBeNull()
  })

  test('an empty session id answers null without asking the backend', async () => {
    // The caller is a notification whose payload carried no id (summary and
    // daemon kinds carry none), and a read on '' would open a transaction to
    // learn nothing.
    const sw = loadWorker()
    const backend = mapBackend()
    let asked = false
    backend.get = () => { asked = true; return Promise.resolve(null) }
    expect(await sw.ledgerStore(backend).get('')).toBeNull()
    expect(asked).toBe(false)
  })

  test('all() answers the rows with their keys — the badge counts keys, not values', async () => {
    const sw = loadWorker()
    const store = sw.ledgerStore(mapBackend({ seed: { 's-1': { state: 'waiting' } } }))
    expect(await store.all()).toEqual([{ key: 's-1', value: { state: 'waiting' } }])
  })
})

describe('the production ledger backend actually opens a database', () => {
  // jsdom has no IndexedDB and the repo takes no new deps, so the backend is
  // driven against a minimal fake — faithful in the three ways it depends on:
  // upgrade fires before success, request callbacks are assigned AFTER the
  // call that returns the request, and a transaction completes only once its
  // requests have. Not a general implementation; enough to prove the
  // production path opens a database and addresses it the right way round.
  function fakeIndexedDB({ data = new Map(), failOpen = false } = {}) {
    const stores = new Map()
    const idb = { opens: 0, closes: 0, data }
    idb.open = () => {
      idb.opens++
      const req = {}
      queueMicrotask(() => {
        if (failOpen) return req.onerror?.()
        const db = {
          objectStoreNames: { contains: (n) => stores.has(n) },
          createObjectStore: (n) => stores.set(n, data),
          close: () => { idb.closes++ },
          transaction: (name, mode) => {
            const rows = stores.get(name)
            const queue = []
            const tx = {}
            tx.objectStore = () => ({
              // (value, key) — the signature of a store with no key path. A
              // backend calling put(key, value) writes the id as the record,
              // which reads back as nothing at all.
              put: (value, key) => queue.push(() => {
                if (mode !== 'readwrite') throw new DOMException('read-only', 'ReadOnlyError')
                rows.set(key, value)
              }),
              delete: (key) => queue.push(() => rows.delete(key)),
              get: (key) => {
                const r = {}
                queue.push(() => { r.result = rows.get(key); r.onsuccess?.() })
                return r
              },
              openCursor: () => {
                const r = {}
                const keys = [...rows.keys()]
                let i = 0
                const step = () => {
                  if (i >= keys.length) { r.result = null; return r.onsuccess?.() }
                  const key = keys[i++]
                  r.result = { key, value: rows.get(key), continue: () => queue.push(step) }
                  r.onsuccess?.()
                }
                queue.push(step)
                return r
              },
            })
            queueMicrotask(() => {
              while (queue.length) queue.shift()()
              tx.oncomplete?.()
            })
            return tx
          },
        }
        req.result = db
        if (!stores.has('sessions')) req.onupgradeneeded?.()
        req.onsuccess?.()
      })
      return req
    }
    return idb
  }

  test('a write reads back — and is keyed by the session id, not by the record', async () => {
    const sw = loadWorker()
    sw.indexedDB = fakeIndexedDB()
    const backend = sw.idbLedgerBackend()
    await backend.put('s-1', { state: 'waiting', at: 1755000000 })
    expect(await backend.get('s-1')).toEqual({ state: 'waiting', at: 1755000000 })
    expect(sw.indexedDB.data.get('s-1')).toEqual({ state: 'waiting', at: 1755000000 })
    expect(sw.indexedDB.opens).toBeGreaterThan(0)
    // Every operation closes the database it opened; a leaked handle blocks
    // the next version change forever.
    expect(sw.indexedDB.closes).toBe(sw.indexedDB.opens)
  })

  test('all() walks a cursor so each row carries its key, and remove() drops one', async () => {
    const sw = loadWorker()
    sw.indexedDB = fakeIndexedDB()
    const backend = sw.idbLedgerBackend()
    await backend.put('s-1', { state: 'waiting' })
    await backend.put('s-2', { state: 'ready' })
    expect(await backend.all()).toEqual([
      { key: 's-1', value: { state: 'waiting' } },
      { key: 's-2', value: { state: 'ready' } },
    ])
    await backend.remove('s-1')
    expect(await backend.all()).toEqual([{ key: 's-2', value: { state: 'ready' } }])
  })

  test('a session it never saw reads null, not undefined', async () => {
    const sw = loadWorker()
    sw.indexedDB = fakeIndexedDB()
    expect(await sw.idbLedgerBackend().get('s-nobody')).toBeNull()
  })

  test('a database that will not open reads null, never an empty ledger', async () => {
    // arc42 §8.3 again, at the layer below the fold: `[]` would mean "no
    // session needs you" and clear the badge on a storage failure.
    const sw = loadWorker()
    sw.indexedDB = fakeIndexedDB({ failOpen: true })
    expect(await sw.idbLedgerBackend().all()).toBeNull()
    await expect(sw.idbLedgerBackend().put('s-1', {})).resolves.toBeNull()
  })

  test('no IndexedDB at all is the same answer, not a throw', async () => {
    const sw = loadWorker()
    expect(sw.indexedDB).toBeUndefined()
    expect(await sw.idbLedgerBackend().all()).toBeNull()
  })
})

describe('the live fold (arc42 §8.5) — the half that lets the badge fall', () => {
  test('the visible set replaces the ledger: named rows are written, unnamed ones dropped', async () => {
    // §8.4 never pushes on `* → working`, so a session that stopped needing
    // input, and a session that ended, are both invisible to the push fold.
    // The dashboard's own view is the correcting signal.
    const sw = loadWorker()
    const backend = mapBackend({
      seed: { 's-1': { state: 'waiting' }, 's-gone': { state: 'waiting' } },
    })
    await sw.ledgerStore(backend).foldLiveSessions([
      { session_id: 's-1', state: 'working', label: 'claude-code', project: 'irrlicht', at: 1755000100 },
      { session_id: 's-2', state: 'ready', label: 'codex', project: 'webapp', at: 1755000100 },
    ])
    expect([...backend.rows.keys()].sort()).toEqual(['s-1', 's-2'])
    expect(backend.rows.get('s-1')).toEqual({
      state: 'working', label: 'claude-code', project: 'irrlicht', at: 1755000100,
    })
    expect(backend.removes).toEqual(['s-gone'])
  })

  test('rows without a session id are ignored rather than written under undefined', async () => {
    const sw = loadWorker()
    const backend = mapBackend()
    await sw.ledgerStore(backend).foldLiveSessions([{ state: 'waiting' }, null, undefined])
    expect(backend.puts).toEqual([])
  })

  test('a non-array snapshot empties nothing it cannot read', async () => {
    const sw = loadWorker()
    const backend = mapBackend({ seed: { 's-1': { state: 'waiting' } } })
    await sw.ledgerStore(backend).foldLiveSessions(undefined)
    // Nothing was named, so everything is dropped — the honest reading of an
    // empty live set. What must NOT happen is a throw that takes the message
    // handler's reply with it.
    expect(backend.rows.size).toBe(0)
  })

  test('an unreadable ledger drops nothing — "could not look" is not "nothing there"', async () => {
    // arc42 §8.3: absence of a finding and inability to look must never
    // produce the same output. A null read that emptied the ledger would make
    // one IndexedDB hiccup indistinguishable from every session ending.
    const sw = loadWorker()
    const backend = mapBackend({ seed: { 's-old': { state: 'waiting' } }, readsNull: true })
    await sw.ledgerStore(backend).foldLiveSessions([{ session_id: 's-1', state: 'working' }])
    expect(backend.removes).toEqual([])
  })
})

describe('badge (arc42 §8.5)', () => {
  test('counts `waiting` and nothing else', () => {
    const sw = loadWorker()
    expect(sw.badgeCount([
      { key: 'a', value: { state: 'waiting' } },
      { key: 'b', value: { state: 'ready' } },
      { key: 'c', value: { state: 'working' } },
      { key: 'd', value: { state: 'waiting' } },
      { key: 'e', value: null },
    ])).toBe(2)
  })

  test('`ready` alone leaves the badge at zero', () => {
    // The two states make different claims: `waiting` is "an agent is blocked
    // on you", `ready` is "work you could pick up" — which a finished session
    // keeps asserting forever, so counting it would rebuild the
    // climbs-and-never-falls badge the live fold exists to remove.
    const sw = loadWorker()
    expect(sw.badgeCount([{ key: 'a', value: { state: 'ready' } }])).toBe(0)
  })

  test('an unreadable ledger answers null, which applyBadge leaves alone', async () => {
    const sw = loadWorker()
    expect(sw.badgeCount(null)).toBeNull()
    const nav = badgingNavigator()
    sw.navigator = nav
    await sw.applyBadge(null)
    expect(nav.setAppBadge).not.toHaveBeenCalled()
    expect(nav.clearAppBadge).not.toHaveBeenCalled()
  })

  test('a missing Badging API costs nothing and throws nothing', async () => {
    const sw = loadWorker()
    sw.navigator = badgingNavigator({ absent: true })
    await expect(sw.applyBadge(3)).resolves.toBeUndefined()
    await expect(sw.applyBadge(0)).resolves.toBeUndefined()
    // And with no `navigator` at all, which is what a stubbed worker scope and
    // an old WebKit have in common.
    delete sw.navigator
    await expect(sw.applyBadge(3)).resolves.toBeUndefined()
  })

  test('a Badging API that throws or rejects does not break the chain it rides', async () => {
    // The chain is the feature; no badge is a better outcome than a
    // notification the worker never got to show.
    const sw = loadWorker()
    sw.navigator = badgingNavigator({ throws: true })
    await expect(sw.applyBadge(2)).resolves.toBeUndefined()
    sw.navigator = badgingNavigator({ rejects: true })
    await expect(sw.applyBadge(2)).resolves.toBeUndefined()
  })

  test('zero clears rather than setting 0', async () => {
    const sw = loadWorker()
    const nav = badgingNavigator()
    sw.navigator = nav
    await sw.applyBadge(0)
    expect(nav.setAppBadge).not.toHaveBeenCalled()
    expect(nav.cleared).toBe(1)
  })

  test('DECAY: a session leaving `waiting` lowers the count the push fold raised', async () => {
    // The assertion this whole slice's live-view decision exists to make
    // possible (arc42 §8.5, ADR-9). Two pushes take the badge to 2 — that half
    // works from push alone. Only the live fold can bring it back down.
    const sw = loadWorker()
    const nav = badgingNavigator()
    sw.navigator = nav
    const store = sw.ledgerStore(mapBackend())

    for (const id of ['s-1', 's-2']) {
      await store.update({ v: 1, kind: 'session', session_id: id, state: 'waiting', at: 1755000000 })
    }
    await sw.refreshBadge(store)
    expect(nav.set.at(-1)).toBe(2)

    await store.foldLiveSessions([
      { session_id: 's-1', state: 'working', at: 1755000100 },
      { session_id: 's-2', state: 'waiting', at: 1755000100 },
    ])
    await sw.refreshBadge(store)
    expect(nav.set.at(-1)).toBe(1)

    // And a session that ends entirely — never pushed about at all — leaves
    // with its row.
    await store.foldLiveSessions([{ session_id: 's-1', state: 'working', at: 1755000200 }])
    await sw.refreshBadge(store)
    expect(nav.cleared).toBe(1)
    expect(nav.set.at(-1)).toBe(1)
  })
})

describe('messages from the app (arc42 §8.5)', () => {
  // A worker built with a live in-memory ledger, so a message handler drives
  // the same store the next message reads.
  function workerWithLedger(opts) {
    const sw = loadWorker()
    const backend = mapBackend(opts)
    sw.idbLedgerBackend = () => backend
    sw.navigator = badgingNavigator()
    return { sw, backend }
  }

  function messageEvent(data) {
    const replies = []
    const ev = {
      data,
      ports: [{ postMessage: (v) => replies.push(v) }],
      chain: null,
      waitUntil(p) { this.chain = p },
      replies,
    }
    return ev
  }

  test('a live-sessions message folds, refreshes the badge, and answers', async () => {
    const { sw, backend } = workerWithLedger({ seed: { 's-gone': { state: 'waiting' } } })
    const ev = messageEvent({
      type: 'beacon-live-sessions',
      sessions: [{ session_id: 's-1', state: 'waiting', at: 1755000100 }],
    })
    sw.listeners.message[0](ev)
    await ev.chain
    expect([...backend.rows.keys()]).toEqual(['s-1'])
    expect(sw.navigator.set.at(-1)).toBe(1)
    expect(ev.replies).toEqual([{ ok: true }])
  })

  test('a ledger-get message answers the entry, and null for a session it never saw', async () => {
    const { sw } = workerWithLedger({ seed: { 's-1': { state: 'waiting', at: 1755000000 } } })
    const hit = messageEvent({ type: 'beacon-ledger-get', session_id: 's-1' })
    sw.listeners.message[0](hit)
    await hit.chain
    expect(hit.replies).toEqual([{ entry: { state: 'waiting', at: 1755000000 } }])

    const miss = messageEvent({ type: 'beacon-ledger-get', session_id: 's-nobody' })
    sw.listeners.message[0](miss)
    await miss.chain
    expect(miss.replies).toEqual([{ entry: null }])
  })

  test('a message this worker does not understand is still ANSWERED', async () => {
    // An older worker meeting a newer app is exactly when a caller awaiting a
    // reply that never arrives would hang — the silent failure arc42 §8.3
    // forbids, and the one this whole slice is about.
    const { sw } = workerWithLedger()
    const ev = messageEvent({ type: 'beacon-from-the-future' })
    sw.listeners.message[0](ev)
    await ev.chain
    expect(ev.replies).toEqual([null])
  })

  test('a ledger failure is reported to the caller rather than swallowed', async () => {
    const { sw, backend } = workerWithLedger()
    backend.all = () => Promise.reject(new Error('QuotaExceededError'))
    const ev = messageEvent({ type: 'beacon-live-sessions', sessions: [] })
    sw.listeners.message[0](ev)
    await ev.chain
    expect(ev.replies).toEqual([{ error: 'QuotaExceededError' }])
  })

  test('a message with no reply port does not throw', async () => {
    const { sw } = workerWithLedger()
    const ev = { data: { type: 'beacon-live-sessions', sessions: [] }, chain: null, waitUntil(p) { this.chain = p } }
    sw.listeners.message[0](ev)
    await expect(ev.chain).resolves.toBeUndefined()
  })
})

describe('the push handler keeps the ledger write inside waitUntil (arc42 §6.2)', () => {
  // The worker is killed as soon as its waitUntil chain settles, so a ledger
  // write detached from that chain is a write iOS can cut mid-flight. Nothing
  // observable distinguishes the two shapes except whether the chain is still
  // pending while the write is — which is what this measures.
  async function chainWaitsForLedger(source) {
    const sw = loadWorkerFrom(source)
    let releasePut = null
    const put = new Promise((r) => { releasePut = r })
    // A complete backend: the chain now also refreshes the badge off the
    // ledger, and a stub missing `all` would fail this arm for a reason that
    // has nothing to do with what it measures.
    sw.idbLedgerBackend = () => ({
      put: () => put,
      get: () => Promise.resolve(null),
      all: () => Promise.resolve([]),
      remove: () => Promise.resolve(),
    })
    const ev = pushEvent({
      json: () => ({ v: 1, kind: 'session', session_id: 's-9', state: 'ready', at: 1755000000 }),
    })
    sw.listeners.push[0](ev)
    let settled = false
    ev.chain.then(() => { settled = true })
    // Two turns is plenty for a chain that is not waiting on anything.
    await Promise.resolve()
    await Promise.resolve()
    const waited = !settled
    releasePut()
    await ev.chain
    return waited
  }

  const LEDGER_IN_CHAIN = 'store.update(payload).then(function () { return self.refreshBadge(store); }),'
  const LEDGER_DETACHED = '(store.update(payload).then(function () { return self.refreshBadge(store); }), Promise.resolve()),'

  test('the chain stays pending until the ledger write resolves', async () => {
    expect(await chainWaitsForLedger(swSource)).toBe(true)
  })

  test('and reports the opposite for a worker whose ledger write is detached from it', async () => {
    expect(await chainWaitsForLedger(mutate(swSource, LEDGER_IN_CHAIN, LEDGER_DETACHED))).toBe(false)
  })
})

describe('notificationclick', () => {
  // includeUncontrolled is load-bearing, not decoration: with no fetch handler
  // (arc42 §5.2) this worker controls no clients at all, so a matchAll without
  // it returns an empty list forever and every tap opens a second window.
  async function matchAllOptionsOf(source) {
    const sw = loadWorkerFrom(source)
    const ev = { notification: { close: vi.fn() }, chain: null, waitUntil(p) { this.chain = p } }
    sw.listeners.notificationclick[0](ev)
    await ev.chain
    return sw.clients.matchAll.mock.calls[0][0]
  }

  test('matchAll asks for uncontrolled windows', async () => {
    expect(await matchAllOptionsOf(swSource)).toEqual({ type: 'window', includeUncontrolled: true })
  })

  test('and reports the opposite for a worker that omits it', async () => {
    const opts = await matchAllOptionsOf(mutate(swSource, ', includeUncontrolled: true', ''))
    expect(opts.includeUncontrolled).toBeUndefined()
  })

  test('closes, then opens the app when no window is up', async () => {
    const sw = loadWorker()
    const ev = {
      notification: { close: vi.fn() },
      chain: null,
      waitUntil(p) {
        this.chain = p
      },
    }
    sw.listeners.notificationclick[0](ev)
    await ev.chain
    expect(ev.notification.close).toHaveBeenCalled()
    expect(sw.clients.openWindow).toHaveBeenCalledWith('./')
  })

  test('focuses an existing window instead of opening a second one', async () => {
    const sw = loadWorker()
    const win = { focus: vi.fn(() => Promise.resolve(win)) }
    sw.clients.matchAll.mockResolvedValue([win])
    const ev = {
      notification: { close: vi.fn() },
      chain: null,
      waitUntil(p) {
        this.chain = p
      },
    }
    sw.listeners.notificationclick[0](ev)
    await ev.chain
    expect(win.focus).toHaveBeenCalled()
    expect(sw.clients.openWindow).not.toHaveBeenCalled()
  })
})

describe('the deep link a tap carries (R6)', () => {
  function clickEvent(data) {
    return { notification: { close: vi.fn(), data }, chain: null, waitUntil(p) { this.chain = p } }
  }

  test('the session kind carries its id into the notification; the others carry none', async () => {
    // Only the session kind names ONE session. A summary names several and the
    // daemon kinds name none, so they open the app with no target rather than
    // guessing one (arc42 §8.2).
    const sw = loadWorker()
    for (const [payload, expected] of [
      [{ v: 1, kind: 'session', session_id: 's-7', state: 'waiting', at: 1 }, { session_id: 's-7' }],
      [{ v: 1, kind: 'summary', count: 2, sessions: ['a', 'b'], at: 1 }, null],
      [{ v: 1, kind: 'daemon_down', daemon_id: 'd-1', at: 1 }, null],
    ]) {
      const ev = pushEvent({ json: () => payload })
      sw.listeners.push[0](ev)
      await ev.chain
      const [, options] = sw.registration.showNotification.mock.calls.at(-1)
      expect(options.data).toEqual(expected)
    }
  })

  test('an open window is told which session, and focused', async () => {
    const sw = loadWorker()
    const win = { focus: vi.fn(() => Promise.resolve(win)), postMessage: vi.fn() }
    sw.clients.matchAll.mockResolvedValue([win])
    const ev = clickEvent({ session_id: 's-7' })
    sw.listeners.notificationclick[0](ev)
    await ev.chain
    expect(win.postMessage).toHaveBeenCalledWith({ type: 'beacon-open-session', session_id: 's-7' })
    expect(win.focus).toHaveBeenCalled()
    expect(sw.clients.openWindow).not.toHaveBeenCalled()
  })

  test('a cold open carries the session in the URL fragment, encoded', async () => {
    // postMessage would race a page that has not run its modules yet, so the
    // cold route is the fragment; beacon.js reads it during initBeacon.
    const sw = loadWorker()
    const ev = clickEvent({ session_id: 'proc 12/34' })
    sw.listeners.notificationclick[0](ev)
    await ev.chain
    expect(sw.clients.openWindow).toHaveBeenCalledWith('./#beacon-session=proc%2012%2F34')
  })

  test('a window that refuses the message is still focused', async () => {
    // A client whose page is mid-unload throws on postMessage. Focusing it
    // shows the user what the app has, which beats a tap that does nothing.
    const sw = loadWorker()
    const win = {
      focus: vi.fn(() => Promise.resolve(win)),
      postMessage: vi.fn(() => { throw new DOMException('client is gone', 'InvalidStateError') }),
    }
    sw.clients.matchAll.mockResolvedValue([win])
    const ev = clickEvent({ session_id: 's-7' })
    sw.listeners.notificationclick[0](ev)
    await expect(ev.chain).resolves.toBeDefined()
    expect(win.focus).toHaveBeenCalled()
  })

  test('a notification with no data opens the app plainly, exactly as before', async () => {
    const sw = loadWorker()
    const ev = clickEvent(null)
    sw.listeners.notificationclick[0](ev)
    await ev.chain
    expect(sw.clients.openWindow).toHaveBeenCalledWith('./')
  })
})
