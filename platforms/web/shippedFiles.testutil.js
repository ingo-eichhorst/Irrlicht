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

import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const WEB_DIR = dirname(fileURLToPath(import.meta.url));

// Everything actually sitting in platforms/web, split into files and
// directories. The Dockerfile's directory-copy strategy ships whatever
// survives .dockerignore, so the guard on it needs the real tree rather than
// a second hand-kept list.
export function webDirEntries() {
  const entries = readdirSync(WEB_DIR, { withFileTypes: true });
  if (entries.length === 0) {
    throw new Error('fail-loud: platforms/web listed zero entries — the directory walk cannot be trusted');
  }
  return {
    files: entries.filter((e) => e.isFile()).map((e) => e.name),
    dirs: entries.filter((e) => e.isDirectory()).map((e) => e.name),
  };
}

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
export const REQUIRED_SHIPPED_MEMBERS = ['index.html', 'sw.js', 'elfdans.webmanifest', 'elfdans-icon.svg'];

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

// ── The other two artifacts that stage platforms/web ─────────────────────
// tools/build-release.sh is not the only place a copy list can fall behind:
// examples/relay/Dockerfile bakes the dashboard into the relay image and
// site/install.sh installs it on the curl --daemon-only path. Both carried
// the same three entry files while irrlicht.js imported ten siblings, so both
// served a dashboard that 404'd its own module graph — measured live for the
// Dockerfile (`GET /collapsedGroups.js` → 404 inside the running container).
//
// Each of the two is allowed to satisfy the obligation in one of two ways,
// read off the file rather than assumed, because they are different claims
// with different obligations:
//   · DIRECTORY — stage the whole platforms/web (or the whole extracted
//     web/). Cannot drift by construction, so what is left to check is that
//     nothing FILTERS a required file back out, and that the filter still
//     keeps the dev-only files from shipping.
//   · ENUMERATED — name files individually, in which case the enumeration has
//     to cover the derived runtime set, the same obligation WEB_FILES carries.
// Declaring neither is a failure, not a skip: a file that stopped matching
// any recognized shape is one this guard cannot vouch for.

// One logical instruction per entry, line continuations joined.
function dockerInstructions(dockerfileSource) {
  return dockerfileSource
    .replace(/\\\r?\n\s*/g, ' ')
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line.length > 0 && !line.startsWith('#'));
}

// Every COPY whose sources reach into platforms/web, with the flags dropped
// and the destination separated off.
export function parseDockerWebCopies(dockerfileSource) {
  const out = [];
  for (const instruction of dockerInstructions(dockerfileSource)) {
    if (!/^COPY\b/i.test(instruction)) continue;
    const args = instruction.slice(4).trim().split(/\s+/).filter((a) => !a.startsWith('--'));
    if (args.length < 2) continue;
    const sources = args.slice(0, -1);
    const webSources = sources.filter((s) => s.replace(/^\.\//, '').startsWith('platforms/web'));
    if (webSources.length === 0) continue;
    out.push({ raw: instruction, sources: webSources, dest: args[args.length - 1] });
  }
  return out;
}

// Which of the two strategies examples/relay/Dockerfile declares, plus the
// enumerated basenames when it declares that one.
export function dockerWebPayload(dockerfileSource) {
  const copies = parseDockerWebCopies(dockerfileSource);
  if (copies.length === 0) {
    throw new Error('fail-loud: no COPY in this Dockerfile stages platforms/web — the parser found nothing to vouch for');
  }
  const sources = copies.flatMap((c) => c.sources).map((s) => s.replace(/^\.\//, ''));
  const directory = sources.filter((s) => s === 'platforms/web' || s === 'platforms/web/');
  const files = sources.filter((s) => !directory.includes(s)).map((s) => s.replace('platforms/web/', ''));
  return {
    strategy: directory.length > 0 ? 'directory' : 'enumerated',
    copies,
    files,
  };
}

// .dockerignore rules in file order. Docker resolves a path against every
// rule and the LAST match wins, which is what makes `!` negation work, so the
// order is part of the data and not an incidental detail.
export function parseDockerignore(source) {
  return source
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line.length > 0 && !line.startsWith('#'))
    .map((line) => (line.startsWith('!') ? { negated: true, pattern: line.slice(1) } : { negated: false, pattern: line }));
}

// A .dockerignore pattern as a regex over the whole context-relative path.
// `**` spans path separators, `*` and `?` do not — Docker's own rule, and the
// difference decides whether a bare `snapshots` reaches platforms/web/snapshots
// (it does not).
function dockerignoreRegex(pattern) {
  const cleaned = pattern.replace(/^\/+/, '').replace(/\/+$/, '');
  const body = cleaned
    .split('/')
    .map((segment) => {
      if (segment === '**') return '.*';
      return segment.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '[^/]*').replace(/\?/g, '[^/]');
    })
    .join('/')
    .replace(/(^|\/)\.\*\//g, '$1(?:.*/)?');
  return new RegExp('^' + body + '$');
}

// Whether relPath (context-relative, `/`-separated) is excluded from the
// build context. A rule matching an ANCESTOR excludes everything beneath it,
// which is how `**/node_modules` removes a whole tree rather than a directory
// entry Docker would then descend into anyway.
export function dockerignoreExcludes(rules, relPath) {
  const prefixes = relPath.split('/').map((_, i, parts) => parts.slice(0, i + 1).join('/'));
  let excluded = false;
  for (const rule of rules) {
    const re = dockerignoreRegex(rule.pattern);
    if (prefixes.some((p) => re.test(p))) excluded = !rule.negated;
  }
  return excluded;
}

// What site/install.sh stages into $HOME/.local/share/irrlicht/web on the
// --daemon-only path. `wholesale` holds the lines that copy the extracted
// web/ as a unit (a glob over its contents, or `cp -R …/web/.`), `enumerated`
// the individual filenames named by hand.
export function parseInstallShWebPayload(scriptSource) {
  const wholesale = [];
  const enumerated = [];
  for (const line of scriptSource.split('\n')) {
    // Quoting is a shell concern and varies freely ("$TMPDIR"/extract vs
    // "$TMPDIR/extract"); stripping it first keeps the two shapes below from
    // needing a spelling each.
    const unquoted = line.replace(/["']/g, '');
    if (!/extract\/web\//.test(unquoted)) continue;
    if (/extract\/web\/(\*|\.(\s|$))/.test(unquoted)) {
      wholesale.push(line.trim());
      continue;
    }
    for (const m of unquoted.matchAll(/extract\/web\/([A-Za-z0-9_][A-Za-z0-9._-]*)/g)) {
      enumerated.push(m[1]);
    }
  }
  if (wholesale.length === 0 && enumerated.length === 0) {
    throw new Error('fail-loud: site/install.sh stages no files out of the extracted web/ — the parser found nothing to vouch for');
  }
  return { strategy: wholesale.length > 0 ? 'directory' : 'enumerated', wholesale, enumerated };
}
