// Loading a committed mutant of one lib/ module (AGENTS.md: commit the
// mutation rather than describing it). The *.mutation.test.js files each
// break one rule this way, and the contract that rule exists for must then
// fail; a mutant that still passes means the contract no longer reaches the
// rule it claims to protect.
//
// The anchor must occur exactly once in the module: one that stopped matching
// fails as stale instead of silently testing the unmutated file.

import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { afterAll, expect } from 'vitest';

const LIB = new URL('../lib/', import.meta.url);
const source = (name) => readFileSync(new URL(name, LIB), 'utf8');

// mutantLoader returns loadMutant(name, file, anchor, replacement), which
// writes every lib/ module into a fresh folder, `file` with `anchor`
// replaced, and imports the mutated `file` from there. The folders go when
// the test file ends.
export function mutantLoader(prefix) {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  afterAll(() => rmSync(dir, { recursive: true, force: true }));
  return async function loadMutant(name, file, anchor, replacement) {
    const original = source(file);
    expect(original.split(anchor).length - 1, `anchor for ${name} must occur exactly once in lib/${file}`).toBe(1);
    const mutant = original.replace(anchor, () => replacement);
    expect(mutant).not.toBe(original);
    const sub = join(dir, name);
    mkdirSync(sub, { recursive: true });
    for (const f of readdirSync(LIB).filter((n) => n.endsWith('.js'))) writeFileSync(join(sub, f), source(f));
    writeFileSync(join(sub, file), mutant);
    return import(pathToFileURL(join(sub, file)).href);
  };
}
