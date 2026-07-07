import { displaySessionId } from './sessionIdentity.js';

// formatters.js — pure, stateless presentation helpers shared by row
// rendering and the quota chips: duration/cost/token/percentage text,
// the task-completion ETA chip's text/range logic (#558/#616/#753), and
// the per-state SVG icon registry. No DOM, no fetch, no module state —
// every function here is a straight input->output transform, which is
// what makes them directly unit-testable (irrlicht.test.js) without a
// jsdom render pass.

// --- SVG Icons (compact 12px) ---
const svgIcons = {
  working: '<svg viewBox="0 0 16 16" fill="none"><circle class="core" cx="8" cy="8" r="8" fill="#8B5CF6"/></svg>',
  waiting: '<svg viewBox="0 0 16 16" fill="none"><rect x="4" y="3" width="2.5" height="10" rx="1" fill="#FF9500"/><rect x="9.5" y="3" width="2.5" height="10" rx="1" fill="#FF9500"/></svg>',
  ready: '<svg viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.5" stroke="#34C759" stroke-width="1.5"/><path d="M5 8.2l2 2 4-4.4" stroke="#34C759" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  cancelled: '<svg viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.5" stroke="#8E8E93" stroke-width="1.5"/><path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="#8E8E93" stroke-width="1.5" stroke-linecap="round"/></svg>',
};

export function stateIcon(state) { return svgIcons[state] || svgIcons.ready; }

// --- Helpers ---
export function shortModel(m) {
  if (!m || m === 'unknown') return '';
  return m.replace(/^claude-/, '').replace(/-(\d)/, '.$1');
}

export function formatCost(usd) {
  if (!usd || usd <= 0) return '';
  return '$' + usd.toFixed(2);
}

// formatCO2 renders an estimated CO2e footprint (issue #829). Grams span a
// wide range across sessions (milligrams for a short chat, kilograms for a
// long agentic run), so the unit adapts rather than showing e.g. "0.0g".
export function formatCO2(grams) {
  if (!grams || grams <= 0) return '';
  if (grams < 1) return (grams * 1000).toFixed(0) + 'mg CO2e';
  if (grams < 1000) return grams.toFixed(1) + 'g CO2e';
  return (grams / 1000).toFixed(2) + 'kg CO2e';
}

// co2TierTitle explains the confidence behind a CO2 estimate — every figure
// here is modeled from public disclosures, never measured (no provider
// exposes per-request energy telemetry), so the tooltip says so rather than
// presenting a bare number as fact. Mirrors capacity.CO2Tier's Go-side values.
export function co2TierTitle(tier) {
  if (tier === 'provider_disclosed') {
    return 'Estimated CO2e, normalized from a provider-published energy/CO2 disclosure — not a live measurement.';
  }
  return 'Estimated CO2e — no public per-model figure exists for this model, so a cross-model fallback average is used. Not a live measurement.';
}

// costCellDisplay resolves the per-session row's cost/CO2 slot (issue #829)
// to its text + tooltip for the given display mode, keeping the mode
// branching out of updateSessionRow's already-large row-rendering function.
export function costCellDisplay(metrics, mode) {
  if (mode === 'co2') {
    return { text: formatCO2(metrics.estimated_co2_grams), title: co2TierTitle(metrics.co2_tier) };
  }
  return { text: formatCost(metrics.estimated_cost_usd), title: 'Click to show CO2 estimate' };
}

export function fmtDuration(secs) {
  const h = Math.floor(secs / 3600);
  const m = Math.floor((secs % 3600) / 60);
  const s = secs % 60;
  if (h > 0) return h + 'h' + m + 'm';
  if (m > 0) return m + 'm' + s + 's';
  return s + 's';
}

export function formatElapsed(firstSeen, elapsedStored, isActive) {
  if (isActive && firstSeen) {
    return fmtDuration(Math.max(0, Math.floor(Date.now() / 1000 - firstSeen)));
  }
  if (elapsedStored && elapsedStored > 0) return fmtDuration(elapsedStored);
  return '';
}

