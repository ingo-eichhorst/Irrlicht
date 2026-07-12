# Landing Page UI Kit

Recreation of the `irrlicht.io` marketing page — the poetic, spacious face of the product. Serif wordmark, three glowing lights, install command, feature trio.

## Components

- `SiteHeader.jsx` — wordmark left, minimal nav right (features / install / GitHub)
- `Hero.jsx` — oversized serif wordmark with gradient fill, phonetic subtitle, tagline
- `LightsSignature` (defined inside `Hero.jsx`) — the three glowing orbs (purple/orange/green) with captions
- `InstallBlock.jsx` — terminal pill with `brew install` command and copy button
- `FeatureTrio` (defined inside `InstallBlock.jsx`) — three-column feature cards (menu bar · real-time · zero-config)
- `SiteFooter` (defined inside `SiteHeader.jsx`) — tiny tracked footer with dictionary-style etymology

## Source of truth

Visual cues lifted directly from `site/index.html` (the in-repo `irrlicht.io` marketing page) — dark cosmic background, serif hero, the three lights as hero motif. Keep this kit in sync with `site/index.html` when the marketing page changes.

Open `index.html` to see the full assembled marketing page.
