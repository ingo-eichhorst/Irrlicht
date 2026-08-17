import { describe, test, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { WEB_DIR, deriveShippedSet } from './shippedFiles.testutil.js'
import { SETUP_BODY } from './vitest.setup.js'
import { ELFDANS_MESSAGES } from './elfdans.js'

// The §5.2 additivity-contract tripwire (docs/mobile-notifications-arc42.md
// §5.2, §8.7). platforms/web is one shared surface served by the localhost
// daemon AND by relays, so "the Elfdans feature changes nothing for plain
// dashboard usage" is enforced, not intended:
//   (a) sw.js handles push + notificationclick ONLY — a handler for the fetch
//       event could interpose on asset loading and cache the dashboard stale.
//   (b) the service worker is registered lazily from the pairing flow only,
//       so exactly one shipped module (elfdans.js) may contain the
//       registration call.
//   (c) behaviorally: a dashboard whose origin 404s /api/v1/push/info makes
//       zero registration calls and renders no Elfdans section.

const swSource = readFileSync(join(WEB_DIR, 'sw.js'), 'utf8')

describe('service worker contract (arc42 §5.2)', () => {
  test('sw.js registers no fetch handler', () => {
    expect(swSource).not.toMatch(/addEventListener\(\s*['"`]fetch['"`]/)
    expect(swSource).not.toMatch(/\bonfetch\b/)
  })

  test('sw.js does register push and notificationclick (vacuity guard)', () => {
    // The positive arm: if these stopped matching, the negative arm above
    // would be reading a file that no longer expresses handlers this way.
    expect(swSource).toMatch(/addEventListener\(\s*['"`]push['"`]/)
    expect(swSource).toMatch(/addEventListener\(\s*['"`]notificationclick['"`]/)
  })

  test('exactly one shipped module contains the service-worker registration call: elfdans.js', () => {
    const { files } = deriveShippedSet()
    const registering = [...files]
      .filter((f) => f.endsWith('.js'))
      .filter((f) => /serviceWorker\s*\.\s*register\s*\(/.test(readFileSync(join(WEB_DIR, f), 'utf8')))
      .sort()
    expect(registering).toEqual(['elfdans.js'])
  })

  // The two copies of the page↔worker vocabulary (arc42 §8.5). sw.js is a
  // classic script with nothing to import from — its header says why — so it
  // spells as literals what elfdans.js exports. A rename on one side would
  // leave the other silently unheard: the app would publish live sessions
  // nobody folds, and a tap would postMessage a type nobody listens for, both
  // answering exactly like a healthy install. This is the third §5.2-shaped
  // tripwire and lives here for the same reason as the other two.
  //
  // Read off the SOURCE rather than by executing sw.js: the point is that the
  // literals in that file match, and a check that ran the file could only
  // observe values it had already been handed.
  function workerMessageLiterals(source) {
    return [...source.matchAll(/^const (?:MSG_[A-Z_]+|SESSION_HASH_KEY) = '([^']+)';$/gm)]
      .map((m) => m[1])
      .sort()
  }

  test('sw.js spells exactly the message names elfdans.js exports', () => {
    expect(workerMessageLiterals(swSource)).toEqual(Object.values(ELFDANS_MESSAGES).sort())
  })

  test('and reports the opposite for a worker whose copy drifted', () => {
    // Committed mutation evidence (AGENTS.md "Testing"): the arm above passes
    // by construction against a correct pair, so it is also driven against a
    // worker with one name renamed — the shape of the drift it exists to
    // catch. A no-op edit throws rather than reading as a pass.
    const drifted = swSource.replace("const MSG_LIVE_SESSIONS = 'elfdans-live-sessions';", "const MSG_LIVE_SESSIONS = 'elfdans-live-session';")
    expect(drifted, 'stale mutation: sw.js no longer declares MSG_LIVE_SESSIONS this way').not.toBe(swSource)
    expect(workerMessageLiterals(drifted)).not.toEqual(Object.values(ELFDANS_MESSAGES).sort())
  })

  test('a worker that stopped declaring them at all fails loudly, not quietly', () => {
    // The regex is the weak point: a spelling it cannot see would return a
    // short list, and a short list must never read as agreement.
    expect(workerMessageLiterals('// no constants here')).toEqual([])
    expect(Object.values(ELFDANS_MESSAGES)).toHaveLength(4)
  })

  // Boots the dashboard the way index.html does: a fresh module registry, so
  // each arm re-runs irrlicht.js's top-level wiring instead of inspecting the
  // module the previous arm already executed.
  async function bootDashboard(routes) {
    vi.resetModules()
    localStorage.clear()
    document.body.innerHTML = SETUP_BODY
    const register = vi.fn(() => Promise.resolve({}))
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: {
        register,
        getRegistration: vi.fn(() => Promise.resolve(null)),
        ready: new Promise(() => {}),
      },
    })
    global.fetch = (url) => {
      const route = routes[String(url)]
      if (!route) return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve(null) })
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(route) })
    }
    await import('./irrlicht.js')
    // Let initElfdans's feature-detection fetch and its render settle.
    for (let i = 0; i < 4; i++) await new Promise((r) => setTimeout(r, 0))
    return { register, section: document.getElementById('elfdans-section') }
  }

  test('dashboard on a non-push origin: zero register calls, no Elfdans section', async () => {
    // Every endpoint answers 404 — the daemon-served (or old-relay) origin.
    const { register, section } = await bootDashboard({})
    expect(register).not.toHaveBeenCalled()
    expect(section.hidden).toBe(true)
    expect(section.children).toHaveLength(0)
  })

  test('push-capable origin, phone not paired: the section renders and STILL no worker is installed', async () => {
    // The other half of §5.2's "registered lazily from the pairing flow only".
    // The arm above is satisfied by an initElfdans nobody calls; this one is
    // not — a section that renders is proof irrlicht.js reaches the module —
    // and it is the state most users' phones sit in before they ever pair.
    const { register, section } = await bootDashboard({
      'api/v1/push/info': { enabled: true, vapid_public_key: 'AQID' },
    })
    expect(section.hidden).toBe(false)
    expect(section.querySelector('h2').textContent).toBe('Irrlicht Elfdans')
    expect(document.getElementById('elfdans-code-input'), 'no pairing entry rendered').toBeTruthy()
    expect(register, 'a service worker was installed by merely opening the dashboard').not.toHaveBeenCalled()
  })
})

// The third copy-pair, and the only one that crosses languages: the relay's
// RFC 8030 collapse key for the diagnostic push (arc42 §8.3) and the tag the
// worker shows it under. Both files already carried a comment naming the
// other and nothing checked them, which the Beacon → Elfdans rename (§8.6)
// turned from tidy into load-bearing: moving one side alone leaves the test
// push landing under a tag that collapses against nothing — indistinguishable
// from a healthy delivery, and visible only as a duplicate banner nobody is
// looking for while they are busy proving push works at all.
const GO_DIR = join(WEB_DIR, '..', '..', 'core')
const observerSource = readFileSync(join(GO_DIR, 'cmd', 'irrlichtrelay', 'push_observer.go'), 'utf8')
const engineSource = readFileSync(join(GO_DIR, 'domain', 'notify', 'engine.go'), 'utf8')

const goStringConst = (source, name) =>
  (source.match(new RegExp(`^const ${name} = "([^"]*)"$`, 'm')) || [])[1] ?? null

// Scoped to the 'test' branch: a tag elsewhere in the switch must not be able
// to answer for this one.
function workerTestTag(source) {
  const branch = source.match(/case 'test':([\s\S]*?)\n\s*default:/)
  if (!branch) return null
  return (branch[1].match(/tag: '([^']*)'/) || [])[1] ?? null
}

describe('the diagnostic push collapse key (arc42 §8.3, §8.6)', () => {
  test('sw.js shows the test push under exactly the Topic the relay sends it with', () => {
    const topic = goStringConst(observerSource, 'testPushTopic')
    const tag = workerTestTag(swSource)
    // Both must be FOUND before they are compared. Two nulls are equal, so
    // "neither file could be parsed" would otherwise read as agreement — the
    // failure mode this repo cares about most (AGENTS.md, "a verification
    // mechanism must fail loudly when it cannot run").
    expect(topic, 'push_observer.go no longer declares testPushTopic as a plain string const').not.toBeNull()
    expect(tag, "sw.js no longer sets a tag in its 'test' branch").not.toBeNull()
    expect(tag).toBe(topic)
  })

  test('and reports a relay that renamed its Topic without the worker', () => {
    // Committed mutation evidence: the arm above passes by construction, so
    // it is also driven against the exact drift a half-applied rename makes.
    const drifted = observerSource.replace('const testPushTopic = "elfdans-test"', 'const testPushTopic = "beacon-test"')
    expect(drifted, 'stale mutation: push_observer.go no longer declares this literal').not.toBe(observerSource)
    expect(goStringConst(drifted, 'testPushTopic')).not.toBe(workerTestTag(swSource))
  })

  test('a worker that stopped tagging its test branch fails loudly, not quietly', () => {
    expect(workerTestTag("case 'test':\n  return { title: 'x' };\n  default:")).toBeNull()
    expect(goStringConst('// no consts here', 'testPushTopic')).toBeNull()
  })

  test('the Topic is one no real push can collide with', () => {
    // Derived from the engine's own topic space rather than restated here: a
    // test push must replace only an earlier test push, never the `ready`
    // banner the user was actually waiting for.
    const topic = goStringConst(observerSource, 'testPushTopic')
    const summary = goStringConst(engineSource, 'summaryTopic')
    const daemonPrefix = (engineSource.match(/func daemonTopic\(id string\) string \{ return "([^"]*)" \+ id \}/) || [])[1] ?? null
    expect(summary, 'engine.go no longer declares summaryTopic this way').not.toBeNull()
    expect(daemonPrefix, 'engine.go no longer declares daemonTopic this way').not.toBeNull()
    expect(topic).not.toBe(summary)
    expect(topic.startsWith(daemonPrefix)).toBe(false)
    // The third and largest slice of the topic space — `Topic: id`
    // (engine.go:338) — is deliberately left unasserted, because it is not
    // pattern-checkable. A session id is whatever the adapter reports: a UUID
    // for Claude Code, `ses_*` for opencode. Nothing can exclude one that
    // happens to equal this literal, so the guarantee there is the literal's
    // unusualness, not a check. Saying so beats a regex that looks like
    // coverage: the first draft of this arm asserted "a session Topic is the
    // session id, which is a UUID" and excluded the UUID shape — false in this
    // codebase, and it would have passed while checking almost nothing.
    // What IS checkable, and was not checked anywhere before, is the header's
    // own constraint (RFC 8030 §5.4): a Topic is a short URL-safe token.
    expect(topic.length).toBeGreaterThan(0)
    expect(topic.length).toBeLessThanOrEqual(32)
    expect(topic).toMatch(/^[A-Za-z0-9_-]+$/)
  })
})
