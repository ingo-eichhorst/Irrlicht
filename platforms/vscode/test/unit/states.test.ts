import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { STATES, isStateName, stateColorId } from '../../src/states';

describe('states', () => {
  it('STATES is the canonical vocabulary in order', () => {
    expect([...STATES]).toEqual(['working', 'waiting', 'ready', 'error']);
  });

  it('isStateName accepts each state and rejects others', () => {
    for (const s of STATES) expect(isStateName(s)).toBe(true);
    expect(isStateName('cancelled')).toBe(false);
    expect(isStateName(undefined)).toBe(false);
  });

  it('stateColorId derives the theme colour id', () => {
    expect(stateColorId('waiting')).toBe('irrlicht.state.waiting');
  });

  it('manifest colours are exactly the STATES colour ids', () => {
    const pkg = JSON.parse(readFileSync(join(import.meta.dirname, '../../package.json'), 'utf8'));
    const ids: string[] = pkg.contributes.colors.map((c: { id: string }) => c.id);
    expect(ids).toEqual(STATES.map(stateColorId));
  });
});
