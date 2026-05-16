---
name: ir:release
description: >
  Build and publish an irrlicht release. Bumps version, builds Go daemon + Swift
  app, creates signed app bundle with icon, packages DMG (branded installer) + PKG,
  updates docs/changelog/landing page, commits, tags, pushes, and creates GitHub
  release with assets. Default: patch bump. Use "/ir:release minor" or "/ir:release major".
---

# Irrlicht Release

Build and publish a complete irrlicht release. Run all steps back-to-back without pausing.

The argument (if any) is the bump type: `patch` (default), `minor`, or `major`.

## Step 1: Determine Version

1. Read `version.json` for the current version.
2. Bump according to the argument (default `patch`):
   - `patch`: 0.2.3 → 0.2.4
   - `minor`: 0.2.3 → 0.3.0
   - `major`: 0.2.3 → 1.0.0
3. Set `$NEW_VERSION` for all subsequent steps.
4. **Capture the release base SHA** — load-bearing for the race guard in
   Step 7b. The Swift build + DMG packaging takes ~5 minutes; a maintainer
   PR squash-merged during that window will silently end up on top of the
   release commit, included in the v$NEW_VERSION tree but absent from the
   release notes and built against stale artifacts. v0.4.5 shipped through
   exactly this race (PR #379 landed mid-build; required an amend PR + a
   force-pushed tag to recover).

   ```bash
   git fetch origin main
   BASE_SHA=$(git rev-parse origin/main)
   echo "$BASE_SHA" > /tmp/irrlicht-release-base.sha
   echo "release base: $BASE_SHA"
   ```

   Keep `BASE_SHA` (or the file) accessible through Step 7b. Step 7b's
   race-guard check fails the release if `origin/main` has moved past it.

## Step 1.5: Refresh Model Aliases (codeburn sync)

Run the `/ir:refresh-aliases` workflow inline before drafting release notes so
any new frontend aliases ship with this release instead of waiting another
cycle. The map in `core/pkg/capacity/aliases.go` is a hand-translated port of
codeburn's `BUILTIN_ALIASES`; new entries upstream mean real users on new
frontends (Cursor variants, Antigravity Gemini models, etc.) price at $0
until we sync.

1. Fetch upstream and diff against the in-repo map (see
   `.claude/skills/ir:refresh-aliases/skill.md` for the full workflow).
2. If the diff is empty: continue to Step 2. No-op is the common case.
3. If **Added** entries exist: append them to `core/pkg/capacity/aliases.go`
   in the appropriate grouping section, preserving the per-group comments.
   The table-driven test (`TestModelAliases_ResolveToCanonical`) auto-covers
   new entries — Step 5 will catch a mistyped canonical.
4. If **Changed** entries exist: pause and surface to the maintainer. A
   canonical-target change is rare (model rename or codeburn correction) and
   warrants review before shipping.
5. If **Removed** entries exist: leave the local entry in place — codeburn
   may have dropped something we still need. Note in the release notes if
   you want to track it.
6. If any Added/Changed canonical isn't in LiteLLM's table (check via
   `~/.local/share/irrlicht/model-capacity-cache.json`), the alias still
   resolves to a zero-value capacity — flag for follow-up but don't block
   the release.

If the refresh fetch fails (offline, upstream 5xx), **continue the
release** with the existing map — this step is "best effort, fail soft."
Don't block shipping on a transient upstream outage. Log a one-line note
in the release notes if the fetch was skipped.

When this step adds entries, mention them under **Fixed** in the
CHANGELOG / release notes drafted in Step 2 — phrase it as
"price non-Anthropic-frontend sessions correctly: added N new aliases
synced from codeburn (Cursor / OMP / Antigravity / …)" so users on
those frontends know the gap closed.

## Step 2: Gather Changes

1. Run `git log --oneline $(git describe --tags --abbrev=0)..HEAD --no-merges` to list all commits since the last release.
2. Categorize into: Features, Fixes, Architecture/Refactoring, Docs, Distribution.
3. Draft release notes in the style of previous releases (see `gh release view` for format).