// Minute-resolution duration for the task-ETA chip — fmtDuration's
// second-level detail would make the chip flicker every tick for a
// number that is inherently rough.
export function fmtEtaDuration(secs) {
  if (secs < 60) return '<1m';
  const mins = Math.round(secs / 60);
  const h = Math.floor(mins / 60);
  const m = mins % 60;
  if (h > 0) return h + 'h' + (m > 0 ? m + 'm' : '');
  return m + 'm';
}

// fmtEtaText renders the remaining-time text with exactly ONE sign —
// "~" (approximate) or "<" (upper bound), never both, never a degenerate
// "2m–2m" range. highSecs null → point estimate.
//   point, ≥1m   → "~12m left"
//   point, <1m   → "<1m left"
//   range, low <1m → "<2m left"   (the range collapses to its upper bound)
//   range, low==high → point rules
//   range        → "~8m–12m left"
export function fmtEtaText(remaining, highSecs) {
  const low = fmtEtaDuration(remaining);
  if (highSecs !== null) {
    const high = fmtEtaDuration(highSecs);
    if (low !== high) {
      if (remaining < 60) return '<' + high + ' left';
      return '~' + low + '–' + high + ' left';
    }
  }
  if (remaining < 60) return '<1m left';
  return '~' + low + ' left';
}

// taskEtaPresentation decides the task-completion ETA chip for a session
// (issue #558, agent-authored estimate). Returns null when the chip must
// be hidden: session not `working`, no estimate, no reported progress, or
// no projected eta. Otherwise { text, stale, title }: a range whose HIGH
// bound is pinned at the last marker — 1.5× the projected remaining time
// below half the rounds ("~8m–12m left"), the bare projected remaining
// at/above half (#616) — and stale=true when the last marker is older
// than 3min so the chip degrades instead of letting the ETA drift.
//
// The eta is anchored at the marker (daemon-side), so the LOW bound
// counts down in real time between marker updates while the HIGH bound
// stays pinned until the agent reports fresh progress: "~3m–5m left"
// becomes "~2m–5m left" a minute later, never the other way around.
// At/above half the rounds low == high right at a marker, so the range
// collapses to a point ("~5m left") and widens as wall clock passes
// without fresh progress — never a bare countdown (#616). Mirrored in
// SessionListView.swift's taskEtaPresentation.
// Zero completed rounds: no MEASURED rate yet, but the daemon projects from
// a corpus prior (#753) so a real number appears at the very first marker
// instead of "estimating…" (the agent has committed to a plan, #604/#602).
// Widen the range generously (2×) to signal a population prior, not a
// measured rate; with no projection (e.g. a subagent aggregate) fall back
// to the progress-only "estimating…" chip.
function zeroRoundsEtaPresentation(est, eta, nowSec, sourceLabel) {
  // Same undefined-handling fix as taskEtaPresentation's completed_rounds
  // check above: `undefined <= 0` is false, so a missing total_rounds must
  // be checked explicitly or this falls through to render "0/undefined".
  if (est.total_rounds == null || est.total_rounds <= 0) return null;
  const age = est.updated_at > 0 ? Math.max(0, Math.floor(nowSec - est.updated_at)) : 0;
  const zeroStale = est.updated_at > 0 && age > 180;
  let zeroTitle = 'Task ETA — ' + sourceLabel + ' 0/' + est.total_rounds + ' rounds';
  if (est.updated_at > 0) zeroTitle += ', updated ' + fmtDuration(age) + ' ago';
  if (!eta) return { text: 'estimating…', stale: zeroStale, title: zeroTitle };
  const rem0 = Math.max(0, Math.floor(eta - nowSec));
  const high0 = est.updated_at > 0
    ? Math.max(rem0, Math.floor((eta - est.updated_at) * 2))
    : Math.floor(rem0 * 2);
  return { text: fmtEtaText(rem0, high0), stale: zeroStale, title: zeroTitle + ' · rough prior' };
}

// Progress without a projection (e.g. a subagent aggregate whose children
// carry no etas yet, #626): show a rounds-only chip rather than hiding one
// that was visible moments ago.
function roundsOnlyEtaPresentation(est, nowSec, sourceLabel) {
  const age = est.updated_at > 0 ? Math.max(0, Math.floor(nowSec - est.updated_at)) : 0;
  let roundsTitle = 'Task ETA — ' + sourceLabel + ' ' + est.completed_rounds + '/' + est.total_rounds + ' rounds';
  if (est.updated_at > 0) roundsTitle += ', updated ' + fmtDuration(age) + ' ago';
  return {
    text: est.completed_rounds + '/' + est.total_rounds,
    stale: est.updated_at > 0 && age > 180,
    title: roundsTitle,
  };
}

