import { describe, test, expect } from 'vitest'
import {
  DEFAULT_PROFILE, buildProfileSelector, evidenceHref, focusFromHash,
  profileFromHash, profileRecordingLink, profileStatus, profileVersions,
  recordingHash, renderProfileEvidence,
} from './profileView.js'

// #1889: the viewer shows Claude Code CLI Local and Claude Desktop Local as
// separate execution profiles. These tests pin the three things the issue's
// acceptance names — changing the profile changes the status, the versions and
// the recording link — plus the isolation rule underneath them: a Desktop
// result can never display a CLI recording.

const CELL = { agent: 'claudecode', subtree: 'scenarios', id: '1-1_session-start' }

// cliDetail is what /api/scenarios/... returns with no ?profile= — the
// backward-compatible view every pre-#1889 link lands on.
const cliDetail = {
  ...CELL,
  execution_profile: 'cli-local',
  latest_recording: '2026-05-01_cli',
  expected: { pass: true, summary: '6/6 phases' },
  latest_manifest: {
    name: '2026-05-01_cli',
    daemon_version: '0.6.1',
    agent_cli_version: '2.1.100',
    execution_profile: 'cli-local',
  },
  profiles: [
    { id: 'cli-local', label: 'Claude Code CLI Local', selectable: true, recordings: 3, has_result: false },
    { id: 'desktop-local', label: 'Claude Desktop Local', selectable: true, recordings: 1, has_result: true },
  ],
}

// desktopDetail is the SAME cell under ?profile=desktop-local: a different
// recording, a different status and different versions.
const desktopDetail = {
  ...CELL,
  execution_profile: 'desktop-local',
  latest_recording: '2026-06-02_desktop',
  expected: { pass: true, summary: '4/4 phases' },
  latest_manifest: {
    name: '2026-06-02_desktop',
    daemon_version: '0.6.2',
    agent_cli_version: '2.1.258',
    desktop_app_version: '1.44121.4',
    execution_profile: 'desktop-local',
  },
  profiles: cliDetail.profiles,
  desktop_result: {
    scenario_id: '1-1_session-start',
    outcome: 'observed-passing',
    recording: '2026-06-02_desktop',
    recording_profile: 'desktop-local',
    versions: { desktop_app: '1.44121.4', agent_cli: '2.1.258', irrlicht: '0.6.2' },
    evidence: [
      { field: 'desktop-registry.json', file: 'desktop-registry.json', present: true },
      { field: 'transcript.jsonl', file: 'transcript.jsonl', present: true },
    ],
  },
}

// crossProfileDetail is the mutation the isolation guard exists for: the
// Desktop result named the CLI recording, so the server refused to bind it and
// answered with an error instead of a recording, versions or evidence links.
//
// It deliberately keeps a populated latest_recording / latest_manifest. Those
// describe a DIFFERENT recording, and a client that fell back to them would
// dress a refused result in some other recording's link and versions —
// exactly the merge this issue forbids, and the reason the result governs.
const crossProfileDetail = {
  ...desktopDetail,
  desktop_result: {
    scenario_id: '1-1_session-start',
    outcome: 'observed-passing',
    recording: '',
    recording_profile: 'cli-local',
    error: 'recording "2026-05-01_cli" is cli-local evidence, not desktop-local — refusing to show it under a Desktop result',
  },
}

function textOf(node) {
  return node.textContent
}

describe('profileFromHash / focusFromHash', () => {
  test('a hash with no query is the CLI Local default', () => {
    expect(profileFromHash('#/recording/claudecode/scenarios/x')).toBe(DEFAULT_PROFILE)
    expect(profileFromHash('')).toBe(DEFAULT_PROFILE)
    expect(profileFromHash(undefined)).toBe(DEFAULT_PROFILE)
  })

  test('an explicit profile is read back', () => {
    expect(profileFromHash('#/recording/a/scenarios/x?profile=desktop-local')).toBe('desktop-local')
    expect(profileFromHash('#/recording/a/scenarios/x?profile=cli-local')).toBe('cli-local')
  })

  test('an unknown profile falls back to the default rather than being passed through', () => {
    // A guessed profile would reach the API as a 400; worse, a silently
    // accepted one could name a third body of evidence that does not exist.
    expect(profileFromHash('#/recording/a/scenarios/x?profile=desktop')).toBe(DEFAULT_PROFILE)
    expect(profileFromHash('#/recording/a/scenarios/x?profile=')).toBe(DEFAULT_PROFILE)
  })

  test('profile and focus coexist in either order', () => {
    const both = '#/recording/a/scenarios/x?profile=desktop-local&focus=recordings'
    expect(profileFromHash(both)).toBe('desktop-local')
    expect(focusFromHash(both)).toBe('recordings')
    const swapped = '#/recording/a/scenarios/x?focus=recordings&profile=desktop-local'
    expect(profileFromHash(swapped)).toBe('desktop-local')
    expect(focusFromHash(swapped)).toBe('recordings')
  })

  test('a focus-only hash still parses (the pre-#1889 shape)', () => {
    expect(focusFromHash('#/recording/a/scenarios/x?focus=spec')).toBe('spec')
    expect(profileFromHash('#/recording/a/scenarios/x?focus=spec')).toBe(DEFAULT_PROFILE)
  })
})

