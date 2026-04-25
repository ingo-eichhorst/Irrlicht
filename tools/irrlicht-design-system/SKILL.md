---
name: irrlicht-design
description: Use this skill to generate well-branded interfaces and assets for Irrlicht, either for production or throwaway prototypes/mocks/etc. Contains essential design guidelines, colors, type, fonts, assets, and UI kit components for prototyping.
user-invocable: true
---

Read the `README.md` file within this skill, and explore the other available files.

If creating visual artifacts (slides, mocks, throwaway prototypes, etc), copy assets out and create static HTML files for the user to view. If working on production code, you can copy assets and read the rules here to become an expert in designing with this brand.

If the user invokes this skill without any other guidance, ask them what they want to build or design, ask some questions, and act as an expert designer who outputs HTML artifacts _or_ production code, depending on the need.

## Quick orientation
- `README.md` — company context, content & visual fundamentals, iconography
- `colors_and_type.css` — all design tokens (CSS vars) — import this first
- `preview/` — small specimen cards for every token cluster
- `ui_kits/dashboard/` — the dense data view (session list, Gas Town, pressure alerts)
- `ui_kits/landing/` — the marketing surface (serif hero, three lights, install pill)
- `assets/` — logos and icon sources

## Non-negotiables
- Three semantic colors only: **working (#8B5CF6 purple)**, **waiting (#FF9500 orange)**, **ready (#34C759 green)**. Never decorative.
- Type pairing: **Cormorant Garamond 300** (display, italic) + **Outfit 300/500** (UI sans) + **DM Mono** (data, labels, logo)
- Background is near-black with a subtle purple-to-black radial and fixed fractal-noise overlay at ~3% opacity
- Lowercase UI prose. Em-dashes over colons. Never exclamation points.
- The logo lock-up is lowercase `irr` + `licht` with the second half in purple
