import { describe, expect, it } from 'vitest';
import { STATES, type StateName } from '../../src/states';
import { formatStatus } from '../../src/statusText';

const URL_ = 'http://127.0.0.1:7837';
function counts(o: Partial<Record<StateName, number>>): Record<StateName, number> {
  const c = {} as Record<StateName, number>;
  for (const s of STATES) c[s] = o[s] ?? 0;
  return c;
}

describe('formatStatus', () => {
  it('online: per-state counts, no error segment at zero', () => {
    const r = formatStatus(counts({ working: 2, waiting: 1, ready: 3, error: 0 }), 'online', URL_);
    expect(r.text).toBe('$(sync~spin) 2  $(bell) 1  $(check) 3');
    expect(r.text).not.toContain('error');
  });

  it('online: error segment appears when > 0', () => {
    const r = formatStatus(counts({ working: 1, error: 2 }), 'online', URL_);
    expect(r.text).toBe('$(sync~spin) 1  $(bell) 0  $(check) 0  $(error) 2');
  });

  it('online with no sessions still renders zero counts', () => {
    expect(formatStatus(counts({}), 'online', URL_).text).toBe('$(sync~spin) 0  $(bell) 0  $(check) 0');
  });

  it('online tooltip has one line per state in words plus the daemon url', () => {
    const lines = formatStatus(counts({ working: 2, waiting: 1, ready: 3, error: 4 }), 'online', URL_).tooltip.split('\n');
    expect(lines).toHaveLength(STATES.length + 1);
    STATES.forEach((s, i) => expect(lines[i]).toContain(s));
    expect(lines[0]).toContain('2');
    expect(lines[STATES.length]).toBe('Daemon: ' + URL_);
  });

  it('offline', () => {
    const r = formatStatus(counts({ working: 9 }), 'offline', URL_);
    expect(r.text).toBe('$(debug-disconnect) Irrlicht: offline');
    expect(r.tooltip).toContain(URL_);
    expect(r.tooltip).toContain('not reachable');
    expect(r.tooltip).toContain('retr');
  });

  it('connecting', () => {
    const r = formatStatus(counts({}), 'connecting', URL_);
    expect(r.text).toBe('$(sync~spin) Irrlicht');
    expect(r.tooltip).toContain(URL_);
  });
});
