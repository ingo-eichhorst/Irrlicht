package junie

// Junie's mark: JetBrains' green rounded tile carrying the white "J"-hook
// glyph of the Junie logo, drawn as solid shapes (no gradients) because the
// macOS menu-bar renderer (NSImage(data:)) flattens SVG gradients to a single
// flat color — the same constraint vibe's icon documents. The vivid green
// reads on both light and dark chrome, so both themes share the tile and
// differ only in the glyph's contrast color.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <rect x="4" y="4" width="92" height="92" rx="18" fill="#15C46D"/>
  <path d="M62 22v38c0 10-7 16-17 16-9 0-15-5-17-13l11-4c1 4 3 6 6 6 4 0 6-2 6-6V22z" fill="#FFFFFF"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <rect x="4" y="4" width="92" height="92" rx="18" fill="#15C46D"/>
  <path d="M62 22v38c0 10-7 16-17 16-9 0-15-5-17-13l11-4c1 4 3 6 6 6 4 0 6-2 6-6V22z" fill="#0B0B0B"/>
</svg>`
