// Tier 2 (opt-in): render each Tier-1 HTML artifact in headless Chromium and
// screenshot it to a true PNG (issue #757). jsdom can't paint pixels, so this is
// the only path to a real image — but it needs a browser, which `npm test`/CI
// must never pull. So playwright is deliberately NOT a package.json dependency
// (its postinstall would download browser binaries on every CI install); it is
// imported dynamically here and only when you opt in:
//
//   npm run snapshot                       # regenerate out/*.html (Tier 1)
//   npm i -D playwright                     # one-time, local only
//   npx playwright install chromium         # one-time, fetch the browser
//   npm run snapshot:png                    # this script
//
// Reads snapshots/out/*.html, writes snapshots/out/png/*.png.

import { readdirSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const htmlDir = join(here, 'out')
const pngDir = join(htmlDir, 'png')

let chromium
try {
  ({ chromium } = await import('playwright'))
} catch {
  console.error(
    'playwright is not installed (it is intentionally not a dependency).\n' +
    'Install it locally, then retry:\n' +
    '  npm i -D playwright && npx playwright install chromium && npm run snapshot:png',
  )
  process.exit(1)
}

let files
try {
  files = readdirSync(htmlDir).filter((f) => f.endsWith('.html'))
} catch {
  files = []
}
if (files.length === 0) {
  console.error('No HTML artifacts in snapshots/out/. Run `npm run snapshot` first.')
  process.exit(1)
}

mkdirSync(pngDir, { recursive: true })
const browser = await chromium.launch()
const page = await browser.newPage({ viewport: { width: 800, height: 600 }, deviceScaleFactor: 2 })
for (const f of files.sort()) {
  await page.goto(pathToFileURL(join(htmlDir, f)).href)
  const out = join(pngDir, f.replace(/\.html$/, '.png'))
  await page.screenshot({ path: out, fullPage: true })
  console.log('wrote', out)
}
await browser.close()
