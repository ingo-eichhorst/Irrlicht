import { describe, test, expect, beforeEach, vi } from 'vitest'
import {
  initBeacon, urlBase64ToUint8Array, formatPairingCode, normalizePairingCode, countdownText,
  healthLineText, liveViewNoteText, publishLedgerSnapshot, ledgerEntry, sessionFromHash,
  BEACON_MESSAGES,
} from './beacon.js'

// Beacon pairing + settings UI (docs/mobile-notifications-arc42.md §6.1,
// §8.1, §8.3, ADR-3). Driven through initBeacon against the #beacon-section
// scaffold from vitest.setup.js, with fetch / serviceWorker / Notification /
// PushManager mocked — no new deps.

// base64url exercising both url-alphabet chars ('-' and '_') and the padding
// path (11 chars → one '='): AQID → [1,2,3], __-_ → [255,255,191], BAU= → [4,5].
const VAPID = 'AQID__-_BAU'
const VAPID_BYTES = [1, 2, 3, 255, 255, 191, 4, 5]

const flush = async () => {
  for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0))
}

// Routes keyed "METHOD url" (urls are the same-origin relative strings the
// module must use); anything unrouted answers 404. A route may carry `replies`
// — an array consumed one call at a time — so a test can express "the relay
// answers differently once the self-heal has run". Returns the call log.
function relayFetch(routes) {
  const calls = []
  global.fetch = (url, opts = {}) => {
    const method = opts.method || 'GET'
    const u = String(url)
    calls.push({ url: u, method, opts })
    let route = routes[method + ' ' + u]
    if (!route) return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve(null) })
    if (Array.isArray(route.replies)) route = route.replies.length > 1 ? route.replies.shift() : route.replies[0]
    if (route.throws) return Promise.reject(new TypeError('Failed to fetch'))
    const status = route.status || 200
    const json = route.badJson
      ? () => Promise.reject(new SyntaxError('Unexpected token < in JSON at position 0'))
      : () => Promise.resolve(route.body ?? null)
    return Promise.resolve({ ok: status < 300, status, json })
  }
  return calls
}

function fakeSubscription() {
  return {
    endpoint: 'https://web.push.example/abc',
    toJSON: () => ({ endpoint: 'https://web.push.example/abc', keys: { p256dh: 'k1', auth: 'k2' } }),
    unsubscribe: vi.fn(() => Promise.resolve(true)),
  }
}

// The Settings → Sources seam initBeacon is handed (irrlicht.js's
// readSourceSettings / writeSourceSettings). A copy of the dashboard's own
// `settings` object, so a test can seed the state a user already configured
// and then read back exactly what pairing wrote — the point being that this
// module never writes localStorage keys of its own.
function liveViewSeam(initial = {}) {
  const settings = {
    enableLocalSource: true, enableRelaySource: false, relayUrl: '', relayToken: '', ...initial,
  }
  const writes = []
  return {
    settings,
    writes,
    read: () => ({ ...settings }),
    write: (next) => { writes.push({ ...next }); Object.assign(settings, next) },
  }
}

// A registration shaped like the one the browser actually hands back:
// `register()` resolves while the worker is still installing, so `active` is
// null, and `pushManager.subscribe()` rejects for exactly as long as it stays
// null (the Push API has no active worker to bind the subscription to).
// `navigator.serviceWorker.ready` is the signal that activation finished.
// The mock this replaced resolved everything instantly and had no `active` at
// all, which is why the pairing flow's activation race was invisible here.
function activatingRegistration({ subscription = null, subscribeFails = false } = {}) {
  const reg = {
    active: null,
    pushManager: {
      getSubscription: vi.fn(() => Promise.resolve(subscription)),
      subscribe: vi.fn(() => {
        if (!reg.active) {
          return Promise.reject(
            new DOMException('Subscription failed - no active Service Worker', 'AbortError'),
          )
        }
        if (subscribeFails) {
          return Promise.reject(new DOMException('Registration failed - push service error', 'AbortError'))
        }
        return Promise.resolve(fakeSubscription())
      }),
    },
  }
  return reg
}

// The activation clock starts when a registration comes into existence, not
// when the mock is built — otherwise a test that flushes a few ticks before
// clicking hands the module an already-active worker and the race disappears
// again. `activates: false` is the worker that never reaches `activated`.
function mockServiceWorker(reg, { activates = true } = {}) {
  let resolveReady
  const ready = new Promise((r) => { resolveReady = r })
  const startActivating = () => {
    if (!activates || reg.active) return
    setTimeout(() => {
      reg.active = { state: 'activated' }
      resolveReady(reg)
    }, 0)
  }
  const sw = {
    register: vi.fn(() => { startActivating(); return Promise.resolve(reg) }),
    getRegistration: vi.fn(() => { startActivating(); return Promise.resolve(reg) }),
    ready,
  }
  Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: sw })
  return sw
}

