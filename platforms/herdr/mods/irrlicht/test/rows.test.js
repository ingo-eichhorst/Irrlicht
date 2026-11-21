import { describe, expect, test } from 'vitest';
import { OTHER_GROUP_KEY, countText, headerSummary, panelLayout, rememberGroups } from '../lib/layout.js';
import * as layoutModule from '../lib/layout.js';
import { rowNotes } from '../lib/notes.js';
import { BRAND } from '../lib/palette.js';
import * as rowsModule from '../lib/rows.js';
import { readSessions } from '../lib/sessions.js';
import {
  focusFollowsTheDrawnOrder, gastownGroupsAreMarked, pressureAlertFiresAtEighty, signatureFollowsEveryDrawnField, tooltipNamesTheBranch,
} from './contracts.js';
import { CLAUDE_SESSION, LIVE_AGENTS, LIVE_TERMINALS, livePayload } from './fixtures.js';

const { focusAddress, panelRows, shortModelName, signature } = rowsModule;
const live = () => panelRows(LIVE_AGENTS, LIVE_TERMINALS, readSessions(livePayload()));
const laid = (rows, remembered) => panelLayout(rows, remembered);

describe('panelRows', () => {
  test('before the daemon answered, every agent shows Herdr\'s status in Irrlicht\'s states', () => {
    const rows = panelRows(LIVE_AGENTS, LIVE_TERMINALS, null);
    expect(rows.map((r) => [r.address, r.label, r.matched, r.state, r.gauge.kind])).toEqual([
      ['t3', 'TCP slow start notes', false, 'ready', 'none'],
      ['t4', 'Create DNS resolution notes | codex', false, 'ready', 'none'],
    ]);
    expect(rows[0].tooltip).toBe('herdr-demo · TCP slow start notes · Herdr: done · no Irrlicht session matched');
    expect(rows[0].stateTip).toBe('Ready (Herdr: done)');
  });

  test('a matched row takes its session\'s state, model, group and gauge, and says how it was matched', () => {
    const [claude, codex] = live();
    expect([claude.state, claude.model, claude.group, claude.gauge.text]).toEqual(['waiting', 'opus-5.5', 'claude', '4.6%']);
    expect([codex.state, codex.model, codex.group, codex.gauge.text]).toEqual(['ready', 'gpt-6.1-sol', 'codex', '3.9%']);
    expect(claude.tooltip).toBe('herdr-demo · TCP slow start notes · Waiting for input · 4.6% of 1.0M context, safe · claude-opus-5-5 · matched by pid');
    expect(codex.tooltip).toBe('herdr-demo · Create DNS resolution notes | codex · Ready · 3.9% of 475K context, safe · gpt-6.1-sol · matched by agent and folder');
  });

  test('an agent on another machine is unmatched, and its tooltip says why', () => {
    const remote = { ...LIVE_AGENTS[0], address: 'vps/t3' };
    const remoteTerminal = { ...LIVE_TERMINALS[1], address: 'vps/t3' };
    const [row] = panelRows([remote], [remoteTerminal], readSessions(livePayload()));
    expect(row.matched).toBe(false);
    expect(row.tooltip).toMatch(/on another machine: Irrlicht reads this machine's sessions only$/);
  });

  test('the tooltip names the session\'s branch', () => {
    tooltipNamesTheBranch(rowsModule);
  });

  test('a session state this panel cannot read is unknown, never a guess', () => {
    const payload = livePayload();
    payload.groups[1].agents[0] = { ...CLAUDE_SESSION, state: 'hibernating' };
    const [claude] = panelRows(LIVE_AGENTS, LIVE_TERMINALS, readSessions(payload));
    expect([claude.state, claude.stateTip]).toEqual(['unknown', 'Unknown state']);
  });
});

describe('shortModelName, as SessionState.shortModelName', () => {
  test.each([
    ['claude-opus-5-5', 'opus-5.5'],
    ['claude-sonnet-4-6', 'sonnet-4.6'],
    ['anthropic/claude-opus-4-7', 'opus-4.7'],
    ['openai/google/gemma-4-26b-a4b', 'gemma-4-26b-a4b'],
    ['gpt-6.1-sol', 'gpt-6.1-sol'],
    ['', ''],
    [undefined, ''],
  ])('%s → %s', (model, want) => {
    expect(shortModelName(model)).toBe(want);
  });
});

describe('panelLayout', () => {
  test('groups matched rows by their session\'s daemon group, in payload order, numbered as drawn', () => {
    const layout = laid(live(), []);
    expect(layout.groups.map((g) => [g.name, g.count, g.rows.map((r) => [r.n, r.address])])).toEqual([
      ['codex', '1 session', [[1, 't4']]],
      ['claude', '1 session', [[2, 't3']]],
    ]);
    expect(layout.rows.map((r) => r.address)).toEqual(['t4', 't3']);
    expect(layout.remembered).toEqual(['codex', 'claude']);
  });

  test('agents no session matched trail in one group, in Herdr\'s order', () => {
    const extra = { ...LIVE_AGENTS[0], address: 't9', title: 'shell', status: 'working' };
    const rows = panelRows([extra, ...LIVE_AGENTS], LIVE_TERMINALS, readSessions(livePayload()));
    const layout = laid(rows, []);
    const other = layout.groups.at(-1);
    expect([other.key, other.name, other.count, other.rows.map((r) => [r.n, r.address, r.state])])
      .toEqual([OTHER_GROUP_KEY, 'Other agents', '1 agent', [[3, 't9', 'working']]]);
  });

  test('the trailing group never merges with a daemon group named "unknown"', () => {
    const payload = livePayload();
    payload.groups[1].name = 'unknown';
    const extra = { ...LIVE_AGENTS[0], address: 't9' };
    const layout = laid(panelRows([...LIVE_AGENTS, extra], LIVE_TERMINALS, readSessions(payload)), []);
    expect(layout.groups.map((g) => g.name)).toEqual(['codex', 'unknown', 'Other agents']);
  });

  test('two rows in one group keep the payload\'s order, whatever Herdr\'s order', () => {
    const payload = livePayload();
    // Both sessions in one project, the Codex one first, as the daemon sends them.
    payload.groups = [{ name: 'herdr-demo', agents: [payload.groups[0].agents[0], payload.groups[1].agents[0]] }];
    const layout = laid(panelRows(LIVE_AGENTS, LIVE_TERMINALS, readSessions(payload)), []);
    expect(layout.groups.map((g) => [g.name, g.count, g.rows.map((r) => r.address)])).toEqual([['herdr-demo', '2 sessions', ['t4', 't3']]]);
  });

  test('a remembered group keeps its place when the payload lists it later, and when it comes back', () => {
    const swapped = livePayload();
    swapped.groups.reverse();
    const rowsSwapped = panelRows(LIVE_AGENTS, LIVE_TERMINALS, readSessions(swapped));
    expect(laid(rowsSwapped, ['codex', 'claude']).groups.map((g) => g.name)).toEqual(['codex', 'claude']);
    // Codex leaves (no session), then returns: it is remembered first.
    const withoutCodex = livePayload();
    withoutCodex.groups.splice(0, 1);
    const gone = laid(panelRows(LIVE_AGENTS, LIVE_TERMINALS, readSessions(withoutCodex)), ['codex', 'claude']);
    expect(gone.groups.map((g) => g.name)).toEqual(['claude', 'Other agents']);
    expect(gone.remembered).toEqual(['codex', 'claude']);
    expect(laid(rowsSwapped, gone.remembered).groups.map((g) => g.name)).toEqual(['codex', 'claude']);
  });

  test('a Gas Town group is marked as one, a project\'s group is not', () => {
    gastownGroupsAreMarked(rowsModule, layoutModule);
  });

  test('the header summary is up to three state icons, else a count', () => {
    expect(headerSummary(laid(live(), []))).toEqual({ states: ['ready', 'waiting'], text: '' });
    const many = [...LIVE_AGENTS, ...LIVE_AGENTS].map((a, i) => ({ ...a, address: `t${i + 10}` }));
    expect(headerSummary(laid(panelRows(many, [], null), []))).toEqual({ states: [], text: '4 agents' });
    expect(headerSummary(laid([], []))).toEqual({ states: [], text: '' });
  });
});

describe('rememberGroups, as SessionManager.orderedGroups', () => {
  test('appends first-seen names in the order given and sorts by what is remembered', () => {
    expect(rememberGroups(['b', 'a'], [])).toEqual({ ordered: ['b', 'a'], remembered: ['b', 'a'] });
    expect(rememberGroups(['c', 'a', 'b'], ['b', 'a'])).toEqual({ ordered: ['b', 'a', 'c'], remembered: ['b', 'a', 'c'] });
  });

  test('never forgets a name, and leaves the remembered order as it was when nothing is new', () => {
    const remembered = ['b', 'gone', 'a'];
    const got = rememberGroups(['a', 'b'], remembered);
    expect(got).toEqual({ ordered: ['b', 'a'], remembered });
    expect(got.remembered).toBe(remembered);
  });

  test('a duplicate in the remembered order does not move a group', () => {
    expect(rememberGroups(['a', 'b'], ['a', 'b', 'a']).ordered).toEqual(['a', 'b']);
  });

  test('counts read as GroupView\'s', () => {
    expect([countText(1, 'session'), countText(2, 'session'), countText(0, 'agent')]).toEqual(['1 session', '2 sessions', '0 agents']);
  });
});

describe('rowNotes', () => {
  const facts = (over) => ({ state: 'ready', percent: 10, questionHeadline: '', lastAssistantText: '', errorMessage: '', errorClass: '', ...over });

  test('a failed session says why: its message, else its class\'s label, else a bare statement', () => {
    expect(rowNotes(facts({ state: 'error', errorMessage: 'Invalid API key', errorClass: 'auth' }))).toEqual([
      { kind: 'error', text: 'Invalid API key', tooltip: 'Invalid API key\nclass: auth', hue: BRAND.error },
    ]);
    expect(rowNotes(facts({ state: 'error', errorClass: 'quota' }))[0].text).toBe('Usage quota exhausted');
    expect(rowNotes(facts({ state: 'error', errorClass: 'brand_new' }))[0].text).toBe('brand_new');
    expect(rowNotes(facts({ state: 'error' }))[0].text).toBe('The session failed');
  });

  test('a waiting session shows its question headline, the full text as its tooltip', () => {
    expect(rowNotes(facts({ state: 'waiting', questionHeadline: 'Ship it?', lastAssistantText: 'Shall I ship it now?' }))).toEqual([
      { kind: 'question', text: 'Ship it?', tooltip: 'Shall I ship it now?', hue: BRAND.waiting },
    ]);
    expect(rowNotes(facts({ state: 'waiting', lastAssistantText: 'Shall I?' }))[0].text).toBe('Shall I?');
    expect(rowNotes(facts({ state: 'waiting' }))).toEqual([]);
    expect(rowNotes(facts({ state: 'ready', questionHeadline: 'Ship it?' }))).toEqual([]);
  });

  test('the alert fires at 80%, the Swift app\'s default threshold', () => {
    pressureAlertFiresAtEighty(rowNotes);
  });

  test('a session still going at 80% or more is told to switch; a finished one is not', () => {
    const alert = (state, percent) => rowNotes(facts({ state, percent })).map((n) => n.kind);
    expect(alert('working', 80)).toEqual(['pressure']);
    expect(alert('waiting', 95)).toEqual(['pressure']);
    expect(alert('working', 79.9)).toEqual([]);
    expect(alert('ready', 95)).toEqual([]);
  });

  test('the lines stack as the Swift row stacks them: error, question, pressure', () => {
    const waiting = rowNotes(facts({ state: 'waiting', percent: 90, questionHeadline: 'Go?' }));
    expect(waiting.map((n) => n.kind)).toEqual(['question', 'pressure']);
  });
});

describe('signature', () => {
  test('every drawn daemon field moves it; a session no row shows does not', () => {
    signatureFollowsEveryDrawnField(rowsModule, layoutModule);
  });

  test('the status line moves it', () => {
    const layout = laid(live(), []);
    expect(signature({ phase: 'ok', where: 'localhost:7837' }, layout)).not.toBe(signature({ phase: 'stalled', where: 'localhost:7837' }, layout));
  });
});

describe('focusAddress', () => {
  test('"focus-<n>" names row n as drawn, counting from 1, not agent n in Herdr\'s order', () => {
    focusFollowsTheDrawnOrder(rowsModule, layoutModule);
  });

  test('a row that does not exist, or another command, names nothing', () => {
    const rows = laid(live(), []).rows;
    expect(focusAddress('focus-3', rows)).toBeNull();
    expect(focusAddress('focus-0', rows)).toBeNull();
    expect(focusAddress('refresh', rows)).toBeNull();
  });
});
