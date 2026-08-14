import { describe, test, expect, beforeEach, vi } from 'vitest'
import { initBeacon, BEACON_MESSAGES } from './beacon.js'

// Where a notification tap LANDS in the page (docs/mobile-notifications-arc42.md
// R6, §8.5). Two routes, because the app may be open or cold:
//   · open   — the worker postMessages the focused client, which arrives on
//              navigator.serviceWorker's `message` event;
//   · cold   — the worker opens `./#beacon-session=<id>`, and initBeacon reads
//              the fragment as it wires up.
//
// This is a file of its own rather than a describe block in beacon.test.js
// because the `message` listener is registered ONCE per module instance
// (beacon.js latches it), and vitest gives each test file a fresh module
// registry. The listener test is deliberately first here: after it, the latch
// is set and a later navigator.serviceWorker replacement would receive no
// listener at all — which would make a subsequent arm pass by never running.
// The fragment route re-reads on every initBeacon call and has no such order.

const DEVICE_TOKEN_KEY = 'beaconDeviceToken'

// A serviceWorker container whose `message` listeners a test can fire. Nothing
// here answers push/info, so initBeacon does its wiring and then renders
// nothing — the state a cold open from a tap is actually in for the instant
// before the section paints.
function swContainer() {
  const listeners = {}
  Object.defineProperty(navigator, 'serviceWorker', {
    configurable: true,
    value: {
      addEventListener: (type, fn) => { (listeners[type] ||= []).push(fn) },
      getRegistration: () => Promise.resolve(null),
      register: vi.fn(),
      ready: new Promise(() => {}),
    },
  })
  return listeners
}

function setHash(hash) {
  history.replaceState(null, '', location.pathname + location.search + hash)
}

beforeEach(() => {
  localStorage.clear()
  setHash('')
  const section = document.getElementById('beacon-section')
  section.hidden = true
  section.innerHTML = ''
  if ('serviceWorker' in navigator) delete navigator.serviceWorker
})

describe('a tap on a focused app (R6)', () => {
  test('the worker names the session and the dashboard is asked to open it', async () => {
    localStorage.setItem(DEVICE_TOKEN_KEY, 'dev-tok-1')
    const listeners = swContainer()
    const openSession = vi.fn()
    await initBeacon({ openSession })

    expect(listeners.message, 'nothing listens for the worker\'s message').toHaveLength(1)
    listeners.message[0]({ data: { type: BEACON_MESSAGES.openSession, session_id: 's-1' } })
    expect(openSession).toHaveBeenCalledWith('s-1')

    // Other worker chatter is not a tap.
    listeners.message[0]({ data: { type: 'beacon-something-else', session_id: 's-2' } })
    listeners.message[0]({ data: { type: BEACON_MESSAGES.openSession } })
    listeners.message[0]({})
    expect(openSession).toHaveBeenCalledTimes(1)
  })
})

describe('a tap that opened the app cold (R6)', () => {
  test('the fragment names the session, and is consumed', async () => {
    localStorage.setItem(DEVICE_TOKEN_KEY, 'dev-tok-2')
    swContainer()
    setHash('#beacon-session=s-7')
    const openSession = vi.fn()
    await initBeacon({ openSession })

    expect(openSession).toHaveBeenCalledWith('s-7')
    // A reload is not a second tap: the fragment is a message, not state.
    expect(location.hash).toBe('')
  })

  test('an encoded id arrives as it was sent', async () => {
    localStorage.setItem(DEVICE_TOKEN_KEY, 'dev-tok-3')
    swContainer()
    setHash('#beacon-session=proc%2012')
    const openSession = vi.fn()
    await initBeacon({ openSession })
    expect(openSession).toHaveBeenCalledWith('proc 12')
  })

  test('a fragment that is not ours is left exactly where it was', async () => {
    localStorage.setItem(DEVICE_TOKEN_KEY, 'dev-tok-4')
    swContainer()
    setHash('#tab=history')
    const openSession = vi.fn()
    await initBeacon({ openSession })
    expect(openSession).not.toHaveBeenCalled()
    expect(location.hash).toBe('#tab=history')
  })

  test('an UNPAIRED dashboard ignores the fragment entirely (arc42 §5.2)', async () => {
    // Only a paired phone can have been sent here by a notification, so a
    // dashboard that never paired wires nothing — including this.
    swContainer()
    setHash('#beacon-session=s-9')
    const openSession = vi.fn()
    await initBeacon({ openSession })
    expect(openSession).not.toHaveBeenCalled()
    expect(location.hash).toBe('#beacon-session=s-9')
  })

  test('a page with no openSession seam wires nothing and throws nothing', async () => {
    // The daemon-served dashboard's own call site passes one, but a caller
    // that does not must not blow up on a stray fragment.
    localStorage.setItem(DEVICE_TOKEN_KEY, 'dev-tok-5')
    swContainer()
    setHash('#beacon-session=s-9')
    await expect(initBeacon({})).resolves.toBeUndefined()
  })
})
