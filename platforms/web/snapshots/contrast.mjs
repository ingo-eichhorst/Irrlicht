// contrast.mjs — print the WCAG contrast ratio of every state hue against the
// surface it is drawn on, in both themes, straight from irrlicht.css.
//
//   node platforms/web/snapshots/contrast.mjs
//
// WHY IT EXISTS. irrlicht.css's light-palette block claims specific measured
// ratios ("--waiting measured ~1.9:1", and #1801's "3.01:1 … 4.69:1"). A
// number typed once into a comment drifts silently away from the value it
// measured the moment someone nudges a hex — AGENTS.md's rule is that a figure
// documenting behaviour names the command that produces it. This is that
// command. The pass/fail assertion itself lives in irrlicht.test.js, so it is
// re-run by CI rather than only reproducible by hand; this script is the
// human-readable view of the same arithmetic.
//
// The comparison is text-on-its-own-wash, not text-on-plain-surface, because
// that is how these hues are actually used: `.summary-question` and friends
// draw the state colour as text on top of the matching 12%-alpha `--*-dim`
// wash over `--surface`.

import { readCss } from './serialize.js'

/** sRGB channel (0-255) to linear light, per WCAG 2.x. */
function toLinear(c) {
  const s = c / 255
  return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
}

/** Relative luminance of a #RRGGBB string. */
export function luminance(hex) {
  const [r, g, b] = parseHex(hex)
  return 0.2126 * toLinear(r) + 0.7152 * toLinear(g) + 0.0722 * toLinear(b)
}

/** WCAG contrast ratio between two #RRGGBB strings. Order-independent. */
export function contrastRatio(a, b) {
  const l1 = luminance(a)
  const l2 = luminance(b)
  const [hi, lo] = l1 >= l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}

/** Composite `hex` at `alpha` over opaque `bg`, both #RRGGBB. */
export function composite(hex, alpha, bg) {
  const f = parseHex(hex)
  const b = parseHex(bg)
  const out = f.map((v, i) => Math.round(alpha * v + (1 - alpha) * b[i]))
  return '#' + out.map(v => v.toString(16).padStart(2, '0')).join('')
}

function parseHex(hex) {
  const h = hex.trim().replace(/^#/, '')
  if (!/^[0-9a-fA-F]{6}$/.test(h)) throw new Error(`not a #RRGGBB colour: ${hex}`)
  return [h.slice(0, 2), h.slice(2, 4), h.slice(4, 6)].map(p => parseInt(p, 16))
}

/**
 * Read one custom property's value out of a CSS block.
 *
 * `blockRe` must match the block the declaration should come from, because
 * every state token is declared more than once — a naive /--error:\s*([^;]+)/
 * over the whole file silently returns the DARK value and would report the
 * light theme as passing when it does not. Throws rather than returning null:
 * a lookup that cannot find its input must be loud, never quietly absent.
 */
export function readToken(css, blockRe, name) {
  const block = css.match(blockRe)
  if (!block) throw new Error(`no CSS block matched ${blockRe}`)
  const decl = block[0].match(new RegExp(`--${name}:\\s*([^;]+);`))
  if (!decl) throw new Error(`no --${name} declaration inside the block matched by ${blockRe}`)
  return decl[1].trim()
}

/** The dark `:root` block — everything up to the first closing brace. */
export const DARK_BLOCK = /:root \{[\s\S]*?\n {4}\}/
/** The `prefers-color-scheme: light` override block. */
export const LIGHT_MEDIA_BLOCK = /@media \(prefers-color-scheme: light\)[\s\S]*?\n {4}\}/
/** The explicit `[data-theme="light"]` override block. */
export const LIGHT_THEME_BLOCK = /:root\[data-theme="light"\] \{[\s\S]*?\n {4}\}/

/**
 * Every state hue's contrast against its own 12%-alpha wash, per theme.
 * Returned rather than printed so irrlicht.test.js can assert on it.
 */
export function stateContrasts(css = readCss()) {
  const rows = []
  const themes = [
    { theme: 'dark', block: DARK_BLOCK, surface: readToken(css, DARK_BLOCK, 'surface') },
    { theme: 'light', block: LIGHT_MEDIA_BLOCK, surface: readToken(css, LIGHT_MEDIA_BLOCK, 'surface') },
  ]
  for (const { theme, block, surface } of themes) {
    for (const state of ['working', 'waiting', 'ready', 'error']) {
      // The light block only re-declares the tokens it overrides, so fall
      // back to the dark declaration for anything it leaves alone — which is
      // exactly how the cascade resolves it in a browser.
      let hex
      try {
        hex = readToken(css, block, state)
      } catch {
        hex = readToken(css, DARK_BLOCK, state)
      }
      const wash = composite(hex, 0.12, surface)
      rows.push({
        theme,
        state,
        hex,
        surface,
        wash,
        vsWash: contrastRatio(hex, wash),
        vsSurface: contrastRatio(hex, surface),
      })
    }
  }
  return rows
}

// Only print when run directly, so importing the helpers costs nothing.
if (process.argv[1] && process.argv[1].endsWith('contrast.mjs')) {
  const pad = (s, n) => String(s).padEnd(n)
  console.log(pad('theme', 7) + pad('state', 9) + pad('hex', 10) + pad('wash', 10) + pad('vs wash', 10) + 'vs surface')
  for (const r of stateContrasts()) {
    const flag = r.vsWash >= 4.5 ? '' : '   <-- FAILS WCAG AA (4.5:1)'
    console.log(
      pad(r.theme, 7) + pad(r.state, 9) + pad(r.hex, 10) + pad(r.wash, 10) +
      pad(r.vsWash.toFixed(2) + ':1', 10) + r.vsSurface.toFixed(2) + ':1' + flag
    )
  }
}