beforeEach(() => {
  localStorage.clear()
  const section = document.getElementById('beacon-section')
  section.hidden = true
  section.innerHTML = ''
  if ('serviceWorker' in navigator) delete navigator.serviceWorker
  global.Notification = class {
    static permission = 'default'
    static requestPermission = vi.fn(() => Promise.resolve('granted'))
  }
})

describe('pure helpers', () => {
  test('urlBase64ToUint8Array decodes base64url byte-for-byte', () => {
    const out = urlBase64ToUint8Array(VAPID)
    expect(out).toBeInstanceOf(Uint8Array)
    expect([...out]).toEqual(VAPID_BYTES)
  })

  test('pairing code display and entry normalization round-trip', () => {
    expect(formatPairingCode('ab12cd34')).toBe('AB12-CD34')
    expect(normalizePairingCode(' ab12-CD34 ')).toBe('AB12CD34')
    expect(countdownText(600)).toBe('10:00')
    expect(countdownText(59)).toBe('0:59')
  })
})

describe('feature detection (arc42 §5.2)', () => {
  test('push/info answering 404 renders nothing', async () => {
    relayFetch({})
    await initBeacon()
    await flush()
    const section = document.getElementById('beacon-section')
    expect(section.hidden).toBe(true)
    expect(section.children).toHaveLength(0)
  })

  test('enabled:false (e.g. an auth-off relay) renders nothing', async () => {
    relayFetch({ 'GET api/v1/push/info': { body: { enabled: false, reason: 'auth is off' } } })
    await initBeacon()
    await flush()
    const section = document.getElementById('beacon-section')
    expect(section.hidden).toBe(true)
    expect(section.children).toHaveLength(0)
  })

  test('enabled:true renders the section; without a relay token there is no mint button', async () => {
    relayFetch({ 'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } } })
    await initBeacon()
    await flush()
    const section = document.getElementById('beacon-section')
    expect(section.hidden).toBe(false)
    expect(section.querySelector('h2').textContent).toBe('Irrlicht Beacon')
    expect(document.getElementById('beacon-code-input')).toBeTruthy()
    expect(document.getElementById('beacon-mint')).toBeNull()
  })
})

describe('phone-side pairing (arc42 §6.1)', () => {
  test('happy path: code → pair → register → permission → subscribe → POST with the device token', async () => {
    const calls = relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pair': { body: { token: 'dev-tok-1', token_id: 't1', vapid_public_key: VAPID } },
      'POST api/v1/push/subscriptions': { status: 201, body: {} },
      // Faithful to handleSubscriptionStatus in
      // core/cmd/irrlichtrelay/push_handlers.go: `created` is unix seconds and
      // `last_delivery` is an object, never a string.
      'GET api/v1/push/subscriptions': {
        body: {
          registered: true, created: 1755165000, endpoint_host: 'web.push.apple.com',
          last_delivery: { at: 1755165120, ok: true },
        },
      },
    })
    const reg = activatingRegistration()
    const sw = mockServiceWorker(reg)

    await initBeacon()
    await flush()
    document.getElementById('beacon-code-input').value = 'ab12-cd34'
    document.getElementById('beacon-pair-submit').click()
    await flush()

    // Pair request carries the normalized code.
    const pair = calls.find((c) => c.method === 'POST' && c.url === 'api/v1/push/pair')
    expect(JSON.parse(pair.opts.body).code).toBe('AB12CD34')
    // Registration is lazy and happened from the pairing flow.
    expect(sw.register).toHaveBeenCalledWith('./sw.js')
    expect(Notification.requestPermission).toHaveBeenCalled()
    // applicationServerKey is a Uint8Array decoding the VAPID key
    // byte-for-byte — the raw base64url string is the classic silent failure.
    const subOpts = reg.pushManager.subscribe.mock.calls[0][0]
    expect(subOpts.userVisibleOnly).toBe(true)
    expect(subOpts.applicationServerKey).toBeInstanceOf(Uint8Array)
    expect([...subOpts.applicationServerKey]).toEqual(VAPID_BYTES)
    // The subscription is registered under the DEVICE token (arc42 §8.1).
    const subPost = calls.find((c) => c.method === 'POST' && c.url === 'api/v1/push/subscriptions')
    expect(subPost.opts.headers.Authorization).toBe('Bearer dev-tok-1')
    expect(JSON.parse(subPost.opts.body)).toEqual({
      endpoint: 'https://web.push.example/abc',
      keys: { p256dh: 'k1', auth: 'k2' },
    })
    expect(localStorage.getItem('beaconDeviceToken')).toBe('dev-tok-1')
    // Health panel replaces the entry form.
    const health = document.getElementById('beacon-health-line').textContent
    expect(health).toContain('web.push.apple.com')
    expect(health).not.toContain('[object Object]')
    expect(document.getElementById('beacon-unpair')).toBeTruthy()
  })

  test('subscribe waits for the worker to activate — register() alone is not enough', async () => {
    const calls = relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pair': { body: { token: 'dev-tok-a', vapid_public_key: VAPID } },
      'POST api/v1/push/subscriptions': { status: 201, body: {} },
      'GET api/v1/push/subscriptions': { body: { registered: true, endpoint_host: 'web.push.apple.com', last_delivery: null } },
    })
    const reg = activatingRegistration()
    mockServiceWorker(reg)

    await initBeacon()
    await flush()
    document.getElementById('beacon-code-input').value = 'AB12CD34'
    document.getElementById('beacon-pair-submit').click()
    await flush()

    // The Push API binds a subscription to the registration's ACTIVE worker;
    // subscribing in the turn register() resolves rejects on a real device
    // (arc42 §6.1's "subscribe(VAPID pub)" step comes after the worker is up).
    expect(reg.active, 'subscribed before navigator.serviceWorker.ready resolved').not.toBeNull()
    expect(calls.some((c) => c.method === 'POST' && c.url === 'api/v1/push/subscriptions')).toBe(true)
  })

  test('a subscribe that fails leaves a verdict naming the spent code, never a frozen "Pairing…"', async () => {
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pair': { body: { token: 'dev-tok-b', vapid_public_key: VAPID } },
    })
    mockServiceWorker(activatingRegistration({ subscribeFails: true }))

    await initBeacon()
    await flush()
    document.getElementById('beacon-code-input').value = 'AB12CD34'
    document.getElementById('beacon-pair-submit').click()
    await flush()

    const status = document.getElementById('beacon-pair-status').textContent
    expect(status).not.toBe('Pairing…')
    // The code is single-use (arc42 ADR-3) and this attempt spent it — the
    // user cannot retry with the same one, which is the part they must be told.
    expect(status).toMatch(/code/i)
    expect(status).toMatch(/mint|fresh|new code/i)
    // Identity survived the failure, so the §8.3 self-heal can finish the job.
    expect(localStorage.getItem('beaconDeviceToken')).toBe('dev-tok-b')
  })

  test('permission is requested inside the click, before any network round-trip (iOS activation, ADR-2/C3)', async () => {
    const order = []
    global.Notification = class {
      static permission = 'default'
      static requestPermission = vi.fn(() => {
        order.push('permission')
        return Promise.resolve('granted')
      })
    }
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pair': { body: { token: 'dev-tok-c', vapid_public_key: VAPID } },
      'POST api/v1/push/subscriptions': { status: 201, body: {} },
      'GET api/v1/push/subscriptions': { body: { registered: true, endpoint_host: 'web.push.apple.com', last_delivery: null } },
    })
    const reg = activatingRegistration()
    const sw = mockServiceWorker(reg)
    sw.register = vi.fn(() => {
      order.push('register')
      return Promise.resolve(reg)
    })

    await initBeacon()
    await flush()
    const originalFetch = global.fetch
    global.fetch = (url, opts = {}) => {
      order.push((opts.method || 'GET') + ' ' + url)
      return originalFetch(url, opts)
    }
    document.getElementById('beacon-code-input').value = 'AB12CD34'
    document.getElementById('beacon-pair-submit').click()
    await flush()

    // Transient user activation is what makes the prompt appear at all, and it
    // does not survive a network round-trip plus a worker registration.
    expect(order[0]).toBe('permission')
  })

  test('a worker that never activates fails the pairing rather than wedging it', async () => {
    vi.useFakeTimers()
    try {
      relayFetch({
        'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
        'POST api/v1/push/pair': { body: { token: 'dev-tok-e', vapid_public_key: VAPID } },
      })
      // navigator.serviceWorker.ready never resolves — the phone whose worker
      // is stuck installing. Without a bound, "Pairing…" is the last thing the
      // user ever sees, with the code spent.
      mockServiceWorker(activatingRegistration(), { activates: false })

      await initBeacon()
      await vi.advanceTimersByTimeAsync(1)
      document.getElementById('beacon-code-input').value = 'AB12CD34'
      document.getElementById('beacon-pair-submit').click()
      await vi.advanceTimersByTimeAsync(20000)

      const status = document.getElementById('beacon-pair-status').textContent
      expect(status).not.toBe('Pairing…')
      expect(status).toContain('did not start')
    } finally {
      vi.useRealTimers()
    }
  })

  test('permission refused does not spend the code — the pair POST never happens', async () => {
    global.Notification = class {
      static permission = 'default'
      static requestPermission = vi.fn(() => Promise.resolve('denied'))
    }
    const calls = relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pair': { body: { token: 'dev-tok-d', vapid_public_key: VAPID } },
    })
    mockServiceWorker(activatingRegistration())

    await initBeacon()
    await flush()
    document.getElementById('beacon-code-input').value = 'AB12CD34'
    document.getElementById('beacon-pair-submit').click()
    await flush()

    expect(calls.some((c) => c.url === 'api/v1/push/pair')).toBe(false)
    expect(localStorage.getItem('beaconDeviceToken')).toBeNull()
    const status = document.getElementById('beacon-pair-status').textContent
    expect(status).toMatch(/notification/i)
    expect(status).toMatch(/still|again/i)
  })

  test('401 shows the uniform rejected-code message (wrong = expired = used)', async () => {
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pair': { status: 401, body: {} },
    })
    mockServiceWorker(activatingRegistration())
    await initBeacon()
    await flush()
    document.getElementById('beacon-code-input').value = 'AB12CD34'
    document.getElementById('beacon-pair-submit').click()
    await flush()
    expect(document.getElementById('beacon-pair-status').textContent).toContain('Code not accepted')
    expect(localStorage.getItem('beaconDeviceToken')).toBeNull()
  })

  test('429 is distinct: waiting helps, retyping does not', async () => {
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pair': { status: 429, body: {} },
    })
    mockServiceWorker(activatingRegistration())
    await initBeacon()
    await flush()
    document.getElementById('beacon-code-input').value = 'AB12CD34'
    document.getElementById('beacon-pair-submit').click()
    await flush()
    const status = document.getElementById('beacon-pair-status').textContent
    expect(status).toContain('try again in a minute')
    expect(status).not.toContain('Code not accepted')
    expect(localStorage.getItem('beaconDeviceToken')).toBeNull()
  })
})

