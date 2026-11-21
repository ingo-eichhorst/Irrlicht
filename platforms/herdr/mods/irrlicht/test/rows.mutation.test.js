// Committed mutants of lib/rows.js, lib/layout.js and lib/notes.js
// (test/mutant.js says how they are loaded): the redraw signature, the
// displayed order that `focus-<n>` follows, and three values carried over from
// the Swift app (the Gas Town mark, the branch in the tooltip, the 80% alert).
// Each mutant breaks one rule; the contract that rule exists for must then
// fail.

import { describe, expect, test } from 'vitest';
import * as layoutModule from '../lib/layout.js';
import * as rowsModule from '../lib/rows.js';
import {
  focusFollowsTheDrawnOrder, gastownGroupsAreMarked, pressureAlertFiresAtEighty, signatureFollowsEveryDrawnField,
  signatureMisses, tooltipNamesTheBranch,
} from './contracts.js';
import { mutantLoader } from './mutant.js';

const loadMutant = mutantLoader('irrlicht-herdr-rows-mutant-');

describe('committed mutants of the redraw signature', () => {
  test('the unmutated signature passes the contract', () => {
    signatureFollowsEveryDrawnField(rowsModule, layoutModule);
  });

  // This panel's first signature: each row's gauge and tooltip. Run against
  // signatureMisses, it misses exactly the fields below: the tooltip carries
  // the state, model and branch, and the row order carries the group order,
  // but nothing in it carries the subagent badge, the notes or the group.
  test('a signature of each row\'s gauge and tooltip misses the badge, the notes and the group', async () => {
    const rows = await loadMutant(
      'gauges-only',
      'lib/rows.js',
      'return JSON.stringify([statusLine(state), layout ? layout.groups : []]);',
      'return JSON.stringify([statusLine(state), layout ? layout.rows.map((r) => [r.address, r.gauge, r.tooltip]) : []]);',
    );
    expect(signatureMisses(rows, layoutModule)).toEqual([
      'subagents', 'question', 'last assistant text', 'error message', 'error class', 'group name', 'gastown',
    ]);
    expect(() => signatureFollowsEveryDrawnField(rows, layoutModule)).toThrow(/fields whose change does not move the signature/);
  });
});

describe('committed mutants of the displayed order', () => {
  test('numbering rows in Herdr\'s order instead of as drawn sends focus-1 to the wrong agent', async () => {
    const layout = await loadMutant(
      'herdr-order',
      'lib/layout.js',
      'return { groups: shown, rows: shown.flatMap((g) => g.rows), remembered: names.remembered };',
      'return { groups: shown, rows: [...rows], remembered: names.remembered };',
    );
    expect(() => focusFollowsTheDrawnOrder(rowsModule, layout)).toThrow(/the agents focus-1..3 name/);
  });
});

describe('committed mutants of the values carried over from the Swift app', () => {
  test('a layout that never marks a Gas Town group fails the Gas Town check', async () => {
    const layout = await loadMutant('no-gastown', 'lib/layout.js', 'gastown: rows[0].gastown === true,', 'gastown: false,');
    expect(() => gastownGroupsAreMarked(rowsModule, layout)).toThrow(/which groups are marked Gas Town/);
  });

  test('a tooltip without the branch fails the branch check', async () => {
    const rows = await loadMutant('no-branch', 'lib/rows.js', "const branch = f.branch ? 'branch ' + f.branch : '';", "const branch = '';");
    expect(() => tooltipNamesTheBranch(rows)).toThrow(/the tooltip of a session on a branch/);
  });

  test('an alert threshold of 85% fails the 80% check', async () => {
    const notes = await loadMutant('alert-at-85', 'lib/notes.js', 'export const PRESSURE_ALERT_PERCENT = 80;', 'export const PRESSURE_ALERT_PERCENT = 85;');
    expect(() => pressureAlertFiresAtEighty(notes.rowNotes)).toThrow(/pressure alerts at 79.9% and at 80%/);
  });
});
