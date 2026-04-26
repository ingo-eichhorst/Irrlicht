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

## Step 2: Gather Changes

1. Run `git log --oneline $(git describe --tags --abbrev=0)..HEAD --no-merges` to list all commits since the last release.
2. Categorize into: Features, Fixes, Architecture/Refactoring, Docs, Distribution.
3. Draft release notes in the style of previous releases (see `gh release view` for format).

## Step 3: Update Version References

1. `version.json` — update version string.
2. `site/index.html` — replace old version in download button, terminal example, and footer (use replace_all).
3. `platforms/macos/Irrlicht/Resources/Info.plist` — update `CFBundleShortVersionString` and `CFBundleVersion`.

## Step 4: Update Docs

1. **`CHANGELOG.md` (repo root) — REQUIRED every release.** Add a new
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
2. `site/docs/changelog.html` — add new version entry at the top (before the
   previous version entry) with the same categorized changes.
3. Review other docs pages (`api-reference.html`, `session-detection.html`,
   `architecture.html`, `cli-tools.html`) — update if any changes in this
   release affect documented behavior.
4. Only update pages where content is actually outdated.

## Step 5: Run Tests

```bash
cd /Users/ingo/projects/irrlicht/core && go test ./... -count=1
```

All tests must pass before proceeding.

## Step 6: Build Artifacts

### Go daemon (universal binary)
```bash
cd /Users/ingo/projects/irrlicht/core
GOOS=darwin GOARCH=arm64 go build -ldflags "-s -w -X main.Version=$NEW_VERSION" -o /tmp/irrlichd-arm64 ./cmd/irrlichd
GOOS=darwin GOARCH=amd64 go build -ldflags "-s -w -X main.Version=$NEW_VERSION" -o /tmp/irrlichd-amd64 ./cmd/irrlichd
lipo -create /tmp/irrlichd-arm64 /tmp/irrlichd-amd64 -output /tmp/irrlichd-darwin-universal
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

### App bundle
1. Create `/tmp/Irrlicht.app/Contents/{MacOS,Resources}`.
2. Copy Swift binary → `Contents/MacOS/Irrlicht` (from path above).
3. Copy universal daemon → `Contents/MacOS/irrlichd`.
4. Copy `AppIcon.icns` → `Contents/Resources/AppIcon.icns`.
5. **Copy the SwiftPM resource bundle** `Irrlicht_Irrlicht.bundle` →
   `Contents/Resources/Irrlicht_Irrlicht.bundle`. The Swift code uses
   `Bundle.module.url(...)` which aborts during its own initialization if
   the bundle isn't present — the `?? Bundle.main...` fallback never runs.
   Missing this bundle shipped a broken v0.3.4 that crashed at launch
   (`EXC_BREAKPOINT` in `resource_bundle_accessor.swift`).
   ```bash
   cp -R /Users/ingo/projects/irrlicht/platforms/macos/.build/apple/Products/Release/Irrlicht_Irrlicht.bundle \
         /tmp/Irrlicht.app/Contents/Resources/Irrlicht_Irrlicht.bundle
   ```
6. Write a **resolved** `Info.plist` to `Contents/Info.plist` (no Xcode variables — use actual values: `CFBundleExecutable=Irrlicht`, `CFBundleIdentifier=io.irrlicht.app`, `CFBundlePackageType=APPL`, version from `$NEW_VERSION`).
7. Ad-hoc code sign:
   ```bash
   codesign --force --deep --sign - /tmp/Irrlicht.app/Contents/MacOS/irrlichd
   codesign --force --deep --sign - /tmp/Irrlicht.app
   codesign --verify --deep --strict /tmp/Irrlicht.app
   ```
8. **Smoke test before packaging** — launch the built app, wait ~2s, confirm
   the process is still alive and has spawned `irrlichd`. Missing resources
   or codesign issues crash the app silently on launch otherwise.
   ```bash
   /tmp/Irrlicht.app/Contents/MacOS/Irrlicht > /tmp/app.log 2>&1 & APP_PID=$!
   sleep 2
   kill -0 $APP_PID 2>/dev/null || { echo "FAIL: app crashed"; tail -20 /tmp/app.log; exit 1; }
   pgrep -f '/tmp/Irrlicht.app/Contents/MacOS/irrlichd' >/dev/null || { echo "FAIL: daemon not spawned"; }
   pkill -f '/tmp/Irrlicht.app' 2>/dev/null; sleep 0.3
   ```

### Branded DMG
1. Create a writable DMG with `hdiutil create -size 50m -fs HFS+ -volname "Irrlicht-Install"`.
2. Mount it read-write.
3. Copy `Irrlicht.app`, create `Applications` symlink, create `.background/` dir with `background.tiff`.
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
pkgbuild --root /tmp/Irrlicht.app --identifier io.irrlicht.app --version $NEW_VERSION \
  --install-location /Applications/Irrlicht.app /tmp/Irrlicht-$NEW_VERSION-mac-installer.pkg
```

