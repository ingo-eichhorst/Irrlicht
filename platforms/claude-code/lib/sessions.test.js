import { describe, expect, test } from 'vitest';
import {
  SESSION_STATES, adapterTag, dayCostUSD, flattenSessions, quotaSessions, sessionDetail, sessionSections, stateColor,
} from './sessions.js';
import { HERE_ID, SESSIONS_PAYLOAD } from './testdata/sessions.fixture.js';

// LOCKS over the committed synthetic payload (testdata/sessions.fixture.js):
// they pin how the group tree becomes the panel's rows.

describe('flattenSessions', () => {
  test('every session once: nested groups and children included', () => {
    expect(flattenSessions(SESSIONS_PAYLOAD).map(s => s.session_id)).toEqual([
      'sess-here-0001', 'sess-child-0002', 'sess-other-0003', 'sess-pi-0004', 'sess-codex-0005',
    ]);
  });

  test('a payload with no groups (a nil slice serialises as null)', () => {
    expect(flattenSessions({ groups: null })).toEqual([]);
  });
});

describe('quotaSessions is the dashboard\'s set', () => {
  test('top-level groups\' agents and their children; no nested group', () => {
    expect(quotaSessions(SESSIONS_PAYLOAD).map(s => s.session_id)).toEqual([
      'sess-here-0001', 'sess-child-0002', 'sess-other-0003', 'sess-pi-0004',
    ]);
  });
});

describe('sessionSections', () => {
  const sections = sessionSections(SESSIONS_PAYLOAD, HERE_ID);

  test('one section per group with sessions; a nested group is named under its parent', () => {
    expect(sections.map(s => [s.name, s.rows.length])).toEqual([
      ['orchard', 3],
      ['lighthouse', 1],
      ['fleet / rig-a', 1], // "fleet" itself holds no agent, so it has no section
    ]);
  });

  test('a child is a row of its own, one level deeper, after its parent', () => {
    expect(sections[0].rows.map(r => [r.id, r.depth])).toEqual([
      ['sess-here-0001', 0], ['sess-child-0002', 1], ['sess-other-0003', 0],
    ]);
  });

  test('the row figures', () => {
    const [here, child, other] = sections[0].rows;
    expect(here).toMatchObject({
      state: 'working', branch: 'main', tokens: '177K', context: '17.7%', model: 'opus-5.5', adapterTag: '', isHere: true,
    });
    // metrics.model_name beats model; a dated snapshot suffix is dropped.
    expect(child).toMatchObject({ tokens: '4.2K', context: '2.1%', model: 'haiku-4.5', adapterTag: '', isHere: false });
    // No model_name: the top-level model is used.
    expect(other).toMatchObject({ state: 'waiting', model: 'sonnet-4.5', isHere: false });
  });

  test('an empty adapter is Claude Code (untagged); other adapters are tagged', () => {
    expect(sections[0].rows[0].adapterTag).toBe(''); // adapter: ''
    expect(sections[0].rows[1].adapterTag).toBe(''); // adapter: 'claude-code'
    expect(sections[1].rows[0]).toMatchObject({ adapterTag: '[pi]', model: 'gpt-5.4', tokens: '40K', context: '4.0%' });
    expect(sections[2].rows[0]).toMatchObject({ adapterTag: '[codex]', state: 'error', tokens: '950', context: '' });
  });
});

describe('"← here" is an exact id match and nothing else', () => {
  const marked = id => sessionSections(SESSIONS_PAYLOAD, id).flatMap(s => s.rows).filter(r => r.isHere).map(r => r.id);

  test('the matching session alone', () => {
    expect(marked(HERE_ID)).toEqual([HERE_ID]);
    expect(marked('sess-pi-0004')).toEqual(['sess-pi-0004']);
  });

  test('no match, no marker: not a prefix, not a guess, not an empty id', () => {
    expect(marked('sess-here')).toEqual([]);
    expect(marked('SESS-HERE-0001')).toEqual([]);
    expect(marked('')).toEqual([]);
    expect(marked(undefined)).toEqual([]);
  });
});

describe('sessionDetail', () => {
  const [here, , other] = sessionSections(SESSIONS_PAYLOAD, HERE_ID)[0].rows;

  test('question headline first', () => {
    expect(other.detail).toEqual({
      state: 'waiting', elapsed: '1h2m', cost: '<$0.01',
      textKind: 'question', text: 'Apply the migration to the staging database?',
    });
  });

  test('else the last reply, on one line', () => {
    expect(here.detail).toEqual({
      state: 'working', elapsed: '10m5s', cost: '$1.23',
      textKind: 'last reply', text: 'Refactored the loader into two passes.',
    });
  });

  test('a long text is cut with an ellipsis', () => {
    const d = sessionDetail({ state: 'ready', metrics: { last_assistant_text: 'x'.repeat(500) } });
    expect(Array.from(d.text)).toHaveLength(200);
    expect(d.text.endsWith('…')).toBe(true);
  });

  test('nothing reported, nothing invented', () => {
    expect(sessionDetail({ state: 'ready' })).toEqual({ state: 'ready', elapsed: '', cost: '', textKind: '', text: '' });
  });
});

describe('dayCostUSD', () => {
  test('sums every provider key, unattributed included', () => {
    expect(dayCostUSD(SESSIONS_PAYLOAD)).toBeCloseTo(16.5, 10);
  });

  test('absent provider_costs is zero', () => {
    expect(dayCostUSD({ groups: [] })).toBe(0);
  });
});

describe('state colours', () => {
  test('each canonical state has its own colour; an unknown one is neutral', () => {
    const colors = SESSION_STATES.map(stateColor);
    expect(new Set(colors).size).toBe(SESSION_STATES.length);
    expect(colors).not.toContain('inactive');
    expect(stateColor('cancelled')).toBe('inactive');
    expect(stateColor('constructor')).toBe('inactive'); // no prototype leak
  });

  test('adapterTag of a missing adapter is empty', () => {
    expect(adapterTag({})).toBe('');
  });
});
