import { describe, expect, test } from 'vitest';
import {
  DEFAULT_PORT, addrFilePath, addrFromFileText, cleanPath, fixedPortOf,
  resolveDaemonAddress, sessionsURL,
} from '../lib/address.js';

// LOCKS: these pin the port of daemonaddr.ClientURL's ladder
// (core/pkg/daemonaddr/daemonaddr.go). They pass by construction against the
// port as written; their job is to fail when it drifts from Go.
//
// The expected values are Go's own answers. The fixedPortOf, addr-file and
// path-join tables are the ones the Claude Code panel (#2139) produced by
// calling net.SplitHostPort + strconv.Atoi, LimitReader(64) +
// strings.TrimSpace, and filepath.Join in a scratch Go program. The multibyte
// cases below were produced the same way for this mod (go1.26, a scratch
// module run with `GOWORK=off go run .`, not committed), because this port
// counts UTF-8 bytes by hand: Herdr's runtime has no TextEncoder.

describe('fixedPortOf matches daemonaddr.fixedPortOf', () => {
  const cases = [
    ['127.0.0.1:7838', 7838],
    [':7839', 7839],
    ['0.0.0.0:7837', 7837],
    ['127.0.0.1:0', null],
    ['localhost', null],
    ['127.0.0.1:65536', null],
    ['127.0.0.1:+7840', 7840],
    ['127.0.0.1:-1', null],
    ['[::1]:7841', 7841],
    ['::1:7841', null],
    ['[::1]7841', null],
    ['host:00007842', 7842],
    [' 127.0.0.1:7843', 7843],
    ['127.0.0.1:7843 ', null],
    ['', null],
    ['a]b:7844', null],
    ['[a]:7845', 7845],
    ['[a]:b:7846', null],
    ['127.0.0.1:', null],
    ['127.0.0.1:+', null],
  ];
  for (const [input, want] of cases) {
    test(JSON.stringify(input) + ' → ' + want, () => {
      expect(fixedPortOf(input)).toBe(want);
    });
  }
});

describe('the addr file is read as publishedAddr reads it', () => {
  const cases = [
    ['127.0.0.1:7850\n', 7850],
    ['127.0.0.1:7851' + ' '.repeat(100), 7851],
    ['127.0.0.1:7852' + 'x'.repeat(100), null],
    ['\u0085127.0.0.1:7854\u0085', 7854],
    ['127.0.0.1:7855﻿', null],
    ['hello', null],
    [' 127.0.0.1:7853 ', 7853],
    // The 64-byte cut lands inside a two-byte character: Go keeps the stray
    // byte, so the port does not parse.
    [' '.repeat(49) + '127.0.0.1:7850' + 'é', null],
    // The cut lands after a whole two-byte character, which is no space.
    [' '.repeat(48) + '127.0.0.1:7851' + 'é', null],
    // A newline, then multibyte characters the cut splits.
    ['127.0.0.1:7852\n' + 'é'.repeat(40), null],
    // A non-space prefix stays in the host, which is ignored.
    ['€127.0.0.1:7853', 7853],
    // The cut lands exactly before a three-byte space.
    [' '.repeat(50) + '127.0.0.1:7854' + ' ', 7854],
  ];
  for (const [content, want] of cases) {
    test(JSON.stringify(content.slice(0, 24)) + '… (' + content.length + ' chars) → ' + want, () => {
      expect(fixedPortOf(addrFromFileText(content))).toBe(want);
    });
  }

  test('anything but text reads as nothing', () => {
    expect(addrFromFileText(null)).toBe('');
    expect(addrFromFileText(undefined)).toBe('');
  });
});

describe('addrFilePath matches daemonaddr.AddrFilePath', () => {
  test('IRRLICHT_HOME wins, joined and cleaned as filepath.Join does', () => {
    expect(addrFilePath({ irrlichtHome: '/x/y/', home: '/home/dev' })).toBe('/x/y/irrlichd.addr');
    expect(addrFilePath({ irrlichtHome: '/x/../y' })).toBe('/y/irrlichd.addr');
    expect(addrFilePath({ irrlichtHome: 'rel/./dir' })).toBe('rel/dir/irrlichd.addr');
    expect(addrFilePath({ irrlichtHome: '/' })).toBe('/irrlichd.addr');
    expect(addrFilePath({ irrlichtHome: 'a/../../b' })).toBe('../b/irrlichd.addr');
    expect(addrFilePath({ irrlichtHome: '//x//y' })).toBe('/x/y/irrlichd.addr');
  });

  test('else the default state tree under HOME', () => {
    expect(addrFilePath({ irrlichtHome: '', home: '/home/dev' }))
      .toBe('/home/dev/.local/share/irrlicht/irrlichd.addr');
    expect(addrFilePath({ irrlichtHome: undefined, home: '/home/dev' }))
      .toBe('/home/dev/.local/share/irrlicht/irrlichd.addr');
  });

  test('and /tmp/irrlicht when HOME is empty', () => {
    expect(addrFilePath({})).toBe('/tmp/irrlicht/irrlichd.addr');
  });

  test('cleanPath folds what filepath.Clean folds', () => {
    expect(cleanPath('')).toBe('.');
    expect(cleanPath('a/..')).toBe('.');
    expect(cleanPath('/..')).toBe('/');
  });
});