describe('self-heal on open (arc42 §8.3)', () => {
  test('stored device token + null getSubscription → re-subscribe + re-POST', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-tok-2')
    const calls = relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/subscriptions': { status: 201, body: {} },
      'GET api/v1/push/subscriptions': { body: { registered: true, endpoint_host: 'fcm.googleapis.com', last_delivery: null } },
    })
    const reg = activatingRegistration()
    mockServiceWorker(reg)

    await initBeacon()
    await flush()

    const subOpts = reg.pushManager.subscribe.mock.calls[0][0]
    expect(subOpts.applicationServerKey).toBeInstanceOf(Uint8Array)
    expect([...subOpts.applicationServerKey]).toEqual(VAPID_BYTES)
    const subPost = calls.find((c) => c.method === 'POST' && c.url === 'api/v1/push/subscriptions')
    expect(subPost.opts.headers.Authorization).toBe('Bearer dev-tok-2')
  })

  test('browser still holds the subscription but the relay lost it → re-POST, then a fresh verdict', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-tok-4')
    const sub = fakeSubscription()
    const calls = relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/subscriptions': { status: 201, body: {} },
      // A relay whose registry lost this phone (restored backup, pruned entry,
      // registry deleted per arc42 §8.6) answers registered:false while the
      // browser's own subscription is intact — then registered:true once the
      // self-heal has re-POSTed it.
      'GET api/v1/push/subscriptions': {
        replies: [
          { body: { registered: false, last_delivery: null } },
          { body: { registered: true, endpoint_host: 'web.push.apple.com', last_delivery: null } },
        ],
      },
    })
    const reg = activatingRegistration({ subscription: sub })
    mockServiceWorker(reg)

    await initBeacon()
    await flush()

    const subPost = calls.find((c) => c.method === 'POST' && c.url === 'api/v1/push/subscriptions')
    expect(subPost, 'the relay reported the subscription gone and nothing re-registered it').toBeTruthy()
    expect(subPost.opts.headers.Authorization).toBe('Bearer dev-tok-4')
    // The address the browser already holds is what gets re-registered — no
    // second subscribe is needed, and the device token is the identity that
    // makes the re-POST safe (arc42 §8.1).
    expect(JSON.parse(subPost.opts.body).endpoint).toBe('https://web.push.example/abc')
    // The panel must show the state AFTER the repair, not the one that
    // prompted it — otherwise it advises reopening an app that just healed.
    const line = document.getElementById('beacon-health-line').textContent
    expect(line).toContain('web.push.apple.com')
    expect(line).not.toContain('reopen')
  })

  test('a revoked device token (401) is reported as un-paired, not as an outage', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-tok-5')
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'GET api/v1/push/subscriptions': { status: 401, body: null },
    })
    mockServiceWorker(activatingRegistration({ subscription: fakeSubscription() }))

    await initBeacon()
    await flush()

    const line = document.getElementById('beacon-health-line').textContent
    expect(line).toMatch(/no longer paired|revoked/i)
    expect(line).not.toContain('unavailable right now')
    // And a way back: re-pairing is the only recovery from a revocation.
    const again = document.getElementById('beacon-repair')
    expect(again).toBeTruthy()
    again.click()
    await flush()
    expect(localStorage.getItem('beaconDeviceToken')).toBeNull()
    expect(document.getElementById('beacon-code-input')).toBeTruthy()
  })

  test('a 401 on the self-heal re-POST is inspected, not swallowed', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-tok-6')
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'GET api/v1/push/subscriptions': { body: { registered: false, last_delivery: null } },
      'POST api/v1/push/subscriptions': { status: 401, body: null },
    })
    mockServiceWorker(activatingRegistration({ subscription: fakeSubscription() }))

    await initBeacon()
    await flush()

    expect(document.getElementById('beacon-health-line').textContent).toMatch(/no longer paired|revoked/i)
  })

  test('an unreachable relay stays a transient outage, distinct from a revocation', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-tok-7')
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'GET api/v1/push/subscriptions': { throws: true },
    })
    mockServiceWorker(activatingRegistration({ subscription: fakeSubscription() }))

    await initBeacon()
    await flush()

    const line = document.getElementById('beacon-health-line').textContent
    expect(line).toContain('unavailable right now')
    expect(line).not.toMatch(/no longer paired|revoked/i)
  })
})

