import { describe, test, expect } from 'vitest'
import { readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import {
  WEB_DIR, deriveShippedSet, parseWebFilesLists, parseHtmlEntries, parseStaticImports,
  parseWebCopySites, rawWebCopiesOutsideTheHelper, REQUIRED_SHIPPED_MEMBERS,
} from './shippedFiles.testutil.js'

// Release copy-list tripwire (docs/mobile-notifications-arc42.md §8.7, risk 6).
//
// tools/build-release.sh copies platforms/web into three artifacts (darwin
// tarball, linux tarballs, app bundle Resources). Before this tripwire the
// copy list held only the entry files — but irrlicht.js statically imports
// ~10 sibling modules, so a dashboard served from a tarball 404'd its own
// module graph and could not boot. The list is now WEB_FILES, defined once,
// and this test derives the required set from index.html + the import graph
// (via shippedFiles.testutil.js) so the list can never silently fall behind
// again. sw.js has no static reference at all — a missing sw.js is a
// silently push-less PWA, the exact failure §8.7 names.
//
// Fail-loud (house rule): zero parsed entries, zero parsed imports, or an
// unreadable module all throw inside deriveShippedSet and fail these tests —
// a verifier that cannot run must never read as a pass.

const RELEASE_SCRIPT = join(WEB_DIR, '..', '..', 'tools', 'build-release.sh')

describe('release copy list (build-release.sh WEB_FILES)', () => {
  test('the derived shipped set is non-vacuous', () => {
    const { files, edges, entries } = deriveShippedSet()
    // Vacuity guard: a known transitive member must be found by the walk. If
    // connectionProtocol.js is missing here, the walker is broken — not the
    // copy list.
    expect([...files]).toContain('connectionProtocol.js')
    expect([...files]).toContain('sw.js')
    expect(entries.length).toBeGreaterThan(0)
    expect(edges).toBeGreaterThan(0)
  })

  test('build-release.sh defines a single WEB_FILES list', () => {
    const lists = parseWebFilesLists(readFileSync(RELEASE_SCRIPT, 'utf8'))
    // The claim in this test's own name, asserted rather than assumed: two
    // lists is the same drift as three hardcoded `cp` lines wearing an array.
    expect(lists, 'tools/build-release.sh must define exactly one WEB_FILES=( … ) list').toHaveLength(1)
    expect(lists[0].length).toBeGreaterThan(0)
  })

  test('every copy site draws from that list — the sites cannot drift apart again', () => {
    const script = readFileSync(RELEASE_SCRIPT, 'utf8')
    // The three artifacts a release stages web files into (arc42 §8.7): the
    // darwin tarball, each linux tarball, and the app bundle Resources.
    expect(parseWebCopySites(script).sort()).toEqual([
      '"$APP_CONTENTS/Resources/web"',
      '"$TARBALL_STAGING/web"',
      '"$staging/web"',
    ])
    // …and nothing reaches into platforms/web behind the helper's back. The
    // predecessor of this arm only matched the ONE removed spelling, so a site
    // could drift back to a hardcoded `cp` of three files with the suite green.
    expect(
      rawWebCopiesOutsideTheHelper(script),
      'copies platforms/web files without going through copy_web_files',
    ).toEqual([])
  })

  test('every file the dashboard + Beacon PWA need is in WEB_FILES', () => {
    const { files } = deriveShippedSet()
    const [list] = parseWebFilesLists(readFileSync(RELEASE_SCRIPT, 'utf8'))
    const missing = [...files].filter((f) => !list.includes(f)).sort()
    expect(missing, 'required at runtime but absent from WEB_FILES in tools/build-release.sh').toEqual([])
  })

  test('every WEB_FILES entry exists in platforms/web', () => {
    const [list] = parseWebFilesLists(readFileSync(RELEASE_SCRIPT, 'utf8'))
    const stale = list.filter((f) => !existsSync(join(WEB_DIR, f))).sort()
    expect(stale, 'listed in WEB_FILES but not present in platforms/web').toEqual([])
  })

  test('the PWA members are required whatever index.html happens to reference', () => {
    // Deriving the requirement purely from index.html means deleting a link
    // SHRINKS it — and a release without beacon.webmanifest cannot be
    // installed on iOS at all, which is where push is mandatory (arc42 ADR-2).
    const html = readFileSync(join(WEB_DIR, 'index.html'), 'utf8')
    const stripped = html.replace(/<link\b[^>]*\brel="manifest"[^>]*>/, '')
    expect(stripped, 'index.html no longer carries the manifest link this case removes').not.toBe(html)
    const { files } = deriveShippedSet({ html: stripped })
    expect(REQUIRED_SHIPPED_MEMBERS).toContain('beacon.webmanifest')
    expect([...files]).toEqual(expect.arrayContaining([...REQUIRED_SHIPPED_MEMBERS, 'beacon-icon.svg']))
  })

  test('index.html does link the manifest (the arm above must not become the only reason it ships)', () => {
    const entries = parseHtmlEntries(readFileSync(join(WEB_DIR, 'index.html'), 'utf8'))
    expect(entries).toContain('beacon.webmanifest')
  })
})

describe('installed-app chrome colours', () => {
  const html = () => readFileSync(join(WEB_DIR, 'index.html'), 'utf8')
  const themeColorMeta = (source, scheme) => {
    const m = new RegExp(
      '<meta\\s+name="theme-color"\\s+content="([^"]+)"\\s+media="\\(prefers-color-scheme: ' + scheme + '\\)"',
    ).exec(source)
    return m && m[1]
  }

  test('both per-scheme metas name a --bg the stylesheet actually defines', () => {
    const css = readFileSync(join(WEB_DIR, 'irrlicht.css'), 'utf8')
    const backgrounds = [...css.matchAll(/--bg:\s*([^;]+);/g)].map((m) => m[1].trim())
    expect(backgrounds.length, 'irrlicht.css defines no --bg at all').toBeGreaterThan(0)
    expect(backgrounds).toContain(themeColorMeta(html(), 'light'))
    expect(backgrounds).toContain(themeColorMeta(html(), 'dark'))
  })

  test('the manifest, which carries one colour per key, stays on the dark palette', () => {
    // The Web App Manifest has no per-scheme form for theme_color or
    // background_color, so the installed app's splash cannot follow the phone
    // the way the two metas above do. Pinning both to the dark meta's value
    // keeps the gap a stated single choice rather than a third colour nobody
    // notices drifting — index.html's comment names it as the surface those
    // metas do not reach.
    const manifest = JSON.parse(readFileSync(join(WEB_DIR, 'beacon.webmanifest'), 'utf8'))
    const dark = themeColorMeta(html(), 'dark')
    expect(dark, 'index.html no longer carries a dark-scheme theme-color meta').toBeTruthy()
    expect(manifest.theme_color).toBe(dark)
    expect(manifest.background_color).toBe(dark)
  })
})

// Committed mutation evidence for the copy-site and list-count guards above
// (AGENTS.md "Testing"): both hold by construction against today's
// build-release.sh, so each is driven here against a fixture that is wrong in
// exactly one way, plus a correct fixture as the vacuity guard.
describe('release-script guards, driven against deliberately-wrong scripts', () => {
  const GOOD = [
    'WEB_FILES=(', '    index.html', '    irrlicht.js', ')', '',
    'copy_web_files() {', '    local dest="$1"', '    local f',
    '    for f in "${WEB_FILES[@]}"; do', '        cp "platforms/web/$f" "$dest/"',
    '    done', '}', '',
    'copy_web_files "$TARBALL_STAGING/web"',
    'copy_web_files "$staging/web"',
    'copy_web_files "$APP_CONTENTS/Resources/web"',
  ].join('\n')

  test('the fixture is correct as written (vacuity guard)', () => {
    expect(parseWebFilesLists(GOOD)).toHaveLength(1)
    expect(parseWebCopySites(GOOD)).toHaveLength(3)
    expect(rawWebCopiesOutsideTheHelper(GOOD)).toEqual([])
  })

  test('a site that drifted back to a hardcoded cp is named', () => {
    const drifted = GOOD.replace(
      'copy_web_files "$staging/web"',
      'cp platforms/web/index.html platforms/web/irrlicht.css "$staging/web/"',
    )
    expect(drifted).not.toBe(GOOD)
    expect(parseWebCopySites(drifted)).toHaveLength(2)
    expect(rawWebCopiesOutsideTheHelper(drifted)).toEqual([
      'cp platforms/web/index.html platforms/web/irrlicht.css "$staging/web/"',
    ])
  })

  test('a second WEB_FILES list is reported, not silently ignored', () => {
    const two = GOOD + '\nWEB_FILES=(\n    index.html\n)\n'
    expect(parseWebFilesLists(two)).toHaveLength(2)
  })
})

// The entry/import parsers decide what the release is REQUIRED to carry, so a
// spelling they cannot read drops a needed file out of the requirement without
// tripping any fail-loud guard. Both `want:true` rows (a spelling that must be
// found) and `want:false` rows (something that must NOT be mistaken for an
// edge) are pinned — a parser that matched everything would look like
// excellent coverage.
describe('shipped-set parsers', () => {
  const importCases = [
    ['default import', "import a from './a.js';", ['a.js']],
    ['named import', "import { a } from './b.js';", ['b.js']],
    ['namespace import', "import * as ns from './c.js';", ['c.js']],
    ['re-export all', "export * from './d.js';", ['d.js']],
    ['re-export named', "export { a } from './e.js';", ['e.js']],
    ['multi-line import', "import {\n  a,\n  b,\n} from './f.js';", ['f.js']],
    ['double-quoted specifier', 'import a from "./g.js";', ['g.js']],
    // The three spellings the first version of this parser silently dropped.
    ['side-effect import', "import './h.js';", ['h.js']],
    ['dynamic import', "const m = await import('./i.js');", ['i.js']],
    ['import not first on its line', "const x = 1; import a from './j.js';", ['j.js']],
    // Must NOT be read as an edge: a bare specifier is not a file in this
    // directory, and the release must not start demanding one.
    ['bare package specifier', "import a from 'vitest';", []],
    ['parent-relative specifier', "import a from '../outside.js';", []],
  ]
  for (const [name, source, want] of importCases) {
    test('import parser: ' + name, () => {
      expect(parseStaticImports(source)).toEqual(want)
    })
  }

  const htmlCases = [
    ['double-quoted script', '<script type="module" src="irrlicht.js"></script>', ['irrlicht.js']],
    ['stylesheet link', '<link rel="stylesheet" href="irrlicht.css">', ['irrlicht.css']],
    ['manifest link', '<link rel="manifest" href="beacon.webmanifest">', ['beacon.webmanifest']],
    ['href before rel', '<link href="beacon.webmanifest" rel="manifest">', ['beacon.webmanifest']],
    // The shapes the first version of this parser silently dropped.
    ['single-quoted attributes', "<script src='irrlicht.js'></script>", ['irrlicht.js']],
    ['unquoted attributes', '<script src=irrlicht.js></script>', ['irrlicht.js']],
    ['multi-token rel', '<link rel="stylesheet alternate" href="alt.css">', ['alt.css']],
    ['icon rel', '<link rel="apple-touch-icon" href="beacon-icon.svg">', ['beacon-icon.svg']],
    // Must NOT be read as an entry.
    ['inline script', '<script>const x = 1;</script>', []],
    ['unrelated rel', '<link rel="dns-prefetch" href="https://example.test">', []],
  ]
  for (const [name, html, want] of htmlCases) {
    test('html parser: ' + name, () => {
      expect(parseHtmlEntries(html)).toEqual(want)
    })
  }
})
