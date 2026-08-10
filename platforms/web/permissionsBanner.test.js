import { describe, test, expect, beforeEach, beforeAll, afterAll, afterEach } from 'vitest'

import { refreshPermissions } from './permissionsWizard.js'
import * as permWizard from './permissionsWizard.js'
import { pendingWizardAgents } from './irrlicht.js'

// --- #1385: an aggregate "granted but not applied" signal ----------------
//
// #1362 made a failed consent effect visible inside the WIZARD, so a startup
// re-apply failure reached only a user who opened Settings. These pin the
// passive dashboard indicator that closes that gap, and the three constraints
// it inherits: it must not nag, must not auto-open the wizard, and must not
// change consent.
//
// `permWizard` is imported as a namespace on purpose: a missing export then
// fails the individual test with a readable TypeError instead of breaking the
// module's link and taking every suite in the file down with it.
//
// This lives in its own file: platforms/web/irrlicht.test.js was already
// 1407 lines before this feature, and CodeScene flags it for size. The
// aggregate's tests are self-contained (each builds the DOM it needs), so
// they cost nothing by moving out of it.

// Shared fixture for both blocks: the aggregate helpers and the wiring
// reachability tests read the same wire shape, so the shape is written once.
const UNAPPLIED_REASON = 'settings.json is malformed: invalid character \'}\''
const UNAPPLIED_FLOOR = 'claude 1.2.0 is below the required 2.0.0; upgrade and grant again'
const unappliedSnap = (...unapplied) => ({
  mode: 'ask',
  agents: [{
    name: 'claude-code', display_name: 'Claude Code', detected: true,
    permissions: [{ key: 'hooks', kind: 'modify', state: 'granted', title: 'Install hooks' }],
  }],
  unapplied_grants: unapplied,
})
const unappliedInstallFailure = {
  agent: 'claude-code', agent_display_name: 'Claude Code',
  key: 'hooks', title: 'Install hooks', reason: UNAPPLIED_REASON,
}
const unappliedVersionRefusal = {
  agent: 'codex', agent_display_name: 'Codex',
  key: 'hooks', title: 'Install hooks', reason: UNAPPLIED_FLOOR,
}

