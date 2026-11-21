// One table of Go's answers, run against both ports of daemonaddr.ClientURL:
// this mod's (lib/address.js) and the Claude Code panel's
// (platforms/claude-code/lib/address.js). A Herdr mod folder is installed on
// its own, so the two cannot share code at run time; this file keeps them
// from drifting apart. It runs only in this repository, where the other port
// exists, so tools/preflight.sh's Herdr gate also triggers on that file.
//
// The expected values are Go's own answers. The fixedPortOf, addr-file and
// path-join rows are the ones the Claude Code panel (#2139) produced by
// calling net.SplitHostPort + strconv.Atoi, LimitReader(64) +
// strings.TrimSpace, and filepath.Join in a scratch Go program. The multibyte
// rows were produced the same way for this mod (go1.26, a scratch module run
// with `GOWORK=off go run .`, not committed), because this port counts UTF-8
// bytes by hand: Herdr's runtime has no TextEncoder.

import { describe, expect, test } from 'vitest';
import * as claudeCode from '../../../../claude-code/lib/address.js';
import { formatTokens as claudeCodeTokens } from '../../../../claude-code/lib/format.js';
import * as herdr from '../lib/address.js';
import { formatTokens as herdrTokens } from '../lib/gauge.js';

describe.each([['herdr mod', herdr], ['Claude Code panel', claudeCode]])('the %s port matches daemonaddr', (_name, port) => {
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
        expect(port.fixedPortOf(input)).toBe(want);
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
      [' '.repeat(50) + '127.0.0.1:7854' + '\u2028', 7854],
    ];
    for (const [content, want] of cases) {
      test(JSON.stringify(content.slice(0, 24)) + '… (' + content.length + ' chars) → ' + want, () => {
        expect(port.fixedPortOf(port.addrFromFileText(content))).toBe(want);
      });
    }
  });

  describe('addrFilePath matches daemonaddr.AddrFilePath', () => {
    test('IRRLICHT_HOME wins, joined and cleaned as filepath.Join does', () => {
      expect(port.addrFilePath({ irrlichtHome: '/x/y/', home: '/home/dev' })).toBe('/x/y/irrlichd.addr');
      expect(port.addrFilePath({ irrlichtHome: '/x/../y' })).toBe('/y/irrlichd.addr');
      expect(port.addrFilePath({ irrlichtHome: 'rel/./dir' })).toBe('rel/dir/irrlichd.addr');
      expect(port.addrFilePath({ irrlichtHome: '/' })).toBe('/irrlichd.addr');
      expect(port.addrFilePath({ irrlichtHome: 'a/../../b' })).toBe('../b/irrlichd.addr');
      expect(port.addrFilePath({ irrlichtHome: '//x//y' })).toBe('/x/y/irrlichd.addr');
    });

    test('else the default state tree under HOME', () => {
      expect(port.addrFilePath({ irrlichtHome: '', home: '/home/dev' }))
        .toBe('/home/dev/.local/share/irrlicht/irrlichd.addr');
      expect(port.addrFilePath({ irrlichtHome: undefined, home: '/home/dev' }))
        .toBe('/home/dev/.local/share/irrlicht/irrlichd.addr');
    });

    test('and /tmp/irrlicht when HOME is empty', () => {
      expect(port.addrFilePath({})).toBe('/tmp/irrlicht/irrlichd.addr');
    });

    test('cleanPath folds what filepath.Clean folds', () => {
      expect(port.cleanPath('')).toBe('.');
      expect(port.cleanPath('a/..')).toBe('.');
      expect(port.cleanPath('/..')).toBe('/');
    });
  });
});

test('both panels print token counts alike', () => {
  for (const n of [0, NaN, -5, 950, 999, 1000, 4200, 9999, 177000, 999999, 1200000, 12000000]) {
    expect(herdrTokens(n)).toBe(claudeCodeTokens(n));
  }
});