describe('health panel copy (arc42 §8.3)', () => {
  // last_delivery is a JSON object on the wire — {at, ok, detail}, see
  // handleSubscriptionStatus in core/cmd/irrlichtrelay/push_handlers.go.
  // Concatenating it renders "[object Object]", which makes a success and a
  // failure read identically.
  test('a failed delivery names the failure', () => {
    const t = healthLineText({
      registered: true,
      endpoint_host: 'web.push.apple.com',
      last_delivery: { at: 1755165120, ok: false, detail: '410 Gone' },
    })
    expect(t).not.toContain('[object Object]')
    expect(t).toContain('410 Gone')
    expect(t).toMatch(/\d{1,2}:\d{2}/)
    expect(t).toMatch(/fail/i)
  })

  test('a successful delivery reads as one, and carries its time', () => {
    const t = healthLineText({
      registered: true,
      endpoint_host: 'fcm.googleapis.com',
      last_delivery: { at: 1755165120, ok: true },
    })
    expect(t).not.toContain('[object Object]')
    expect(t).toContain('fcm.googleapis.com')
    expect(t).toMatch(/\d{1,2}:\d{2}/)
    expect(t).not.toMatch(/fail/i)
  })

  test('null last_delivery says nothing has been attempted, not "never delivered"', () => {
    // Delivery health is RAM-only on the relay (arc42 §8.6): null means "no
    // send since the relay started", which is not the same claim as a failure.
    const t = healthLineText({ registered: true, endpoint_host: 'web.push.apple.com', last_delivery: null })
    expect(t).not.toContain('[object Object]')
    expect(t).toMatch(/no delivery attempted|since the relay/i)
    expect(t).not.toMatch(/fail/i)
  })
})