**Line-wrap rule (load-bearing — don't skip):** GitHub renders the release
body with GFM "breaks": soft line breaks (single newlines) become `<br>`.
A paragraph hand-wrapped at 80 columns therefore lands on the release page
as a stack of short ragged lines (the v0.4.1 release shipped this bug).
Write each paragraph and each bullet as **one long line, no hard wraps**,
and rely on the reader's browser to wrap it. Only insert a newline when
you actually want a paragraph break or a new list item. This rule applies
to the release notes drafted here, the PR body in Step 7b, and the
`--notes` body in Step 8 — they all use GFM-with-breaks rendering. It does
**not** apply to `CHANGELOG.md`, which renders as standard CommonMark
where soft breaks collapse to spaces; CHANGELOG entries can stay
one-long-line too, but 80-col wrap there is harmless.

## Step 3: Update Version References

1. `version.json` — update version string.
2. `site/index.html` — replace old version in download button, terminal example, and footer (use replace_all).
3. `platforms/macos/Irrlicht/Resources/Info.plist` — update `CFBundleShortVersionString` and `CFBundleVersion`.

## Step 4: Update Docs

### 4a. CHANGELOG (mandatory)

**`CHANGELOG.md` (repo root) — REQUIRED every release.** Add a new
`## [$NEW_VERSION] — YYYY-MM-DD` section at the top (directly under
`## [Unreleased]`), using the Keep a Changelog categories already in the
file: **Added**, **Changed**, **Fixed**, **Docs**, **Distribution**, etc.
Reuse the release notes drafted in Step 2 — don't paraphrase them into
something different. Also add the new version to the reference-link
section at the bottom of the file
(`[X.Y.Z]: https://github.com/ingo-eichhorst/Irrlicht/releases/tag/vX.Y.Z`)
and update the `[Unreleased]` compare link to point at `vX.Y.Z...HEAD`.
This step is mandatory — never ship a release without updating
`CHANGELOG.md`.

Then mirror the same categorized entries into `site/docs/changelog.html`,
adding a new version block at the top (before the previous version entry).

### 4b. Doc + README sweep (mandatory)

Walk **every** docs page and every top-level README, check each against the
release's diff, and update any content that this release made stale. Don't
rely on a hardcoded list — enumerate the targets dynamically so new pages
can't be silently missed:

```bash
# Files in scope for the sweep — top-level site/*.html (the landing page
# carries its own compatibility grid, separate from site/docs/), the docs
# pages, and every top-level README.
ls site/*.html site/docs/*.html
echo README.md AGENTS.md CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md

# What changed in this release — drives which pages are likely affected
git diff --name-only $(git describe --tags --abbrev=0)..HEAD
```

For each file in scope, read it and ask: *does this release's diff
invalidate anything written here?* Common triggers, with the doc page that
typically owns the surface:

| Diff touches… | Likely-affected docs |
|---|---|
| `core/adapters/inbound/agents/**` | `site/docs/adapters.html`, README compatibility grid, `AGENTS.md` "Adding a new agent adapter" section |
| `core/ports/**`, `core/domain/**` | `site/docs/architecture.html`, `site/docs/adapters.html`, `AGENTS.md` if the adapter contract shape changes |
| `core/cmd/irrlichd/main.go` slice/wiring rename (e.g. `agentCfgs` → `allAgents`) | `site/docs/api-reference.html` (the `GET /api/v1/agents` blurb references the slice name), `site/docs/contributing.html` adapter-PR checklist, `AGENTS.md` |
| `core/cmd/irrlichd/handlers.go` (HTTP routes / payloads) | `site/docs/api-reference.html` |
| `core/cmd/irrlicht-ls`, `core/cmd/irrlicht-focus` | `site/docs/cli-tools.html` |
| `core/application/services/**`, `core/domain/session/**` | `site/docs/state-machine.html`, `site/docs/session-detection.html` |
| `site/install.sh`, `tools/homebrew-tap/**`, install flow | `site/docs/installation.html`, `site/docs/quickstart.html`, `README.md` install section |
| Config schema / settings files | `site/docs/configuration.html` |
| New agent adapter shipped | README compatibility grid + `site/index.html` "Supported Agents & Platforms" grid (search for `tag-planned` / `tag-alpha`) + `site/docs/adapters.html` per-adapter row |
| Adapter maturity-stage change | Same three places as "new adapter shipped" — README, landing page grid, adapters doc |
| New platform shipped | README + `site/index.html` "Supported Agents & Platforms" grid (Platforms column) + `site/docs/index.html` overview |

Update only where content is actually outdated — do **not** paraphrase
correct content for the sake of touching the file. If a doc is fine, leave
it untouched. When in doubt, prefer to update over leaving stale: a
half-true doc is worse than a slightly chatty one.

When this sweep finishes, every line you read should either be (a) still
true on `main` after the release, or (b) edited to be true.

## Step 5: Run Tests

```bash
cd /Users/ingo/projects/irrlicht/core && go test ./... -count=1
```

All tests must pass before proceeding.

## Step 6: Build Artifacts

### Go daemon (universal binary + tarball)
The daemon reads `platforms/web/index.html` from disk at runtime; no embed.
The standalone curl `--daemon-only` install ships a tarball containing both
the binary and `web/index.html`.

```bash
cd /Users/ingo/projects/irrlicht/core
GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w -X main.Version=$NEW_VERSION" -o /tmp/irrlichd-arm64 ./cmd/irrlichd
GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w -X main.Version=$NEW_VERSION" -o /tmp/irrlichd-amd64 ./cmd/irrlichd
lipo -create /tmp/irrlichd-arm64 /tmp/irrlichd-amd64 -output /tmp/irrlichd-darwin-universal

# Tarball for the curl --daemon-only installer
rm -rf /tmp/irrlichd-tarball && mkdir -p /tmp/irrlichd-tarball/web
cp /tmp/irrlichd-darwin-universal /tmp/irrlichd-tarball/irrlichd
cp /Users/ingo/projects/irrlicht/platforms/web/index.html /tmp/irrlichd-tarball/web/index.html
tar -czf /tmp/irrlichd-darwin-universal.tar.gz -C /tmp/irrlichd-tarball .
```

### Swift app (release build — universal)
**MUST pass both arches explicitly.** A plain `swift build -c release` only
builds the host arch (arm64 on Apple Silicon) and leaves
`.build/apple/Products/Release/Irrlicht` untouched if Xcode last built it — a
stale Xcode universal binary from a previous session will silently get shipped
instead of current code. This shipped a 10-day-old Swift app in v0.3.4.

```bash
cd /Users/ingo/projects/irrlicht/platforms/macos && \
  swift build -c release --arch arm64 --arch x86_64
```

The universal binary lands at `.build/apple/Products/Release/Irrlicht`.

**Verify it's fresh before bundling:**
```bash
SWIFT_BIN=/Users/ingo/projects/irrlicht/platforms/macos/.build/apple/Products/Release/Irrlicht
# Must be universal
file "$SWIFT_BIN" | grep -q 'universal binary with 2 architectures' || { echo "NOT universal"; exit 1; }
# Must be newer than the newest tracked Swift source
NEWEST_SRC=$(find /Users/ingo/projects/irrlicht/platforms/macos/Irrlicht -name '*.swift' -print0 | xargs -0 stat -f '%m %N' | sort -n | tail -1 | awk '{print $2}')
[ "$SWIFT_BIN" -nt "$NEWEST_SRC" ] || { echo "STALE Swift binary"; exit 1; }
```

**Audit the linked frameworks (load-bearing — this is what caught
v0.4.3 too late).** Ad-hoc-signed builds must not statically link any
framework whose APIs trigger a TCC preflight at process startup. The
shipped v0.4.3 binary linked `Intents.framework` via `import Intents`
in `FocusMonitor.swift`; TCC preflighted `kTCCServiceListenEvent` before
any of our code ran and SIGABRT'd every install (#358).

Each forbidden framework must either be (a) gated at the source level
via a Developer-ID runtime detector + `NSClassFromString` dispatch (see
`FocusMonitor.swift` post-#358 for the pattern), or (b) deferred until
Developer ID lands (#233 / #357). The audit below fails the release
on any unauthorized link:

```bash
# Each entry is "framework-name | reason | fix-pointer".
# Add more here as we discover them. When DevID is in (#233) the
# entitlement-bearing entries can be removed from this list.
FORBIDDEN_FRAMEWORKS=(
  "Intents.framework|preflights kTCCServiceListenEvent at startup; needs com.apple.developer.focus-status (DevID-gated)|FocusMonitor.swift uses NSClassFromString dispatch since #358 — keep that pattern"
)

violations=0
for entry in "${FORBIDDEN_FRAMEWORKS[@]}"; do
  fw="${entry%%|*}"
  reason="$(echo "$entry" | cut -d'|' -f2)"
  fix="$(echo "$entry" | cut -d'|' -f3)"
  if otool -L "$SWIFT_BIN" 2>/dev/null | grep -q "$fw"; then
    echo "FAIL: $fw is statically linked into the Swift binary."
    echo "      reason: $reason"
    echo "      fix:    $fix"
    violations=$((violations + 1))
  fi
done
if [ $violations -gt 0 ]; then
  echo ""
  echo "Aborting release. Resolve the framework link at the source level"
  echo "(not by hand-patching the bundle) and rebuild."
  exit 1
fi
echo "OK no forbidden frameworks linked"
```

### App bundle

**Path discipline — do not assemble under `/tmp/`.** TCC's
responsibility-tracking treats `/tmp/`-rooted bundles as untrustworthy
for privacy-gated APIs (Focus status, in particular); the smoke test
will crash silently with no useful diagnostic — see #352, surfaced
during the v0.4.3 release. Assemble under `.build/release/` instead,
which lives under `$HOME` and is gitignored:

```bash
APP_STAGING=/Users/ingo/projects/irrlicht/.build/release/Irrlicht.app
rm -rf "$APP_STAGING"
mkdir -p "$APP_STAGING/Contents/MacOS" "$APP_STAGING/Contents/Resources"
```

All `$APP_STAGING` references below refer to this path. The final DMG,
PKG, and ZIP land in `/tmp/` as before — only the *assembly* path moves.

1. Copy Swift binary → `$APP_STAGING/Contents/MacOS/Irrlicht` (from path above).
2. Copy universal daemon → `$APP_STAGING/Contents/MacOS/irrlichd`.
3. Copy `AppIcon.icns` → `$APP_STAGING/Contents/Resources/AppIcon.icns`.
4. **Copy the SwiftPM resource bundle** `Irrlicht_Irrlicht.bundle` →
   `$APP_STAGING/Contents/Resources/Irrlicht_Irrlicht.bundle`. The Swift code uses
   `Bundle.module.url(...)` which aborts during its own initialization if
   the bundle isn't present — the `?? Bundle.main...` fallback never runs.
   Missing this bundle shipped a broken v0.3.4 that crashed at launch
   (`EXC_BREAKPOINT` in `resource_bundle_accessor.swift`).
   ```bash
   cp -R /Users/ingo/projects/irrlicht/platforms/macos/.build/apple/Products/Release/Irrlicht_Irrlicht.bundle \
         "$APP_STAGING/Contents/Resources/Irrlicht_Irrlicht.bundle"
   ```
5. **Copy the dashboard UI** → `$APP_STAGING/Contents/Resources/web/index.html`.
   The daemon resolves it at runtime via `<exe>/../Resources/web/`
   (`resolveUIDir` in `core/cmd/irrlichd/main.go`). Without this copy,
   `GET /` returns the 503 "Dashboard UI not found" fallback — every
   v0.4.4 install shipped without this file and the dashboard at
   `http://127.0.0.1:7837/` was unreachable until v0.4.5 re-spun the assets.
   The smoke test at step 8 asserts the dashboard responds; do not skip it.
   ```bash
   mkdir -p "$APP_STAGING/Contents/Resources/web"
   cp /Users/ingo/projects/irrlicht/platforms/web/index.html \
      "$APP_STAGING/Contents/Resources/web/index.html"
   ```
6. **Write a resolved `Info.plist`** to `$APP_STAGING/Contents/Info.plist`.
   This is a hand-written file, *not* a copy of `platforms/macos/Irrlicht/Resources/Info.plist`
   (which contains unresolved Xcode variables like `$(PRODUCT_NAME)`). Use
   the full template below verbatim, substituting only `$NEW_VERSION` and
   the build number.

   **Coupling rule (counterintuitive — read this before changing the
   template):** the relationship between `Irrlicht.entitlements`, the
   Info.plist `NS*UsageDescription` keys, and the linked frameworks is
   *inverted* for ad-hoc-signed builds.

   - **Apple-restricted entitlements** (e.g. `com.apple.developer.focus-status`)
     cannot be claimed by an ad-hoc-signed binary — AMFI rejects the
     bundle at launch with `launchd POSIX 153` / "Launchd job spawn
     failed" (v0.4.3 crash, fixed in #356).
   - **`NS*UsageDescription` keys for those entitlements** can still
     trigger a TCC SIGABRT if the matching framework is *statically
     linked* — TCC preflights `kTCCServiceListenEvent` (and similar)
     at process startup whenever it sees the framework, regardless of
     whether any API is actually called. **`NSFocusStatusUsageDescription`
     in particular crashes ad-hoc-signed builds that link
     `Intents.framework`** (v0.4.3 crash, fixed in #358).
   - **The fix is structural, not declarative.** Source code must not
     statically link those frameworks on ad-hoc builds. `FocusMonitor.swift`
     uses a Developer-ID-signature runtime gate + `NSClassFromString`
     dispatch for exactly this reason (#357 tracks the eventual
     restoration of the static path once Developer ID lands).

   For the Info.plist template below, the practical consequences are:

   | Key | Include for ad-hoc? | Include once DevID lands (#233 / #357)? |
   |---|---|---|
   | `NSAppleEventsUsageDescription` | Yes | Yes |
   | `NSFocusStatusUsageDescription` | **No** — even though `FocusMonitor.swift` exists in source, the runtime gate keeps Intents.framework unloaded. Adding the key with no framework reference is harmless; adding it once the framework is linked is a SIGABRT. | Yes (alongside the entitlement re-claim). |
   | Anything new | Audit the source. If the relevant Swift code uses `import <Framework>` directly, the binary will link the framework, and TCC may preflight at startup. Either gate the source (FocusMonitor pattern) or skip the usage description until DevID. |

   ```xml
   <?xml version="1.0" encoding="UTF-8"?>
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0">
   <dict>
       <key>CFBundleDevelopmentRegion</key>
       <string>en</string>
       <key>CFBundleExecutable</key>
       <string>Irrlicht</string>
       <key>CFBundleIconFile</key>
       <string>AppIcon</string>
       <key>CFBundleIdentifier</key>
       <string>io.irrlicht.app</string>
       <key>CFBundleInfoDictionaryVersion</key>
       <string>6.0</string>
       <key>CFBundleName</key>
       <string>Irrlicht</string>
       <key>CFBundlePackageType</key>
       <string>APPL</string>
       <key>CFBundleShortVersionString</key>
       <string>$NEW_VERSION</string>
       <key>CFBundleVersion</key>
       <string>$BUILD_NUMBER</string>
       <key>LSApplicationCategoryType</key>
       <string>public.app-category.developer-tools</string>
       <key>LSMinimumSystemVersion</key>
       <string>13.0</string>
       <key>LSUIElement</key>
       <true/>
       <key>NSAppleEventsUsageDescription</key>
       <string>Irrlicht needs to send Apple Events to focus terminal and IDE windows when you click a session in the menu bar.</string>
       <key>NSHumanReadableCopyright</key>
       <string>Copyright © 2026 Ingo Eichhorst. MIT License.</string>
       <key>NSPrincipalClass</key>
       <string>NSApplication</string>
   </dict>
   </plist>
   ```

7. Ad-hoc code sign. **Do not pass `--entitlements`.** AMFI (Apple Mobile
   File Integrity) rejects ad-hoc-signed binaries that claim Apple-restricted
   entitlements; `com.apple.developer.focus-status` (in
   `platforms/macos/Irrlicht/Resources/Irrlicht.entitlements`) is one such
   entitlement. Applying the entitlements file at sign time bakes that claim
   into the binary, which `amfid` then refuses to launch — surfacing as
   `launchd POSIX 153` / `Launchd job spawn failed` on first launch (#356).
   The `Irrlicht.entitlements` file is preserved in the repo for the future
   Developer-ID-signed + notarized path (separate work, tracked under #233);
   until that lands, it must not be applied. When DevID arrives, restore the
   `--entitlements` flag *and* re-add `NSFocusStatusUsageDescription` to the
   Info.plist template above *and* restore the static `import Intents` /
   direct API usage in `FocusMonitor.swift` (#357 tracks the full restoration
   checklist — there are three coupled touchpoints that all flip together).
   ```bash
   codesign --force --deep --sign - "$APP_STAGING/Contents/MacOS/irrlichd"
   codesign --force --deep --sign - "$APP_STAGING"
   codesign --verify --deep --strict "$APP_STAGING"
   # Sanity check — must be empty (matches v0.4.2 working behavior).
   codesign -d --entitlements - "$APP_STAGING" 2>&1 | grep -q '\[Key\]' \
     && { echo "FAIL: entitlements baked into ad-hoc binary"; exit 1; } \
     || echo "OK no entitlements (AMFI won't reject)"
   ```
8. **Smoke test before packaging** — launch the built app, wait ~2s, confirm
   the process is still alive, has spawned `irrlichd`, and that the daemon
   serves the dashboard at `127.0.0.1:7837/`.

   **Do not ship through a smoke-test failure.** v0.4.3 shipped broken
   because the smoke test failed locally and the failure was dismissed
   as "/tmp/ TCC weirdness, end users will be fine." End users were not
   fine; every install hit `launchd POSIX 153`. If the smoke test fails
   and you can't explain why, the release is broken. **Period.** The
   diagnostic toolkit below is for finding the root cause, not for
   building a case to ship anyway.

   Reset any poisoned TCC state for the bundle before the test — Sequoia
   caches "this bundle id == no permission" decisions across runs, and
   stale entries from earlier failed builds make a perfectly-shipping
   bundle appear to crash. Resetting is safe (TCC will re-prompt on
   first use post-install).

   ```bash
   tccutil reset All io.irrlicht.app 2>/dev/null || true

   SMOKE_START=$(date +%s)
   "$APP_STAGING/Contents/MacOS/Irrlicht" > /tmp/app.log 2>&1 & APP_PID=$!
   sleep 2
   if ! pgrep -f "$APP_STAGING/Contents/MacOS/Irrlicht" >/dev/null; then
     echo "FAIL: app exited within 2s — RELEASE IS BROKEN, DO NOT SHIP"
     tail -20 /tmp/app.log
     # Tail the most recent crash report for the TCC `details` field — the
     # only place a privacy-violation reason actually shows up.
     LATEST_CRASH=$(ls -t ~/Library/Logs/DiagnosticReports/Irrlicht*.ips 2>/dev/null | head -1)
     if [ -n "$LATEST_CRASH" ] && [ "$(stat -f %m "$LATEST_CRASH")" -ge "$SMOKE_START" ]; then
       echo "=== TCC details from $LATEST_CRASH ==="
       grep -o '"details":\[[^]]*\]' "$LATEST_CRASH" | head -1
       # The faulting-thread frames pinpoint the offending framework
       # (look for SLSMainConnection / NSWorkspaceNotificationCenter /
       # INFocusStatusCenter / similar) — that's the lead for the
       # source-level fix.
     fi
     exit 1
   fi
   pgrep -f "$APP_STAGING/Contents/MacOS/irrlichd" >/dev/null || { echo "FAIL: daemon not spawned"; }

   # Dashboard reachability — catches missing Resources/web/index.html
   # (v0.4.4 shipping defect). The grep on `<title>` is a stable marker in
   # platforms/web/index.html and distinguishes the real dashboard from
   # the 503 plain-text "Dashboard UI not found" body.
   DASH_OK=0
   for i in 1 2 3 4 5; do
     if curl -fsS http://127.0.0.1:7837/ 2>/dev/null | grep -q '<title>'; then
       DASH_OK=1; break
     fi
     sleep 1
   done
   if [ "$DASH_OK" -ne 1 ]; then
     echo "FAIL: dashboard not served at 127.0.0.1:7837 — Resources/web/index.html missing? RELEASE IS BROKEN, DO NOT SHIP"
     curl -sS -o /dev/null -w "HTTP %{http_code}\n" http://127.0.0.1:7837/
     curl -sS http://127.0.0.1:7837/ | head -3
     pkill -f "$APP_STAGING" 2>/dev/null
     exit 1
   fi
   echo "OK dashboard served at 127.0.0.1:7837/"

   pkill -f "$APP_STAGING" 2>/dev/null; sleep 0.3
   ```

   **If the smoke test fails, debugging checklist** (in order — each
   has caught a real shipping bug):
   1. Diff `otool -L "$SWIFT_BIN"` against the prior release's binary
      (`otool -L /Applications/Irrlicht.app/Contents/MacOS/Irrlicht`).
      A new framework dependency is the most common cause of TCC-class
      crashes — every new framework potentially adds a startup preflight.
   2. Read the latest crash report's triggering thread frames. The
      symbol just before `__TCC_CRASHING_DUE_TO_PRIVACY_VIOLATION__` is
      the API or framework the preflight checked.
   3. Compare `codesign -d --entitlements -` output against the prior
      release. New entitlement entries on an ad-hoc binary are killed
      by AMFI with POSIX 153 (v0.4.3 mode, fixed in #356; the entitlement
      audit in step 7 should have caught it).
   4. If steps 1–3 all clear, copy the bundle to `/Applications/` (kill
      the prior install first) and retry. A real shipping defect will
      crash from both paths; an environment-only failure will only crash
      from one. Even then: investigate, don't ship through.

### Branded DMG
1. Create a writable DMG with `hdiutil create -size 50m -fs HFS+ -volname "Irrlicht-Install"`.
2. Mount it read-write.
3. Copy `Irrlicht.app` (from `$APP_STAGING`), create `Applications` symlink, create `.background/` dir with `background.tiff`.
4. The background image is at `site/assets/dmg-background.tiff`. If missing, generate it programmatically (dark theme, purple glow, dot grid, arrow, "Irrlicht" title, "Drag to Applications" subtitle, version footer).
5. Apply Finder layout via AppleScript:
   - Icon view, icon size 80, no toolbar/statusbar
   - Window bounds: `{200, 200, 860, 600}`
   - Background picture: `.background:background.tiff`
   - `Irrlicht.app` position: `{170, 190}`
   - `Applications` position: `{490, 190}`
6. Detach, convert to compressed UDZO → `/tmp/Irrlicht-$NEW_VERSION.dmg`.

### PKG installer
```bash
pkgbuild --root "$APP_STAGING" --identifier io.irrlicht.app --version $NEW_VERSION \
  --install-location /Applications/Irrlicht.app /tmp/Irrlicht-$NEW_VERSION-mac-installer.pkg
```

### ZIP archive (for curl installer)
Used by `https://irrlicht.io/install.sh`. Must be created with `ditto` so
macOS metadata (including the ad-hoc code signature) is preserved.

```bash
ditto -c -k --sequesterRsrc --keepParent "$APP_STAGING" /tmp/Irrlicht-$NEW_VERSION.zip
```

### Checksums

Include the daemon-only tarball alongside the zip — `site/install.sh`
verifies it on the curl `--daemon-only` path. Omitting it ships a
release where the standalone daemon installer fails the integrity check.

```bash
cd /tmp && shasum -a 256 \
  irrlichd-darwin-universal \
  irrlichd-darwin-universal.tar.gz \
  Irrlicht-$NEW_VERSION.dmg \
  Irrlicht-$NEW_VERSION-mac-installer.pkg \
  Irrlicht-$NEW_VERSION.zip \
  > checksums.sha256
```

## Step 6.5: Update Homebrew Cask (in-repo only)

Bump the in-repo cask template `tools/homebrew-tap/Casks/irrlicht.rb` to
`$NEW_VERSION` with the sha256 of the freshly built DMG. **Do not** touch
the sibling tap repo yet — Step 8.5 owns that, and committing in the tap
now would create a local tap commit that the script's "nothing to commit"
guard then refuses to push.

`update-cask.sh` auto-discovers a sibling `../homebrew-irrlicht` clone and
commits there unconditionally, so don't run it here. Patch the in-repo
file directly instead:

```bash
DMG_SHA=$(shasum -a 256 "/tmp/Irrlicht-$NEW_VERSION.dmg" | awk '{print $1}')
CASK=/Users/ingo/projects/irrlicht/tools/homebrew-tap/Casks/irrlicht.rb
sed -i '' -E "s/^  version \".*\"/  version \"$NEW_VERSION\"/" "$CASK"
sed -i '' -E "s/^  sha256 \".*\"/  sha256 \"$DMG_SHA\"/" "$CASK"
grep -E '^  (version|sha256) ' "$CASK"   # sanity check
```

The bumped template gets committed alongside the release in Step 7;
external publish happens in Step 8.5 after the GitHub release exists.

## Step 7: Commit, PR, Merge, Tag

`main` is protected by a "Changes must be made through a pull request"
repo rule — a direct `git push origin main` is rejected with `GH013`.
Every release commit goes through a short-lived `release/v$NEW_VERSION`
branch + squash-merged PR, then the tag is pushed to the *merged* commit
on `main`.

### 7a. Stage and commit on a release branch

```bash
# Core release artefacts plus any top-level README/doc files the Step 4b
# sweep edited. The `git add -- ...` form is safe for missing files.
git add version.json CHANGELOG.md site/ \
        platforms/macos/Irrlicht/Resources/Info.plist \
        tools/homebrew-tap/Casks/irrlicht.rb
git add -- README.md AGENTS.md CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md 2>/dev/null || true

# Confirm nothing the sweep edited is left unstaged before committing.
git status --short

git checkout -b "release/v$NEW_VERSION"
git commit -m "chore: release v$NEW_VERSION"
git push -u origin "release/v$NEW_VERSION"
```

### 7b. Open the release PR

Use the release notes drafted in Step 2 as the PR body so the same prose
ships in three places (PR, CHANGELOG, GitHub release).

```bash
gh pr create --title "chore: release v$NEW_VERSION" \
  --body-file /tmp/release-notes-v$NEW_VERSION.md

# Wait for mergeability if needed:
gh pr view --json mergeable,mergeStateStatus \
  --jq '"mergeable=\(.mergeable) state=\(.mergeStateStatus)"'
```

### 7b-guard. Race check — has `origin/main` moved since Step 1?

**Load-bearing — do not skip.** If a maintainer squash-merged another
PR into `main` while you were building artifacts (~5 min window for
Swift + DMG), the squash-merge in Step 7b will silently land your
release commit on top of that PR. The v$NEW_VERSION tag will then
include code you never built artifacts for, never tested in this run,
and never mentioned in the release notes. v0.4.5 shipped through this
exact race and required an amend PR + force-pushed tag to recover.

```bash
if [ ! -s /tmp/irrlicht-release-base.sha ]; then
  echo "FAIL: /tmp/irrlicht-release-base.sha missing or empty."
  echo "Step 1.4 didn't run, or /tmp was cleared between sessions."
  echo "Re-run Step 1.4 to capture BASE_SHA before merging."
  exit 1
fi
BASE_SHA=$(cat /tmp/irrlicht-release-base.sha)
git fetch origin main
CURRENT_MAIN=$(git rev-parse origin/main)
if [ "$BASE_SHA" != "$CURRENT_MAIN" ]; then
  echo "RACE DETECTED: origin/main moved during release window."
  echo "  base at Step 1:  $BASE_SHA"
  echo "  current main:    $CURRENT_MAIN"
  echo ""
  echo "Commits that landed mid-release:"
  git log --oneline "$BASE_SHA..$CURRENT_MAIN"
  echo ""
  echo "DO NOT MERGE THE RELEASE PR. Recovery:"
  echo "  1. Rebase release/v$NEW_VERSION onto origin/main:"
  echo "       git checkout release/v$NEW_VERSION && git rebase origin/main"
  echo "  2. Add the new commit(s) above to CHANGELOG.md + site/docs/changelog.html."
  echo "  3. Re-run Step 5 (tests) + Step 6 (build artifacts) on the new base."
  echo "  4. Re-run Step 6.5 (bump cask sha to the new DMG)."
  echo "  5. Force-push the rebased branch:"
  echo "       git push -f origin release/v$NEW_VERSION"
  echo "     The existing PR will update — do NOT re-run gh pr create."
  echo "  6. Update BASE_SHA, then re-run this guard:"
  echo "       git rev-parse origin/main > /tmp/irrlicht-release-base.sha"
  echo "     and proceed to Step 7b-merge once it reports unchanged."
  exit 1
fi
echo "OK release base unchanged; safe to merge"
```

If the race is detected, follow the recovery above instead of patching
post-merge. A force-rebase before merge is much cleaner than an
amend-PR-plus-force-tag-move after the fact (v0.4.5's recovery path).

### 7b-merge. Squash-merge the release PR

Squash-merge so `main` gets exactly one commit titled
`chore: release v$NEW_VERSION (#N)`.

```bash
gh pr merge --squash
```

Do **not** pass `--delete-branch`. The release branch is intentionally
kept after merge so the pre-squash commit and its CI history remain
addressable (e.g. for forensic comparison against the squashed commit
if a regression surfaces).

### 7c. Realign local `main` and tag the merged commit

The squash creates a new commit SHA on `origin/main`, so a plain
`git pull` reports diverged branches. Hard-reset local `main` to the
remote — your local release commit (the pre-squash one on the deleted
branch) is now redundant.

```bash
git checkout main
git fetch origin main
git reset --hard origin/main

# v$NEW_VERSION must point at the squashed commit, not the local one.
# Drop any local tag from before the squash, then re-tag.
git tag -d "v$NEW_VERSION" 2>/dev/null || true
git tag "v$NEW_VERSION"
git push origin "v$NEW_VERSION"
```

## Step 8: Create GitHub Release

**Before posting**: the release notes you pass to `gh release` render with
GitHub's GFM "breaks" extension, so any soft line break becomes `<br>` on
the release page. Confirm the body you're about to ship has each paragraph
and each bullet on **one long line, no hard wraps at 80 columns** (see the
line-wrap rule in Step 2). Write the body to a tempfile and pass it via
`--notes-file`, not `--notes`, so the wrap discipline is reviewable and
re-runnable; a single shell heredoc with a long-line paragraph survives
better in a file than inline-escaped.

```bash
gh release create v$NEW_VERSION \
  /tmp/irrlichd-darwin-universal.tar.gz \
  /tmp/Irrlicht-$NEW_VERSION.dmg \
  /tmp/Irrlicht-$NEW_VERSION-mac-installer.pkg \
  /tmp/Irrlicht-$NEW_VERSION.zip \
  /tmp/checksums.sha256 \
  --title "v$NEW_VERSION" \
  --notes-file /tmp/release-notes-v$NEW_VERSION.md
```

If the body was hand-wrapped by mistake, fix it without re-shipping
artifacts: rewrite `/tmp/release-notes-v$NEW_VERSION.md` with long-line
paragraphs and run `gh release edit v$NEW_VERSION --notes-file ...` to
update only the body.

## Step 8.5: Publish Cask to External Tap

Push the bumped cask to `ingo-eichhorst/homebrew-irrlicht` so
`brew install --cask irrlicht` resolves to the new version. The script
auto-discovers a sibling `../homebrew-irrlicht` clone, or honors
`IRRLICHT_TAP_DIR` if set explicitly.

The `||` fallback below is load-bearing: the script uses `set -e` and exits
non-zero on tap failures (offline, auth, rebase conflict). The release
itself is already on GitHub at this point, so we explicitly **do not**
propagate that failure — log a warning and move on. The cask can be
republished later by re-running the script; version + sha256 are already
pinned in the in-repo template from Step 6.5.

```bash
tools/homebrew-tap/update-cask.sh --version "$NEW_VERSION" --push \
  || echo "WARNING: cask publish failed — re-run later. GitHub release is unaffected."
```

The script prints `tap repo already at $NEW_VERSION — nothing to commit`
when a previous run already created the local commit (e.g. a retried
release). In that state it exits 0 *without* pushing — so push the tap
defensively from whichever clone the script discovered, before verifying:

```bash
TAP_DIR="${IRRLICHT_TAP_DIR:-$(cd .. && pwd)/homebrew-irrlicht}"
if [ -d "$TAP_DIR/.git" ]; then
  git -C "$TAP_DIR" push origin main 2>&1 | grep -vE '^Everything up-to-date$' || true
fi
```

Then verify the published tap actually advanced — silent skips here
previously stranded the tap four versions behind:

```bash
PUBLISHED=$(curl -fsSL "https://raw.githubusercontent.com/ingo-eichhorst/homebrew-irrlicht/main/Casks/irrlicht.rb" \
  | awk -F\" '/^  version /{print $2; exit}')
if [ "$PUBLISHED" = "$NEW_VERSION" ]; then
    echo "tap publishes $PUBLISHED ✓"
else
    echo "WARNING: tap still at $PUBLISHED (expected $NEW_VERSION) — re-run update-cask.sh --push, or push the tap clone directly"
fi
```

If the tap repo doesn't exist yet (first release), the publish step exits 0
without `--push`; the verification will report a mismatch you can ignore.

## Step 9: Verify — including a real end-to-end install canary

1. Confirm release URL is returned.
2. Run `gh release view v$NEW_VERSION` to verify **all five assets** are attached:
   - `irrlichd-darwin-universal.tar.gz` *(daemon + web/index.html — required by curl --daemon-only)*
   - `Irrlicht-$NEW_VERSION.dmg`
   - `Irrlicht-$NEW_VERSION-mac-installer.pkg`
   - `Irrlicht-$NEW_VERSION.zip` *(required by the curl installer)*
   - `checksums.sha256`
3. **Download an asset and confirm it matches the shipped checksum.**
   The pre-upload checksum file can drift from the actual uploaded bytes
   (interrupted uploads, `--clobber` race, byte-counter bugs in `gh`):
   ```bash
   rm -rf /tmp/verify && mkdir /tmp/verify && cd /tmp/verify
   curl -fsSL -o Irrlicht-${NEW_VERSION}.zip \
     "https://github.com/ingo-eichhorst/Irrlicht/releases/download/v${NEW_VERSION}/Irrlicht-${NEW_VERSION}.zip"
   curl -fsSL "https://github.com/ingo-eichhorst/Irrlicht/releases/download/v${NEW_VERSION}/checksums.sha256" -o shipped.sha256
   ACTUAL=$(shasum -a 256 "Irrlicht-${NEW_VERSION}.zip" | awk '{print $1}')
   EXPECTED=$(awk -v f="Irrlicht-${NEW_VERSION}.zip" '$2==f{print $1}' shipped.sha256)
   [ "$ACTUAL" = "$EXPECTED" ] || { echo "FAIL: shipped zip sha mismatches checksums.sha256"; exit 1; }
   echo "OK shipped zip sha matches checksums.sha256"
   cd /Users/ingo/projects/irrlicht
   ```
4. **End-to-end install canary — load-bearing, do not skip.** The pre-
   packaging smoke test (Step 6 step 7) checks the assembly-path bundle.
   This step checks what every end user actually runs. v0.4.3 passed
   in-process smoke tests but failed every end-user install because
   the failure was misdiagnosed as environment-specific. The canary
   below would have caught it before the release page was visible.

   ```bash
   # Backup current install (probably from the just-finished release if
   # you're on the build machine).
   pkill -f '/Applications/Irrlicht.app' 2>/dev/null; sleep 0.5
   mv /Applications/Irrlicht.app /tmp/Irrlicht-canary-backup.app 2>/dev/null

   # Run the live curl installer against the just-published release.
   # The installer discovers the latest version via the GitHub API.
   curl -fsSL https://irrlicht.io/install.sh | sh
   CANARY_RC=$?
   if [ "$CANARY_RC" -ne 0 ]; then
     echo "FAIL: curl installer exited $CANARY_RC — release is broken"
     # Restore: mv /tmp/Irrlicht-canary-backup.app /Applications/Irrlicht.app
     exit 1
   fi

   # The installer reports "Launching... ✓" even on apps that AMFI/TCC
   # immediately kill. pgrep is the only ground truth.
   sleep 3
   if ! pgrep -fl '/Applications/Irrlicht.app/Contents/MacOS/Irrlicht' >/dev/null; then
     echo "FAIL: app not running 3s after curl install — release is broken"
     LATEST_CRASH=$(ls -t ~/Library/Logs/DiagnosticReports/Irrlicht*.ips 2>/dev/null | head -1)
     if [ -n "$LATEST_CRASH" ]; then
       echo "=== latest crash details ==="
       grep -o '"details":\[[^]]*\]' "$LATEST_CRASH" | head -1
     fi
     exit 1
   fi

   # Confirm the version matches what we just shipped (catches stale
   # GitHub-API cache where /releases/latest hasn't updated yet — wait
   # and re-run if so, don't pretend the release succeeded).
   INSTALLED=$(defaults read /Applications/Irrlicht.app/Contents/Info CFBundleShortVersionString)
   [ "$INSTALLED" = "$NEW_VERSION" ] || { echo "FAIL: canary installed v$INSTALLED, expected v$NEW_VERSION"; exit 1; }

   # Confirm no entitlements baked in (matches the build-time guard in
   # Step 6 step 6, but on the actual shipping artifact this time —
   # paranoia is cheap).
   codesign -d --entitlements - /Applications/Irrlicht.app 2>&1 | grep -q '\[Key\]' \
     && { echo "FAIL: shipping binary has entitlements baked in"; exit 1; }

   echo "OK canary install: v$INSTALLED, no entitlements, running"
   ```
5. **Installer-script staleness check.** `irrlicht.io/install.sh` is
   served by GitHub Pages and lags `main` by a few minutes after a merge:
   ```bash
   curl -fsSL https://irrlicht.io/install.sh -o /tmp/install-check.sh
   diff /tmp/install-check.sh site/install.sh \
     && echo "OK irrlicht.io/install.sh in sync with main" \
     || echo "NOTE: irrlicht.io/install.sh hasn't caught up to main yet — wait for Pages rebuild"
   ```
6. Print summary: version, number of commits included, asset sizes.

## Step 10: Install script maintenance

The install script at `site/install.sh` is version-agnostic — it queries the
GitHub API for the latest version and downloads `Irrlicht-<version>.zip`
(full install) or `irrlichd-darwin-universal.tar.gz` (daemon-only) from the
matching release, then extracts the daemon binary plus `web/index.html`.
**It does not need to be edited on every release.**

However, every release must:
- Upload both `Irrlicht-$NEW_VERSION.zip` and
  `irrlichd-darwin-universal.tar.gz` (done in Step 6 / Step 8).
- Include both hashes in `checksums.sha256` (the installer verifies them).
- Preserve backward compatibility with the script's current download URL
  pattern. If you rename an asset, bump the installer too.

If `site/install.sh` has been changed in this release, it deploys
automatically via GitHub Pages when the release commit lands on `main` —
no extra step. Confirm by diffing live against repo (see Step 9).
