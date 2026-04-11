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

### Swift app (release build)
```bash
cd /Users/ingo/projects/irrlicht/platforms/macos && swift build -c release
```

### App bundle
1. Create `/tmp/Irrlicht.app/Contents/{MacOS,Resources}`.
2. Copy Swift binary → `Contents/MacOS/Irrlicht`.
3. Copy universal daemon → `Contents/MacOS/irrlichd`.
4. Copy `AppIcon.icns` → `Contents/Resources/AppIcon.icns`.
5. Write a **resolved** `Info.plist` to `Contents/Info.plist` (no Xcode variables — use actual values: `CFBundleExecutable=Irrlicht`, `CFBundleIdentifier=com.anthropic.irrlicht`, `CFBundlePackageType=APPL`, version from `$NEW_VERSION`).
6. Ad-hoc code sign:
   ```bash
   codesign --force --deep --sign - /tmp/Irrlicht.app/Contents/MacOS/irrlichd
   codesign --force --deep --sign - /tmp/Irrlicht.app
   codesign --verify --deep --strict /tmp/Irrlicht.app
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
pkgbuild --root /tmp/Irrlicht.app --identifier com.anthropic.irrlicht --version $NEW_VERSION \
  --install-location /Applications/Irrlicht.app /tmp/Irrlicht-$NEW_VERSION-mac-installer.pkg
```

### Checksums
```bash
cd /tmp && shasum -a 256 irrlichd-darwin-universal Irrlicht-$NEW_VERSION.dmg Irrlicht-$NEW_VERSION-mac-installer.pkg > checksums.sha256
```

## Step 7: Commit, Tag, Push

```bash
git add version.json CHANGELOG.md site/ platforms/macos/Irrlicht/Resources/Info.plist
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
  /tmp/checksums.sha256 \
  --title "v$NEW_VERSION" \
  --notes "<drafted release notes from Step 2>"
```

## Step 9: Verify

1. Confirm release URL is returned.
2. Run `gh release view v$NEW_VERSION` to verify assets are attached.
3. Print summary: version, number of commits included, asset sizes.
