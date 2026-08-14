import { describe, test, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { WEB_DIR } from './shippedFiles.testutil.js'

// On-device composition + ledger fold (docs/mobile-notifications-arc42.md
// §8.2, §8.4, §8.5). sw.js is a classic script structured as pure functions
// on `self`, so these tests evaluate the file against a stubbed `self` and
// drive the functions (and the registered handlers) directly.

const swSource = readFileSync(join(WEB_DIR, 'sw.js'), 'utf8')

function loadWorker() {
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
  new Function('self', swSource)(self)
  return self
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

describe('notificationclick', () => {
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