describe('unpair (arc42 §8.1 revocation)', () => {
  test('DELETE + unsubscribe + token forgotten, entry form returns', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-tok-3')
    const sub = fakeSubscription()
    const calls = relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'GET api/v1/push/subscriptions': { body: { registered: true, endpoint_host: 'web.push.apple.com', last_delivery: null } },
      'DELETE api/v1/push/subscriptions': { status: 204 },
    })
    const reg = activatingRegistration({ subscription: sub })
    mockServiceWorker(reg)

    await initBeacon()
    await flush()
    // An intact subscription means the §8.3 self-heal must NOT re-subscribe.
    expect(reg.pushManager.subscribe).not.toHaveBeenCalled()

    document.getElementById('beacon-unpair').click()
    await flush()

    const del = calls.find((c) => c.method === 'DELETE' && c.url === 'api/v1/push/subscriptions')
    expect(del.opts.headers.Authorization).toBe('Bearer dev-tok-3')
    expect(sub.unsubscribe).toHaveBeenCalled()
    expect(localStorage.getItem('beaconDeviceToken')).toBeNull()
    expect(document.getElementById('beacon-code-input')).toBeTruthy()
    expect(document.getElementById('beacon-unpair')).toBeNull()
  })
})

describe("pairing configures the phone's own live view (arc42 §6.2, ADR-9)", () => {
  // §8.4 never pushes on `* → working` and iOS forbids a silent push, so a
  // phone fed only by push learns that sessions NEED attention and never that
  // one stopped. Pairing therefore also makes this phone an ordinary client of
  // the relay it paired with — possible because a device token is a full
  // client token (§8.1), and necessary because nothing else can lower a badge.
  const PAIR_ROUTES = () => ({
    'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
    'POST api/v1/push/pair': { body: { token: 'dev-live-1', vapid_public_key: VAPID } },
    'POST api/v1/push/subscriptions': { status: 201, body: {} },
    'GET api/v1/push/subscriptions': {
      body: { registered: true, endpoint_host: 'web.push.apple.com', last_delivery: null },
    },
  })

  async function pairWith(seam, routes = PAIR_ROUTES()) {
    relayFetch(routes)
    mockServiceWorker(activatingRegistration())
    await initBeacon({ liveView: seam })
    await flush()
    document.getElementById('beacon-code-input').value = 'AB12CD34'
    document.getElementById('beacon-pair-submit').click()
    await flush()
  }

  test('a successful pair writes exactly the three source settings, through the dashboard mechanism', async () => {
    const seam = liveViewSeam()
    await pairWith(seam)
    expect(seam.writes).toEqual([{
      enableRelaySource: true,
      // location.origin, not the page URL: relayWsUrl maps https:→wss: and
      // appends the stream path itself (connectionProtocol.js).
      relayUrl: location.origin,
      relayToken: 'dev-live-1',
    }])
    // enableLocalSource is not this module's to touch, and a write naming it
    // would be a phone quietly switching off a source the user chose.
    expect(seam.settings.enableLocalSource).toBe(true)
    // Nothing to explain, so the panel's second line stays empty and hidden.
    const note = document.getElementById('beacon-live-view-line')
    expect(note.textContent).toBe('')
    expect(note.hidden).toBe(true)
  })

  test('a live view already pointed at ANOTHER relay is left as the user set it, and the panel says so', async () => {
    // The phone may legitimately watch another relay; overwriting would lose
    // that configuration and hand this phone's device token to a host it was
    // not minted for.
    const seam = liveViewSeam({
      enableRelaySource: true, relayUrl: 'https://relay.example', relayToken: 'user-tok',
    })
    await pairWith(seam)
    expect(seam.writes).toEqual([])
    expect(seam.settings.relayToken).toBe('user-tok')
    const note = document.getElementById('beacon-live-view-line')
    expect(note.hidden).toBe(false)
    expect(note.textContent).toContain('relay.example')
    expect(note.textContent).toMatch(/counts up/)
    // The pairing itself still succeeded — only the live view was declined.
    expect(localStorage.getItem('beaconDeviceToken')).toBe('dev-live-1')
  })

  test('the same relay spelled differently is the same relay, not another one', async () => {
    // Compared through relayWsUrl, exactly as the dashboard's own source list
    // does — so an origin with a trailing slash is not mistaken for elsewhere
    // and left unconfigured.
    const seam = liveViewSeam({ enableRelaySource: false, relayUrl: location.origin + '/' })
    await pairWith(seam)
    expect(seam.writes).toEqual([{
      enableRelaySource: true, relayUrl: location.origin, relayToken: 'dev-live-1',
    }])
  })

  test('unpair takes it back down, symmetrically', async () => {
    // Otherwise the phone keeps a source holding a revoked token: a 4401 close
    // parks it in `unauthorized` with no retry (the ev.code branch in
    // irrlicht.js connectSource), so it does not loop — but it is a source
    // that can never connect, configured by something the user just switched
    // off.
    localStorage.setItem('beaconDeviceToken', 'dev-live-2')
    const seam = liveViewSeam({
      enableRelaySource: true, relayUrl: location.origin, relayToken: 'dev-live-2',
    })
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'GET api/v1/push/subscriptions': {
        body: { registered: true, endpoint_host: 'web.push.apple.com', last_delivery: null },
      },
      'DELETE api/v1/push/subscriptions': { status: 204 },
    })
    mockServiceWorker(activatingRegistration({ subscription: fakeSubscription() }))
    await initBeacon({ liveView: seam })
    await flush()

    document.getElementById('beacon-unpair').click()
    await flush()

    expect(seam.writes).toEqual([{ enableRelaySource: false, relayUrl: '', relayToken: '' }])
    expect(seam.settings.relayToken).toBe('')
    expect(seam.settings.enableLocalSource).toBe(true)
  })

  test('unpair leaves a live view that was never ours alone', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-live-3')
    const seam = liveViewSeam({
      enableRelaySource: true, relayUrl: 'https://relay.example', relayToken: 'user-tok',
    })
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'GET api/v1/push/subscriptions': {
        body: { registered: true, endpoint_host: 'web.push.apple.com', last_delivery: null },
      },
      'DELETE api/v1/push/subscriptions': { status: 204 },
    })
    mockServiceWorker(activatingRegistration({ subscription: fakeSubscription() }))
    await initBeacon({ liveView: seam })
    await flush()

    document.getElementById('beacon-unpair').click()
    await flush()

    expect(seam.writes).toEqual([])
    expect(seam.settings.relayUrl).toBe('https://relay.example')
  })

  test('a revoked phone lets go of both at once', async () => {
    // The token is dead on every route the relay serves, live view included
    // (§8.1), so "Pair again" cannot leave a source behind that only ever
    // answers 4401.
    localStorage.setItem('beaconDeviceToken', 'dev-live-4')
    const seam = liveViewSeam({
      enableRelaySource: true, relayUrl: location.origin, relayToken: 'dev-live-4',
    })
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'GET api/v1/push/subscriptions': { status: 401, body: null },
    })
    mockServiceWorker(activatingRegistration({ subscription: fakeSubscription() }))
    await initBeacon({ liveView: seam })
    await flush()

    document.getElementById('beacon-repair').click()
    await flush()

    expect(seam.writes).toEqual([{ enableRelaySource: false, relayUrl: '', relayToken: '' }])
    expect(localStorage.getItem('beaconDeviceToken')).toBeNull()
  })
})

