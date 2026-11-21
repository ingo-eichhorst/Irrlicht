// Committed mutants of lib/rows.js and lib/layout.js (test/mutant.js says how
// they are loaded): the redraw signature and the displayed order that
// `focus-<n>` follows. Each mutant breaks one rule; the contract that rule
// exists for must then fail.

import { describe, expect, test } from 'vitest';
import * as layoutModule from '../lib/layout.js';
import { readSessions } from '../lib/sessions.js';
import { signatureFollowsEveryDrawnField } from './contracts.js';
import { LIVE_AGENTS, LIVE_TERMINALS, livePayload } from './fixtures.js';
import { mutantLoader } from './mutant.js';

const loadMutant = mutantLoader('irrlicht-herdr-rows-mutant-');

describe('committed mutants of the redraw signature', () => {
  test('the unmutated signature passes the contract', async () => {
    signatureFollowsEveryDrawnField(await import('../lib/rows.js'), layoutModule);
  });

  // This panel's first signature: each row's gauge and tooltip. The tooltip
  // carries the state and the model, but not the subagent badge, the notes or
  // the group's name, so the contract fails on those.
  test('a signature of each row\'s gauge and tooltip misses the badge, the notes and the group', async () => {
    const rows = await loadMutant(
      'gauges-only',
      'rows.js',
      'return JSON.stringify([statusLine(state), layout ? layout.groups : []]);',
      'return JSON.stringify([statusLine(state), layout ? layout.rows.map((r) => [r.address, r.gauge, r.tooltip]) : []]);',
    );
    expect(() => signatureFollowsEveryDrawnField(rows, layoutModule)).toThrow(/must move the signature/);
  });
});

describe('committed mutants of the displayed order', () => {
  test('numbering rows in Herdr\'s order instead of as drawn sends focus-1 to the wrong agent', async () => {
    const layout = await loadMutant(
      'herdr-order',
      'layout.js',
      'return { groups: shown, rows: shown.flatMap((g) => g.rows), remembered: names.remembered };',
      'return { groups: shown, rows: [...rows], remembered: names.remembered };',
    );
    const { focusAddress, panelRows } = await import('../lib/rows.js');
    const rows = layout.panelLayout(panelRows(LIVE_AGENTS, LIVE_TERMINALS, readSessions(livePayload())), []).rows;
    // The panel draws the codex group (t4) first; Herdr lists t3 first.
    expect(focusAddress('focus-1', rows)).not.toBe('t4');
  });
});