describe('recordingHash — the viewer stable link target', () => {
  test('the CLI Local default is omitted, so old links keep their exact shape', () => {
    expect(recordingHash({ ...CELL })).toBe('#/recording/claudecode/scenarios/1-1_session-start')
    expect(recordingHash({ ...CELL, profile: 'cli-local' }))
      .toBe('#/recording/claudecode/scenarios/1-1_session-start')
  })

  test('Desktop Local is an explicit, round-trippable suffix', () => {
    const hash = recordingHash({ ...CELL, profile: 'desktop-local' })
    expect(hash).toBe('#/recording/claudecode/scenarios/1-1_session-start?profile=desktop-local')
    expect(profileFromHash(hash)).toBe('desktop-local')
  })

  test('recording and focus ride along with the profile', () => {
    const hash = recordingHash({ ...CELL, recording: '2026-06-02_desktop', profile: 'desktop-local', focus: 'recordings' })
    expect(hash).toBe(
      '#/recording/claudecode/scenarios/1-1_session-start/2026-06-02_desktop?profile=desktop-local&focus=recordings')
    expect(focusFromHash(hash)).toBe('recordings')
  })
})

describe('changing the profile changes the status, the versions and the recording link', () => {
  test('status', () => {
    expect(profileStatus(cliDetail)).toMatchObject({ label: 'pass', detail: '6/6 phases' })
    expect(profileStatus(desktopDetail)).toMatchObject({ label: 'observed-passing' })
    expect(profileStatus(cliDetail).label).not.toBe(profileStatus(desktopDetail).label)
  })

  test('versions', () => {
    expect(profileVersions(cliDetail)).toEqual({ desktop_app: '', agent_cli: '2.1.100', irrlicht: '0.6.1' })
    expect(profileVersions(desktopDetail)).toEqual({ desktop_app: '1.44121.4', agent_cli: '2.1.258', irrlicht: '0.6.2' })
  })

  test('recording link', () => {
    expect(profileRecordingLink(cliDetail)).toEqual({
      name: '2026-05-01_cli',
      hash: '#/recording/claudecode/scenarios/1-1_session-start/2026-05-01_cli',
    })
    expect(profileRecordingLink(desktopDetail)).toEqual({
      name: '2026-06-02_desktop',
      hash: '#/recording/claudecode/scenarios/1-1_session-start/2026-06-02_desktop?profile=desktop-local',
    })
  })

  test('rendered together, the two profiles disagree on every one of the three', () => {
    const cli = renderProfileEvidence(cliDetail)
    const desktop = renderProfileEvidence(desktopDetail)
    expect(cli.querySelector('[data-testid=profile-status]').textContent)
      .not.toBe(desktop.querySelector('[data-testid=profile-status]').textContent)
    expect(cli.querySelector('[data-version=desktop_app]').textContent).toBe('—')
    expect(desktop.querySelector('[data-version=desktop_app]').textContent).toBe('1.44121.4')
    expect(cli.querySelector('[data-testid=profile-recording-link]').getAttribute('href'))
      .not.toBe(desktop.querySelector('[data-testid=profile-recording-link]').getAttribute('href'))
  })
})

describe('a Desktop result cannot display a CLI recording', () => {
  test('a refused result renders no recording link, no versions and no evidence links', () => {
    // The payload still carries an unrelated recording + manifest, so this
    // asserts the result governs rather than the fixture simply being empty.
    expect(crossProfileDetail.latest_recording).toBe('2026-06-02_desktop')
    expect(crossProfileDetail.latest_manifest.desktop_app_version).toBe('1.44121.4')

    expect(profileRecordingLink(crossProfileDetail)).toBeNull()
    const box = renderProfileEvidence(crossProfileDetail)
    expect(box.querySelector('[data-testid=profile-recording-link]')).toBeNull()
    expect(box.querySelector('[data-testid=profile-evidence-link]')).toBeNull()
    for (const key of ['desktop_app', 'agent_cli', 'irrlicht']) {
      expect(box.querySelector(`[data-version=${key}]`).textContent).toBe('—')
    }
    // Neither the CLI recording it named nor the unrelated sibling recording
    // may appear as this result's recording.
    const row = box.querySelector('[data-testid=profile-recording]')
    expect(textOf(row)).not.toContain('2026-05-01_cli')
    expect(textOf(row)).not.toContain('2026-06-02_desktop')
  })

  test('the refusal is loud: an error status carrying the reason', () => {
    const status = profileStatus(crossProfileDetail)
    expect(status.kind).toBe('error')
    expect(status.label).toBe('evidence rejected')
    expect(status.detail).toContain('cli-local')
    const box = renderProfileEvidence(crossProfileDetail)
    expect(box.querySelector('[data-testid=profile-status]').dataset.kind).toBe('error')
    expect(textOf(box)).toContain('refusing to show it under a Desktop result')
  })

  test('"nothing recorded" and "evidence rejected" are different answers', () => {
    const nothing = profileStatus({ ...CELL, execution_profile: 'desktop-local' })
    expect(nothing.kind).toBe('none')
    expect(nothing.label).toBe('no Desktop result')
    expect(nothing.label).not.toBe(profileStatus(crossProfileDetail).label)
  })
})

