import { describe, test, expect, beforeEach, afterEach } from 'vitest'

import { stateIcon } from './formatters.js'
import { readCss, readMacosTokens } from './snapshots/serialize.js'
import {
  contrastRatio, composite, readToken,
  DARK_BLOCK, LIGHT_MEDIA_BLOCK, LIGHT_THEME_BLOCK,
} from './snapshots/contrast.mjs'
import * as irr from './irrlicht.js'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

// --- #1801: the `error` session state on the web dashboard -----------------
//
// #1798 added the fourth lifecycle state to the domain; nothing in
// platforms/web/ knew about it. These pin the three independent places a state
// has to be declared (the CSS tokens, stateColor, the icon map), the red line
// beneath an errored session, and the daemon-wide banner.
//
// `irr` is imported as a namespace deliberately, the reason
// permissionsBanner.test.js records: a missing export then fails one test with
// a readable TypeError instead of breaking the module link and taking every
// suite in the file down with it.
//
// Its own file rather than irrlicht.test.js, which is ~62 KB and already
// CodeScene-flagged for size.

const errorPayload = (over = {}) => ({
  phase: 'retrying',
  class: 'rate_limit',
  message: 'API Error: 429 rate limited',
  http_status: 429,
  attempt: 3,
  max_attempts: 10,
  retry_in_ms: 616.4520045919932,
  ...over,
})

const sessionWithError = (sessionError, over = {}) => ({
  session_id: 'sess-err',
  state: 'error',
  metrics: sessionError ? { session_error: sessionError } : {},
  ...over,
})

describe('stateColor (#1801)', () => {
  // stateColor had NO test of any kind before this issue — stateIcon gained
  // one in #1797, this did not, and it is the map most likely to be forgotten
  // when a state is added, because nothing fails when it is.

  // LOCK — passes by construction; the three existing states must not move.
  test('the three original states keep their own tokens', () => {
    expect(irr.stateColor('working')).toBe('var(--working)')
    expect(irr.stateColor('waiting')).toBe('var(--waiting)')
    expect(irr.stateColor('ready')).toBe('var(--ready)')
  })

  test('error maps to its own token, not the fallback', () => {
    expect(irr.stateColor('error')).toBe('var(--error)')
    expect(irr.stateColor('error')).not.toBe(irr.stateColor('zzz-nope'))
  })

  test('every arm returns a CSS variable, never a literal colour', () => {
    // This is what makes the light-theme override reachable at all. An arm
    // returning a hex would look correct in the default dark theme and be
    // wrong — and unfixable from CSS — in light.
    for (const s of ['working', 'waiting', 'ready', 'error', '', undefined, 'zzz-nope']) {
      expect(irr.stateColor(s)).toMatch(/^var\(--[a-z-]+\)$/)
    }
  })

  test('an unrecognized state still falls back to the neutral token', () => {
    expect(irr.stateColor('zzz-nope')).toBe('var(--muted)')
    expect(irr.stateColor(undefined)).toBe('var(--muted)')
  })
})

