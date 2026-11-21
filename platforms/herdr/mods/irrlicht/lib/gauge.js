// What a row shows for one matched session: its context fill, or its raw token
// count when the daemon could not resolve the model's context window.

// The pressure levels the daemon assigns from the fill (tailer_metrics.go:
// safe < 60% <= caution < 80% <= warning < 90% <= critical), each drawn in a
// colour Herdr's theme() names, so the gauge follows the person's theme.
const PRESSURE_COLOR = {
  safe: 'idle',
  caution: 'working',
  warning: 'blocked',
  critical: 'blocked',
};

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
export function formatPercent(p) {
  return p.toFixed(1) + '%';
}

// gaugeOf answers what a matched session's row shows:
//   { kind: 'percent', text, fraction, color, detail }  the context fill;
//   { kind: 'tokens', text, detail }                    the window is unknown;
//   { kind: 'none' }                                    no figure yet.
// A known window with no pressure level yet ("unknown" or absent, as a
// session reads before its first turn) shows nothing rather than a 0%.
export function gaugeOf(facts) {
  if (!facts) return { kind: 'none' };
  if (facts.windowUnknown) {
    const text = formatTokens(facts.tokens);
    return text ? { kind: 'tokens', text, detail: text + ' tokens, context window unknown' } : { kind: 'none' };
  }
  const color = PRESSURE_COLOR[facts.pressure];
  if (!color || facts.percent === null || facts.percent < 0) return { kind: 'none' };
  const text = formatPercent(facts.percent);
  const of = facts.contextWindow ? ' of ' + formatTokens(facts.contextWindow) : '';
  return {
    kind: 'percent',
    text,
    fraction: Math.min(1, facts.percent / 100),
    color,
    detail: text + of + ' context, ' + facts.pressure,
  };
}
