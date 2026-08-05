package copilot

// GitHub Copilot's mark: the visor-like head with two oval eyes. Drawn as a
// single evenodd path so the eyes are knocked out to transparency rather than
// filled with an assumed background color — the icon renders on the web
// dashboard's dark chrome and the macOS menu bar alike, and neither can be
// relied on to match a hardcoded eye fill.
//
// Only the ink color differs between themes: GitHub's near-black on light
// chrome, its near-white on dark.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path fill-rule="evenodd" fill="#1F2328" d="M36 26 h28 a28 28 0 0 1 0 56 H36 a28 28 0 0 1 0-56 Z M29 54 a7 10 0 1 0 14 0 a7 10 0 1 0 -14 0 Z M57 54 a7 10 0 1 0 14 0 a7 10 0 1 0 -14 0 Z"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path fill-rule="evenodd" fill="#E6EDF3" d="M36 26 h28 a28 28 0 0 1 0 56 H36 a28 28 0 0 1 0-56 Z M29 54 a7 10 0 1 0 14 0 a7 10 0 1 0 -14 0 Z M57 54 a7 10 0 1 0 14 0 a7 10 0 1 0 -14 0 Z"/>
</svg>`
