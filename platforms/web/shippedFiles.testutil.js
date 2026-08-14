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

// <link rel> values that name a file the serving directory has to hold. A
// rel the browser would fetch from another origin (dns-prefetch, preconnect)
// is deliberately absent.
const REFERENCING_RELS = new Set(['stylesheet', 'manifest', 'icon', 'apple-touch-icon', 'preload']);

// One attribute out of one tag, in any of the three spellings HTML allows —
// double-quoted, single-quoted, unquoted. The first version of this parser
// read only the double-quoted form, so a perfectly ordinary
// `<script src='irrlicht.js'>` dropped irrlicht.js out of the release
// requirement without tripping a single fail-loud guard.
function attrValue(tag, name) {
  const m = new RegExp('\\b' + name + '\\s*=\\s*(?:"([^"]*)"|\'([^\']*)\'|([^\\s>]+))', 'i').exec(tag);
  if (!m) return null;
  return m[1] ?? m[2] ?? m[3] ?? null;
}

// index.html references: <script src>, plus <link href> for the stylesheet,
// the PWA manifest and the icons. Attribute order inside the tag is not
// assumed, nor is quoting, and `rel` is matched as the TOKEN SET it is —
// `rel="stylesheet alternate"` is a stylesheet.
export function parseHtmlEntries(html) {
  const entries = [];
  for (const tag of html.matchAll(/<script\b[^>]*>/g)) {
    const src = attrValue(tag[0], 'src');
    if (src) entries.push(src);
  }
  for (const tag of html.matchAll(/<link\b[^>]*>/g)) {
    const rel = attrValue(tag[0], 'rel');
    const href = attrValue(tag[0], 'href');
    if (!rel || !href) continue;
    if (rel.split(/\s+/).some((token) => REFERENCING_RELS.has(token.toLowerCase()))) {
      entries.push(href);
    }
  }
  return entries;
}

// Module-graph edges into this directory. Three spellings the first version
// of this parser missed are load-bearing here, and each was measured against
// it rather than guessed:
//   · `import './x.js'` — the side-effect form, which has no `from` at all;
//   · `import('./x.js')` — deferred, but still a fetch the directory must
//     satisfy, and a 404 there fails at the worst possible moment;
//   · a statement that is not the first token on its line, which the old
//     `^\s*` anchor excluded.
// Only `./`-relative specifiers count: a bare package name is not a file the
// release carries, and a `../` one lives outside the served directory.
export function parseStaticImports(source) {
  const out = [];
  // `[^;'"]` keeps each match inside one statement's specifier list, which can
  // contain neither a semicolon nor a quote.
  for (const m of source.matchAll(/\b(?:import|export)\b[^;'"]*?\bfrom\s*['"]\.\/([^'"]+)['"]/g)) {
    out.push(m[1]);
  }
  for (const m of source.matchAll(/\bimport\s*\(\s*['"]\.\/([^'"]+)['"]\s*\)/g)) {
    out.push(m[1]);
  }
  for (const m of source.matchAll(/\bimport\s+['"]\.\/([^'"]+)['"]/g)) {
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

// Files the release must carry whatever index.html happens to say. Deriving
// the whole requirement from index.html means DELETING a link shrinks it —
// exactly backwards for these three: sw.js has no static reference at all (it
// is registered at runtime by the pairing flow), and without the manifest and
// its icon the page cannot be installed to a Home Screen, which is where iOS
// delivers push at all (arc42 ADR-2/ADR-3). A release that forgets any of
// them ships a silently push-less PWA (arc42 §8.7).
export const REQUIRED_SHIPPED_MEMBERS = ['index.html', 'sw.js', 'beacon.webmanifest', 'beacon-icon.svg'];

// The full set of files a served copy of platforms/web needs at runtime: the
// members above, index.html's script/stylesheet/manifest/icon entries, the
// transitive ES-module graph, and the manifest's own icons. `html` may be
// overridden so the guards above can be driven against an index.html that is
// wrong in exactly one way.
export function deriveShippedSet({ html: htmlOverride = null } = {}) {
  const html = htmlOverride !== null ? htmlOverride : readFileSync(join(WEB_DIR, 'index.html'), 'utf8');
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
  const files = new Set([...REQUIRED_SHIPPED_MEMBERS, ...entries, ...modules]);
  for (const entry of entries.filter((f) => f.endsWith('.webmanifest'))) {
    const manifest = JSON.parse(readFileSync(join(WEB_DIR, entry), 'utf8'));
    for (const icon of manifest.icons || []) files.add(icon.src);
  }
  return { files, modules, edges, entries };
}

// Every WEB_FILES=( … ) array in tools/build-release.sh. Plural on purpose:
// the guard built on this asserts there is exactly ONE — the single copy list
// all three sites draw from — and a helper that returned only the first could
// not tell one list from three.
export function parseWebFilesLists(scriptSource) {
  return [...scriptSource.matchAll(/WEB_FILES=\(([^)]*)\)/g)]
    .map((m) => m[1].split(/\s+/).filter((token) => token.length > 0));
}

// The destination each `copy_web_files <dest>` call site stages into. The
// contents of WEB_FILES being right buys nothing if a site stopped reading it,
// and matching the one REMOVED spelling — which is all the predecessor of this
// did — cannot see a site that drifted back to some other hardcoded `cp`.
export function parseWebCopySites(scriptSource) {
  return [...scriptSource.matchAll(/^[ \t]*copy_web_files[ \t]+(\S+)/gm)].map((m) => m[1]);
}

// Any `cp` reaching into platforms/web from outside the helper's own body —
// the drift the guard above exists to catch, in whatever spelling it takes.
export function rawWebCopiesOutsideTheHelper(scriptSource) {
  const withoutHelper = scriptSource.replace(/copy_web_files\(\)[\s\S]*?\n\}/, '');
  return [...withoutHelper.matchAll(/^[ \t]*cp[ \t][^\n]*platforms\/web\/[^\n]*/gm)]
    .map((m) => m[0].trim());
}