describe('what the health panel says about the live view (arc42 §8.3)', () => {
  const ORIGIN = 'https://relay.mine'

  test('ours → nothing to explain', () => {
    expect(liveViewNoteText(
      { enableRelaySource: true, relayUrl: ORIGIN, relayToken: 'dev-1' }, 'dev-1', ORIGIN,
    )).toBe('')
  })

  test('pointed elsewhere → names where, and what it costs', () => {
    const t = liveViewNoteText(
      { enableRelaySource: true, relayUrl: 'https://relay.theirs', relayToken: 'x' }, 'dev-1', ORIGIN,
    )
    expect(t).toContain('relay.theirs')
    expect(t).toMatch(/left as you set it/i)
    expect(t).toMatch(/counts up/)
  })

  test('off for this relay → says the badge only counts up, not that push is broken', () => {
    const t = liveViewNoteText({ enableRelaySource: false, relayUrl: '', relayToken: '' }, 'dev-1', ORIGIN)
    expect(t).toMatch(/off for this relay/i)
    expect(t).toMatch(/notifications still arrive/i)
  })

  test('a token the user typed is not this phone\'s live view either', () => {
    // Same origin, but a client token the user entered by hand: this module
    // neither publishes through it nor takes it down.
    expect(liveViewNoteText(
      { enableRelaySource: true, relayUrl: ORIGIN, relayToken: 'client-tok' }, 'dev-1', ORIGIN,
    )).not.toBe('')
  })
})

