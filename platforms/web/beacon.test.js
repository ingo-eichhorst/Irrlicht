import { describe, test, expect, beforeEach, vi } from 'vitest'
import {
  initBeacon, urlBase64ToUint8Array, formatPairingCode, normalizePairingCode, countdownText,
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
  await new Promise((r) => setTimeout(r, 0))
  await new Promise((r) => setTimeout(r, 0))
}

// Routes keyed "METHOD url" (urls are the same-origin relative strings the
// module must use); anything unrouted answers 404. Returns the call log.
function relayFetch(routes) {
  const calls = []
  global.fetch = (url, opts = {}) => {
    const method = opts.method || 'GET'
    const u = String(url)
    calls.push({ url: u, method, opts })
    const route = routes[method + ' ' + u]
    if (!route) return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve(null) })
    const status = route.status || 200
    return Promise.resolve({ ok: status < 300, status, json: () => Promise.resolve(route.body ?? null) })
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

function mockServiceWorker(reg) {
  const sw = {
    register: vi.fn(() => Promise.resolve(reg)),
    getRegistration: vi.fn(() => Promise.resolve(reg)),
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
      'GET api/v1/push/subscriptions': {
        body: { registered: true, created: '2026-08-14', endpoint_host: 'web.push.apple.com', last_delivery: '2026-08-14T10:00:00Z' },
      },
    })
    const reg = {
      pushManager: {
        getSubscription: vi.fn(() => Promise.resolve(null)),
        subscribe: vi.fn(() => Promise.resolve(fakeSubscription())),
      },
    }
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
    expect(document.getElementById('beacon-health-line').textContent).toContain('web.push.apple.com')
    expect(document.getElementById('beacon-unpair')).toBeTruthy()
  })

  test('401 shows the uniform rejected-code message (wrong = expired = used)', async () => {
    relayFetch({
      'GET api/v1/push/info': { body: { enabled: true, vapid_public_key: VAPID } },
      'POST api/v1/push/pair': { status: 401, body: {} },
    })
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
    const reg = {
      pushManager: {
        getSubscription: vi.fn(() => Promise.resolve(null)),
        subscribe: vi.fn(() => Promise.resolve(fakeSubscription())),
      },
    }
    mockServiceWorker(reg)

    await initBeacon()
    await flush()

    const subOpts = reg.pushManager.subscribe.mock.calls[0][0]
    expect(subOpts.applicationServerKey).toBeInstanceOf(Uint8Array)
    expect([...subOpts.applicationServerKey]).toEqual(VAPID_BYTES)
    const subPost = calls.find((c) => c.method === 'POST' && c.url === 'api/v1/push/subscriptions')
    expect(subPost.opts.headers.Authorization).toBe('Bearer dev-tok-2')
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
    const reg = {
      pushManager: {
        getSubscription: vi.fn(() => Promise.resolve(sub)),
        subscribe: vi.fn(() => Promise.resolve(fakeSubscription())),
      },
    }
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
})
