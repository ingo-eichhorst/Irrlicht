package muse

// Muse's mark: a solid rounded tile carrying a bold, blocky "M" monogram.
// This is an ORIGINAL placeholder shape, not sourced from any Meta brand
// guideline — none was consulted while writing this adapter, so this is not
// an assertion that it matches Muse's own (unseen) app icon. The tile uses
// Meta's public brand blue (#0866FF) as a reasonable placeholder tying the
// mark to its publisher. Drawn as solid shapes only (no gradients), because
// the macOS menu-bar renderer (NSImage(data:)) flattens SVG gradients to a
// single flat color — the same constraint junie's and vibe's icons document.
// Replace with an official mark if/when one becomes available.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <rect x="4" y="4" width="92" height="92" rx="18" fill="#0866FF"/>
  <path d="M22 76 L22 24 L34 24 L50 52 L66 24 L78 24 L78 76 L66 76 L66 46 L54 66 L46 66 L34 46 L34 76 Z" fill="#FFFFFF"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <rect x="4" y="4" width="92" height="92" rx="18" fill="#0866FF"/>
  <path d="M22 76 L22 24 L34 24 L50 52 L66 24 L78 24 L78 76 L66 76 L66 46 L54 66 L46 66 L34 46 L34 76 Z" fill="#0B0B0B"/>
</svg>`
