import { describe, expect, it } from 'vitest';
import { DEFAULT_DAEMON_URL, resolveDaemonUrl } from '../../src/config';

type Resolver = typeof resolveDaemonUrl;

const REJECTED: unknown[] = [
  42, null, undefined, '', 'garbage', 'ftp://127.0.0.1', 'ws://127.0.0.1:7837',
  'http://192.168.1.5:7837', 'http://example.com', 'http://127.0.0.1.evil.com',
  'http://evil.com/127.0.0.1', 'http://localhost.evil.com',
];

// The reject-table guard, factored so it can run against the real resolver
// (must pass) and the committed mutant below (must throw).
function checkRejects(resolve: Resolver, raw: unknown): void {
  const r = resolve(raw);
  expect(r.origin).toBe('http://127.0.0.1:7837');
  expect(r.url).toBe(DEFAULT_DAEMON_URL);
  expect(r.wsUrl).toBe('ws://127.0.0.1:7837/api/v1/sessions/stream');
  expect(r.warning).toBeTruthy();
  expect(r.warning).not.toContain('\n');
  if (typeof raw === 'string' && raw !== '') expect(r.warning).toContain(raw);
}

describe('resolveDaemonUrl', () => {
  it('accepts the default', () => {
    const r = resolveDaemonUrl(DEFAULT_DAEMON_URL);
    expect(r.origin).toBe('http://127.0.0.1:7837');
    expect(r.wsUrl).toBe('ws://127.0.0.1:7837/api/v1/sessions/stream');
    expect(r.warning).toBeUndefined();
  });

  it.each([
    ['http://127.0.0.1:9000', 'http://127.0.0.1:9000', 'ws://127.0.0.1:9000/api/v1/sessions/stream'],
    ['http://127.5.6.7:80', 'http://127.5.6.7', 'ws://127.5.6.7/api/v1/sessions/stream'],
    ['http://localhost:7838', 'http://localhost:7838', 'ws://localhost:7838/api/v1/sessions/stream'],
    ['https://localhost:7838', 'https://localhost:7838', 'wss://localhost:7838/api/v1/sessions/stream'],
    ['http://[::1]:7837', 'http://[::1]:7837', 'ws://[::1]:7837/api/v1/sessions/stream'],
  ])('accepts loopback %s', (raw, origin, wsUrl) => {
    const r = resolveDaemonUrl(raw);
    expect(r.origin).toBe(origin);
    expect(r.wsUrl).toBe(wsUrl);
    expect(r.warning).toBeUndefined();
  });

  it('ignores path and query (origin-based)', () => {
    const r = resolveDaemonUrl('http://127.0.0.1:7838/some/path?x=1');
    expect(r.origin).toBe('http://127.0.0.1:7838');
    expect(r.wsUrl).toBe('ws://127.0.0.1:7838/api/v1/sessions/stream');
  });

  it.each(REJECTED)('rejects %j with a warning naming it and falls back to default', (raw) => {
    checkRejects(resolveDaemonUrl, raw);
  });

  // Committed in-language mutant: the loopback-host condition is dropped, so
  // any http(s) URL is accepted. Every non-loopback row must now fail.
  it('mutant: dropping the loopback-host condition is caught by the reject table', () => {
    const noLoopbackCheck: Resolver = (raw) => {
      const r = resolveDaemonUrl(raw);
      if (r.warning === undefined || typeof raw !== 'string') return r;
      try {
        const u = new URL(raw.trim());
        if (u.protocol === 'http:' || u.protocol === 'https:') {
          return { url: u.origin, origin: u.origin, wsUrl: u.origin.replace(/^http/, 'ws') + '/api/v1/sessions/stream' };
        }
      } catch { /* unparseable stays rejected */ }
      return r;
    };
    expect(noLoopbackCheck('http://example.com').origin).toBe('http://example.com');
    const nonLoopback = ['http://192.168.1.5:7837', 'http://example.com', 'http://127.0.0.1.evil.com', 'http://evil.com/127.0.0.1', 'http://localhost.evil.com'];
    for (const raw of nonLoopback) expect(() => checkRejects(noLoopbackCheck, raw)).toThrow();
  });
});
