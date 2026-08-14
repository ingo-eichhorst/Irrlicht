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
  // The IndexedDB half is structural only (jsdom has no IndexedDB, no new
  // deps) — the fold logic runs against an injected in-memory backend here.
  function mapBackend() {
    const puts = []
    return {
      puts,
      put(key, value) {
        puts.push([key, value])
        return Promise.resolve()
      },
    }
  }

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

describe('the push handler keeps the ledger write inside waitUntil (arc42 §6.2)', () => {
  // The worker is killed as soon as its waitUntil chain settles, so a ledger
  // write detached from that chain is a write iOS can cut mid-flight. Nothing
  // observable distinguishes the two shapes except whether the chain is still
  // pending while the write is — which is what this measures.
  async function chainWaitsForLedger(source) {
    const sw = loadWorkerFrom(source)
    let releasePut = null
    const put = new Promise((r) => { releasePut = r })
    sw.idbLedgerBackend = () => ({ put: () => put })
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

  const LEDGER_IN_CHAIN = 'self.ledgerStore(self.idbLedgerBackend()).update(payload),'
  const LEDGER_DETACHED = '(self.ledgerStore(self.idbLedgerBackend()).update(payload), Promise.resolve()),'

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
