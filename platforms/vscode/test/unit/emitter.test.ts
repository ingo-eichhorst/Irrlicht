import { describe, expect, it } from 'vitest';
import { Emitter } from '../../src/emitter';

describe('Emitter', () => {
  it('fires over a snapshot: a listener added while firing is first called on the next fire', () => {
    const e = new Emitter<number>();
    const seen: string[] = [];
    let added = false;
    e.event((v) => {
      seen.push(`a${v}`);
      if (!added) { added = true; e.event((w) => seen.push(`late${w}`)); }
    });
    e.fire(1);
    expect(seen).toEqual(['a1']);
    e.fire(2);
    expect(seen).toEqual(['a1', 'a2', 'late2']);
  });

  it('fires over a snapshot: a listener removed while firing still gets this fire, not the next', () => {
    const e = new Emitter<number>();
    const seen: string[] = [];
    let later: { dispose(): void } | undefined;
    e.event((v) => { seen.push(`a${v}`); later?.dispose(); });
    later = e.event((v) => seen.push(`b${v}`));
    e.fire(1);
    e.fire(2);
    expect(seen).toEqual(['a1', 'b1', 'a2']);
  });

  it('a throwing listener goes to onError and does not stop its siblings', () => {
    const errors: unknown[] = [];
    const e = new Emitter<void>((err) => errors.push(err));
    const seen: string[] = [];
    e.event(() => { throw new Error('boom'); });
    e.event(() => seen.push('b'));
    e.fire();
    expect(seen).toEqual(['b']);
    expect(errors.map(String)).toEqual(['Error: boom']);
  });
});