describe('talking to the service worker (arc42 §8.5)', () => {
  // A worker that is already active, with a postMessage a test can read. The
  // pairing mocks above deliberately model the ACTIVATION race; here the
  // worker is up, which is the state every one of these calls requires.
  function activeWorkerMock(postMessage = vi.fn()) {
    const worker = { postMessage }
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        register: vi.fn(() => Promise.resolve({ active: worker })),
        getRegistration: vi.fn(() => Promise.resolve({ active: worker })),
        ready: Promise.resolve({ active: worker }),
        addEventListener: vi.fn(),
      },
    })
    return worker
  }

  const ROWS = [{ session_id: 's-1', state: 'waiting', label: 'claude-code', project: 'irrlicht', at: 1755000000 }]

  test('the live snapshot reaches the worker while the live view is ours', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-live-5')
    const worker = activeWorkerMock()
    relayFetch({})
    await initBeacon({
      liveView: liveViewSeam({
        enableRelaySource: true, relayUrl: location.origin, relayToken: 'dev-live-5',
      }),
    })
    await flush()
    expect(await publishLedgerSnapshot(ROWS)).toBe(true)
    expect(worker.postMessage).toHaveBeenCalledWith({
      type: BEACON_MESSAGES.liveSessions, sessions: ROWS,
    })
  })

  test('a live view watching a DIFFERENT relay publishes nothing', async () => {
    // The ledger's key space is the paired relay's bare session ids. Folding a
    // foreign relay's sessions in would both add rows that are not ours and
    // delete the paired relay's as absent.
    localStorage.setItem('beaconDeviceToken', 'dev-live-6')
    const worker = activeWorkerMock()
    relayFetch({})
    await initBeacon({
      liveView: liveViewSeam({
        enableRelaySource: true, relayUrl: 'https://relay.example', relayToken: 'user-tok',
      }),
    })
    await flush()
    expect(await publishLedgerSnapshot(ROWS)).toBe(false)
    expect(worker.postMessage).not.toHaveBeenCalled()
  })

  test('an unpaired dashboard publishes nothing — there is no worker of ours', async () => {
    const worker = activeWorkerMock()
    relayFetch({})
    await initBeacon({
      liveView: liveViewSeam({
        enableRelaySource: true, relayUrl: location.origin, relayToken: 'dev-live-7',
      }),
    })
    await flush()
    expect(await publishLedgerSnapshot(ROWS)).toBe(false)
    expect(worker.postMessage).not.toHaveBeenCalled()
  })

  test('a ledger read answers the worker over a reply port', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-live-8')
    activeWorkerMock((msg, transfer) => {
      expect(msg).toEqual({ type: BEACON_MESSAGES.ledgerGet, session_id: 's-1' })
      transfer[0].postMessage({ entry: { state: 'waiting', at: 1755000000 } })
    })
    expect(await ledgerEntry('s-1')).toEqual({ state: 'waiting', at: 1755000000 })
  })

  test('a session the worker has no row for answers null', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-live-9')
    activeWorkerMock((msg, transfer) => transfer[0].postMessage({ entry: null }))
    expect(await ledgerEntry('s-nobody')).toBeNull()
  })

  test('a worker that never answers resolves null rather than hanging', async () => {
    // The caller is composing a "that session is not here" notice (R6), so a
    // promise that never settles would reproduce the very tap-does-nothing
    // failure the notice exists to prevent.
    vi.useFakeTimers()
    try {
      localStorage.setItem('beaconDeviceToken', 'dev-live-10')
      activeWorkerMock(() => {})
      const pending = ledgerEntry('s-1')
      await vi.advanceTimersByTimeAsync(5000)
      expect(await pending).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  test('no worker at all answers null, without throwing', async () => {
    localStorage.setItem('beaconDeviceToken', 'dev-live-11')
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { getRegistration: () => Promise.resolve(null), addEventListener: vi.fn() },
    })
    expect(await ledgerEntry('s-1')).toBeNull()
  })
})

