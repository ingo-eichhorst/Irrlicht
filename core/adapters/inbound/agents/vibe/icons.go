package vibe

// Mistral's mark: the five-band flame, yellow→red top to bottom. Drawn as
// solid-colored horizontal bars (not a <linearGradient>) because the macOS
// menu-bar renderer (NSImage(data:)) flattens an SVG gradient to a single flat
// color — solid segments keep the brand's warm ramp in the menu bar as well as
// the web dashboard. The two black notches on the top-right reproduce the
// logo's stepped silhouette. The same vivid ramp reads on light and dark
// chrome, so both themes share it.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <rect x="8" y="16" width="84" height="14" fill="#FFD800"/>
  <rect x="8" y="34" width="84" height="14" fill="#FFA300"/>
  <rect x="8" y="52" width="84" height="14" fill="#FF7000"/>
  <rect x="8" y="70" width="84" height="14" fill="#E10500"/>
  <rect x="64" y="16" width="14" height="14" fill="#1A1A1A"/>
  <rect x="78" y="16" width="14" height="32" fill="#1A1A1A"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <rect x="8" y="16" width="84" height="14" fill="#FFD800"/>
  <rect x="8" y="34" width="84" height="14" fill="#FFA300"/>
  <rect x="8" y="52" width="84" height="14" fill="#FF7000"/>
  <rect x="8" y="70" width="84" height="14" fill="#E10500"/>
  <rect x="64" y="16" width="14" height="14" fill="#E0E0E0"/>
  <rect x="78" y="16" width="14" height="32" fill="#E0E0E0"/>
</svg>`
