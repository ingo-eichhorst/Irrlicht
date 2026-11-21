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

const PORTS = [['herdr mod', herdr], ['Claude Code panel', claudeCode]];

// fixedPortOf: [host:port, Go's port or null].
const FIXED_PORT_CASES = [
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

// The addr file's text as publishedAddr reads it: [text, Go's port or null].
// The multibyte rows are this mod's: they pin the 64-byte cut, which this
// port counts by hand.
const ADDR_FILE_CASES = [
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

// AddrFilePath: [{ irrlichtHome, home }, Go's path].
const ADDR_PATH_CASES = [
  [{ irrlichtHome: '/x/y/', home: '/home/dev' }, '/x/y/irrlichd.addr'],
  [{ irrlichtHome: '/x/../y' }, '/y/irrlichd.addr'],
  [{ irrlichtHome: 'rel/./dir' }, 'rel/dir/irrlichd.addr'],
  [{ irrlichtHome: '/' }, '/irrlichd.addr'],
  [{ irrlichtHome: 'a/../../b' }, '../b/irrlichd.addr'],
  [{ irrlichtHome: '//x//y' }, '/x/y/irrlichd.addr'],
  [{ irrlichtHome: '', home: '/home/dev' }, '/home/dev/.local/share/irrlicht/irrlichd.addr'],
  [{ irrlichtHome: undefined, home: '/home/dev' }, '/home/dev/.local/share/irrlicht/irrlichd.addr'],
  [{}, '/tmp/irrlicht/irrlichd.addr'], // HOME empty: os.UserHomeDir fails
];

// filepath.Clean: [path, Go's answer].
const CLEAN_CASES = [['', '.'], ['a/..', '.'], ['/..', '/']];

describe.each(PORTS)('the %s port', (_name, port) => {
  test.each(FIXED_PORT_CASES)('fixedPortOf(%j) is %s', (input, want) => {
    expect(port.fixedPortOf(input)).toBe(want);
  });

  test.each(ADDR_FILE_CASES)('an addr file holding %j names port %s', (content, want) => {
    expect(port.fixedPortOf(port.addrFromFileText(content))).toBe(want);
  });

  test.each(ADDR_PATH_CASES)('addrFilePath(%j) is %s', (env, want) => {
    expect(port.addrFilePath(env)).toBe(want);
  });

  test.each(CLEAN_CASES)('cleanPath(%j) is %j', (input, want) => {
    expect(port.cleanPath(input)).toBe(want);
  });
});

test('both panels print token counts alike', () => {
  for (const n of [0, NaN, -5, 950, 999, 1000, 4200, 9999, 177000, 999999, 1200000, 12000000]) {
    expect(herdrTokens(n)).toBe(claudeCodeTokens(n));
  }
});
