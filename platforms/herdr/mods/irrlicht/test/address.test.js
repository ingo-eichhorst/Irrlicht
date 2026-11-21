import { describe, expect, test } from 'vitest';
import { DEFAULT_PORT, addrFromFileText, resolveDaemonAddress, sessionsURL } from '../lib/address.js';
import { HOME_ADDR, world } from './fixtures.js';

// LOCKS: the address ladder as this mod performs it, through main.js's io
// (an isRegularFile check, then a read). They pass by construction; their job
// is to fail when the ladder drifts from daemonaddr.ClientURL.

// The Go-answer tables for fixedPortOf, the addr-file read and AddrFilePath
// run in address.parity.test.js, against this port and the Claude Code
// panel's at once.

test('an addr file that is not text reads as nothing', () => {
  expect(addrFromFileText(null)).toBe('');
  expect(addrFromFileText(undefined)).toBe('');
});

// The ladder end to end, through the same injected reads main.js performs
// (world() in fixtures.js).
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