describe('aggregate "granted but not applied" indicator (#1385)', () => {
  const REASON = UNAPPLIED_REASON
  const FLOOR = UNAPPLIED_FLOOR
  const snapWith = unappliedSnap
  const failedInstall = unappliedInstallFailure
  const versionRefusal = unappliedVersionRefusal

  beforeEach(() => {
    document.body.innerHTML =
      '<div id="permission-apply-banner" role="status" aria-live="polite" hidden></div>'
  })

  test('a healthy snapshot produces no summary', () => {
    expect(permWizard.unappliedGrantSummary(snapWith())).toBeNull()
    expect(permWizard.unappliedGrantSummary({ mode: 'ask', agents: [] })).toBeNull()
    expect(permWizard.unappliedGrantSummary(null)).toBeNull()
  })

  test('the headline counts, and reads naturally at one', () => {
    expect(permWizard.unappliedGrantSummary(snapWith(failedInstall)).text)
      .toBe('1 permission is granted but not applied')
    expect(permWizard.unappliedGrantSummary(snapWith(failedInstall, versionRefusal)).text)
      .toBe('2 permissions are granted but not applied')
    expect(permWizard.unappliedGrantSummary(snapWith(failedInstall, versionRefusal)).count).toBe(2)
  })

  test('the two diagnoses in the aggregate stay distinguishable', () => {
    // The headline is one number; the detail must still say WHICH and WHY,
    // or an install failure (#1362) and a version-floor refusal (#1365)
    // collapse into the same undiagnosable warning.
    const s = permWizard.unappliedGrantSummary(snapWith(failedInstall, versionRefusal))
    expect(s.items.map(i => i.reason)).toEqual([REASON, FLOOR])
    expect(s.items.map(i => `${i.agent_display_name}: ${i.title}`))
      .toEqual(['Claude Code: Install hooks', 'Codex: Install hooks'])
  })

  // Reachability: assert the REAL banner element the dashboard renders,
  // not a pure helper beside it.
  test('the dashboard renders a passive banner naming each cause', () => {
    permWizard.renderUnappliedGrantsBanner(snapWith(failedInstall, versionRefusal))
    const el = document.getElementById('permission-apply-banner')
    expect(el.hidden).toBe(false)
    expect(el.textContent).toContain('2 permissions are granted but not applied')
    expect(el.textContent).toContain(REASON)
    expect(el.textContent).toContain(FLOOR)
    expect(el.textContent).toContain('Claude Code')
    expect(el.textContent).toContain('Codex')
  })

  test('the banner is passive: polite, never an alert, and has no dismiss', () => {
    // role=status/aria-live=polite is the difference between "told" and
    // "interrupted". A dismiss button would let a real fault be hidden
    // while it is still broken; #1385 says dismissible-BY-FIXING only.
    permWizard.renderUnappliedGrantsBanner(snapWith(failedInstall))
    const el = document.getElementById('permission-apply-banner')
    expect(el.getAttribute('role')).toBe('status')
    expect(el.getAttribute('aria-live')).toBe('polite')
    expect(el.querySelector('[data-dismiss], .banner-dismiss')).toBeNull()
  })

  test('fixing it makes the banner go away with no gesture', () => {
    permWizard.renderUnappliedGrantsBanner(snapWith(failedInstall))
    expect(document.getElementById('permission-apply-banner').hidden).toBe(false)
    permWizard.renderUnappliedGrantsBanner(snapWith())
    const el = document.getElementById('permission-apply-banner')
    expect(el.hidden).toBe(true)
    expect(el.textContent).toBe('')
  })

  test('it never opens the wizard by itself', () => {
    // The #1362 loop this avoids: fail -> wizard -> retry -> fail. The
    // route to the wizard is a button the USER clicks.
    document.body.innerHTML +=
      '<div id="permissions-backdrop"></div><div id="permissions-body"></div>'
    permWizard.renderUnappliedGrantsBanner(snapWith(failedInstall))
    expect(document.getElementById('permissions-backdrop').classList.contains('open')).toBe(false)
    const btn = document.querySelector('#permission-apply-banner button')
    expect(btn).not.toBeNull()
    expect(btn.textContent).toBe('Review permissions')
  })

  test('the aggregate does not widen the auto wizard', () => {
    // pendingWizardAgents is what pops the wizard open. An unapplied grant
    // is answered, so it must not appear there — that is #1362's
    // deliberate non-widening of needsWizard, and #1385 must not reverse it.
    expect(pendingWizardAgents(snapWith(failedInstall))).toEqual([])
  })
})

// Reachability for #1385, and the counterpart of the "#1362 auto wizard"
// suite above: the seven tests in the block before this one call
// renderUnappliedGrantsBanner DIRECTLY, so deleting the call inside
// refreshPermissions left every one of them green (verified by mutation in
// review). This drives the REAL path — fetch → refreshPermissions →
// renderUnappliedGrantsBanner — which is the one a refactor can break.
describe('the dashboard actually wires the aggregate banner up (#1385)', () => {
  const REASON = UNAPPLIED_REASON
  const snapWith = unappliedSnap
  const failed = unappliedInstallFailure

  let realFetch
  const serve = (snap) => {
    globalThis.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(snap) })
    return refreshPermissions()
  }
  const banner = () => document.getElementById('permission-apply-banner')

  beforeAll(() => { realFetch = globalThis.fetch })
  afterAll(() => { globalThis.fetch = realFetch })
  beforeEach(() => {
    document.body.innerHTML +=
      '<div id="permission-apply-banner" role="status" aria-live="polite" hidden></div>'
  })
  afterEach(() => { banner()?.remove() })

  test('a snapshot fetched from the daemon reaches the banner', async () => {
    await serve(snapWith(failed))
    expect(banner().hidden).toBe(false)
    expect(banner().textContent).toContain('1 permission is granted but not applied')
    expect(banner().textContent).toContain(REASON)
  })

  test('the next fetch clears it once the install is repaired', async () => {
    await serve(snapWith(failed))
    expect(banner().hidden).toBe(false)
    await serve(snapWith())
    expect(banner().hidden).toBe(true)
  })

  test('an unchanged banner is not rebuilt, so role=status stops re-announcing', async () => {
    // aria-atomic re-reads the whole strip on every rebuild, and this path
    // runs on every push AND every websocket reconnect. Identity of the
    // button node is the observable proxy for "was it rebuilt".
    await serve(snapWith(failed))
    const first = banner().querySelector('button')
    await serve(snapWith(failed))
    expect(banner().querySelector('button')).toBe(first)
  })
})
