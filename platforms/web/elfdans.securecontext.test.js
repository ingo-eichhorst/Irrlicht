import { describe, test, expect, afterEach } from 'vitest'
import { pairingBlockedReason, pairingHintText } from './elfdans.js'

// A plain-http:// LAN origin is the one deployment where the old single
// message named the wrong thing. Measured on http://192.168.188.119:7901 in
// Chrome:
//
//   isSecureContext: false
//   serviceWorker:   absent
//   PushManager:     present
//   Notification:    present
//
// So the browser is perfectly capable and the ORIGIN is not — but the guard
// saw only the missing service worker and reported "This browser cannot
// receive push notifications", sending the reader to blame Safari for
// something one word fixes. The two globals still being present is what makes
// this worth a named check rather than a comment: naive feature detection
// reports "supported" right up until subscribe().

function setSecure(value) {
  Object.defineProperty(window, 'isSecureContext', { configurable: true, value })
}

function withoutServiceWorker(fn) {
  const had = Object.getOwnPropertyDescriptor(navigator, 'serviceWorker')
  Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: undefined })
  // `'serviceWorker' in navigator` is still true with an own property, so drop
  // it the way a non-secure context does — the property is simply not there.
  delete navigator.serviceWorker
  try { return fn() } finally { if (had) Object.defineProperty(navigator, 'serviceWorker', had) }
}

afterEach(() => { setSecure(true) })

describe('pairingBlockedReason separates an incapable browser from an insecure origin', () => {
  test('a non-secure origin is named as an HTTPS problem, not a browser one', () => {
    setSecure(false)
    const reason = withoutServiceWorker(() => pairingBlockedReason())
    expect(reason).toMatch(/HTTPS/i)
    expect(reason).not.toMatch(/this browser cannot/i)
  })

  test('the secure-context check runs first', () => {
    // Ordering is the whole fix. On a non-secure origin the service worker is
    // absent too, so a serviceWorker-first guard wins with the wrong message
    // every time — which is exactly what shipped.
    setSecure(false)
    expect(pairingBlockedReason()).toMatch(/HTTPS/i)
  })

  test('a secure origin in a browser without push still reports the browser', () => {
    setSecure(true)
    const reason = withoutServiceWorker(() => pairingBlockedReason())
    expect(reason).toMatch(/browser/i)
    expect(reason).not.toMatch(/HTTPS/i)
  })

  test('a secure origin in a capable browser is not blocked', () => {
    setSecure(true)
    if (!('serviceWorker' in navigator)) {
      Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: {} })
    }
    expect(pairingBlockedReason()).toBe('')
  })
})

// ── The mint hint's address ─────────────────────────────────────────────────
// The Mac mints a code and tells you where to type it. It built that line from
// location.origin, so a dashboard opened on http://127.0.0.1:7839 instructed
// the operator to open http://127.0.0.1:7839 *on the phone* — an address no
// phone can reach, printed at the exact moment it is needed and nowhere else.
describe('pairingHintText refuses to hand a phone an address it cannot use', () => {
  test('a loopback origin is called out instead of being printed as instructions', () => {
    for (const origin of ['http://127.0.0.1:7839', 'http://localhost:7839', 'http://[::1]:7839']) {
      const hint = pairingHintText(origin, '/')
      expect(hint, origin).not.toMatch(/^On the phone, open/)
      expect(hint, origin).toMatch(/this Mac|cannot reach|network address/i)
    }
  })

  test('a plain-http network origin names the HTTPS requirement', () => {
    const hint = pairingHintText('http://192.168.188.119:7839', '/')
    expect(hint).toMatch(/https/i)
  })

  test('an https origin gives the plain instruction, with the address', () => {
    const hint = pairingHintText('https://mac.tail1234.ts.net', '/')
    expect(hint).toMatch(/^On the phone, open/)
    expect(hint).toContain('https://mac.tail1234.ts.net/')
  })

  test('the path is preserved, so a sub-path deployment still works', () => {
    expect(pairingHintText('https://host.example', '/dash/')).toContain('https://host.example/dash/')
  })
})
