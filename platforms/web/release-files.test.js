import { describe, test, expect } from 'vitest'
import { readFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { WEB_DIR, deriveShippedSet, parseWebFilesList } from './shippedFiles.testutil.js'

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
    const script = readFileSync(RELEASE_SCRIPT, 'utf8')
    const list = parseWebFilesList(script)
    expect(list, 'tools/build-release.sh has no WEB_FILES=( … ) list — the release web payload is not derivable').not.toBeNull()
    expect(list.length).toBeGreaterThan(0)
    // One list, used by every copy site: the raw per-site `cp platforms/web/…`
    // spellings must be gone, or the sites can drift apart again.
    expect(script).not.toMatch(/cp platforms\/web\/index\.html platforms\/web/)
  })

  test('every file the dashboard + Beacon PWA need is in WEB_FILES', () => {
    const { files } = deriveShippedSet()
    const list = parseWebFilesList(readFileSync(RELEASE_SCRIPT, 'utf8'))
    expect(list).not.toBeNull()
    const missing = [...files].filter((f) => !list.includes(f)).sort()
    expect(missing, 'required at runtime but absent from WEB_FILES in tools/build-release.sh').toEqual([])
  })

  test('every WEB_FILES entry exists in platforms/web', () => {
    const list = parseWebFilesList(readFileSync(RELEASE_SCRIPT, 'utf8'))
    expect(list).not.toBeNull()
    const stale = list.filter((f) => !existsSync(join(WEB_DIR, f))).sort()
    expect(stale, 'listed in WEB_FILES but not present in platforms/web').toEqual([])
  })
})
