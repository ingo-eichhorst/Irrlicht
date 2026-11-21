// What a row's context line shows for one matched session: its context fill
// as the Swift row's ContextBar (SessionRowView.swift:402-449), or its raw
// token count when the daemon could not resolve the model's context window.

import { pressureHue } from './palette.js';

// formatTokens is compact for a narrow sidebar: 950, 4.2K, 177K, 1.2M, 12M.
// It is the Claude Code panel's (test/address.parity.test.js pins the two
// alike) and is used in hover text; the bar's own label is tokenCount below.
export function formatTokens(n) {
  if (!Number.isFinite(n) || n <= 0) return '';
  if (n < 1000) return String(Math.round(n));
  if (n < 10_000) return (n / 1000).toFixed(1) + 'K';
  if (n < 1_000_000) return Math.round(n / 1000) + 'K';
  if (n < 10_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  return Math.round(n / 1_000_000) + 'M';
}

// scaled is n in K or M with `digits` decimals, as the Swift row prints its
// counts with String(format: "%.<digits>fK").
function scaled(n, digits) {
  if (n < 1000) return String(Math.round(n));
  if (n < 1_000_000) return (n / 1000).toFixed(digits) + 'K';
  return (n / 1_000_000).toFixed(digits) + 'M';
}

const isCount = (n) => Number.isFinite(n) && n > 0;

// tokenCount is the label inside the Swift row's bar, SessionMetrics
// .formattedTokenCount (SessionState.swift:736-746): 950, 45.0K, 120.0K, 1.2M.
export function tokenCount(n) {
  return isCount(n) ? scaled(n, 1) : '—';
}

// windowCount is the window half of SessionMetrics.formattedTokenUsage
// (SessionState.swift:748-763): 200K, 1M, or "?" when it is not known.
function windowCount(n) {
  return isCount(n) ? scaled(n, 0) : '?';
}

// formatPercent prints one decimal, as the macOS app does
// (SessionState.swift:765-768: String(format: "%.1f%%")).
function formatPercent(p) {
  return p.toFixed(1) + '%';
}

const NONE = { kind: 'none' };

// tokenGauge is the row's figure when the context window is unknown: the
// Swift row's token text, "31.0K / ?", never a percentage.
function tokenGauge(facts) {
  const hasTokens = facts.tokens > 0;
  if (!hasTokens) return NONE;
  const text = tokenCount(facts.tokens) + ' / ' + windowCount(facts.contextWindow);
  return { kind: 'tokens', text, detail: formatTokens(facts.tokens) + ' tokens, context window unknown' };
}

// percentGauge is the context fill: the bar in its pressure hue with the
// token count inside, and the percentage beside it in the same hue, as the
// Swift row draws both (SessionRowView.swift:405-420).
function percentGauge(facts, hue) {
  const text = formatPercent(facts.percent);
  const of = facts.contextWindow ? ' of ' + formatTokens(facts.contextWindow) : '';
  return {
    kind: 'percent',
    text,
    fraction: Math.min(1, facts.percent / 100),
    hue,
    label: tokenCount(facts.tokens),
    detail: text + of + ' context, ' + facts.pressure,
  };
}

// gaugeOf answers what a matched session's row shows:
//   { kind: 'percent', text, fraction, hue, label, detail }  the fill;
//   { kind: 'tokens', text, detail }               the window is unknown;
//   { kind: 'none' }                               no figure yet.
// A known window with no pressure level yet ("unknown" or absent, as a
// session reads before its first turn) shows nothing rather than a 0%.
export function gaugeOf(facts) {
  if (!facts) return NONE;
  if (facts.windowUnknown) return tokenGauge(facts);
  const hue = pressureHue(facts.pressure);
  const hasFigure = Number.isFinite(facts.percent) && facts.percent >= 0;
  if (!hue || !hasFigure) return NONE;
  return percentGauge(facts, hue);
}
