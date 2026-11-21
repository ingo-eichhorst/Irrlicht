import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it, vi } from 'vitest';
import { SessionStore } from '../../src/sessionStore';
import { STATES, isStateName, type StateName } from '../../src/states';

const fixture = JSON.parse(readFileSync(join(__dirname, 'fixtures', 'sessions-response.json'), 'utf8'));

function hydrated(): SessionStore {
  const s = new SessionStore();
  s.hydrate(fixture);
  return s;
}

// The top-level-only counting guard, factored so it can run against the real
// store (must pass) and the committed mutant below (must throw).
function checkTopLevelOnlyCounts(make: () => SessionStore): void {
  const s = make();
  s.hydrate(fixture);
  expect(s.counts()).toEqual({ working: 2, waiting: 1, ready: 1, error: 1 });
  const late = make();
  late.apply({ type: 'session_created', session: { session_id: 'kid', parent_session_id: 'p', state: 'working' } });
  late.apply({ type: 'session_created', session: { session_id: 'p', state: 'ready' } });
  expect(late.counts()).toEqual({ working: 0, waiting: 0, ready: 1, error: 0 });
}

describe('SessionStore.hydrate', () => {
  it('flattens groups, nested groups, agents and children', () => {
    const ids = hydrated().all().map((x) => x.session_id).sort();
    expect(ids).toEqual(['sess-a', 'sess-a-child', 'sess-b', 'sess-c', 'sess-d', 'sess-orphan']);
  });

  it('tolerates a bare array of groups', () => {
    const s = new SessionStore();
    s.hydrate(fixture.groups);
    expect(s.all()).toHaveLength(6);
  });

  it('ignores garbage without throwing', () => {
    const s = hydrated();
    for (const g of [null, undefined, 7, 'x', {}, { groups: 'no' }, { groups: [null, 3, { agents: 'x' }, { agents: [{}, { session_id: 5 }] }] }]) {
      expect(() => s.hydrate(g)).not.toThrow();
    }
    expect(s.all()).toHaveLength(0);
  });

  it('replaces the whole set and fires change', () => {
    const s = hydrated();
    const l = vi.fn();
    s.onDidChange(l);
    s.hydrate({ groups: [{ name: 'g', agents: [{ session_id: 'only', state: 'ready' }] }] });
    expect(s.all().map((x) => x.session_id)).toEqual(['only']);
    expect(l).toHaveBeenCalledTimes(1);
  });
});

describe('SessionStore.counts / topLevel', () => {
  it('counts top-level sessions only; orphans count, children do not', () => {
    const s = hydrated();
    expect(s.counts()).toEqual({ working: 2, waiting: 1, ready: 1, error: 1 });
    expect(s.topLevel().map((x) => x.session_id).sort()).toEqual(['sess-a', 'sess-b', 'sess-c', 'sess-d', 'sess-orphan']);
  });

  // Committed in-language mutant: counts() iterates all() instead of topLevel().
  it('mutant: counting every session (children included) is caught by the top-level check', () => {
    class CountsAll extends SessionStore {
      override counts(): Record<StateName, number> {
        const c = {} as Record<StateName, number>;
        for (const st of STATES) c[st] = 0;
        for (const x of this.all()) if (isStateName(x.state)) c[x.state]++;
        return c;
      }
    }
    expect(() => checkTopLevelOnlyCounts(() => new CountsAll())).toThrow();
  });

  it('real store passes the top-level-only check', () => {
    checkTopLevelOnlyCounts(() => new SessionStore());
  });

  it('has every StateName key with 0 default', () => {
    expect(Object.keys(new SessionStore().counts()).sort()).toEqual([...STATES].sort());
    for (const st of STATES) expect(new SessionStore().counts()[st]).toBe(0);
  });

  it('excludes unknown states from counts but keeps them in all()', () => {
    const s = new SessionStore();
    s.hydrate({ groups: [{ name: 'g', agents: [{ session_id: 'x', state: 'cancelled' }, { session_id: 'y', state: 'ready' }] }] });
    expect(s.all()).toHaveLength(2);
    expect(s.counts()).toEqual({ working: 0, waiting: 0, ready: 1, error: 0 });
  });

  it('a child whose parent arrives later stops being top-level', () => {
    const s = new SessionStore();
    s.apply({ type: 'session_created', session: { session_id: 'kid', parent_session_id: 'p', state: 'working' } });
    expect(s.counts().working).toBe(1);
    s.apply({ type: 'session_created', session: { session_id: 'p', state: 'ready' } });
    expect(s.counts()).toEqual({ working: 0, waiting: 0, ready: 1, error: 0 });
  });
});

