import { describe, test, expect } from 'vitest'
import { readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import {
  WEB_DIR, deriveShippedSet, parseWebFilesLists, parseHtmlEntries, parseStaticImports,
  parseWebCopySites, rawWebCopiesOutsideTheHelper, REQUIRED_SHIPPED_MEMBERS,
  webDirEntries, parseDockerWebCopies, dockerWebPayload, parseDockerignore,
  dockerignoreExcludes, parseInstallShWebPayload,
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

const REPO_ROOT = join(WEB_DIR, '..', '..')
const RELEASE_SCRIPT = join(REPO_ROOT, 'tools', 'build-release.sh')
const RELAY_DOCKERFILE = join(REPO_ROOT, 'examples', 'relay', 'Dockerfile')
const DOCKERIGNORE = join(REPO_ROOT, '.dockerignore')
const INSTALL_SH = join(REPO_ROOT, 'site', 'install.sh')

// The effective contents of the relay image's /web, given the Dockerfile's
// declared strategy. Directory-copy ships whatever survives the context
// filter, so .dockerignore is part of the payload and not a side note.
function relayImageWebFiles() {
  const payload = dockerWebPayload(readFileSync(RELAY_DOCKERFILE, 'utf8'))
  if (payload.strategy === 'enumerated') return { payload, files: payload.files }
  const rules = parseDockerignore(readFileSync(DOCKERIGNORE, 'utf8'))
  const files = webDirEntries().files
    .filter((f) => !dockerignoreExcludes(rules, 'platforms/web/' + f))
  return { payload, files }
}

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
    // The four artifacts a release stages web files into (arc42 §8.7): the
    // darwin tarball, each linux daemon tarball, the app bundle Resources, and
    // each linux relay tarball. The relay's destination is the odd one out —
    // Resources/web under its own staging root rather than a flat web/ —
    // because that is the only exe-relative directory its UI resolution looks
    // in (core/cmd/irrlichtrelay/main.go resolveUIDirFor).
    expect(parseWebCopySites(script).sort()).toEqual([
      '"$APP_CONTENTS/Resources/web"',
      '"$TARBALL_STAGING/web"',
      '"$staging/Resources/web"',
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

  test('every file the dashboard + Elfdans PWA need is in WEB_FILES', () => {
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
    // SHRINKS it — and a release without elfdans.webmanifest cannot be
    // installed on iOS at all, which is where push is mandatory (arc42 ADR-2).
    const html = readFileSync(join(WEB_DIR, 'index.html'), 'utf8')
    const stripped = html.replace(/<link\b[^>]*\brel="manifest"[^>]*>/, '')
    expect(stripped, 'index.html no longer carries the manifest link this case removes').not.toBe(html)
    const { files } = deriveShippedSet({ html: stripped })
    expect(REQUIRED_SHIPPED_MEMBERS).toContain('elfdans.webmanifest')
    expect([...files]).toEqual(expect.arrayContaining([...REQUIRED_SHIPPED_MEMBERS, 'elfdans-icon.svg']))
  })

  test('index.html does link the manifest (the arm above must not become the only reason it ships)', () => {
    const entries = parseHtmlEntries(readFileSync(join(WEB_DIR, 'index.html'), 'utf8'))
    expect(entries).toContain('elfdans.webmanifest')
  })
})

// The relay bakes a dashboard snapshot into its image, and the curl installer
// stages one into ~/.local/share/irrlicht/web. Both are copy lists that can
// fall behind exactly as build-release.sh's did, and both did: each shipped
// the three entry files while irrlicht.js imported ten siblings. The
// Dockerfile's failure was measured live — `GET /collapsedGroups.js` answered
// 404 inside the running container, so the dashboard could not boot and the
// Elfdans PWA files were absent entirely, leaving the relay serving a push API
// with no client able to consume it (docs/mobile-notifications-arc42.md §8.7,
// risk 6).
describe('relay image web payload (examples/relay/Dockerfile)', () => {
  test('the Dockerfile stages platforms/web in a shape this guard understands', () => {
    // Vacuity guard for both arms below: a Dockerfile that stopped copying
    // platforms/web in any recognized shape must redden here rather than let
    // the coverage arm pass over an empty parse.
    const copies = parseDockerWebCopies(readFileSync(RELAY_DOCKERFILE, 'utf8'))
    expect(copies.length, 'no COPY in examples/relay/Dockerfile reaches platforms/web').toBeGreaterThan(0)
    expect(['directory', 'enumerated']).toContain(relayImageWebFiles().payload.strategy)
  })

  test('every file the dashboard + Elfdans PWA need reaches the image', () => {
    const { files } = deriveShippedSet()
    const shipped = relayImageWebFiles().files
    const missing = [...files].filter((f) => !shipped.includes(f)).sort()
    expect(missing, 'required at runtime but never reaches /web in the relay image').toEqual([])
  })

  test('the dev-only files stay out of the image', () => {
    // A directory copy is only safe because .dockerignore filters the context;
    // an image carrying node_modules and the vitest suite is the cost of
    // getting that wrong, and it is invisible without an arm that looks.
    const { payload, files } = relayImageWebFiles()
    if (payload.strategy === 'enumerated') {
      expect(files.filter((f) => /\.(test|testutil)\.js$/.test(f))).toEqual([])
      return
    }
    const rules = parseDockerignore(readFileSync(DOCKERIGNORE, 'utf8'))
    const { files: onDisk, dirs } = webDirEntries()
    const devFiles = onDisk.filter((f) => /\.(test|testutil)\.js$/.test(f))
    expect(devFiles.length, 'platforms/web holds no test files — this arm would assert nothing').toBeGreaterThan(0)
    expect(files.filter((f) => devFiles.includes(f))).toEqual([])
    expect(dirs).toContain('node_modules')
    expect(dockerignoreExcludes(rules, 'platforms/web/node_modules')).toBe(true)
  })
})

describe('curl installer web payload (site/install.sh)', () => {
  test('install.sh stages the extracted web/ in a shape this guard understands', () => {
    // Same vacuity guard, same reason: parseInstallShWebPayload throws when it
    // recognizes neither shape, so an installer that changed out from under
    // this test fails rather than passing on an empty parse.
    const payload = parseInstallShWebPayload(readFileSync(INSTALL_SH, 'utf8'))
    expect(['directory', 'enumerated']).toContain(payload.strategy)
    expect(payload.wholesale.length + payload.enumerated.length).toBeGreaterThan(0)
  })

  test('every file the dashboard + Elfdans PWA need is installed', () => {
    const payload = parseInstallShWebPayload(readFileSync(INSTALL_SH, 'utf8'))
    if (payload.strategy === 'directory') {
      // Copying the extracted directory wholesale carries whatever the tarball
      // carries, which the WEB_FILES arms above already grade. What is left to
      // check is that no half-migrated enumeration survives beside it, since
      // that would be a second list nobody is watching.
      expect(payload.enumerated, 'install.sh copies web/ wholesale AND still names files by hand').toEqual([])
      return
    }
    const { files } = deriveShippedSet()
    const missing = [...files].filter((f) => !payload.enumerated.includes(f)).sort()
    expect(missing, 'required at runtime but not installed by site/install.sh').toEqual([])
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
    const manifest = JSON.parse(readFileSync(join(WEB_DIR, 'elfdans.webmanifest'), 'utf8'))
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

// Committed mutation evidence for the two artifacts above (AGENTS.md
// "Testing"). Reality supplied the first red for free — both files carried the
// stale three-file list, and the coverage arms named all fourteen missing
// files before either was touched. That evidence disappears the moment they
// are fixed, and both now satisfy their obligation by copying a directory,
// which passes by construction. So each shape is driven here against a fixture
// wrong in exactly one way, with a correct fixture as the vacuity guard.
describe('relay/installer guards, driven against deliberately-wrong sources', () => {
  const DOCKER_GOOD = [
    'FROM golang AS build',
    'COPY core/ ./',
    'FROM debian AS runtime',
    'COPY --chown=relay:relay platforms/web/ /web/',
  ].join('\n')

  const IGNORE_GOOD = ['**/node_modules', 'platforms/web/*.test.js', 'platforms/web/snapshots'].join('\n')

  test('the Dockerfile fixture is correct as written (vacuity guard)', () => {
    const payload = dockerWebPayload(DOCKER_GOOD)
    expect(payload.strategy).toBe('directory')
    expect(payload.copies).toHaveLength(1)
    const rules = parseDockerignore(IGNORE_GOOD)
    expect(dockerignoreExcludes(rules, 'platforms/web/irrlicht.js')).toBe(false)
    expect(dockerignoreExcludes(rules, 'platforms/web/elfdans.webmanifest')).toBe(false)
  })

  test('a Dockerfile that drifted back to naming files is read as an enumeration, not a directory', () => {
    const drifted = DOCKER_GOOD.replace(
      'COPY --chown=relay:relay platforms/web/ /web/',
      'COPY --chown=relay:relay platforms/web/index.html platforms/web/irrlicht.css platforms/web/irrlicht.js /web/',
    )
    expect(drifted).not.toBe(DOCKER_GOOD)
    const payload = dockerWebPayload(drifted)
    expect(payload.strategy).toBe('enumerated')
    // This is the shape the coverage arm grades against the derived set, and
    // it is exactly the one that was live: three files, fourteen short.
    expect(payload.files).toEqual(['index.html', 'irrlicht.css', 'irrlicht.js'])
  })

  test('a Dockerfile that stages no web files at all fails loudly', () => {
    const stripped = DOCKER_GOOD.replace('COPY --chown=relay:relay platforms/web/ /web/', '')
    expect(() => dockerWebPayload(stripped)).toThrow(/fail-loud/)
  })

  test('an over-broad ignore pattern silently empties the image — and is caught', () => {
    // The directory copy's whole safety rests on the context filter, so a
    // pattern one character too wide has to be visible here. `*.js` scoped to
    // the web tree takes the entire module graph out of the image while the
    // COPY line still reads perfectly.
    const tooWide = IGNORE_GOOD + '\nplatforms/web/*.js'
    const rules = parseDockerignore(tooWide)
    expect(dockerignoreExcludes(rules, 'platforms/web/irrlicht.js')).toBe(true)
    expect(dockerignoreExcludes(parseDockerignore(IGNORE_GOOD), 'platforms/web/irrlicht.js')).toBe(false)
  })

  const INSTALL_GOOD = [
    '    mkdir -p "$UI_DIR"',
    '    _web_count=0',
    '    for _web_file in "$TMPDIR"/extract/web/*; do',
    '        [ -f "$_web_file" ] || continue',
    '        install -m 644 "$_web_file" "$UI_DIR/"',
    '        _web_count=$((_web_count + 1))',
    '    done',
  ].join('\n')

  test('the installer fixture is correct as written (vacuity guard)', () => {
    const payload = parseInstallShWebPayload(INSTALL_GOOD)
    expect(payload.strategy).toBe('directory')
    expect(payload.wholesale).toHaveLength(1)
    expect(payload.enumerated).toEqual([])
  })

  test('an installer that names files by hand is read as an enumeration', () => {
    const drifted = [
      '    mkdir -p "$UI_DIR"',
      '    install -m 644 "$TMPDIR/extract/web/index.html" "$UI_DIR/index.html"',
      '    install -m 644 "$TMPDIR/extract/web/irrlicht.css" "$UI_DIR/irrlicht.css"',
      '    install -m 644 "$TMPDIR/extract/web/irrlicht.js"  "$UI_DIR/irrlicht.js"',
    ].join('\n')
    const payload = parseInstallShWebPayload(drifted)
    expect(payload.strategy).toBe('enumerated')
    expect(payload.enumerated).toEqual(['index.html', 'irrlicht.css', 'irrlicht.js'])
  })

  test('a half-migrated installer keeps both, so the leftover enumeration is reportable', () => {
    // Wholesale copy plus a surviving by-name install is the state a partial
    // edit leaves behind, and it reads as healthy unless the enumeration is
    // still collected alongside the glob.
    const half = INSTALL_GOOD + '\n    install -m 644 "$TMPDIR/extract/web/index.html" "$UI_DIR/index.html"'
    const payload = parseInstallShWebPayload(half)
    expect(payload.strategy).toBe('directory')
    expect(payload.enumerated).toEqual(['index.html'])
  })

  test('an installer that stages nothing out of web/ fails loudly', () => {
    expect(() => parseInstallShWebPayload('    mkdir -p "$UI_DIR"\n    ok')).toThrow(/fail-loud/)
  })
})

// The .dockerignore matcher decides which files reach the relay image, so it
// gets the same treatment as the import/entry parsers: both rows that must
// match and rows that must NOT. A matcher that excluded everything would make
// the coverage arm above fail loudly; one that excluded nothing would make it
// pass over an image full of node_modules, which is the quieter failure.
describe('dockerignore matcher', () => {
  const cases = [
    ['exact path', ['platforms/web/package.json'], 'platforms/web/package.json', true],
    ['glob within a segment', ['platforms/web/*.test.js'], 'platforms/web/elfdans.test.js', true],
    ['double-star prefix', ['**/node_modules'], 'platforms/web/node_modules', true],
    ['a matched directory takes its contents with it', ['**/node_modules'], 'platforms/web/node_modules/vitest/index.js', true],
    ['leading slash is stripped', ['/.build'], '.build', true],
    ['negation re-includes', ['.env.*', '!.env.example'], '.env.example', false],
    ['negation only applies to what follows it', ['!.env.example', '.env.*'], '.env.example', true],
    // Must NOT be excluded. The first is the one that matters most: a bare
    // segment is anchored at the context root, so `snapshots` does not reach
    // platforms/web/snapshots — which is why the real file spells that one out.
    ['bare segment does not match a nested path', ['snapshots'], 'platforms/web/snapshots', false],
    ['a single star does not cross a separator', ['platforms/*.js'], 'platforms/web/irrlicht.js', false],
    ['a glob on a different extension leaves .js alone', ['platforms/web/*.test.js'], 'platforms/web/irrlicht.js', false],
    // Pinned because the first draft of the row above got it backwards: a
    // pattern that matches an ANCESTOR removes the whole subtree, so
    // `platforms/*` matching the directory platforms/web is what excludes the
    // file under it — the single star never crosses the separator itself.
    ['a matched ancestor directory excludes what is under it', ['platforms/*'], 'platforms/web/irrlicht.js', true],
    ['an unrelated rule matches nothing', ['.git'], 'platforms/web/index.html', false],
  ]
  for (const [name, patterns, path, want] of cases) {
    test(name, () => {
      expect(dockerignoreExcludes(parseDockerignore(patterns.join('\n')), path)).toBe(want)
    })
  }
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
    ['manifest link', '<link rel="manifest" href="elfdans.webmanifest">', ['elfdans.webmanifest']],
    ['href before rel', '<link href="elfdans.webmanifest" rel="manifest">', ['elfdans.webmanifest']],
    // The shapes the first version of this parser silently dropped.
    ['single-quoted attributes', "<script src='irrlicht.js'></script>", ['irrlicht.js']],
    ['unquoted attributes', '<script src=irrlicht.js></script>', ['irrlicht.js']],
    ['multi-token rel', '<link rel="stylesheet alternate" href="alt.css">', ['alt.css']],
    ['icon rel', '<link rel="apple-touch-icon" href="elfdans-icon.svg">', ['elfdans-icon.svg']],
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

describe('relay tarball layout and publication (tools/build-release.sh)', () => {
  // The three arms below close gaps a review probe found: each of these could
  // be severed while every other arm stayed green, and each ships something
  // broken — a 503 dashboard, an image that serves nothing, or an asset the
  // installer refuses because it cannot verify it.
  const script = () => readFileSync(RELEASE_SCRIPT, 'utf8')

  test('the relay tarball stages bin/ and Resources/web/, not the daemon flat layout', () => {
    // Load-bearing and measured, not stylistic: the relay's only exe-relative
    // UI branch is <exedir>/../Resources/web, so a flat {binary, web/} tarball
    // — the shape the daemon uses two functions above — logs "dashboard UI not
    // found" and answers 503 on / with no environment set.
    const fn = script().match(/build_relay_linux_tarball\(\)\s*\{[\s\S]*?\n\}/)
    expect(fn, 'build_relay_linux_tarball is gone or no longer a shell function').not.toBeNull()
    const body = fn[0]
    // Match the COPY OF THE BINARY, not merely the string "$staging/bin"
    // anywhere in the function — the mkdir a line above contains it too, so a
    // substring check passes with the binary staged flat beside it.
    expect(body, 'the relay binary is not copied into $staging/bin/')
      .toMatch(/\bcp\s+"[^"]*"\s+"\$staging\/bin\/[^"]*"/)
    expect(body, 'the web payload is not staged under Resources/web').toMatch(/\$staging\/Resources\/web/)
    expect(body, 'the relay tarball must inherit WEB_FILES via copy_web_files').toMatch(/copy_web_files\s+"\$staging\/Resources\/web"/)
  })

  test('every tarball the script builds is listed in the checksum manifest', () => {
    // site/install.sh's sha256_verify greps checksums.sha256 for the asset it
    // just downloaded and aborts when it is absent, so an unlisted asset is
    // not merely unverified — it is uninstallable.
    const s = script()
    const built = [...s.matchAll(/tar -czf "\$BUILD_DIR\/([^"]+\.tar\.gz)"/g)].map((m) => m[1])
    expect(built.length, 'no tar -czf targets parsed — this arm would assert nothing').toBeGreaterThan(0)
    const manifest = s.match(/shasum -a 256[\s\S]*?> checksums\.sha256/)
    expect(manifest, 'the checksum manifest block is gone or reshaped').not.toBeNull()
    const listed = manifest[0]
    // The build sites carry a ${arch} loop variable while the manifest lists
    // each arch expanded, so compare on the part that is stable across the
    // loop: everything up to the last ${…}. That still fails when a whole
    // family goes missing, which is the regression this arm is here for.
    const unlisted = built.filter((name) => {
      const lastVar = name.lastIndexOf('${')
      const stable = lastVar > 0 ? name.slice(0, lastVar) : name
      return !listed.includes(stable)
    })
    expect(unlisted, 'built but absent from checksums.sha256 — install.sh cannot verify these').toEqual([])
  })

  test('the relay image points IRRLICHT_UI_DIR at the directory it copies into', () => {
    // The payload arms grade WHICH files are copied; nothing graded WHERE they
    // land or what points at them, so changing the COPY destination or
    // dropping the ENV shipped a 503 image with every other arm green.
    const dockerfile = readFileSync(RELAY_DOCKERFILE, 'utf8')
    const dest = dockerfile.match(/^COPY\b[^\n]*\bplatforms\/web\/?\s+(\S+)\s*$/m)
    expect(dest, 'no COPY of platforms/web with a destination found').not.toBeNull()
    const uiDir = dockerfile.match(/^ENV\s+IRRLICHT_UI_DIR=(\S+)/m)
    expect(uiDir, 'the image sets no IRRLICHT_UI_DIR — the relay would have to guess').not.toBeNull()
    const norm = (p) => p.replace(/\/+$/, '')
    expect(norm(uiDir[1]), 'IRRLICHT_UI_DIR does not name the directory the web payload is copied into')
      .toBe(norm(dest[1]))
  })
})
