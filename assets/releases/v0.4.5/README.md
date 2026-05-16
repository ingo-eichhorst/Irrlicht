# v0.4.5 release assets

Per the release-notes template (`.claude/skills/ir:release/release-notes-template.md`),
Highlight screenshots live under `assets/releases/v<version>/` with WebP +
PNG site copies under `site/assets/releases/v<version>/`.

## What's here

| File | Source | Used in |
|---|---|---|
| `quota-forecast.png` | Copied from `assets/subscription_progress.png` | Roadmap (Shipped v0.4.5), would have anchored the v0.4.5 "Quota forecast" Highlight |
| `flame-before-after.png` | Composed by `/tmp/compose-flame-compare.py` — rsvg-convert renders of the old `OffFlameImage.swift` paths + `assets/irrlicht_flame_gradient_purple.svg`, joined into a 1600x900 side-by-side | Roadmap (Shipped v0.4.5), anchors the v0.4.5 brand-refresh story |
| `flame-icon-1024.png` | rsvg-convert of `assets/irrlicht_flame_gradient_purple.svg` at 1024x1024 | Available for marketing / social use |

## What's NOT here (and needs you to capture)

Two screenshots the user picked are macOS-app-only and cannot be produced
from this session. To capture them yourself with the running macOS app:

### `state-icons.png` — Unified state icons (web vs macOS)

What to capture: a single composite image showing the three state icons
(working = heartbeat halo / waiting = pause bars / ready = checkmark) as
they appear in both the macOS menu-bar overlay and the web dashboard, side
by side or stacked.

Steps:
1. Open the macOS overlay with at least one session in each state. The
   easiest way is to launch the app under `IRRLICHT_DEBUG=1` and use the
   replay fixtures to seed example sessions of each state.
2. Screenshot the overlay rows (Cmd-Shift-4, drag).
3. Open `http://127.0.0.1:7837` and screenshot the same three sessions there.
4. Compose the two screenshots side-by-side in Pixelmator / Sketch / Figma.
5. Save as `assets/releases/v0.4.5/state-icons.png` and matching
   `site/assets/releases/v0.4.5/state-icons.{png,webp}`.

### `notification-sound-picker.png` — Per-event sound picker (v0.4.0)

What to capture: the macOS Preferences pane with the three notification-
event rows expanded (ready / waiting / context pressure), each showing
its enable toggle, sound picker, and preview button.

Steps:
1. Launch Irrlicht.app.
2. Open Preferences (menu-bar icon → Preferences, or `,`).
3. Scroll to the Notifications section.
4. Screenshot the three event rows (Cmd-Shift-4, Space to capture the
   window, click).
5. Save as `assets/releases/v0.4.5/notification-sound-picker.png` and
   matching `site/assets/releases/v0.4.5/notification-sound-picker.{png,webp}`.

### Web dashboard parity (full dashboard at ~1200x800)

What to capture: the full web dashboard at `http://127.0.0.1:7837` with
realistic session data — at least one session in each state, ideally
with subagents, task progress dots, and context pressure visible.

This requires the daemon running with active sessions. Easiest path:
1. Let the daemon run while you do normal agent work for an afternoon.
2. Resize the browser to ~1200x800.
3. Cmd-Shift-4 / Space / click on the browser window.
4. Save as `assets/releases/v0.4.5/web-dashboard.png` and matching
   `site/assets/releases/v0.4.5/web-dashboard.{png,webp}`.

## How to add the WebP variants

Once you've captured a PNG, run:

```bash
python3 -c "
from PIL import Image
import sys
src = sys.argv[1]
dst = src.rsplit('.', 1)[0] + '.webp'
Image.open(src).save(dst, 'WEBP', quality=88)
print('wrote', dst)
" site/assets/releases/v0.4.5/state-icons.png
```

The template's Step 4a-img checklist enforces that every Highlight has all
three asset variants (source PNG, site PNG, site WebP) before it's allowed
to remain a Highlight — so producing the WebP is mandatory if you want the
screenshot referenced from a Highlight.