describe('SessionStore.apply', () => {
  it('upserts on created/updated and removes on deleted', () => {
    const s = hydrated();
    expect(s.apply({ type: 'session_updated', session: { session_id: 'sess-b', state: 'ready' } })).toBe('changed');
    expect(s.all().find((x) => x.session_id === 'sess-b')?.state).toBe('ready');
    expect(s.apply({ type: 'session_created', session: { session_id: 'new', state: 'working' } })).toBe('changed');
    expect(s.all()).toHaveLength(7);
    expect(s.apply({ type: 'session_deleted', session: { session_id: 'new', state: 'working' } })).toBe('changed');
    expect(s.all()).toHaveLength(6);
  });

  it('returns unchanged for other types and malformed messages', () => {
    const s = hydrated();
    expect(s.apply({ type: 'history_snapshot' })).toBe('unchanged');
    expect(s.apply({ type: 'session_updated' })).toBe('unchanged');
    expect(s.apply({ type: 'session_deleted', session: { session_id: 'nope', state: 'ready' } })).toBe('unchanged');
    expect(s.all()).toHaveLength(6);
  });

  it('fires listeners on change and not after dispose', () => {
    const s = hydrated();
    const l = vi.fn();
    const d = s.onDidChange(l);
    s.apply({ type: 'session_updated', session: { session_id: 'sess-b', state: 'error' } });
    s.clear();
    expect(l).toHaveBeenCalledTimes(2);
    d.dispose();
    s.hydrate(fixture);
    expect(l).toHaveBeenCalledTimes(2);
  });

  it('detects a seq gap after applying the message', () => {
    const s = hydrated();
    expect(s.apply({ type: 'session_updated', seq: 10, session: { session_id: 'sess-b', state: 'ready' } })).toBe('changed');
    expect(s.apply({ type: 'session_updated', seq: 11, session: { session_id: 'sess-b', state: 'working' } })).toBe('changed');
    expect(s.apply({ type: 'session_updated', seq: 13, session: { session_id: 'sess-b', state: 'error' } })).toBe('gap');
    expect(s.all().find((x) => x.session_id === 'sess-b')?.state).toBe('error');
    expect(s.apply({ type: 'session_updated', seq: 14, session: { session_id: 'sess-b', state: 'ready' } })).toBe('changed');
  });

  it('ignores absent/zero seq for gap detection and hydrate resets the cursor', () => {
    const s = hydrated();
    s.apply({ type: 'session_updated', seq: 5, session: { session_id: 'sess-b', state: 'ready' } });
    expect(s.apply({ type: 'session_updated', seq: 0, session: { session_id: 'sess-b', state: 'working' } })).toBe('changed');
    expect(s.apply({ type: 'session_updated', session: { session_id: 'sess-b', state: 'ready' } })).toBe('changed');
    s.hydrate(fixture);
    expect(s.apply({ type: 'session_updated', seq: 500, session: { session_id: 'sess-b', state: 'ready' } })).toBe('changed');
  });

  it('clear empties the store', () => {
    const s = hydrated();
    s.clear();
    expect(s.all()).toEqual([]);
    expect(s.counts()).toEqual({ working: 0, waiting: 0, ready: 0, error: 0 });
  });
});

describe('SessionStore.apply newerOnly (replay over a snapshot)', () => {
  const snapshot = { groups: [{ name: 'g', agents: [{ session_id: 'x', state: 'working', updated_at: 100 }] }] };
  it('a replayed push with the SAME updated_at second as the snapshot still applies', () => {
    // updated_at has one-second granularity, so a transition in the same second
    // the snapshot was read ties with it; the push is the later observation.
    const s = new SessionStore();
    s.hydrate(snapshot);
    s.apply({ type: 'session_updated', session: { session_id: 'x', state: 'waiting', updated_at: 100 } }, true);
    expect(s.all().find((r) => r.session_id === 'x')?.state).toBe('waiting');
  });
  it('a replayed push strictly older than the snapshot is dropped', () => {
    const s = new SessionStore();
    s.hydrate(snapshot);
    s.apply({ type: 'session_updated', session: { session_id: 'x', state: 'ready', updated_at: 99 } }, true);
    expect(s.all().find((r) => r.session_id === 'x')?.state).toBe('working');
  });
});