### ZIP archive (for curl installer)
Used by `https://irrlicht.io/install.sh`. Must be created with `ditto` so
macOS metadata (including the ad-hoc code signature) is preserved.

```bash
ditto -c -k --sequesterRsrc --keepParent /tmp/Irrlicht.app /tmp/Irrlicht-$NEW_VERSION.zip
```

### Checksums
```bash
cd /tmp && shasum -a 256 \
  irrlichd-darwin-universal \
  Irrlicht-$NEW_VERSION.dmg \
  Irrlicht-$NEW_VERSION-mac-installer.pkg \
  Irrlicht-$NEW_VERSION.zip \
  > checksums.sha256
```

## Step 6.5: Update Homebrew Cask

Bump the cask in `tools/homebrew-tap/Casks/irrlicht.rb` to `$NEW_VERSION`
with the sha256 of the freshly built DMG. The same script also syncs to the
external tap repo (`ingo-eichhorst/homebrew-irrlicht`) when
`IRRLICHT_TAP_DIR` is set — but skip the `--push` flag here so the local
in-repo template gets committed alongside the release in Step 7 first;
external publish happens in Step 8.5 after the GitHub release exists.

```bash
tools/homebrew-tap/update-cask.sh --version "$NEW_VERSION"
```

Without `IRRLICHT_TAP_DIR` set, this only updates the in-repo template —
fine for first releases before the tap repo exists.

## Step 7: Commit, Tag, Push

```bash
git add version.json CHANGELOG.md site/ platforms/macos/Irrlicht/Resources/Info.plist tools/homebrew-tap/Casks/irrlicht.rb
git commit -m "chore: release v$NEW_VERSION"
git tag v$NEW_VERSION
git push origin main --tags
```

## Step 8: Create GitHub Release

```bash
gh release create v$NEW_VERSION \
  /tmp/irrlichd-darwin-universal \
  /tmp/Irrlicht-$NEW_VERSION.dmg \
  /tmp/Irrlicht-$NEW_VERSION-mac-installer.pkg \
  /tmp/Irrlicht-$NEW_VERSION.zip \
  /tmp/checksums.sha256 \
  --title "v$NEW_VERSION" \
  --notes "<drafted release notes from Step 2>"
```

## Step 8.5: Publish Cask to External Tap

Push the bumped cask to `ingo-eichhorst/homebrew-irrlicht` so
`brew install --cask irrlicht` resolves to the new version. Requires
`IRRLICHT_TAP_DIR` to point at a clone of the tap repo.

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

If the tap repo doesn't exist yet, this step is a no-op (the script exits 0
when `IRRLICHT_TAP_DIR` isn't set).

## Step 9: Verify

1. Confirm release URL is returned.
2. Run `gh release view v$NEW_VERSION` to verify **all five assets** are attached:
   - `irrlichd-darwin-universal`
   - `Irrlicht-$NEW_VERSION.dmg`
   - `Irrlicht-$NEW_VERSION-mac-installer.pkg`
   - `Irrlicht-$NEW_VERSION.zip` *(required by the curl installer)*
   - `checksums.sha256`
3. Smoke-test the curl installer against the new release — it's version-agnostic (discovers the latest via the GitHub API), but the `.zip` asset is the piece that's tied to this release:
   ```bash
   # Download the installer and dry-run the asset fetch
   curl -fsSL https://irrlicht.io/install.sh -o /tmp/install-check.sh
   diff /tmp/install-check.sh site/install.sh || echo "WARNING: irrlicht.io/install.sh lags behind main — wait for GitHub Pages to rebuild"
   # Verify the zip and checksums are downloadable
   curl -fsI "https://github.com/ingo-eichhorst/Irrlicht/releases/download/v${NEW_VERSION}/Irrlicht-${NEW_VERSION}.zip" | head -1
   curl -fsI "https://github.com/ingo-eichhorst/Irrlicht/releases/download/v${NEW_VERSION}/checksums.sha256" | head -1
   ```
4. Print summary: version, number of commits included, asset sizes.

## Step 10: Install script maintenance

The install script at `site/install.sh` is version-agnostic — it queries the
GitHub API for the latest version and downloads `Irrlicht-<version>.zip` /
`irrlichd-darwin-universal` from the matching release. **It does not need to
be edited on every release.**

However, every release must:
- Upload `Irrlicht-$NEW_VERSION.zip` (done in Step 6 / Step 8).
- Include that zip's hash in `checksums.sha256` (the installer verifies it).
- Preserve backward compatibility with the script's current download URL
  pattern. If you rename an asset, bump the installer too.

If `site/install.sh` has been changed in this release, it deploys
automatically via GitHub Pages when the release commit lands on `main` —
no extra step. Confirm by diffing live against repo (see Step 9).