// Progress with a projected eta: range whose HIGH bound is pinned at the
// last marker — see taskEtaPresentation's doc comment for the full rationale.
function projectedEtaPresentation(est, eta, nowSec, sourceLabel) {
  const remaining = Math.max(0, Math.floor(eta - nowSec));
  const frac = est.total_rounds > 0 ? est.completed_rounds / est.total_rounds : 0;
  // 1.5× padding while the rate is barely measurable, bare projected
  // remaining once it's trusted; no marker timestamp at/above half →
  // nothing to pin to, keep the point estimate.
  const factor = frac < 0.5 ? 1.5 : 1;
  let highSecs = null;
  if (est.updated_at > 0) {
    highSecs = Math.max(remaining, Math.floor((eta - est.updated_at) * factor));
  } else if (frac < 0.5) {
    highSecs = Math.floor(remaining * 1.5);
  }
  const text = fmtEtaText(remaining, highSecs);
  const ageSec = est.updated_at > 0 ? Math.max(0, Math.floor(nowSec - est.updated_at)) : 0;
  const stale = est.updated_at > 0 && ageSec > 180;
  let title = 'Task ETA — ' + sourceLabel + ' ' + est.completed_rounds + '/' + est.total_rounds + ' rounds';
  if (est.updated_at > 0) title += ', updated ' + fmtDuration(ageSec) + ' ago';
  return { text: text, stale: stale, title: title };
}

export function taskEtaPresentation(metrics, state, nowSec) {
  const est = metrics?.task_estimate;
  const eta = metrics?.task_completion_eta;
  if (state !== 'working' || !est) return null;
  const sourceLabel = est.source === 'tasks' ? 'from task list'
    : est.source === 'subagents' ? 'from subagents' : 'agent-reported';
  // Explicit null/undefined check before the <= comparison (SonarQube
  // javascript:S1940 wants <= over !(... > ...), but `undefined <= 0` is
  // false while `!(undefined > 0)` is true — a missing completed_rounds
  // must still take the zero-rounds fallback, not fall through to a path
  // that renders "undefined/N rounds").
  if (est.completed_rounds == null || est.completed_rounds <= 0) return zeroRoundsEtaPresentation(est, eta, nowSec, sourceLabel);
  if (!eta) return roundsOnlyEtaPresentation(est, nowSec, sourceLabel);
  return projectedEtaPresentation(est, eta, nowSec, sourceLabel);
}

export function shortID(id) { return id ? displaySessionId(id).slice(0, 6) : ''; }

export function pressureClass(level) {
  if (level === 'critical') return 'critical';
  if (level === 'warning' || level === 'high') return 'high';
  if (level === 'caution' || level === 'medium') return 'medium';
  return '';
}

export function pressureColor(level) {
  if (level === 'critical') return 'var(--pressure-critical)';
  if (level === 'warning' || level === 'high') return 'var(--pressure-high)';
  if (level === 'caution' || level === 'medium') return 'var(--pressure-medium)';
  return 'var(--pressure-low)';
}

export function formatTokens(n) {
  if (n < 1000) return n + '';
  if (n < 1000000) return (n / 1000).toFixed(1) + 'K';
  return (n / 1000000).toFixed(1) + 'M';
}

export function esc(s) {
  if (s == null) return '';
  return String(s).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
}

export function activeSubagentCount(a) {
  if (!a.children) return 0;
  return a.children.filter(c => c.state === 'working' || c.state === 'waiting').length;
}

// Cache-creation regression badge (#813) — short visible text. `tooltip` is
// the daemon's cache_bloat_tooltip: the version-attribution string when it
// could name the regressing upstream version, else '' (no attribution). The
// longer hover explanation is composed daemon-side (cache_bloat_explanation,
// issue #827) and rendered verbatim — see updateCacheBloatRow in irrlicht.js.
export function cacheBloatBadgeText(tooltip) {
  return tooltip || 'cache ↑';
}

