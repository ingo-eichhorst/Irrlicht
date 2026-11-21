// Committed mutants of lib/match.js (AGENTS.md: commit the mutation rather than
// describing it). Each mutant breaks one matching rule; the contract that
// rule exists for must then fail. A mutant that still passes means the
// contract no longer reaches the rule it claims to protect.
//
// Each anchor must occur exactly once in lib/match.js: an anchor that stopped
// matching fails here as stale instead of silently testing the unmutated file.

import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { afterAll, describe, expect, test } from 'vitest';
import {
  ambiguousPairsGetNoGauge, claimedSessionsStayWithTheirRow, foregroundPidWinsOverTerminalPid,
  pidMatchedAgentsCountInTheirFolder,
} from './contracts.js';

const LIB = new URL('../lib/', import.meta.url);
const source = (name) => readFileSync(new URL(name, LIB), 'utf8');
const dir = mkdtempSync(join(tmpdir(), 'irrlicht-herdr-mutant-'));
afterAll(() => rmSync(dir, { recursive: true, force: true }));

// loadMutant writes lib/match.js with `anchor` replaced, next to a copy of
// the one module it imports, and loads it.
async function loadMutant(name, anchor, replacement) {
  const original = source('match.js');
  expect(original.split(anchor).length - 1, `anchor for ${name} must occur exactly once in lib/match.js`).toBe(1);
  const mutant = original.replace(anchor, replacement);
  expect(mutant).not.toBe(original);
  const sub = join(dir, name);
  mkdirSync(sub, { recursive: true });
  writeFileSync(join(sub, 'sessions.js'), source('sessions.js'));
  writeFileSync(join(sub, 'match.js'), mutant);
  return import(pathToFileURL(join(sub, 'match.js')).href);
}

describe('committed mutants of the matching rules', () => {
  test('dropping the uniqueness check lets an ambiguous pair trade numbers', async () => {
    const { matchAgents } = await loadMutant(
      'no-uniqueness',
      'const unique = agentsPerKey.get(k) === 1 && pair.length === 1;',
      'const unique = pair.length >= 1;',
    );
    expect(() => ambiguousPairsGetNoGauge(matchAgents)).toThrow();
  });

  test('dropping the claimed check hands a pid-matched session to a second row', async () => {
    const { matchAgents } = await loadMutant(
      'no-claimed-check',
      'if (unique && !claimed.has(pair[0].id)) {',
      'if (unique) {',
    );
    expect(() => claimedSessionsStayWithTheirRow(matchAgents)).toThrow();
  });

  test('counting only the undecided agents lets a folder look unambiguous', async () => {
    const { matchAgents } = await loadMutant(
      'count-undecided-only',
      'const agentsPerKey = countBy(agents, agentKeyOf);',
      'const agentsPerKey = countBy(undecided, agentKeyOf);',
    );
    expect(() => pidMatchedAgentsCountInTheirFolder(matchAgents)).toThrow();
  });

  test('asking the terminal pid first picks the wrapper\'s session', async () => {
    const { matchAgents } = await loadMutant(
      'terminal-pid-first',
      'const pids = [terminal?.foreground?.pid, terminal?.pid];',
      'const pids = [terminal?.pid, terminal?.foreground?.pid];',
    );
    expect(() => foregroundPidWinsOverTerminalPid(matchAgents)).toThrow();
  });
});