describe('evidence-based reasons for the non-observed outcomes', () => {
  for (const [outcome, reason, missing] of [
    ['not-applicable', 'The scenario is outside Local Desktop.', ''],
    ['unobservable', 'The local runtime leaves no observable trace.', ''],
    ['not-runnable', 'The Desktop driver cannot perform this recipe.', 'model selector'],
  ]) {
    test(outcome, () => {
      const detail = {
        ...CELL,
        execution_profile: 'desktop-local',
        profiles: cliDetail.profiles,
        desktop_result: {
          scenario_id: '1-1_session-start',
          outcome,
          reason,
          missing_control: missing,
          evidence_refs: ['replaydata/agents/claudecode/desktop-evidence/probe.md'],
        },
      }
      const box = renderProfileEvidence(detail)
      expect(box.querySelector('[data-testid=profile-status]').textContent).toContain(outcome)
      expect(box.querySelector('[data-testid=profile-reason]').textContent).toContain(reason)
      expect(box.querySelector('[data-testid=profile-evidence-ref]').textContent)
        .toBe('replaydata/agents/claudecode/desktop-evidence/probe.md')
      const control = box.querySelector('[data-testid=profile-missing-control]')
      if (missing) {
        expect(control.textContent).toContain(missing)
      } else {
        expect(control).toBeNull()
      }
      // No recording means no recording link — the reason IS the evidence.
      expect(box.querySelector('[data-testid=profile-recording-link]')).toBeNull()
    })
  }
})

describe('raw identity evidence links', () => {
  test('each present file links to its profile-scoped API path', () => {
    const box = renderProfileEvidence(desktopDetail)
    const links = [...box.querySelectorAll('[data-testid=profile-evidence-link]')]
    expect(links.map(a => a.textContent)).toEqual(['desktop-registry.json', 'transcript.jsonl'])
    expect(links[1].getAttribute('href')).toBe(evidenceHref(desktopDetail, '2026-06-02_desktop', 'transcript.jsonl'))
    expect(links[1].getAttribute('href')).toContain('profile=desktop-local')
  })

  test('a referenced-but-absent file renders as missing, never as a link', () => {
    const detail = {
      ...desktopDetail,
      desktop_result: {
        ...desktopDetail.desktop_result,
        evidence: [{ field: 'hooks.jsonl', file: 'hooks.jsonl', present: false }],
      },
    }
    const box = renderProfileEvidence(detail)
    expect(box.querySelector('[data-testid=profile-evidence-link]')).toBeNull()
    expect(box.querySelector('[data-testid=profile-evidence-missing]').textContent).toBe('hooks.jsonl (missing)')
  })
})

describe('buildProfileSelector', () => {
  test('offers every selectable profile and preselects the current one', () => {
    const selector = buildProfileSelector(desktopDetail)
    const select = selector.querySelector('[data-testid=profile-select]')
    expect([...select.options].map(o => o.value)).toEqual(['cli-local', 'desktop-local'])
    expect(select.value).toBe('desktop-local')
    expect(select.options[1].textContent).toContain('1 recording')
    expect(select.options[1].textContent).toContain('explicit result')
  })

  test('a profile the server marked unselectable is not offered', () => {
    const detail = {
      ...cliDetail,
      profiles: [
        { id: 'cli-local', label: 'Claude Code CLI Local', selectable: true, recordings: 3, has_result: false },
        { id: 'desktop-local', label: 'Claude Desktop Local', selectable: false, recordings: 0, has_result: false },
      ],
    }
    const select = buildProfileSelector(detail).querySelector('[data-testid=profile-select]')
    expect([...select.options].map(o => o.value)).toEqual(['cli-local'])
  })

  test('changing the selection reports the new profile id', () => {
    let picked = ''
    const selector = buildProfileSelector(cliDetail, next => { picked = next })
    const select = selector.querySelector('[data-testid=profile-select]')
    select.value = 'desktop-local'
    select.dispatchEvent(new Event('change'))
    expect(picked).toBe('desktop-local')
  })

  test('a profile value never reaches the DOM as markup', () => {
    const detail = {
      ...cliDetail,
      profiles: [{ id: 'cli-local', label: '<img src=x onerror=alert(1)>', selectable: true, recordings: 0, has_result: false }],
    }
    const selector = buildProfileSelector(detail)
    expect(selector.querySelector('img')).toBeNull()
  })
})
