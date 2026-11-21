// Irrlicht's brand hues, so the panel reads as Irrlicht rather than as Herdr.
//
// Transcribed from tools/irrlicht-design-system/colors_and_type.css, the file
// the macOS app's Theme/Tokens.swift says it is transcribed from too. Each key
// is a CSS custom property's name without its `--`.
// test/palette.parity.test.js reads that CSS file and fails when a value here
// differs from it, or when a key names no token there.
//
// Only hues come from here. The neutrals (the panel's background, text,
// borders, hover, and the context bar's track) stay on Herdr's theme(), so the
// panel follows the person's light or dark Herdr theme the way the Swift app
// follows the system appearance (Tokens.swift:162-170 keeps its neutrals on
// the system colours for the same reason).
export const BRAND = Object.freeze({
  working: '#8B5CF6',
  waiting: '#FF9500',
  ready: '#34C759',
  error: '#FF3B30',
  cancelled: '#8E8E93',
  'pressure-low': '#34C759',
  'pressure-medium': '#FF9500',
  'pressure-high': '#FF3B30',
  'pressure-critical': '#D70015',
  'ws-connected': '#34C759',
  'ws-disconnected': '#FF3B30',
});

// withAlpha is `hex` at `alpha` (0..1) as #rrggbbaa, the colour form gpui
// takes. It accepts #rgb, #rrggbb and #rrggbbaa (an alpha already there is
// replaced). Anything else comes back unchanged, so a theme colour in a shape
// this does not know is still drawn, only without the transparency.
export function withAlpha(hex, alpha) {
  const short = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(hex);
  const full = short ? `#${short[1]}${short[1]}${short[2]}${short[2]}${short[3]}${short[3]}` : hex;
  if (!/^#[0-9a-f]{6}([0-9a-f]{2})?$/i.test(full)) return hex;
  const a = Math.round(Math.min(1, Math.max(0, alpha)) * 255);
  return full.slice(0, 7) + a.toString(16).padStart(2, '0').toUpperCase();
}

// NOTICE_WASH is the alpha of the tinted ground under a notice (the error
// line, the waiting question, the pressure alert): Tokens.swift:74
// `noticeWashAlpha`, and the CSS's --*-dim tokens (rgba … 0.12).
export const NOTICE_WASH = 0.12;
export const wash = (hue) => withAlpha(hue, NOTICE_WASH);

// TRACK_ALPHA is the context bar's unfilled track: Tokens.swift:170
// `trackFill = Color.primary.opacity(0.08)`. It is a neutral there, not a
// brand hue (colors_and_type.css has no track token), so here it is the
// theme's foreground at the same alpha.
export const TRACK_ALPHA = 0.08;
export const track = (foreground) => withAlpha(foreground, TRACK_ALPHA);

// The context-pressure hue for a daemon pressure_level, as the Swift row's
// SessionMetrics.contextPressureColor (SessionState.swift:787-795): safe,
// caution, warning and critical take the four pressure tokens, anything else
// the grey of `cancelled`.
const PRESSURE_HUE = new Map([
  ['safe', BRAND['pressure-low']],
  ['caution', BRAND['pressure-medium']],
  ['warning', BRAND['pressure-high']],
  ['critical', BRAND['pressure-critical']],
]);

export function pressureHue(level) {
  return PRESSURE_HUE.get(level) ?? null;
}

// The connection dot's hue for a poll phase, as the Swift header's
// statusIndicator (SessionListView.swift:1264-1300): connected is green, a
// first look still under way is "connecting", and a daemon that does not
// answer (stalled included, which the Swift app also flags red) is red. The
// Swift app's wsConnecting (#FF9500, Tokens.swift:40) has no token of its own
// in the CSS; it is value-equal to --waiting, which is used here.
const CONNECTION_HUE = new Map([
  ['ok', BRAND['ws-connected']],
  ['starting', BRAND.waiting],
]);

export function connectionHue(phase) {
  return CONNECTION_HUE.get(phase) ?? BRAND['ws-disconnected'];
}
