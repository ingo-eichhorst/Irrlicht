// Committed mutants of lib/match.js (test/mutant.js says how they are
// loaded). Each mutant breaks one matching rule; the contract that rule exists
// for must then fail.

import { describe, expect, test } from 'vitest';
import {
  ambiguousPairsGetNoGauge, claimedSessionsStayWithTheirRow, foregroundPidWinsOverTerminalPid,
  pidMatchedAgentsCountInTheirFolder, remoteAgentsAreNeverMatched,
} from './contracts.js';
import { mutantLoader } from './mutant.js';

const load = mutantLoader('irrlicht-herdr-mutant-');
const loadMutant = (name, anchor, replacement) => load(name, 'match.js', anchor, replacement);

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
      'const agentsPerKey = countBy(herdr, agentKey);',
      'const agentsPerKey = countBy(undecided, agentKey);',
    );
    expect(() => pidMatchedAgentsCountInTheirFolder(matchAgents)).toThrow();
  });

  test('asking the terminal pid first picks the wrapper\'s session', async () => {
    const { matchAgents } = await loadMutant(
      'terminal-pid-first',
      'const pids = [terminal.foreground?.pid, terminal.pid];',
      'const pids = [terminal.pid, terminal.foreground?.pid];',
    );
    expect(() => foregroundPidWinsOverTerminalPid(matchAgents)).toThrow();
  });

  test('matching agents on other machines lets a remote pid show a local session', async () => {
    const { matchAgents } = await loadMutant(
      'remote-agents-matched',
      'allAgents.filter(isLocal).map(',
      'allAgents.map(',
    );
    expect(() => remoteAgentsAreNeverMatched(matchAgents)).toThrow();
  });
});