describe('sessionFromHash (R6, the cold-open route)', () => {
  test('reads the target the worker put in the fragment, decoded', () => {
    expect(sessionFromHash('#beacon-session=proc%2012')).toBe('proc 12')
    expect(sessionFromHash('beacon-session=s-1')).toBe('s-1')
  })

  test('finds its own key among others, and answers empty when there is none', () => {
    expect(sessionFromHash('#tab=history&beacon-session=s-2')).toBe('s-2')
    expect(sessionFromHash('#tab=history')).toBe('')
    expect(sessionFromHash('')).toBe('')
    expect(sessionFromHash(null)).toBe('')
  })

  test('a malformed escape yields the raw value rather than nothing', () => {
    // A target that fails to decode is still a better target than none: the
    // dashboard will report it as absent, which is a visible answer.
    expect(sessionFromHash('#beacon-session=%E0%A4%A')).toBe('%E0%A4%A')
  })
})

describe('mac-side minting (arc42 §6.1, §8.1)', () => {
  test('mint renders the formatted code, the countdown, and this page URL', async () => {
    const calls = relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pairings': { status: 201, body: { code: 'ab12cd34', expires_in: 600 } },
    })
    await initBeacon({ relayToken: () => 'client-tok' })
    await flush()

    const mint = document.getElementById('beacon-mint')
    expect(mint).toBeTruthy()
    mint.click()
    await flush()

    // Minting is the authed-client door (§8.1): Bearer relay token.
    const minted = calls.find((c) => c.method === 'POST' && c.url === 'api/v1/push/pairings')
    expect(minted.opts.headers.Authorization).toBe('Bearer client-tok')
    expect(document.getElementById('beacon-code').textContent).toBe('AB12-CD34')
    expect(document.getElementById('beacon-code-expiry').textContent).toMatch(/expires in 10:00/)
    expect(document.querySelector('.beacon-code-url').textContent).toContain('and enter this code')
  })

  test('a 200 that is not JSON leaves a verdict, not "Minting code…" forever', async () => {
    // Anything in front of the relay — a captive portal, a proxy error page —
    // can answer 200 with HTML. The same rule as the phone side: a throw on
    // this leg becomes a verdict the user can act on.
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pairings': { status: 201, badJson: true },
    })
    await initBeacon({ relayToken: () => 'client-tok' })
    await flush()
    document.getElementById('beacon-mint').click()
    await flush()

    const out = document.getElementById('beacon-mint-out').textContent
    expect(out).not.toBe('Minting code…')
    expect(out).toMatch(/could not|not.*code/i)
  })
})
