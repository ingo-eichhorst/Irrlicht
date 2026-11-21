// Loading a committed mutant of one of the mod's modules, lib/ or main.js
// (AGENTS.md: commit the mutation rather than describing it). The
// *.mutation.test.js files each break one rule this way, and the contract that
// rule exists for must then fail; a mutant that still passes means the
// contract no longer reaches the rule it claims to protect.
//
// The anchor must occur exactly once in the module: one that stopped matching
// fails as stale instead of silently testing the unmutated file.

import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { afterAll, expect } from 'vitest';

const MOD = new URL('../', import.meta.url);
const source = (path) => readFileSync(new URL(path, MOD), 'utf8');

// The mod as it loads: main.js and every lib/ module, by path from the mod's
// folder.
const MODULES = ['main.js', ...readdirSync(new URL('lib/', MOD)).filter((n) => n.endsWith('.js')).map((n) => 'lib/' + n)];

// mutantLoader returns loadMutant(name, file, anchor, replacement), which
// writes main.js and lib/ into a fresh folder, `file` (a path from the mod's
// folder, such as "lib/match.js" or "main.js") with `anchor` replaced, and
// imports the mutated `file` from there. main.js's Herdr imports resolve to
// test/stubs through vitest.config.js's aliases, as they do for the real one.
// The folders go when the test file ends.
export function mutantLoader(prefix) {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  afterAll(() => rmSync(dir, { recursive: true, force: true }));
  return async function loadMutant(name, file, anchor, replacement) {
    const original = source(file);
    expect(original.split(anchor).length - 1, `anchor for ${name} must occur exactly once in ${file}`).toBe(1);
    const mutant = original.replace(anchor, () => replacement);
    expect(mutant).not.toBe(original);
    const sub = join(dir, name);
    for (const path of MODULES) {
      mkdirSync(dirname(join(sub, path)), { recursive: true });
      writeFileSync(join(sub, path), path === file ? mutant : source(path));
    }
    return import(pathToFileURL(join(sub, file)).href);
  };
}