describe('the error state icon (#1801)', () => {
  // Extends #1797's "known states keep their own icons" lock to four. Without
  // this, adding svgIcons.error silently changes what THAT test means: its
  // sibling asserts an unrecognized state is not any known icon, and `error`
  // quietly moved from the unknown bucket to a known one.
  test('error has its own glyph, distinct from every other state', () => {
    const icon = stateIcon('error')
    for (const other of ['working', 'waiting', 'ready', 'zzz-unknown']) {
      expect(icon).not.toBe(stateIcon(other))
    }
    expect(icon).toMatch(/^<svg\b/)
    expect(icon).toMatch(/<\/svg>$/)
  })

  test('the error icon is token-driven, not a hardcoded hex', () => {
    // The four older glyphs inline their DARK hex and follow no theme
    // override — the wart #1797 documented and deliberately left alone. The
    // error glyph must not copy it, because --error's light value is a
    // genuinely different hex (see the WCAG test below), so an inlined hex
    // would render light theme at 3.01:1.
    const icon = stateIcon('error')
    expect(icon).toMatch(/currentColor/)
    expect(icon).not.toMatch(/#[0-9a-f]{6}/i)
    expect(icon).toMatch(/class="state-error"/)
  })

  test('the error glyph is not animated as working', () => {
    // `.row-state-icon svg circle.core` is the breathe animation. A pulsing
    // red dot would read as "working", the one thing an errored session isn't.
    expect(stateIcon('error')).not.toMatch(/class="core"/)
  })

  test('the stylesheet points the error icon at the --error token', () => {
    const css = readCss()
    const rule = css.match(/svg\.state-error[^{]*\{[^}]*\}/)
    expect(rule, 'no svg.state-error rule found in irrlicht.css').not.toBeNull()
    expect(rule[0]).toMatch(/var\(--error\)/)
  })
})

describe('--error is declared in every theme block (#1801)', () => {
  // Colour lives in three CSS blocks that do NOT share a source, and a token
  // added to only one works in only one theme. Each is looked up inside ITS
  // OWN block: a bare /--error:/ over the whole file matches the dark
  // declaration first and would report the light theme as present when it is
  // not — the failure mode #1797's --unknown test notes but does not have.
  const css = readCss()

  test('all three blocks declare it', () => {
    for (const [name, block] of [
      ['dark :root', DARK_BLOCK],
      ['prefers-color-scheme: light', LIGHT_MEDIA_BLOCK],
      ['[data-theme="light"]', LIGHT_THEME_BLOCK],
    ]) {
      expect(() => readToken(css, block, 'error'), `--error missing from ${name}`).not.toThrow()
      expect(() => readToken(css, block, 'error-dim'), `--error-dim missing from ${name}`).not.toThrow()
    }
  })

  test('the two light blocks agree with each other', () => {
    // They are literal duplicates by construction; a value updated in one and
    // not the other is a theme that changes depending on how the user got there.
    expect(readToken(css, LIGHT_THEME_BLOCK, 'error')).toBe(readToken(css, LIGHT_MEDIA_BLOCK, 'error'))
    expect(readToken(css, LIGHT_THEME_BLOCK, 'error-dim')).toBe(readToken(css, LIGHT_MEDIA_BLOCK, 'error-dim'))
  })

  test('the light value is NOT a copy of the dark one', () => {
    // The whole reason --error needs a light override, unlike --cancelled and
    // --unknown, which are mid-greys that read on both surfaces.
    expect(readToken(css, LIGHT_MEDIA_BLOCK, 'error')).not.toBe(readToken(css, DARK_BLOCK, 'error'))
  })

  test('--error clears WCAG AA against its own wash in BOTH themes', () => {
    // The claim the CSS comment makes, re-run rather than recorded. Reproduce
    // the full table with: node platforms/web/snapshots/contrast.mjs
    for (const [name, block] of [['dark', DARK_BLOCK], ['light', LIGHT_MEDIA_BLOCK]]) {
      const hex = readToken(css, block, 'error')
      const surface = readToken(css, block, 'surface')
      const ratio = contrastRatio(hex, composite(hex, 0.12, surface))
      expect(ratio, `${name} --error ${hex} on its 12% wash over ${surface} is ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5)
    }
  })

  test('--error-dim is the same hue as --error', () => {
    // A wash built from a different colour than the text drawn on it is how
    // a measured contrast ratio silently stops describing what ships.
    for (const block of [DARK_BLOCK, LIGHT_MEDIA_BLOCK]) {
      const hex = readToken(css, block, 'error').replace('#', '')
      const rgb = [0, 2, 4].map(i => parseInt(hex.slice(i, i + 2), 16))
      const dim = readToken(css, block, 'error-dim')
      const nums = dim.match(/rgba\((\d+),\s*(\d+),\s*(\d+),/)
      expect(nums, `--error-dim ${dim} is not an rgba() triple`).not.toBeNull()
      expect([1, 2, 3].map(i => Number(nums[i]))).toEqual(rgb)
    }
  })

  test('--error is paired by value with the macOS red', () => {
    // Reads Tokens.swift rather than comparing against a hand-typed literal:
    // a third copy of the constant cannot fail when the Swift side drifts,
    // which is the only scenario this test exists for (#1797's reasoning).
    //
    // #1802 owns adding IrrHex.error. Until it lands the pairing is asserted
    // against the red macOS ALREADY declares for its failure surfaces, so the
    // two frontends cannot ship two different reds in the gap; once it lands,
    // this asserts against IrrHex.error itself and goes red if #1802 picked a
    // different hue. Both branches assert loudly that they found something to
    // compare, so "could not look" can never pass as "matches".
    //
    // CHECKED AGAINST THE REAL SIBLING BRANCH, not assumed: #1802's PR #1810
    // declares `static let error = "#FF3B30"`, which is both this file's dark
    // --error and the existing IrrHex.wsDisconnected — so both branches of the
    // fallback agree today and this test passes either side of that merge.
    // (Verified by running these exact regexes over `git show
    // pr1810:platforms/macos/Irrlicht/Theme/Tokens.swift`.)
    //
    // The LIGHT halves diverge by design and are not compared: this file
    // overrides --error itself to #CC0C00, while #1810 keeps IrrHex.error as
    // the base hue and re-tunes only the pill TEXT via
    // Color.adaptive(light: "#C1121C", dark: "#FF7A70"). Both clear AA against
    // their own platform's surface, which is a different colour on each.
    const tokens = readMacosTokens()
    const explicit = tokens.match(/static let error\s*=\s*"(#[0-9A-Fa-f]{6})"/)
    const existingRed = tokens.match(/static let wsDisconnected\s*=\s*"(#[0-9A-Fa-f]{6})"/)
    expect(existingRed, 'no IrrHex.wsDisconnected in platforms/macos/Irrlicht/Theme/Tokens.swift').not.toBeNull()
    const want = (explicit ? explicit[1] : existingRed[1]).toLowerCase()
    // The DARK value is the paired one: Color(hex:) is not appearance-aware,
    // so the Swift side carries one hex per token and adapts, where it does at
    // all, through Color.adaptive — the seam waitingPillText already uses.
    expect(readToken(readCss(), DARK_BLOCK, 'error').toLowerCase()).toBe(want)
  })
})

describe('the red error line under a session (#1801)', () => {
  test('no error payload renders no row', () => {
    expect(irr.sessionErrorText(undefined)).toBeNull()
    expect(irr.sessionErrorText(null)).toBeNull()
  })

  test('the agent\'s own message is what is shown', () => {
    const t = irr.sessionErrorText(errorPayload())
    expect(t.head).toBe('API Error: 429 rate limited')
  })

  test('a message-less error falls back to its class, never to nothing', () => {
    // A blank red line reads as a rendering bug; "rate_limit" is at least the
    // fact the daemon has.
    expect(irr.sessionErrorText(errorPayload({ message: '' })).head).toBe('rate_limit')
    expect(irr.sessionErrorText(errorPayload({ message: '   ' })).head).toBe('rate_limit')
    expect(irr.sessionErrorText({ phase: 'terminal' }).head).toBeTruthy()
  })

  test('the retry ladder is rendered when the agent reported one', () => {
    const t = irr.sessionErrorText(errorPayload())
    expect(t.retry).toContain('attempt 3 of 10')
    // retry_in_ms is fractional milliseconds — 616.45ms reads as 0.6s.
    expect(t.retry).toContain('next in 0.6s')
  })

  test('ABSENT counters are never rendered as zero', () => {
    // The defect the daemon's pointer fields exist to prevent: claudecode's
    // TERMINAL error carries none of the four numbers, and a `||` fallback in
    // this renderer would turn that into "attempt 0 of 0" — a give-up derived
    // from a transcript that said nothing.
    const t = irr.sessionErrorText({ phase: 'terminal', class: 'provider', message: 'API returned an empty response' })
    expect(t.retry).toBe('')
    expect(t.head).toBe('API returned an empty response')
  })

  test('a real zero is still shown', () => {
    // The other half of the same rule: `attempt: 0` is a value, not an absence,
    // and a falsiness check would swallow it exactly like a null.
    expect(irr.sessionErrorText(errorPayload({ attempt: 0, max_attempts: 0, retry_in_ms: 0 })).retry)
      .toBe('attempt 0 of 0, next in 0.0s')
  })

  test('a retrying error with no counters still says it is retrying', () => {
    // ErrorPhaseRetrying with a nil RetryIn is a real recorded shape —
    // "another attempt is coming, timing unstated" — not a gap to render blank.
    const t = irr.sessionErrorText({ phase: 'retrying', message: 'overloaded' })
    expect(t.retry).toBe('retrying')
  })

  test('a terminal error adds no retry clause', () => {
    expect(irr.sessionErrorText({ phase: 'terminal', message: 'quota exhausted' }).retry).toBe('')
  })
})

describe('the daemon-wide error banner (#1801)', () => {
  const fault = (over = {}) => ({
    kind: 'hook_channel_silent',
    scope: 'claude-code',
    message: 'Irrlicht is receiving no hook events from claude-code — its sessions have fallen back to slower transcript-only detection.',
    detail: '9 completed turns with no receipt',
    ...over,
  })
  const payload = (...faults) => ({ groups: [], daemon_errors: faults })
  const banner = () => document.getElementById('daemon-error-banner')

  beforeEach(() => {
    document.body.innerHTML +=
      '<div id="daemon-error-banner" role="alert" aria-live="polite" hidden></div>'
  })
  afterEach(() => { banner()?.remove() })

  test('a healthy payload produces no summary', () => {
    // Every shape a healthy or older daemon can send. The field is omitempty
    // on the Go side and a relay-served payload omits it entirely.
    expect(irr.daemonErrorSummary({ groups: [] })).toBeNull()
    expect(irr.daemonErrorSummary({ groups: [], daemon_errors: [] })).toBeNull()
    expect(irr.daemonErrorSummary(null)).toBeNull()
    expect(irr.daemonErrorSummary([])).toBeNull()
  })

  test('the headline pluralizes', () => {
    expect(irr.daemonErrorSummary(payload(fault())).text).toContain('1 fault')
    expect(irr.daemonErrorSummary(payload(fault(), fault({ scope: 'codex' }))).text).toContain('2 faults')
  })

  test('the real element renders the scope, message and detail of each fault', () => {
    irr.renderDaemonErrorBanner(payload(
      fault(),
      fault({ kind: 'hook_entries_missing', scope: 'codex/hooks', message: 'entries went missing', detail: '/home/u/.codex/config.toml' }),
    ))
    expect(banner().hidden).toBe(false)
    const text = banner().textContent
    expect(text).toContain('claude-code')
    expect(text).toContain('no hook events')
    expect(text).toContain('9 completed turns with no receipt')
    expect(text).toContain('codex/hooks')
    expect(text).toContain('/home/u/.codex/config.toml')
    expect(banner().querySelectorAll('.daemon-error-list li')).toHaveLength(2)
  })

  test('it is passive: alert semantics, polite delivery, and no dismiss', () => {
    irr.renderDaemonErrorBanner(payload(fault()))
    expect(banner().getAttribute('role')).toBe('alert')
    expect(banner().getAttribute('aria-live')).toBe('polite')
    // No dismiss control of any kind. "Dismissible by fixing" is the contract:
    // a hide button would let a live fault be silenced while still broken.
    expect(banner().querySelectorAll('button')).toHaveLength(0)
  })

  test('fixing the fault removes it with no gesture', () => {
    irr.renderDaemonErrorBanner(payload(fault()))
    expect(banner().hidden).toBe(false)
    irr.renderDaemonErrorBanner({ groups: [] })
    expect(banner().hidden).toBe(true)
    expect(banner().textContent).toBe('')
  })

  test('an unchanged banner is not rebuilt, so role=alert stops re-announcing', () => {
    // aria-atomic re-reads the whole strip on every rebuild, and this runs on
    // the initial load AND every 30s rehydrate poll. Node identity is the
    // observable proxy for "was it rebuilt", the same one #1385's suite uses.
    irr.renderDaemonErrorBanner(payload(fault()))
    const first = banner().querySelector('li')
    irr.renderDaemonErrorBanner(payload(fault()))
    expect(banner().querySelector('li')).toBe(first)
  })

  test('a changed fault list IS rebuilt', () => {
    // The other half of the guard above: skip-if-unchanged must not become
    // skip-always, which would freeze the banner on its first fault forever.
    irr.renderDaemonErrorBanner(payload(fault()))
    const first = banner().querySelector('li')
    irr.renderDaemonErrorBanner(payload(fault({ detail: '20 completed turns with no receipt' })))
    expect(banner().querySelector('li')).not.toBe(first)
    expect(banner().textContent).toContain('20 completed turns')
  })

  test('fault text is inserted as text, never as markup', () => {
    // These strings carry an agent config's own error text and file paths,
    // from a file this process does not control.
    irr.renderDaemonErrorBanner(payload(fault({
      message: '<img src=x onerror=alert(1)>',
      detail: '<script>bad()</script>',
    })))
    expect(banner().querySelector('img')).toBeNull()
    expect(banner().querySelector('script')).toBeNull()
    expect(banner().textContent).toContain('<img src=x onerror=alert(1)>')
  })
})

describe('the web "is active" partition (#1801)', () => {
  // The client had its own two inline copies of `working || waiting`, and they
  // drift for exactly the reason the Go ones did. Collapsed into one predicate
  // mirroring session.SessionState.HasWorkInFlight.

  // LOCK — passes by construction; the three original answers must not move.
  test('the original states keep their answers', () => {
    expect(irr.hasWorkInFlight({ state: 'working' })).toBe(true)
    expect(irr.hasWorkInFlight({ state: 'waiting' })).toBe(true)
    expect(irr.hasWorkInFlight({ state: 'ready' })).toBe(false)
  })

  test('a RETRYING errored session is still working', () => {
    // The elapsed clock must keep ticking: the agent has another attempt
    // scheduled, so the turn has not stopped.
    expect(irr.hasWorkInFlight(sessionWithError(errorPayload({ phase: 'retrying' })))).toBe(true)
  })

  test('a TERMINAL or unknown-phase errored session is not', () => {
    expect(irr.hasWorkInFlight(sessionWithError(errorPayload({ phase: 'terminal' })))).toBe(false)
    // Unknown phase is an honest "the transcript did not say", not a licence to
    // assume another attempt is coming.
    expect(irr.hasWorkInFlight(sessionWithError(errorPayload({ phase: '' })))).toBe(false)
    expect(irr.hasWorkInFlight(sessionWithError(null))).toBe(false)
  })

  test('it never throws on a partial session object', () => {
    // Sessions arrive from the websocket as whole-object replacements and a
    // freshly-discovered one can be missing metrics entirely.
    for (const a of [undefined, null, {}, { state: 'error' }, { state: 'error', metrics: {} }]) {
      expect(() => irr.hasWorkInFlight(a)).not.toThrow()
      expect(irr.hasWorkInFlight(a)).toBe(false)
    }
  })

  test('it agrees with the Go domain predicate it mirrors', () => {
    // Reads the Go source rather than restating its rule, so the two cannot
    // drift silently — the same technique the --error/Tokens.swift pairing
    // uses. Asserted to have FOUND the function, so "could not look" fails
    // loudly instead of passing.
    const go = readFileSync(
      join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'core', 'domain', 'session', 'session.go'),
      'utf8',
    )
    const body = go.match(/func \(s \*SessionState\) HasWorkInFlight\(\) bool \{[\s\S]*?\n\}/)
    expect(body, 'HasWorkInFlight not found in core/domain/session/session.go').not.toBeNull()
    expect(body[0]).toMatch(/StateWorking/)
    expect(body[0]).toMatch(/StateWaiting/)
    expect(body[0]).toMatch(/StateError/)
    expect(body[0]).toMatch(/IsRetrying/)
  })
})
