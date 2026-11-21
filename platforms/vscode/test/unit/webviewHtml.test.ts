import { describe, expect, it } from 'vitest';
import { buildCsp, buildWrapperHtml } from '../../src/webviewHtml';

const base = { daemonOrigin: 'http://127.0.0.1:7837', cspSource: 'vscode-webview://abc', nonce: 'AbC123xyz' };
const wrap = base;

type CspBuilder = typeof buildCsp;

// The frame-src strictness guard, factored so it can run against the real
// builder (must pass) and against the committed mutant below (must throw).
function checkFrameSrcStrict(build: CspBuilder): void {
  const csp = build(base);
  const frame = csp.split('; ').find((d) => d.startsWith('frame-src '))!;
  expect(frame.split(' ').slice(1)).toEqual(['http://127.0.0.1:7837']);
  expect(csp).not.toContain('unsafe-inline');
  expect(csp).not.toContain('unsafe-eval');
  expect(csp).not.toContain('*');
  expect(csp).not.toContain('connect-src');
}

describe('buildCsp', () => {
  it('matches the exact contract string', () => {
    expect(buildCsp(base)).toBe(
      "default-src 'none'; frame-src http://127.0.0.1:7837; script-src 'nonce-AbC123xyz'; style-src vscode-webview://abc 'nonce-AbC123xyz'; img-src vscode-webview://abc data:",
    );
  });
  it('frame-src holds exactly one origin and nothing permissive', () => {
    checkFrameSrcStrict(buildCsp);
  });
  // Committed in-language mutant: frame-src gains a wildcard source.
  it('mutant: frame-src widened with a wildcard is caught by the strictness check', () => {
    const wildcardFrameSrc: CspBuilder = (o) =>
      buildCsp(o).replace(`frame-src ${o.daemonOrigin}`, `frame-src ${o.daemonOrigin} *`);
    expect(wildcardFrameSrc(base)).toContain('frame-src http://127.0.0.1:7837 *;');
    expect(() => checkFrameSrcStrict(wildcardFrameSrc)).toThrow();
  });
  it.each([
    'http://127.0.0.1:7837/path',
    "http://127.0.0.1:7837'",
    'http://127.0.0.1:7837;',
    'http://127.0.0.1:7837 *',
    'ftp://127.0.0.1',
    'javascript:alert(1)',
    '',
  ])('rejects bad origin %j', (daemonOrigin) => {
    expect(() => buildCsp({ ...base, daemonOrigin })).toThrow();
    expect(() => buildWrapperHtml({ ...wrap, daemonOrigin })).toThrow();
  });
  it.each(["a'b", 'a b', 'a;b', '', 'a<b'])('rejects bad nonce %j', (nonce) => {
    expect(() => buildCsp({ ...base, nonce })).toThrow();
    expect(() => buildWrapperHtml({ ...wrap, nonce })).toThrow();
  });
  it('accepts https and ipv6 origins', () => {
    expect(buildCsp({ ...base, daemonOrigin: 'https://localhost:8443' })).toContain('frame-src https://localhost:8443;');
    expect(buildCsp({ ...base, daemonOrigin: 'http://[::1]:7837' })).toContain('frame-src http://[::1]:7837;');
  });
});

describe('real VS Code cspSource', () => {
  const real = "'self' https://*.vscode-cdn.net";
  it('is accepted by buildCsp and buildWrapperHtml', () => {
    const csp = buildCsp({ ...base, cspSource: real });
    expect(csp).toContain(`style-src ${real} 'nonce-AbC123xyz'`);
    expect(csp).toContain(`img-src ${real} data:`);
    expect(csp).toContain('frame-src http://127.0.0.1:7837;');
    expect(() => buildWrapperHtml({ ...wrap, cspSource: real })).not.toThrow();
  });
  it.each(['a; script-src *', 'a"b', 'a<b', 'a>b', 'a&b', 'a,b', 'a\nb', 'a\rb'])('still rejects injecting cspSource %j', (cspSource) => {
    expect(() => buildCsp({ ...base, cspSource })).toThrow();
  });
});

describe('buildWrapperHtml', () => {
  const html = buildWrapperHtml(wrap);
  it('has the CSP meta and a src-less titled iframe', () => {
    expect(html).toContain(`<meta http-equiv="Content-Security-Policy" content="${buildCsp(base)}">`);
    const iframe = html.match(/<iframe[^>]*>/)![0];
    expect(iframe).not.toMatch(/\ssrc=/);
    expect(iframe).toContain('title="Irrlicht dashboard"');
  });
  it('nonces the script and style, with no inline handlers', () => {
    expect(html).toContain(`<script nonce="${base.nonce}">`);
    expect(html).toContain(`<style nonce="${base.nonce}">`);
    expect(html).not.toMatch(/\son[a-z]+=/);
    expect(html).toContain('acquireVsCodeApi()');
    expect(html).toContain("type: 'ready'");
  });
  it('derives the iframe URL from the daemon origin', () => {
    expect(html).toContain('data-url="http://127.0.0.1:7837/?embed=vscode"');
    expect(buildWrapperHtml({ ...wrap, daemonOrigin: 'http://[::1]:9000' })).toContain('data-url="http://[::1]:9000/?embed=vscode"');
  });
});
