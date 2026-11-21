import { describe, expect, test } from 'vitest';
import {
  DEFAULT_PORT, addrFilePath, addrFromFileText, cleanPath, fixedPortOf,
  isReadableAddrFile, resolveDaemonAddress, sessionsURL,
} from './address.js';

// LOCKS: these pin the port of daemonaddr.ClientURL's ladder
// (core/pkg/daemonaddr/daemonaddr.go). They pass by construction against the
// port as written; their job is to fail when it drifts from Go.
//
// The expected values for fixedPortOf, the addr-file read and the path join
// are Go's own answers, not hand-derived: they were produced by calling
// net.SplitHostPort + strconv.Atoi (daemonaddr.fixedPortOf's body), the
// LimitReader(64)+strings.TrimSpace read (publishedAddr's), and
// filepath.Join on exactly these inputs in a scratch Go program (go1.26,
// `GOWORK=off go run .`, not committed: it would be a third Go module).

describe('fixedPortOf matches daemonaddr.fixedPortOf', () => {
  const cases = [
    ['127.0.0.1:7838', 7838],
    [':7839', 7839],
    ['0.0.0.0:7837', 7837],
    ['127.0.0.1:0', null], // asks the OS for a port: names none
    ['localhost', null], // missing port
    ['127.0.0.1:65536', null],
    ['127.0.0.1:+7840', 7840], // strconv.Atoi takes a sign
    ['127.0.0.1:-1', null],
    ['[::1]:7841', 7841],
    ['::1:7841', null], // too many colons
    ['[::1]7841', null],
    ['host:00007842', 7842],
    [' 127.0.0.1:7843', 7843], // the host is ignored, spaces and all
    ['127.0.0.1:7843 ', null], // ...the port is not
    ['', null],
    ['a]b:7844', null], // unexpected ']'
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
    // Longer than 64 bytes, but the first 64 are an address plus spaces:
    // Go's LimitReader cuts, it does not refuse.
    ['127.0.0.1:7851' + ' '.repeat(100), 7851],
    // Longer than 64 bytes with garbage glued to the port: the cut keeps the
    // garbage, so the port does not parse.
    ['127.0.0.1:7852' + 'x'.repeat(100), null],
    // U+0085 is space to Go's TrimSpace and not to String.prototype.trim.
    ['\u0085127.0.0.1:7854\u0085', 7854],
    // U+FEFF is space to String.prototype.trim and not to Go's TrimSpace.
    ['127.0.0.1:7855﻿', null],
    ['hello', null],
    [' 127.0.0.1:7853 ', 7853],
  ];
  for (const [content, want] of cases) {
    test(JSON.stringify(content.slice(0, 24)) + ' → ' + want, () => {
      expect(fixedPortOf(addrFromFileText(content))).toBe(want);
    });
  }
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

  test('and /tmp/irrlicht when HOME is empty (os.UserHomeDir fails)', () => {
    expect(addrFilePath({})).toBe('/tmp/irrlicht/irrlichd.addr');
  });

  test('cleanPath folds what filepath.Clean folds', () => {
    expect(cleanPath('')).toBe('.');
    expect(cleanPath('a/..')).toBe('.');
    expect(cleanPath('/..')).toBe('/');
  });
});

describe('only a regular file that is not a symlink is read (os.Lstat + IsRegular)', () => {
  test('regular file', () => {
    expect(isReadableAddrFile({ kind: 'file', isLink: false, size: 15, mtimeMs: 0 })).toBe(true);
  });
  test('a symlink, even to a regular file', () => {
    expect(isReadableAddrFile({ kind: 'file', isLink: true, size: 15, mtimeMs: 0 })).toBe(false);
  });
  test('a device or a directory', () => {
    expect(isReadableAddrFile({ kind: 'other', isLink: false, size: 0, mtimeMs: 0 })).toBe(false);
    expect(isReadableAddrFile({ kind: 'dir', isLink: false, size: 0, mtimeMs: 0 })).toBe(false);
  });
  test('nothing there', () => {
    expect(isReadableAddrFile(undefined)).toBe(false);
  });
});

// The ladder end to end, through the same injected reads the hooks module
// performs on `$`. Each fake records what was asked, so "the file was never
// read" is an observation rather than an absence.
function world({ env = {}, files = {} } = {}) {
  const asked = [];
  return {
    asked,
    io: {
      bindAddr: async () => env.IRRLICHT_BIND_ADDR,
      irrlichtHome: async () => env.IRRLICHT_HOME,
      home: async () => env.HOME,
      stat: async path => {
        asked.push(['stat', path]);
        const f = files[path];
        if (!f) throw new Error('ENOENT');
        return { kind: f.kind || 'file', isLink: !!f.isLink, size: (f.text || '').length, mtimeMs: 0 };
      },
      read: async path => {
        asked.push(['read', path]);
        const f = files[path];
        if (!f) throw new Error('ENOENT');
        return f.text;
      },
    },
  };
}

const HOME_ADDR = '/home/dev/.local/share/irrlicht/irrlichd.addr';

describe('resolveDaemonAddress walks the client ladder', () => {
  test('a valid IRRLICHT_BIND_ADDR wins and no file is consulted', async () => {
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

  test('the addr file is looked for under IRRLICHT_HOME when set', async () => {
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
    expect(w.asked).toEqual([['stat', HOME_ADDR], ['read', HOME_ADDR]]);
  });

  test('a symlinked addr file is never read', async () => {
    const w = world({ env: { HOME: '/home/dev' }, files: { [HOME_ADDR]: { text: '127.0.0.1:7903', isLink: true } } });
    expect((await resolveDaemonAddress(w.io)).port).toBe(DEFAULT_PORT);
    expect(w.asked).toEqual([['stat', HOME_ADDR]]);
  });

  test('no env, no file: the default port', async () => {
    const w = world({ env: { HOME: '/home/dev' } });
    expect(await resolveDaemonAddress(w.io)).toEqual({ port: DEFAULT_PORT, source: 'default', addrFile: HOME_ADDR });
    expect(sessionsURL({ port: DEFAULT_PORT })).toBe('http://localhost:7837/api/v1/sessions');
  });

  test('a read that rejects counts as "not there", never as a throw', async () => {
    const io = {
      bindAddr: async () => { throw new Error('refused'); },
      irrlichtHome: async () => { throw new Error('refused'); },
      home: async () => { throw new Error('refused'); },
      stat: async () => { throw new Error('refused'); },
      read: async () => { throw new Error('refused'); },
    };
    expect(await resolveDaemonAddress(io))
      .toEqual({ port: DEFAULT_PORT, source: 'default', addrFile: '/tmp/irrlicht/irrlichd.addr' });
  });
});
