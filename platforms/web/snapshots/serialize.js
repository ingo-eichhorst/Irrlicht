// Serializes the live-rendered #session-list subtree into a single
// self-contained HTML document with irrlicht.css inlined (issue #757). The
// output has no external references — no CDN, no fonts, no scripts — so it is
// safe to view via the Artifact tool's strict CSP or over WebFetch.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const cssPath = join(here, '..', 'irrlicht.css')

/** The dashboard's stylesheet, read from disk so the artifact tracks the real CSS. */
export function readCss() {
  return readFileSync(cssPath, 'utf8')
}

/**
 * Build a self-contained HTML page from the current #session-list DOM.
 * @param {Document} document - the jsdom document after a render
 * @param {{theme?: string, title?: string}} opts
 */
export function serializeSessionList(document, { theme = 'dark', title = 'irrlicht session list' } = {}) {
  const list = document.getElementById('session-list')
  const body = list ? list.outerHTML : '<p>(no #session-list rendered)</p>'
  const themeAttr = theme ? ` data-theme="${theme}"` : ''
  const css = readCss()
  return `<!doctype html>
<html lang="en"${themeAttr}>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${title}</title>
<style>
${css}
/* Snapshot determinism: this is a captured still frame. Complete every
   animation INSTANTLY (duration 0s) rather than disabling it — disabling would
   strand intro animations at their first frame, and .session-row starts at
   opacity:0 and reveals via @keyframes row-reveal, so it would vanish. Kill
   transitions outright. */
*, *::before, *::after {
  animation-duration: 0s !important;
  animation-delay: 0s !important;
  transition: none !important;
}
/* Belt-and-suspenders: pin the row to its revealed state regardless of how the
   reveal keyframe is honored. */
.session-row { opacity: 1 !important; }
html, body { margin: 0; }
body { padding: 16px; }
#session-list { max-width: 760px; }
</style>
</head>
<body>
${body}
</body>
</html>
`
}
