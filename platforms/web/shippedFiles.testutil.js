// shippedFiles.testutil.js — shared derivation of the web tree's REQUIRED
// shipped file set. Used by release-files.test.js (the tools/build-release.sh
// copy-list tripwire, docs/mobile-notifications-arc42.md §8.7) and
// sw-contract.test.js (the §5.2 additivity contract). Test-only: this file is
// not shipped and is imported by no dashboard module, so it never appears in
// the derived set itself.
//
// Fail-loud rules (house rule: a verifier that cannot run must never read as
// a pass): an unreadable file, an index.html that yields zero entries, and an
// import walk that parses zero imports each THROW here — the derivation
// refuses to produce a set it cannot vouch for, which fails every test built
// on it.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const WEB_DIR = dirname(fileURLToPath(import.meta.url));

// index.html references: <script src>, plus <link href> for the stylesheet
// and the PWA manifest (attribute order inside the tag is not assumed).
export function parseHtmlEntries(html) {
  const entries = [];
  for (const m of html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/g)) {
    entries.push(m[1]);
  }
  for (const tag of html.matchAll(/<link\b[^>]*>/g)) {
    const rel = /\brel="([^"]+)"/.exec(tag[0]);
    const href = /\bhref="([^"]+)"/.exec(tag[0]);
    if (rel && href && (rel[1] === 'stylesheet' || rel[1] === 'manifest')) {
      entries.push(href[1]);
    }
  }
  return entries;
}

// Static module-graph edges: `import … from './x.js'` and the re-export form
// `export … from './x.js'` — both are load-time fetches the serving directory
// must satisfy, so both count.
export function parseStaticImports(source) {
  const out = [];
  for (const m of source.matchAll(/^\s*(?:import|export)\b[^;]*?\bfrom\s+['"]\.\/([^'"]+)['"]/gm)) {
    out.push(m[1]);
  }
  return out;
}

export function walkImportGraph(entryModules) {
  const modules = new Set();
  let edges = 0;
  const queue = [...entryModules];
  while (queue.length > 0) {
    const file = queue.shift();
    if (modules.has(file)) continue;
    modules.add(file);
    // readFileSync throws on a missing/unreadable module — fail-loud, never
    // a silently smaller set.
    const source = readFileSync(join(WEB_DIR, file), 'utf8');
    for (const imported of parseStaticImports(source)) {
      edges += 1;
      queue.push(imported);
    }
  }
  return { modules, edges };
}

// The full set of files a served copy of platforms/web needs at runtime:
// index.html itself, its script/stylesheet/manifest entries, the transitive
// ES-module graph, the manifest's icon, and sw.js. sw.js is added by name
// because nothing references it statically — it is registered at runtime by
// the pairing flow — which is exactly why it needs this tripwire: a release
// that forgets it ships a silently push-less PWA (arc42 §8.7).
export function deriveShippedSet() {
  const html = readFileSync(join(WEB_DIR, 'index.html'), 'utf8');
  const entries = parseHtmlEntries(html);
  if (entries.length === 0) {
    throw new Error('fail-loud: parsed zero script/link entries from index.html — the entry parser cannot be trusted');
  }
  const jsEntries = entries.filter((f) => f.endsWith('.js'));
  if (jsEntries.length === 0) {
    throw new Error('fail-loud: found no <script src> module entry in index.html');
  }
  const { modules, edges } = walkImportGraph(jsEntries);
  if (edges === 0) {
    throw new Error('fail-loud: the import-graph walk parsed zero imports — the import parser cannot be trusted');
  }
  const files = new Set(['index.html', ...entries, ...modules, 'sw.js']);
  for (const entry of entries.filter((f) => f.endsWith('.webmanifest'))) {
    const manifest = JSON.parse(readFileSync(join(WEB_DIR, entry), 'utf8'));
    for (const icon of manifest.icons || []) files.add(icon.src);
  }
  return { files, modules, edges, entries };
}

// The single WEB_FILES=( … ) array in tools/build-release.sh — the one copy
// list all three copy sites (darwin tarball, linux tarballs, app bundle
// Resources) draw from. Returns null when the list is absent, so the test
// can fail with a message naming what is missing.
export function parseWebFilesList(scriptSource) {
  const m = /WEB_FILES=\(([^)]*)\)/.exec(scriptSource);
  if (!m) return null;
  return m[1].split(/\s+/).filter((token) => token.length > 0);
}
