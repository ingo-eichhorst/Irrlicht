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
7. Ad-hoc code sign. The app entitlements come from
   `platforms/macos/Irrlicht/Resources/Irrlicht.entitlements` (currently
   `get-task-allow` + `com.apple.developer.focus-status` for #338's TTS-under-
   Focus suppression). `--entitlements` is required at sign time; INFocusStatusCenter
   silently reports "unauthorized" without it regardless of what the user grants.
   ```bash
   ENTITLEMENTS="/Users/ingo/projects/irrlicht/platforms/macos/Irrlicht/Resources/Irrlicht.entitlements"
   codesign --force --deep --sign - /tmp/Irrlicht.app/Contents/MacOS/irrlichd
   codesign --force --deep --sign - --entitlements "$ENTITLEMENTS" /tmp/Irrlicht.app
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

### 7b. Open and merge the release PR

Use the release notes drafted in Step 2 as the PR body so the same prose
ships in three places (PR, CHANGELOG, GitHub release). Squash-merge so
`main` gets exactly one commit titled `chore: release v$NEW_VERSION (#N)`.

```bash
gh pr create --title "chore: release v$NEW_VERSION" \
  --body "<drafted release notes from Step 2>"

# Wait for mergeability if needed:
gh pr view --json mergeable,mergeStateStatus \
  --jq '"mergeable=\(.mergeable) state=\(.mergeStateStatus)"'

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

## Step 9: Verify

1. Confirm release URL is returned.
2. Run `gh release view v$NEW_VERSION` to verify **all five assets** are attached:
   - `irrlichd-darwin-universal.tar.gz` *(daemon + web/index.html — required by curl --daemon-only)*
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
