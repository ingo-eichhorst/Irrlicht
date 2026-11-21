// What a row shows for one matched session: its context fill, or its raw token
// count when the daemon could not resolve the model's context window.

// The pressure levels the daemon assigns from the fill (tailer_metrics.go:
// safe < 60% <= caution < 80% <= warning < 90% <= critical), each drawn in a
// colour Herdr's theme() names, so the gauge follows the person's theme.
const PRESSURE_COLOR = new Map([
  ['safe', 'idle'],
  ['caution', 'working'],
  ['warning', 'blocked'],
  ['critical', 'blocked'],
]);

// formatTokens is compact for a narrow sidebar: 950, 4.2K, 177K, 1.2M, 12M.
export function formatTokens(n) {
  if (!Number.isFinite(n) || n <= 0) return '';
  if (n < 1000) return String(Math.round(n));
  if (n < 10_000) return (n / 1000).toFixed(1) + 'K';
  if (n < 1_000_000) return Math.round(n / 1000) + 'K';
  if (n < 10_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  return Math.round(n / 1_000_000) + 'M';
}

// formatPercent prints one decimal, as the macOS app does
// (SessionState.swift: String(format: "%.1f%%")).
function formatPercent(p) {
  return p.toFixed(1) + '%';
}

const NONE = { kind: 'none' };

// tokenGauge is the row's figure when the context window is unknown: the raw
// token count, never a percentage.
function tokenGauge(tokens) {
  const text = formatTokens(tokens);
  return text ? { kind: 'tokens', text, detail: text + ' tokens, context window unknown' } : NONE;
}

// percentGauge is the context fill, drawn in its pressure colour. A safe fill
// is not news: its figure is drawn in `muted`, the others in their colour.
function percentGauge(facts, color) {
  const text = formatPercent(facts.percent);
  const of = facts.contextWindow ? ' of ' + formatTokens(facts.contextWindow) : '';
  return {
    kind: 'percent',
    text,
    fraction: Math.min(1, facts.percent / 100),
    color,
    textColor: facts.pressure === 'safe' ? 'muted' : color,
    detail: text + of + ' context, ' + facts.pressure,
  };
}

// gaugeOf answers what a matched session's row shows:
//   { kind: 'percent', text, fraction, color, textColor, detail }  the fill;
//   { kind: 'tokens', text, detail }                    the window is unknown;
//   { kind: 'none' }                                    no figure yet.
// A known window with no pressure level yet ("unknown" or absent, as a
// session reads before its first turn) shows nothing rather than a 0%.
export function gaugeOf(facts) {
  if (!facts) return NONE;
  if (facts.windowUnknown) return tokenGauge(facts.tokens);
  const color = PRESSURE_COLOR.get(facts.pressure);
  const hasFigure = Number.isFinite(facts.percent) && facts.percent >= 0;
  if (!color || !hasFigure) return NONE;
  return percentGauge(facts, color);
}
