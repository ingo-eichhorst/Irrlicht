import { describe, test, expect, beforeAll, vi } from 'vitest'

// Two first-contact defects on a push-capable relay, both found by running
// docs/elfdans-device-test.md against a real one:
//
//  1. The relay serves this page, and the page's Local source connects back to
//     that same origin — but irrlicht.js sends hello.token only for relay-kind
//     sources, so an auth-enabled relay closes it with CloseRevoked (4401,
//     core/cmd/irrlichtrelay/hub.go). Push REQUIRES auth (arc42 §8.1), so on
//     every relay that can push, first contact reads "no configured source is
//     reachable" — which names the wrong cause and implies the wrong fix. The
//     per-source 'unauthorized' state already exists; only the banner never
//     looked at it.
//
//  2. The "Pair a phone…" mint row is built only when a relay token already
//     exists at page-build time, so entering one leaves the panel unchanged
//     until a reload — and there is no way to mint a pairing code in between.

const mocks = vi.hoisted(() => ({
  initElfdans: vi.fn(async () => {}),
}))

// elfdans.js's own wiring is covered by elfdans.test.js; here it is replaced so
// the re-render seam is observable and its feature-detection fetch stays out of
// the way.
vi.mock('./elfdans.js', async (importOriginal) => ({ ...(await importOriginal()), ...mocks }))

let irr = null

const banner = () => document.getElementById('connection-banner')

beforeAll(async () => {
  localStorage.setItem('irrlicht_settings', JSON.stringify({
    enableLocalSource: true,
    enableRelaySource: false,
    relayUrl: '',
    relayToken: '',
  }))
  // The Settings → Sources inputs are not part of vitest.setup.js's scaffold,
  // and the change handler is attached while irrlicht.js is imported — so they
  // have to exist before the import, not after it.
  document.body.insertAdjacentHTML(
    'beforeend',
    '<input id="t-relay-token" type="password" data-setting="relayToken">' +
    '<input id="t-relay-url" type="text" data-setting="relayUrl">',
  )
  irr = await import('./irrlicht.js')
})

describe('a source refused for its token says so', () => {
  test('the two causes produce different text', () => {
    expect(irr.disconnectedBannerText(['unauthorized'])).toMatch(/token/i)
    expect(irr.disconnectedBannerText(['disconnected'])).not.toMatch(/token/i)
    expect(irr.disconnectedBannerText([])).not.toMatch(/token/i)
  })

  test('an unauthorized source among reachable-but-down ones still names the token', () => {
    // The relay-served case exactly: Local is refused for its token while a
    // second source is merely down. The actionable cause must win.
    expect(irr.disconnectedBannerText(['disconnected', 'unauthorized'])).toMatch(/token/i)
  })

  test('the real banner element carries it', () => {
    irr.setDotLabel('disconnected', ['unauthorized'])
    expect(banner().textContent).toMatch(/token/i)
    irr.setDotLabel('disconnected', ['disconnected'])
    expect(banner().textContent).not.toMatch(/token/i)
    expect(banner().textContent).toMatch(/disconnected/i)
  })

  test('a connected source clears the banner regardless of states', () => {
    irr.setDotLabel('connected', ['unauthorized', 'connected'])
    expect(banner().textContent).toBe('')
  })
})

describe('entering a relay token rebuilds the pairing panel without a reload', () => {
  test('the panel is rebuilt on the change that makes minting possible', async () => {
    mocks.initElfdans.mockClear()
    const input = document.getElementById('t-relay-token')
    input.value = 'a-client-token'
    input.dispatchEvent(new Event('change'))
    await new Promise((r) => setTimeout(r, 0))
    expect(
      mocks.initElfdans,
      'the Elfdans panel was never rebuilt, so the mint row stays absent until a reload',
    ).toHaveBeenCalled()
  })

  test('a setting unrelated to the token does not rebuild it', async () => {
    // The rebuild is scoped to the one setting the panel reads. Re-rendering on
    // every source change would discard a code the user is mid-way through
    // typing on the phone.
    mocks.initElfdans.mockClear()
    const input = document.getElementById('t-relay-url')
    input.value = 'ws://relay.example:7839'
    input.dispatchEvent(new Event('change'))
    await new Promise((r) => setTimeout(r, 0))
    expect(mocks.initElfdans).not.toHaveBeenCalled()
  })
})