// The ladder end to end, through the same injected reads main.js performs.
// Each fake records what was asked, so "the file was never read" is an
// observation rather than an absence.
function world({ env = {}, files = {} } = {}) {
  const asked = [];
  return {
    asked,
    io: {
      bindAddr: async () => env.IRRLICHT_BIND_ADDR,
      irrlichtHome: async () => env.IRRLICHT_HOME,
      home: async () => env.HOME,
      isRegularFile: async (path) => {
        asked.push(['isRegularFile', path]);
        const f = files[path];
        return !!f && !f.isLink && (f.kind || 'file') === 'file';
      },
      read: async (path) => {
        asked.push(['read', path]);
        const f = files[path];
        return f ? f.text : null;
      },
    },
  };
}

const HOME_ADDR = '/home/dev/.local/share/irrlicht/irrlichd.addr';

describe('resolveDaemonAddress walks the client ladder', () => {
  test('rung 1: a valid IRRLICHT_BIND_ADDR wins and no file is consulted', async () => {
    const w = world({
      env: { IRRLICHT_BIND_ADDR: '127.0.0.1:7838', HOME: '/home/dev' },
      files: { [HOME_ADDR]: { text: '127.0.0.1:7900\n' } },
    });
    const got = await resolveDaemonAddress(w.io);
    expect(got).toEqual({ port: 7838, source: 'env', addrFile: null });
    expect(w.asked).toEqual([]);
    expect(sessionsURL(got)).toBe('http://localhost:7838/api/v1/sessions');
  });

  test('IRRLICHT_BIND_ADDR on port 0 falls through to the addr file', async () => {
    const w = world({
      env: { IRRLICHT_BIND_ADDR: '127.0.0.1:0', HOME: '/home/dev' },
      files: { [HOME_ADDR]: { text: '127.0.0.1:7900\n' } },
    });
    expect(await resolveDaemonAddress(w.io)).toEqual({ port: 7900, source: 'file', addrFile: HOME_ADDR });
  });

  test('a malformed IRRLICHT_BIND_ADDR falls through to the addr file', async () => {
    const w = world({
      env: { IRRLICHT_BIND_ADDR: 'not-an-address', HOME: '/home/dev' },
      files: { [HOME_ADDR]: { text: '127.0.0.1:7900\n' } },
    });
    expect((await resolveDaemonAddress(w.io)).port).toBe(7900);
  });

  test('rung 2: the addr file under HOME', async () => {
    const w = world({ env: { HOME: '/home/dev' }, files: { [HOME_ADDR]: { text: '127.0.0.1:7900\n' } } });
    expect(await resolveDaemonAddress(w.io)).toEqual({ port: 7900, source: 'file', addrFile: HOME_ADDR });
    expect(w.asked).toEqual([['isRegularFile', HOME_ADDR], ['read', HOME_ADDR]]);
  });

  test('rung 2: the addr file under IRRLICHT_HOME when set', async () => {
    const w = world({
      env: { IRRLICHT_HOME: '/srv/irrlicht-dev', HOME: '/home/dev' },
      files: {
        '/srv/irrlicht-dev/irrlichd.addr': { text: '127.0.0.1:7901' },
        [HOME_ADDR]: { text: '127.0.0.1:7900\n' },
      },
    });
    expect(await resolveDaemonAddress(w.io))
      .toEqual({ port: 7901, source: 'file', addrFile: '/srv/irrlicht-dev/irrlichd.addr' });
  });

  test('an oversized addr file is read by its first 64 bytes, not refused', async () => {
    const w = world({ env: { HOME: '/home/dev' }, files: { [HOME_ADDR]: { text: '127.0.0.1:7902' + ' '.repeat(500) } } });
    expect((await resolveDaemonAddress(w.io)).port).toBe(7902);
  });

  test('a garbage addr file falls through to the default port', async () => {
    const w = world({ env: { HOME: '/home/dev' }, files: { [HOME_ADDR]: { text: 'garbage\n' } } });
    expect(await resolveDaemonAddress(w.io)).toEqual({ port: DEFAULT_PORT, source: 'default', addrFile: HOME_ADDR });
  });

  test('a symlinked addr file is never read', async () => {
    const w = world({ env: { HOME: '/home/dev' }, files: { [HOME_ADDR]: { text: '127.0.0.1:7903', isLink: true } } });
    expect((await resolveDaemonAddress(w.io)).port).toBe(DEFAULT_PORT);
    expect(w.asked).toEqual([['isRegularFile', HOME_ADDR]]);
  });

  test('a directory or device at the addr path is never read', async () => {
    const w = world({ env: { HOME: '/home/dev' }, files: { [HOME_ADDR]: { kind: 'other', text: '127.0.0.1:7904' } } });
    expect((await resolveDaemonAddress(w.io)).port).toBe(DEFAULT_PORT);
    expect(w.asked).toEqual([['isRegularFile', HOME_ADDR]]);
  });

  test('rung 3: no env, no file, the default port', async () => {
    const w = world({ env: { HOME: '/home/dev' } });
    expect(await resolveDaemonAddress(w.io)).toEqual({ port: DEFAULT_PORT, source: 'default', addrFile: HOME_ADDR });
    expect(sessionsURL({ port: DEFAULT_PORT })).toBe('http://localhost:7837/api/v1/sessions');
  });

  test('a read that rejects counts as "not there", never as a throw', async () => {
    const io = {
      bindAddr: async () => { throw new Error('refused'); },
      irrlichtHome: async () => { throw new Error('refused'); },
      home: async () => { throw new Error('refused'); },
      isRegularFile: async () => { throw new Error('refused'); },
      read: async () => { throw new Error('refused'); },
    };
    expect(await resolveDaemonAddress(io))
      .toEqual({ port: DEFAULT_PORT, source: 'default', addrFile: '/tmp/irrlicht/irrlichd.addr' });
  });
});
