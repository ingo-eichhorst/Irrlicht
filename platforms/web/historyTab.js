// --- History tab (issue #369) ---
// A top-level view (toggled by #history-tab-toggle) that charts historical
// USD cost from GET /api/v1/history. Hard constraint: exactly one chart +
// one side panel. Phase 1 wires chart=cost grouped by project; the other
// chart/group buttons are disabled stubs. The stacked-area chart is
// hand-rolled on a canvas (no external lib), mirroring paintRowHistory's
// DPI handling.
const ACTIVE_TAB_KEY = 'irrlicht_activeTab';
const HISTORY_COLORS = [
  '#8B5CF6', '#34C759', '#FF9500', '#0A84FF', '#FF375F',
  '#5E5CE6', '#FFD60A', '#30D158', '#BF5AF2', '#64D2FF',
];
const RANGE_LABELS = { day: 'Day', week: 'Week', month: 'Month', year: 'Year', 'this-month': 'This Month', custom: 'Custom' };
export const CHART_LABELS = { cost: 'Cost', tokens: 'Tokens', co2: 'CO2', models: 'Models', providers: 'Providers', agents: 'Agents', state: 'Activity', yield: 'Yield', dora: 'DORA', autonomy: 'Autonomy' };
// Autonomy (#1905). `autonomy` is a CLIENT-SIDE pseudo-chart: selecting it
// fans out into the daemon's two real charts, chart=autonomy_duration (the
// percentile lines) and chart=autonomy_spans (the run strip), because the
// section shows both elements at once.
//
// WINDOW LENGTHS, not bucket widths. These keys overlap GRANULARITY_LABELS'
// textually and mean something else entirely: there a key names a bucket width
// that is multiplied by a count (so '24h' resolves to a THIRTY-DAY window),
// here a key IS the window. The two tables must never be merged — see
// autonomy window vocabularies in irrlicht.history.autonomy.test.js.
export const AUTONOMY_RANGE_LABELS = { '30d': '30 days', '1y': 'Year' };
export const AUTONOMY_SPAN_LABELS = { '8h': '8h', '24h': '24h', '7d': '7d', '30d': '30d', '12mo': '12mo' };
// The strip's pixel-collapse ladder: when one device-pixel column holds
// several runs, the column paints the highest-ranked reason in it. Same order
// as the session-history strip's ladder (#1805) — one error in a column paints
// the whole column. One state per line, because a single line naming three of
// the four canonical states is what tools/state-vocabulary-lint.sh refuses.
export const AUTONOMY_REASON_PRIORITY = {
  error: 3,
  waiting: 2,
  ready: 1,
};
// Legend glyphs + wording, matching the issue's sketch and the macOS legend.
export const AUTONOMY_REASON_LEGEND = [
  ['error', '\u2717', 'error'],
  ['waiting', '?', 'it asked'],
  ['ready', '\u2713', 'turn finished'],
];
// Granularity steps for chart=state's activity matrix (issue #981) — each
// picks both the server's bucket width and the matrix's visible column
// count at once (see historyGranularitySpecs on the daemon side).
const GRANULARITY_LABELS = { '1m': '1 min', '10m': '10 min', '60m': '60 min', '8h': '8 hr', '24h': '24 hr', '7d': '7 day', '1mo': '1 mo', '6mo': '6 mo', '1y': '1 yr' };
// Fixed stack order for the activity matrix's per-cell mini bar, bottom to
// top — mirrors the canonical state order in core/domain/session/session.go,
// including #1798's `error` (#1801). The daemon emits a bucket for every
// canonical state, so a state missing from this list is silently dropped from
// the chart, the tooltip, the legend AND the CSV export at once, which is
// exactly what happened to `error` between #1798 and here.
//
// The labels live in the same table as the order, because before #1801 the
// pairs were re-typed in three more places (legend, tooltip, and two hardcoded
// working+waiting+ready sums) and a fourth state had to be added to all of
// them or the chart would disagree with itself about what a cell totals.
const STATE_STACK = [
  ['working', 'Working'],
  ['waiting', 'Waiting'],
  ['ready', 'Ready'],
  ['error', 'Error'],
];
const STATE_STACK_ORDER = STATE_STACK.map(([state]) => state);
// Drilldown order: clicking a contributor scopes to it and re-groups by the
// next finer axis. A leaf (no entry) makes that contributor non-drillable.
export const DRILL_NEXT = { project: 'branch', branch: 'session', provider: 'model', model: 'session' };
// Cross-filter dimensions and the fixed token-type vocabulary. A dimension
// is never both the active group and a filter (the grouped one is hidden).
const HISTORY_FILTER_DIMS = ['provider', 'token_type', 'project'];
const TOKEN_TYPE_OPTIONS = [['input', 'Input'], ['output', 'Output'], ['cache_read', 'Cache read'], ['cache_creation', 'Cache create']];
const TOKEN_TYPE_LABEL = { input: 'Input', output: 'Output', cache_read: 'Cache read', cache_creation: 'Cache create' };
// scope is null or { field, value } — a single-level drilldown filter.
// filters holds per-dimension multi-select sets; known accumulates the
// provider/project option lists seen across responses (token_type is fixed).
const historyState = {
  range: 'day', chart: 'cost', group: 'project', forecast: true, start: null, end: null, scope: null, data: null,
  // granularity is chart=state's own zoom-level axis (#981) — independent of
  // range, which every other chart uses instead.
  granularity: '24h',
  // Autonomy (#1905): one window per element, neither of them `range`.
  // autonomyData holds BOTH responses, since the section renders both at once.
  autonomyRange: '30d',
  autonomySpan: '24h',
  autonomyData: null,
  filters: { provider: [], token_type: [], project: [] },
  known: { provider: [], project: [] },
  // DORA (#951) is inherently repo-scoped — needs exactly one project,
  // unlike cost/yield's implicit "all projects." Sourced from
  // known.project (already populated from cost fetches grouped by
  // project), so no separate project-discovery fetch is needed.
  doraProject: null,
};
let historyFetchSeq = 0;
let historyResizeRAF = 0;

function historyColorFor(i) { return HISTORY_COLORS[i % HISTORY_COLORS.length]; }
function histDollar(v) { return '$' + (Number(v) || 0).toFixed(2); }
// Compact token count: 1.2M / 3.4k / 970.
export function histTokens(v) {
  v = Number(v) || 0;
  if (v >= 1e6) return (v / 1e6).toFixed(1) + 'M';
  if (v >= 1e3) return (v / 1e3).toFixed(1) + 'k';
  return String(Math.round(v));
}
// Integer agent count (concurrency is a whole number of sessions).
export function histCount(v) { return String(Math.round(Number(v) || 0)); }
// Compact estimated CO2e footprint (issue #829): unit-adaptive like the
// session row's formatCO2, but always renders a value — a chart axis needs
// "0g" at an empty bucket, not the row display's hide-on-zero blank.
export function histCO2(v) {
  v = Number(v) || 0;
  if (v < 1) return (v * 1000).toFixed(0) + 'mg';
  if (v < 1000) return v.toFixed(1) + 'g';
  return (v / 1000).toFixed(2) + 'kg';
}
// DORA (#951) metric formatters — mirror the daemon's own format_hours
// convention (hours below a day, days at or above) for Lead Time/MTTR.
export function histDoraPerWeek(v) { return (Number(v) || 0).toFixed(1) + '/week'; }
export function histDoraPercent(v) { return Math.round(Number(v) || 0) + '%'; }
export function histDoraHours(v) {
  v = Number(v) || 0;
  if (v >= 24) return (v / 24).toFixed(1) + ' days';
  return Math.round(v) + ' hours';
}

// The value formatter for the active chart — dollars for cost/models/providers,
// token counts for tokens, integer agent counts for agents, grams for co2.
function histValue(v) {
  if (historyState.chart === 'tokens') return histTokens(v);
  if (historyState.chart === 'agents') return histCount(v);
  if (historyState.chart === 'co2') return histCO2(v);
  return histDollar(v);
}

// CO2 equivalents (issue #952): everyday high-carbon activities used as red
// dotted reference lines on the CO2 chart, so a raw gram total maps to
// something tangible instead of an abstract number. Every figure is a
// widely-cited public average — not measured against irrlicht's own
// sessions — chosen to be recognizable across different countries rather
// than US/UK-centric only. Full citations live in the "CO2 Methodology"
// docs page linked from the chart. Kept ascending by grams.
export const CO2_EQUIVALENTS = [
  { id: 'search', grams: 0.2, label: 'a web search' },
  { id: 'phone-charge', grams: 10, label: 'charging a smartphone' },
  { id: 'stream-hour', grams: 36, label: '1 hour of video streaming' },
  { id: 'kettle', grams: 60, label: 'boiling a kettle' },
  { id: 'car-km', grams: 170, label: 'driving 1 km by car' },
  { id: 'grid-kwh', grams: 460, label: '1 kWh of average grid electricity' },
  { id: 'shower', grams: 1000, label: 'a hot shower' },
  { id: 'laundry', grams: 1500, label: 'a load of laundry' },
  { id: 'petrol-liter', grams: 2350, label: 'burning 1 liter of petrol' },
  { id: 'bike-frame', grams: 5500, label: 'manufacturing a bicycle frame' },
  { id: 'running-shoes', grams: 9500, label: 'manufacturing a pair of running shoes' },
  { id: 'jeans', grams: 33400, label: 'a pair of jeans, cradle to grave' },
  { id: 'flight-short', grams: 43800, label: 'a short-haul flight (London → Paris)' },
  { id: 'tree-year', grams: 60000, label: "a tree's CO2 absorption for a year" },
  { id: 'car-commute-month', grams: 118000, label: 'a month of average car commuting' },
  { id: 'laptop', grams: 185000, label: 'a laptop, cradle to grave' },
  { id: 'flight-long', grams: 650000, label: 'a long-haul flight (London → New York)' },
  { id: 'flight-long-return', grams: 1300000, label: 'a round-trip long-haul flight (there and back)' },
  { id: 'car-year', grams: 4290000, label: "an average car's emissions for a year" },
  { id: 'person-year', grams: 4800000, label: "an average person's annual carbon footprint" },
  { id: 'cars-9t', grams: 8580000, label: "roughly 2 average cars' annual emissions" },
  { id: 'cars-13t', grams: 12870000, label: "roughly 3 average cars' annual emissions" },
  { id: 'cars-25t', grams: 25000000, label: "roughly 6 average cars' annual emissions" },
  { id: 'people-100t', grams: 100000000, label: "roughly 21 people's average annual carbon footprint" },
];

// co2EquivalentTargets returns the log-scale fractions of the axis maximum
// pickCO2Equivalents aims each reference line at, based on how many
// candidates are available to fill them — 3 spread bands when there's
// enough range to fill them, fewer otherwise. Deliberately wide spread
// (0.04/0.2/0.8, not evenly spaced) so the 3 lines read as low/mid/high
// scale rather than clustering in the middle of the visible range.
function co2EquivalentTargets(candidateCount) {
  if (candidateCount >= 3) return [0.04, 0.2, 0.8];
  if (candidateCount === 2) return [0.1, 0.7];
  return [0.4];
}

// nearestUnpickedEquivalent returns whichever candidate not already in picks
// sits closest (in log-space, so magnitudes compare fairly) to targetLog.
function nearestUnpickedEquivalent(candidates, picks, targetLog) {
  let best = null, bestDist = Infinity;
  for (const eq of candidates) {
    if (picks.includes(eq)) continue;
    const dist = Math.abs(Math.log(eq.grams) - targetLog);
    if (dist < bestDist) { bestDist = dist; best = eq; }
  }
  return best;
}

// pickCO2Equivalents chooses up to 3 reference lines that sit inside the
// chart's y-axis range, spread across low/mid/high bands (rather than
// picking the 3 closest to maxY, which would cluster them together) so a
// viewer gets a sense of scale. Values within 2% of the axis ceiling are
// excluded — a line drawn on top of the topmost gridline reads as clutter,
// not a reference. Deterministic (no randomness), so the same data always
// draws the same lines.
export function pickCO2Equivalents(maxY) {
  if (maxY <= 0) return [];
  const ceiling = maxY * 0.98;
  const candidates = CO2_EQUIVALENTS.filter(eq => eq.grams > 0 && eq.grams < ceiling);
  if (!candidates.length) return [];
  const picks = [];
  for (const frac of co2EquivalentTargets(candidates.length)) {
    const best = nearestUnpickedEquivalent(candidates, picks, Math.log(maxY * frac));
    if (best) picks.push(best);
  }
  return picks.sort((a, b) => a.grams - b.grams);
}
function histAxisLabel(ts, bucketSeconds) {
  const d = new Date(ts * 1000);
  if (bucketSeconds < 86400) {
    return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
  }
  return (d.getMonth() + 1) + '/' + d.getDate();
}
// Running total of a per-bucket series, so stacked bands climb to the grand
// total at the right edge instead of reading as a spiky per-bucket rate.
export function historyRunningSum(arr) {
  let total = 0;
  return (arr || []).map(v => { total += (Number(v) || 0); return total; });
}

function historyTabOn() { return document.body.classList.contains('tab-history'); }
function setHistoryTab(on) {
  document.body.classList.toggle('tab-history', on);
  const btn = document.getElementById('history-tab-toggle');
  if (btn) {
    btn.classList.toggle('active', on);
    btn.textContent = on ? 'Live' : 'History';
    btn.title = on ? 'Back to live sessions' : 'Show historical cost analytics';
  }
  localStorage.setItem(ACTIVE_TAB_KEY, on ? 'history' : 'live');
  if (on) fetchHistory();
}

// setHistoryWindowParams writes whichever window selector the active chart
// uses: the activity matrix resolves its window from a granularity zoom-level
// instead of range/start/end — see historyGranularitySpecs on the daemon.
function setHistoryWindowParams(p, state) {
  if (state.chart === 'state') {
    p.set('granularity', state.granularity);
  } else if (state.range === 'custom' && state.start != null && state.end != null) {
    p.set('start', String(state.start));
    p.set('end', String(state.end));
  } else {
    p.set('range', state.range);
  }
}

// setHistoryFilterParams emits the orthogonal cross-filters: each non-empty
// dimension except the one being grouped on. token_type only narrows tokens.
function setHistoryFilterParams(p, state) {
  const filters = state.filters || {};
  for (const dim of HISTORY_FILTER_DIMS) {
    if (dim === state.group) continue;
    if (dim === 'token_type' && state.chart !== 'tokens') continue;
    const vals = filters[dim];
    if (vals?.length) p.set(dim, vals.join(','));
  }
}

// autonomyQuery builds one of the two Autonomy requests. `element` picks
// which daemon chart and therefore which window vocabulary applies — they are
// different sets, and neither endpoint accepts the other's keys.
export function autonomyQuery(element, state = historyState) {
  const p = new URLSearchParams();
  if (element === 'spans') {
    p.set('chart', 'autonomy_spans');
    p.set('window', state.autonomySpan);
  } else {
    p.set('chart', 'autonomy_duration');
    p.set('window', state.autonomyRange);
  }
  return p.toString();
}

export function historyQuery(state = historyState) {
  const p = new URLSearchParams();
  p.set('chart', state.chart);
  p.set('group', state.group);
  p.set('forecast', state.forecast ? 'true' : 'false');
  if (state.scope) p.set('scope', state.scope.field + ':' + state.scope.value);
  setHistoryWindowParams(p, state);
  setHistoryFilterParams(p, state);
  // DORA is repo-scoped — exactly one project, not the multi-select project
  // filter above (#951).
  if (state.chart === 'dora' && state.doraProject) p.set('project', state.doraProject);
  return p.toString();
}

// fetchAutonomy fetches BOTH Autonomy elements. Either failing marks the whole
// section unavailable rather than leaving one element beside a spinner that
// never resolves.
function fetchAutonomy() {
  const seq = ++historyFetchSeq;
  const get = (element) => fetch('/api/v1/history?' + autonomyQuery(element))
    .then(r => (r.ok ? r.json() : null))
    .catch(() => null);
  return Promise.all([get('duration'), get('spans')]).then(([duration, spans]) => {
    if (seq !== historyFetchSeq) return; // superseded by a newer request
    historyState.autonomyData = (duration && spans) ? { duration, spans } : null;
    historyState.data = historyState.autonomyData ? duration : null;
    renderHistory();
  });
}

function fetchHistory() {
  if (historyState.chart === 'autonomy') return fetchAutonomy();
  // DORA needs exactly one project — with none selected, there's nothing
  // to fetch at all (a distinct empty state, not a load failure or a
  // spinner; see renderDoraPanel).
  if (historyState.chart === 'dora' && !historyState.doraProject) {
    historyState.data = null;
    renderHistory();
    return Promise.resolve();
  }
  const seq = ++historyFetchSeq;
  return fetch('/api/v1/history?' + historyQuery())
    .then(r => (r.ok ? r.json() : null))
    .catch(() => null)
    .then(data => {
      if (seq !== historyFetchSeq) return; // superseded by a newer request
      historyState.data = data || null;
      // Grow the provider/project filter vocabularies from any response
      // grouped on that axis (token_type's options are fixed).
      if (data && (data.group === 'provider' || data.group === 'project')) {
        const set = new Set(historyState.known[data.group]);
        for (const c of (data.top_contributors || [])) {
          if (c.label && c.label !== 'unknown') set.add(c.label);
        }
        historyState.known[data.group] = [...set].sort((a, b) => a.localeCompare(b));
      }
      renderHistory();
    });
}

// syncHistoryCO2Info shows the "how is this calculated" methodology link
// only while the CO2 chart is active — it's meaningless for cost/tokens/etc.
function syncHistoryCO2Info() {
  const el = document.getElementById('history-co2-info');
  if (el) el.hidden = historyState.chart !== 'co2';
}

// historyEmptyCaption is the "nothing to show" caption for the active chart.
// Yield counts completed sessions; agents/state are reconstructed from opt-in
// recordings — each gets its own wording.
function historyEmptyCaption() {
  switch (historyState.chart) {
    case 'yield':
      return 'no completed sessions in this range yet';
    case 'dora':
      return historyState.doraProject ? 'DORA metrics — see panel' : 'select a project to see DORA metrics';
    case 'agents':
    case 'state':
      return 'no recordings in this range yet';
    case 'autonomy':
      // Never "no runs": this feature collects from the day it ships, so an
      // empty view has to say which of the two it is (#1905). The full
      // sentence lives in the side panel; this is the canvas overlay.
      return autonomyEverRecorded() ? 'no runs in this range' : 'not collecting yet — runs appear as sessions run';
    default:
      return 'no cost data in this range yet';
  }
}

// markHistoryCanvasEmpty blanks the shared canvas wrapper — for the charts that
// never paint onto it (DORA is a period summary whose content lives entirely in
// the side panel, see renderDoraPanel) and whenever there is no data at all.
function markHistoryCanvasEmpty() {
  const wrap = document.getElementById('history-chart-wrap');
  if (wrap) wrap.classList.add('empty');
}

// paintActiveHistoryChart routes to the active chart's painter and side panel.
function paintActiveHistoryChart() {
  switch (historyState.chart) {
    case 'dora':
      markHistoryCanvasEmpty();
      renderDoraPanel();
      break;
    case 'yield':
      paintYieldChart();
      renderYieldPanel();
      break;
    case 'state':
      renderStateMatrix();
      renderStatePanel();
      break;
    case 'autonomy':
      paintAutonomyChart();
      renderAutonomyStrip();
      renderAutonomyPanel();
      break;
    default:
      paintHistoryChart();
      renderHistoryPanel();
  }
}

function renderHistory() {
  renderHistoryBreadcrumb();
  renderHistoryFilters();
  syncDoraProjectRow();
  syncGranularityRow();
  syncHistoryRangeRow();
  syncHistoryCO2Info();
  // The activity matrix is a grid, not a time-series line — it replaces the
  // shared canvas with its own scrollable DOM grid (see history-matrix-scroll).
  syncHistoryMatrixVisibility(historyState.chart === 'state');
  syncAutonomyRows(historyState.chart === 'autonomy');
  const emptyEl = document.getElementById('history-chart-empty');
  if (emptyEl) emptyEl.textContent = historyEmptyCaption();
  if (!historyState.data) {
    markHistoryCanvasEmpty();
    if (historyState.chart === 'dora') renderDoraPanel();
    return;
  }
  paintActiveHistoryChart();
}

// syncHistoryMatrixVisibility toggles between the shared canvas (every
// time-series chart) and the activity matrix's own DOM grid (chart=state
// only) — the matrix doesn't fit the canvas's continuous-time painter.
function syncHistoryMatrixVisibility(isState) {
  const canvas = document.getElementById('history-chart');
  const matrixScroll = document.getElementById('history-matrix-scroll');
  if (canvas) canvas.hidden = isState;
  if (matrixScroll) matrixScroll.hidden = !isState;
}

// syncHistoryRangeRow hides the Day/Week/Month/… range selector for
// chart=state: the activity matrix resolves its window from ?granularity=
// instead, so the range buttons would be visible but silently inert.
function syncHistoryRangeRow() {
  const row = document.getElementById('history-range-row');
  // Autonomy joins chart=state here: it resolves both its windows from
  // ?window=, so the Day/Week/Month buttons would be visible but inert.
  if (row) row.hidden = historyState.chart === 'state' || historyState.chart === 'autonomy';
}

// syncAutonomyRows shows the Autonomy pickers and the run strip only while the
// section is active, mirroring syncGranularityRow's per-chart row toggle.
function syncAutonomyRows(isAutonomy) {
  const ctl = document.getElementById('history-autonomy-row');
  if (ctl) ctl.hidden = !isAutonomy;
  const strip = document.getElementById('history-autonomy-strip');
  if (strip) strip.hidden = !isAutonomy;
  const groupRow = document.getElementById('history-group-sel')?.closest('.history-ctl-row');
  // Group and the cross-filters do not apply to spans — the section has no
  // stacking axis at all.
  if (groupRow) groupRow.hidden = isAutonomy;
  const filterRow = document.getElementById('history-filter-row');
  if (filterRow) filterRow.hidden = isAutonomy;
}

// syncGranularityRow shows the granularity zoom-level control only while
// chart=state is active, mirroring syncDoraProjectRow's per-chart row toggle.
function syncGranularityRow() {
  const row = document.getElementById('history-granularity-row');
  if (row) row.hidden = historyState.chart !== 'state';
}

// setupHistoryCanvas sizes the canvas for the current DPR/layout, clears
// it, and returns the 2D context plus the CSS-pixel plot dimensions.
function setupHistoryCanvas(canvas, wrap) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.offsetWidth || wrap.clientWidth || 600;
  const h = canvas.offsetHeight || 340;
  const pxW = Math.round(w * dpr), pxH = Math.round(h * dpr);
  if (canvas.width !== pxW || canvas.height !== pxH) { canvas.width = pxW; canvas.height = pxH; }
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);
  return { ctx, w, h };
}

// buildHistoryMatrix lays out one row per project — top_contributors order
// first (so side-panel dots match chart colors), then any extra projects
// seen only in the series — and fills a [project][bucket] value matrix.
function buildHistoryMatrix(data, buckets, B) {
  const projects = [];
  const idx = new Map();
  for (const c of (data.top_contributors || [])) {
    if (!idx.has(c.label)) { idx.set(c.label, projects.length); projects.push(c.label); }
  }
  for (const pt of (data.series || [])) {
    if (!idx.has(pt.project)) { idx.set(pt.project, projects.length); projects.push(pt.project); }
  }
  const matrix = projects.map(() => new Array(B).fill(0));
  const tsIdx = new Map();
  buckets.forEach((t, i) => tsIdx.set(t, i));
  for (const pt of (data.series || [])) {
    const r = idx.get(pt.project), c = tsIdx.get(pt.ts);
    if (r != null && c != null) matrix[r][c] += pt.value;
  }
  return { projects, matrix };
}

// historyForecastSeries resolves the forecast points in display space:
// continuing the cumulative climb from the grand total, or the flat
// per-bucket projected rate when incremental.
function historyForecastSeries(data, cumulative, grandTotal) {
  const fc = (historyState.forecast && data.forecast && Array.isArray(data.forecast.series)) ? data.forecast.series : [];
  const fcY = cumulative
    ? historyRunningSum(fc.map(p => p.value)).map(v => grandTotal + v)
    : fc.map(p => p.value);
  return { H: fc.length, fcY };
}

// historyMaxY finds the Y-axis scale: the tallest stacked column (summed
// across bands per bucket), also covering the forecast points, with 12%
// headroom.
function historyMaxY(matrix, projects, B, fcY) {
  let maxY = 0;
  for (let c = 0; c < B; c++) {
    let s = 0;
    for (let r = 0; r < projects.length; r++) s += matrix[r][c];
    if (s > maxY) maxY = s;
  }
  for (const v of fcY) if (v > maxY) maxY = v;
  if (maxY <= 0) maxY = 1;
  return maxY * 1.12;
}

// drawHistoryGridlines draws the Y gridlines and their value labels,
// underneath where the stacked areas will be drawn.
function drawHistoryGridlines(geo, { w, padL, padR, muted, gridColor, maxY }) {
  const { ctx, yAt } = geo;
  ctx.font = '10px ui-monospace, monospace';
  ctx.textBaseline = 'middle';
  ctx.textAlign = 'right';
  const ticks = 4;
  for (let t = 0; t <= ticks; t++) {
    const v = maxY * t / ticks;
    const y = yAt(v);
    ctx.strokeStyle = gridColor;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(padL, y);
    ctx.lineTo(w - padR, y);
    ctx.stroke();
    ctx.fillStyle = muted;
    ctx.fillText(histValue(v), padL - 6, y);
  }
}

// drawHistoryStackedAreas draws the bottom-up stacked project bands.
function drawHistoryStackedAreas(ctx, projects, matrix, B, xAt, yAt) {
  const baseline = new Array(B).fill(0);
  for (let r = 0; r < projects.length; r++) {
    ctx.beginPath();
    for (let c = 0; c < B; c++) {
      const x = xAt(c), y = yAt(baseline[c] + matrix[r][c]);
      if (c === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    for (let c = B - 1; c >= 0; c--) ctx.lineTo(xAt(c), yAt(baseline[c]));
    ctx.closePath();
    ctx.fillStyle = historyColorFor(r);
    ctx.fill();
    for (let c = 0; c < B; c++) baseline[c] += matrix[r][c];
  }
}

// drawHistoryForecastLine draws the dashed forecast continuation. Cumulative
// charts continue the climb from the grand total to ≈forecast.projected;
// incremental charts hold a flat line at the projected per-bucket rate,
// anchored at the forecast's own first value so an empty trailing bucket
// (the in-progress current minute) doesn't draw a spurious dip-and-spike.
function drawHistoryForecastLine(geo, { B, H, cumulative, grandTotal, fcY, waiting }) {
  if (H <= 0) return;
  const { ctx, xAt, yAt } = geo;
  ctx.save();
  ctx.setLineDash([4, 3]);
  ctx.strokeStyle = waiting;
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  ctx.moveTo(xAt(B - 1), yAt(cumulative ? grandTotal : fcY[0]));
  for (let k = 0; k < H; k++) ctx.lineTo(xAt(B + k), yAt(fcY[k]));
  ctx.stroke();
  ctx.restore();
}

// MIN_LABEL_GAP_PX is the smallest vertical gap enforced between two
// CO2-equivalent labels (issue #980) — a backstop on top of a densified
// CO2_EQUIVALENTS table (which does the real work of keeping picks spread
// out): even if two picks' reference lines still land close together for
// some axis range, their text is pushed apart by at least this much so it
// never visually overlaps.
const MIN_LABEL_GAP_PX = 12;
// TEXT_HEIGHT_PX approximates the rendered height of a label's text — used
// to convert a baseline anchor (which sits on the label's top edge when
// flipped below the line, or its bottom edge otherwise) into the actual
// top/bottom extent being compared for overlap.
const TEXT_HEIGHT_PX = 10;

// drawHistoryCO2Equivalents overlays red dotted reference lines at grams
// equivalent to a relatable everyday activity (issue #952) — only called for
// the CO2 chart, so every other chart type is unaffected. Labels are left-
// aligned near where the line starts (not the far right, which used to
// overlap the stacked-area content) and flip below the line instead of
// above near the top edge so they don't clip off-canvas.
function drawHistoryCO2Equivalents(geo, { w, padL, padR, padT, maxY, danger }) {
  const { ctx, yAt } = geo;
  const picks = pickCO2Equivalents(maxY);
  if (!picks.length) return;
  ctx.save();
  ctx.font = '10px ui-monospace, monospace';
  ctx.strokeStyle = danger;
  ctx.fillStyle = danger;
  ctx.lineWidth = 1.5;
  ctx.lineCap = 'round';
  ctx.setLineDash([1, 4]);
  ctx.textAlign = 'left';
  // Lines are drawn at their true data position regardless of crowding —
  // only the label text's anchor is nudged, and only ever downward, so
  // labels stay in top-to-bottom grams order. Tracked by the text's bottom
  // edge (not the raw baseline anchor), since a below-flipped label's
  // anchor is its top edge while a normal label's anchor is its bottom —
  // comparing anchors directly would understate the gap between a flipped
  // label and the one below it.
  let prevBottom = null;
  for (const eq of [...picks].reverse()) {
    const lineY = yAt(eq.grams);
    ctx.beginPath();
    ctx.moveTo(padL, lineY);
    ctx.lineTo(w - padR, lineY);
    ctx.stroke();
    const below = (lineY - padT) < 10;
    let labelY = below ? lineY + 3 : lineY - 3;
    const top = below ? labelY : labelY - TEXT_HEIGHT_PX;
    if (prevBottom !== null && top - prevBottom < MIN_LABEL_GAP_PX) {
      // Only labelY is carried further — prevBottom below is recomputed from it,
      // so the shifted top would never be read again.
      labelY += MIN_LABEL_GAP_PX - (top - prevBottom);
    }
    ctx.textBaseline = below ? 'top' : 'bottom';
    ctx.fillText('≈ ' + eq.label, padL + 4, labelY);
    prevBottom = below ? labelY + TEXT_HEIGHT_PX : labelY;
  }
  ctx.restore();
}

// drawHistoryXAxisLabels draws up to 6 evenly-spaced time labels.
function drawHistoryXAxisLabels(geo, { buckets, B, bucketSeconds, muted, h, padB }) {
  const { ctx, xAt } = geo;
  ctx.fillStyle = muted;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'top';
  const labelCount = Math.min(6, B);
  for (let i = 0; i < labelCount; i++) {
    const c = Math.round(i * (B - 1) / Math.max(1, labelCount - 1));
    ctx.fillText(histAxisLabel(buckets[c], bucketSeconds), xAt(c), h - padB + 5);
  }
}

function paintHistoryChart() {
  const canvas = document.getElementById('history-chart');
  const wrap = document.getElementById('history-chart-wrap');
  if (!canvas || !wrap) return;
  const data = historyState.data;
  const { ctx, w, h } = setupHistoryCanvas(canvas, wrap);

  const buckets = data?.bucket_starts || [];
  const B = buckets.length;
  const hasData = !!(data && data.total > 0 && B > 0);
  wrap.classList.toggle('empty', !hasData);
  if (!hasData) return;

  const cs = getComputedStyle(document.documentElement);
  const muted = (cs.getPropertyValue('--muted') || '#888').trim();
  const waiting = (cs.getPropertyValue('--waiting') || '#FF9500').trim();
  const danger = (cs.getPropertyValue('--pressure-high') || '#FF3B30').trim();
  const gridColor = 'rgba(128,140,170,0.18)';

  const { projects, matrix } = buildHistoryMatrix(data, buckets, B);
  // Cumulative for the stacked cost/token area charts: each band becomes a
  // running total climbing to its grand total at the right edge. Agents (a
  // concurrency count, not a flow) stays a per-bucket rate.
  const cumulative = historyState.chart !== 'agents';
  if (cumulative) for (let r = 0; r < matrix.length; r++) matrix[r] = historyRunningSum(matrix[r]);

  // Grand cumulative total = the stack's right-edge height; it anchors the
  // forecast when cumulative.
  let grandTotal = 0;
  for (const row of matrix) grandTotal += row[B - 1] || 0;
  const { H, fcY } = historyForecastSeries(data, cumulative, grandTotal);

  // Y scale = the tallest stacked column (sum across bands per bucket), also
  // covering the forecast points.
  const maxY = historyMaxY(matrix, projects, B, fcY);

  const padL = 46, padR = 12, padT = 12, padB = 22;
  const plotW = Math.max(1, w - padL - padR);
  const plotH = Math.max(1, h - padT - padB);
  const N = B + H;
  const xAt = (i) => (N <= 1 ? padL : padL + plotW * (i / (N - 1)));
  const yAt = (v) => padT + plotH * (1 - v / maxY);

  // Shared canvas geometry (context + coordinate mappers) every draw* helper
  // below needs; the rest of each call is data specific to that helper
  // (javascript:S107 — bundling this alone dropped each from 8-9 params to 2).
  const geo = { ctx, xAt, yAt };

  // Y gridlines + dollar labels (drawn first, behind the areas).
  drawHistoryGridlines(geo, { w, padL, padR, muted, gridColor, maxY });

  // Stacked areas, bottom-up.
  drawHistoryStackedAreas(ctx, projects, matrix, B, xAt, yAt);

  // Forecast: a dashed line into the future.
  drawHistoryForecastLine(geo, { B, H, cumulative, grandTotal, fcY, waiting });

  // CO2 equivalents: red dotted reference lines for relatable everyday
  // activities (issue #952) — meaningless for any other chart.
  if (historyState.chart === 'co2') drawHistoryCO2Equivalents(geo, { w, padL, padR, padT, maxY, danger });

  // X axis time labels.
  drawHistoryXAxisLabels(geo, { buckets, B, bucketSeconds: data.bucket_seconds, muted, h, padB });
}

// --- Autonomy (#1905) ---
//
// Two elements over the always-on span log: a percentile line chart of
// autonomous run duration over time (drawn on the shared canvas, like every
// other time series here), and a per-project run strip (its own DOM section,
// one canvas per project row).
//
// An autonomy span is one unbroken stretch of `working`; the state the session
// left `working` FOR is the signal both elements carry.

// autonomyEverRecorded reports whether the span log holds anything at all,
// anywhere — the fact that separates "no runs in this range" from "this
// feature has not collected anything yet". Both are empty views; only one of
// them means the user did nothing.
export function autonomyEverRecorded(state = historyState) {
  const d = state.autonomyData;
  if (!d) return false;
  return (d.duration?.total_recorded || 0) > 0 || (d.spans?.total_recorded || 0) > 0;
}

// autonomyDuration formats a run length: "41s", "11m", "1h58m", "2d3h".
export function autonomyDuration(seconds) {
  const s = Math.round(Number(seconds) || 0);
  if (s < 60) return s + 's';
  if (s < 3600) {
    const m = Math.floor(s / 60), rem = s % 60;
    return rem === 0 ? m + 'm' : m + 'm' + rem + 's';
  }
  if (s < 86400) {
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
    return m === 0 ? h + 'h' : h + 'h' + m + 'm';
  }
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600);
  return h === 0 ? d + 'd' : d + 'd' + h + 'h';
}

// autonomyProvenanceLine states when collection started, so an empty or short
// history is never read as "you did nothing" (#1905). This sentence is part of
// the feature, not decoration.
export function autonomyProvenanceLine(duration) {
  const earliest = Number(duration?.earliest_span) || 0;
  const total = Number(duration?.total_recorded) || 0;
  if (!earliest) {
    return 'No autonomous runs recorded yet. Irrlicht began measuring them with this update — '
      + 'an empty chart means "nothing recorded", not "nothing happened".';
  }
  const since = new Date(earliest * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
  return 'Collecting since ' + since + ' · ' + total + ' runs recorded.';
}

// collapseAutonomyStrip is the strip's pixel-collapse rule (#1905 design
// decision 4), as a pure function so it can be tested without a canvas — and
// so this and the macOS AutonomyStripLayout draw the same strip.
//
// TWO HALVES, both load-bearing:
//   - Every span draws at a minimum of ONE column. At 12mo a 40-second run is
//     far under one device pixel; rounding it away would erase exactly the
//     short runs the p5 line is about.
//   - A column holding several spans takes the HIGHEST-priority end reason
//     (AUTONOMY_REASON_PRIORITY). One error in a column paints the whole
//     column, because what is worth seeing at a glance is that something broke.
//
// Returns one {occupied, reason} per column. occupied and reason are separate
// because a run whose reason this build cannot name still HAPPENED: it holds
// its column (drawn neutral) rather than reading as idle.
export function collapseAutonomyStrip(spans, start, end, columns) {
  if (columns <= 0 || end <= start) return [];
  const out = [];
  for (let i = 0; i < columns; i++) out.push({ occupied: false, reason: null });
  for (const sp of (spans || [])) paintSpanColumns(out, sp, start, end, columns);
  return out;
}

// paintSpanColumns writes one span into the column array, applying both halves
// of the rule: the span's column range (never empty — the minimum-one-column
// rule) and the ladder that decides a shared column's color.
function paintSpanColumns(out, sp, start, end, columns) {
  const width = end - start;
  let first = Math.floor((sp.start - start) / width * columns);
  let last = Math.floor((sp.end - start) / width * columns);
  if (last < 0 || first > columns - 1) return; // wholly outside the window
  first = Math.max(0, first);
  last = Math.min(columns - 1, last);
  if (last < first) last = first; // the minimum-one-column rule
  const reason = AUTONOMY_REASON_PRIORITY[sp.reason] ? sp.reason : null;
  const rank = AUTONOMY_REASON_PRIORITY[sp.reason] || 0;
  for (let i = first; i <= last; i++) {
    // -1 for an untouched column, so even an unnamed reason (rank 0) claims it.
    const existingRank = out[i].occupied ? (AUTONOMY_REASON_PRIORITY[out[i].reason] || 0) : -1;
    out[i].occupied = true;
    if (rank > existingRank) out[i].reason = reason;
  }
}

// autonomyReasonColor maps an end reason onto the shared state palette. An
// unnamed reason draws neutral — the run happened, this build just cannot say
// how it ended.
function autonomyReasonColor(reason, cs) {
  const pick = (name, fallback) => (cs.getPropertyValue(name) || fallback).trim();
  if (reason === 'error') return pick('--pressure-high', '#FF3B30');
  if (reason === 'waiting') return pick('--waiting', '#FF9500');
  if (reason === 'ready') return pick('--ready', '#34C759');
  return pick('--muted', '#888');
}

// autonomyChartPoints turns the sparse bucket list into per-series point lists
// aligned to bucket_starts, with a null for every bucket the daemon OMITTED.
//
// The null is the honesty rule, not a convenience: an empty bucket is a GAP,
// and a day with no runs must not pull the line down to the axis. The painter
// below breaks the stroke at a null instead of interpolating through it.
export function autonomyChartPoints(duration) {
  const starts = duration?.bucket_starts || [];
  const byTs = new Map();
  for (const b of (duration?.buckets || [])) byTs.set(b.ts, b);
  return starts.map(ts => byTs.get(ts) || null);
}

// The three drawn lines, in draw order: series key, the CSS custom property
// that colours it, and the fallback for a stylesheet that has not loaded.
//
// EXPORTED, and read by both the canvas painter and the side panel's key
// swatches. The web shipped its first round with three unlabelled curves and a
// panel that explicitly blanked its dots, so a reader had to infer which line
// was which from vertical order. A key drawn from a SECOND colour table would
// be worse than none — it can disagree with the chart — so there is one table
// and one resolver.
export const AUTONOMY_SERIES = [
  ['p95', '--ready', '#34C759'],
  ['p50', '--working', '#8B5CF6'],
  ['p5', '--waiting', '#FF9500'],
];

// Panel labels for the three lines. Split from the colour table only because
// the canvas has no use for them.
const AUTONOMY_SERIES_LABELS = {
  p95: 'p95 · how long the good runs go',
  p50: 'p50 · the typical run',
  p5: 'p5 · how short the bad runs are',
};

// autonomySeriesColor resolves one line's colour against the live theme,
// falling back to the literal when the custom property is unset (an
// unstyled document, or a test's stub). Returns '' for a key that is not a
// drawn line, so a caller cannot silently paint a swatch for something the
// chart never drew.
export function autonomySeriesColor(key, cs) {
  const row = AUTONOMY_SERIES.find(([k]) => k === key);
  if (!row) return '';
  const [, cssVar, fallback] = row;
  return ((cs && cs.getPropertyValue(cssVar)) || fallback).trim();
}

function paintAutonomyChart() {
  const canvas = document.getElementById('history-chart');
  const wrap = document.getElementById('history-chart-wrap');
  if (!canvas || !wrap) return;
  const duration = historyState.autonomyData?.duration;
  const { ctx, w, h } = setupHistoryCanvas(canvas, wrap);
  const points = autonomyChartPoints(duration);
  const drawn = points.filter(Boolean);
  const hasData = drawn.length > 0;
  wrap.classList.toggle('empty', !hasData);
  if (!hasData) return;

  const cs = getComputedStyle(document.documentElement);
  const muted = (cs.getPropertyValue('--muted') || '#888').trim();
  const gridColor = 'rgba(128,140,170,0.18)';

  // Log Y: the interesting range is seconds to hours, so a linear axis spends
  // its whole height on the longest run. Floored at 1s — a log scale cannot
  // plot 0, and a sub-second span is not a run.
  const lo = Math.max(1, Math.min(...drawn.map(b => Math.max(1, b.p5))) * 0.8);
  const hi = Math.max(lo * 2, Math.max(...drawn.map(b => Math.max(1, b.p95))) * 1.25);
  const padL = 52, padR = 12, padT = 12, padB = 22;
  const plotW = Math.max(1, w - padL - padR);
  const plotH = Math.max(1, h - padT - padB);
  const n = Math.max(1, points.length);
  const xAt = (i) => (n <= 1 ? padL : padL + plotW * (i / (n - 1)));
  const yAt = (v) => {
    const clamped = Math.min(hi, Math.max(lo, Math.max(1, v)));
    const t = (Math.log(clamped) - Math.log(lo)) / (Math.log(hi) - Math.log(lo));
    return padT + plotH * (1 - t);
  };

  drawAutonomyGridlines(ctx, { lo, hi, yAt, padL, padR, w, muted, gridColor });
  for (const [key] of AUTONOMY_SERIES) {
    drawAutonomySeries(ctx, { points, key, color: autonomySeriesColor(key, cs), xAt, yAt });
  }
  drawAutonomyXLabels(ctx, { duration, xAt, muted, h, padB });
}

// drawAutonomyGridlines draws the log-scale Y gridlines, labelled in the same
// duration units the summary row uses, so an axis tick and a headline figure
// can never be read in different units.
function drawAutonomyGridlines(ctx, { lo, hi, yAt, padL, padR, w, muted, gridColor }) {
  ctx.strokeStyle = gridColor;
  ctx.fillStyle = muted;
  ctx.font = '10px ui-monospace, monospace';
  ctx.textAlign = 'right';
  ctx.textBaseline = 'middle';
  const decades = 4;
  for (let i = 0; i <= decades; i++) {
    const v = Math.exp(Math.log(lo) + (Math.log(hi) - Math.log(lo)) * (i / decades));
    const y = yAt(v);
    ctx.beginPath();
    ctx.moveTo(padL, y);
    ctx.lineTo(w - padR, y);
    ctx.stroke();
    ctx.fillText(autonomyDuration(v), padL - 6, y);
  }
}

function drawAutonomyXLabels(ctx, { duration, xAt, muted, h, padB }) {
  ctx.fillStyle = muted;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'top';
  const starts = duration.bucket_starts || [];
  const labels = Math.min(5, starts.length);
  for (let i = 0; i < labels; i++) {
    const c = Math.round(i * (starts.length - 1) / Math.max(1, labels - 1));
    ctx.fillText(histAxisLabel(starts[c], duration.bucket_seconds), xAt(c), h - padB + 5);
  }
}

// drawAutonomySeries strokes one percentile line, breaking at every omitted
// bucket and DASHING any segment that touches a thin bucket — so a bucket
// under the sample floor is visibly different rather than hidden or smoothed.
function drawAutonomySeries(ctx, { points, key, color, xAt, yAt }) {
  ctx.strokeStyle = color;
  ctx.lineWidth = 1.6;
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1], b = points[i];
    if (!a || !b) continue; // a gap is a gap: never interpolate across it
    ctx.save();
    ctx.setLineDash(a.thin || b.thin ? [3, 3] : []);
    ctx.globalAlpha = (a.thin || b.thin) ? 0.55 : 1;
    ctx.beginPath();
    ctx.moveTo(xAt(i - 1), yAt(a[key]));
    ctx.lineTo(xAt(i), yAt(b[key]));
    ctx.stroke();
    ctx.restore();
  }
  // Hollow markers on thin buckets, and a solid dot on an isolated bucket that
  // has no neighbour to draw a segment to (which would otherwise vanish).
  for (let i = 0; i < points.length; i++) {
    const b = points[i];
    if (!b) continue;
    const isolated = !points[i - 1] && !points[i + 1];
    if (!b.thin && !isolated) continue;
    ctx.beginPath();
    ctx.arc(xAt(i), yAt(b[key]), 2.5, 0, Math.PI * 2);
    if (b.thin) {
      ctx.strokeStyle = color;
      ctx.globalAlpha = 0.7;
      ctx.stroke();
      ctx.globalAlpha = 1;
    } else {
      ctx.fillStyle = color;
      ctx.fill();
    }
  }
}

// renderAutonomyStrip draws element 2: one row per project, ordered by the
// daemon (busiest first), each a canvas of collapsed columns.
function renderAutonomyStrip() {
  const rowsEl = document.getElementById('history-autonomy-rows');
  const titleEl = document.getElementById('history-autonomy-strip-title');
  const legendEl = document.getElementById('history-autonomy-legend');
  const noteEl = document.getElementById('history-autonomy-note');
  const spans = historyState.autonomyData?.spans;
  if (titleEl) titleEl.textContent = 'Runs · last ' + (AUTONOMY_SPAN_LABELS[historyState.autonomySpan] || historyState.autonomySpan);
  if (!rowsEl) return;
  rowsEl.innerHTML = '';
  if (legendEl) legendEl.innerHTML = '';
  if (noteEl) noteEl.textContent = '';

  const projects = spans?.projects || [];
  if (!projects.length) {
    rowsEl.appendChild(buildAutonomyStripEmpty(spans));
    return;
  }

  const cs = getComputedStyle(document.documentElement);
  // A header over the value column: the figure at the end of each row was a
  // bare duration with nothing saying what it measured.
  rowsEl.appendChild(buildAutonomyStripHeader());
  for (const project of projects) {
    rowsEl.appendChild(buildAutonomyStripRow(project, spans, cs));
  }
  // …and the window's bounds under it, so a mark can be placed in time. Two
  // labels, not a tick axis: at 12mo a full axis is more furniture than the
  // strip is worth, but "from when to when" is the difference between a
  // timeline and a texture.
  rowsEl.appendChild(buildAutonomyStripAxis(spans));
  if (legendEl) fillAutonomyLegend(legendEl, cs);
  if (noteEl && spans.truncated) {
    noteEl.textContent = 'This window holds more runs than one request returns; the strip shows the oldest part of it. '
      + 'Pick a shorter span for a complete picture.';
  }
}

// buildAutonomyStripEmpty renders the strip's empty state. Two wordings, and
// the distinction is the honesty rule: "no runs in this window" and "nothing
// has ever been recorded" are different claims, and only the second one is
// about the feature rather than about the user.
function buildAutonomyStripEmpty(spans) {
  const empty = document.createElement('div');
  empty.className = 'history-autonomy-empty';
  const label = AUTONOMY_SPAN_LABELS[historyState.autonomySpan] || historyState.autonomySpan;
  empty.textContent = autonomyEverRecorded()
    ? 'No runs in the last ' + label + '. ' + (spans?.total_recorded || 0)
      + ' runs are on record over a longer period.'
    : 'No runs recorded yet — this strip fills in as sessions run.';
  return empty;
}

function fillAutonomyLegend(legendEl, cs) {
  for (const [reason, glyph, label] of AUTONOMY_REASON_LEGEND) {
    const item = document.createElement('span');
    item.className = 'history-autonomy-legend-item';
    const swatch = document.createElement('i');
    swatch.style.background = autonomyReasonColor(reason, cs);
    item.appendChild(swatch);
    item.appendChild(document.createTextNode(glyph + ' ' + label));
    legendEl.appendChild(item);
  }
}

// buildAutonomyStripHeader labels the value column. It reuses the row grid so
// the header sits exactly over the values it names.
export function buildAutonomyStripHeader() {
  const head = document.createElement('div');
  head.className = 'history-autonomy-row history-autonomy-head';
  head.setAttribute('aria-hidden', 'true'); // each row's aria-label already says "longest"
  const label = document.createElement('span');
  label.className = 'history-autonomy-label';
  label.textContent = 'project';
  const spacer = document.createElement('span');
  const val = document.createElement('span');
  val.className = 'history-autonomy-val';
  val.textContent = 'longest';
  head.appendChild(label);
  head.appendChild(spacer);
  head.appendChild(val);
  return head;
}

// buildAutonomyStripAxis puts the window's start on the left and "now" on the
// right, under the strip, on the same grid so both land under the canvas
// column rather than under the labels.
export function buildAutonomyStripAxis(spans) {
  const axis = document.createElement('div');
  axis.className = 'history-autonomy-row history-autonomy-axis';
  const label = document.createElement('span');
  const bounds = document.createElement('span');
  bounds.className = 'history-autonomy-axis-bounds';
  const from = document.createElement('i');
  from.textContent = autonomyAxisLabel(spans.start, (spans.end || 0) - (spans.start || 0));
  const to = document.createElement('i');
  to.textContent = 'now';
  bounds.appendChild(from);
  bounds.appendChild(to);
  const tail = document.createElement('span');
  axis.appendChild(label);
  axis.appendChild(bounds);
  axis.appendChild(tail);
  return axis;
}

// autonomyAxisLabel formats the strip's left bound, coarsening with the window
// the way stateBucketLabel does for the activity matrix: an 8h strip needs a
// time of day, a 12mo strip needs a month.
export function autonomyAxisLabel(ts, windowSeconds) {
  const d = new Date((Number(ts) || 0) * 1000);
  if (windowSeconds <= 36 * 3600) return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  if (windowSeconds <= 60 * 86400) return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  return d.toLocaleDateString(undefined, { month: 'short', year: 'numeric' });
}

function buildAutonomyStripRow(project, spans, cs) {
  const row = document.createElement('div');
  row.className = 'history-autonomy-row';

  const label = document.createElement('span');
  label.className = 'history-autonomy-label';
  label.textContent = project;
  row.appendChild(label);

  const mine = (spans.spans || []).filter(sp => sp.project === project);
  const longest = mine.reduce((m, sp) => Math.max(m, (sp.end || 0) - (sp.start || 0)), 0);

  const canvas = document.createElement('canvas');
  canvas.className = 'history-autonomy-canvas';
  canvas.setAttribute('role', 'img');
  canvas.setAttribute('aria-label', project + ': ' + mine.length + ' runs, longest ' + autonomyDuration(longest));
  row.appendChild(canvas);

  const val = document.createElement('span');
  val.className = 'history-autonomy-val';
  val.textContent = autonomyDuration(longest);
  row.appendChild(val);

  // Painted on the next frame: the canvas has no layout width until it is in
  // the document, and a zero-width canvas would collapse every span into
  // nothing — the exact failure the minimum-one-column rule exists to prevent.
  requestAnimationFrame(() => paintAutonomyStripRow(canvas, mine, spans, cs));
  return row;
}

function paintAutonomyStripRow(canvas, mine, spans, cs) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.offsetWidth || 300;
  const h = canvas.offsetHeight || 14;
  const pxW = Math.max(1, Math.round(w * dpr)), pxH = Math.max(1, Math.round(h * dpr));
  if (canvas.width !== pxW || canvas.height !== pxH) { canvas.width = pxW; canvas.height = pxH; }
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  ctx.setTransform(1, 0, 0, 1, 0, 0); // draw in DEVICE pixels: one column = one pixel
  ctx.clearRect(0, 0, pxW, pxH);
  const cells = collapseAutonomyStrip(mine, spans.start, spans.end, pxW);
  for (let i = 0; i < cells.length; i++) {
    if (!cells[i].occupied) continue;
    ctx.fillStyle = autonomyReasonColor(cells[i].reason, cs);
    ctx.fillRect(i, 0, 1, pxH);
  }
}

// autonomyPanelRows builds the side panel's rows, INCLUDING each row's swatch
// colour, as a pure function.
//
// The swatch belongs here rather than at the DOM call site because that is
// exactly where the defect was: the panel used to build the p95/p50/p5 rows and
// then blank every dot, leaving three unlabelled curves on the canvas with no
// key at all. As a value it is testable; as a `style.background =` buried in a
// render loop it was not.
//
// The first three rows ARE the chart's lines, so they carry the chart's own
// colours (from AUTONOMY_SERIES, the same table the canvas strokes from) and
// double as its key — the macOS surface says the same thing through Swift
// Charts' legend. longest/shortest/runs are FIGURES, not lines, and stay
// unswatched on purpose: a swatch would claim a curve the chart deliberately
// does not draw.
export function autonomyPanelRows(summary, cs) {
  const s = summary || {};
  const line = (key, value) => ({
    series: key,
    label: AUTONOMY_SERIES_LABELS[key],
    value,
    swatch: autonomySeriesColor(key, cs),
  });
  const figure = (label, value) => ({ series: null, label, value, swatch: 'transparent' });
  return [
    line('p95', autonomyDuration(s.p95)),
    line('p50', autonomyDuration(s.p50)),
    line('p5', autonomyDuration(s.p5)),
    figure('longest', autonomyDuration(s.max)),
    figure('shortest', autonomyDuration(s.min)),
    figure('runs', String(s.count || 0)),
  ];
}

// renderAutonomyPanel fills the shared side panel with element 1's figures.
// The true extremes are FIGURES here and deliberately not lines on the chart:
// one overnight run would otherwise redraw the whole Y scale.
function renderAutonomyPanel() {
  const titleEl = document.getElementById('history-panel-title');
  const totalEl = document.getElementById('history-total');
  const fcEl = document.getElementById('history-forecast-line');
  const listEl = document.getElementById('history-contrib');
  const duration = historyState.autonomyData?.duration;
  if (titleEl) titleEl.textContent = 'Autonomy · ' + (AUTONOMY_RANGE_LABELS[historyState.autonomyRange] || historyState.autonomyRange);
  const s = duration?.summary || {};
  if (totalEl) totalEl.textContent = (s.count || 0) > 0 ? autonomyDuration(s.p50) : '—';
  if (fcEl) fcEl.textContent = (s.count || 0) > 0 ? 'median run · ' + s.count + ' runs' : '';
  if (!listEl) return;
  listEl.innerHTML = '';
  if (!(s.count > 0)) {
    appendHistoryEmpty(listEl, autonomyEverRecorded()
      ? 'no runs in this range'
      : 'nothing recorded yet');
  } else {
    for (const row of autonomyPanelRows(s, getComputedStyle(document.documentElement))) {
      const li = document.createElement('li');
      const dot = document.createElement('span');
      dot.className = 'dot';
      dot.style.background = row.swatch;
      const lbl = document.createElement('span');
      lbl.className = 'label';
      lbl.textContent = row.label;
      const val = document.createElement('span');
      val.className = 'val';
      val.textContent = row.value;
      li.appendChild(dot);
      li.appendChild(lbl);
      li.appendChild(val);
      listEl.appendChild(li);
    }
    const thin = (duration.buckets || []).filter(b => b.thin).length;
    if (thin > 0) {
      appendHistoryEmpty(listEl, thin + ' of ' + duration.buckets.length + ' buckets hold fewer than '
        + duration.sample_floor + ' runs (dashed, hollow): there p95 is that bucket’s longest run '
        + 'and p5 its shortest — not percentiles.');
    }
  }
  // The provenance line is part of the feature: "no data" must never read as
  // "you did nothing".
  appendHistoryEmpty(listEl, autonomyProvenanceLine(duration));
}

// --- Activity matrix (chart=state, issue #981) ---
// A grid — projects as rows, time buckets as columns — replacing the shared
// canvas's single continuous-time painter with its own scrollable DOM grid,
// since a matrix doesn't fit that shape (see renderStateMatrix). "No data
// recorded" vs. "zero activity" isn't distinguished per cell: the daemon has
// no persisted record of when --record was toggled per project, only the
// recordings it did capture, so that distinction isn't reliably derivable
// today — a zero-activity cell and a before-recording-started cell render
// identically (flat, no bar). Every cell's exact values stay reachable via
// the panel's peak/average/current summary, the per-cell tooltip/aria-label,
// and the existing CSV/JSON export buttons (chart-agnostic already).

const STATE_CELL_INNER_H = 26; // px — must match the bar's available height in irrlicht.css (.hsm-cell's grid row height minus .hsm-bar's bottom margin)

// stateCellCounts returns one (project, bucket-index) cell's per-state counts,
// one key per STATE_STACK_ORDER entry, defaulting missing entries to 0.
export function stateCellCounts(data, project, i) {
  const by = data?.by_state || {};
  const out = {};
  for (const state of STATE_STACK_ORDER) out[state] = (by[state]?.[project]?.[i]) || 0;
  return out;
}

// stateCellTotal sums one cell's per-state counts.
//
// Derived from the stack order rather than adding three named fields (#1801):
// the hand-written sum was a second enumeration that had to be kept in step
// with the first, and a state present in the chart but absent from the total
// would size every bar wrong.
export function stateCellTotal(data, project, i) {
  return sumCounts(stateCellCounts(data, project, i));
}

// sumCounts totals a per-state counts object over the stack order.
function sumCounts(counts) {
  let total = 0;
  for (const state of STATE_STACK_ORDER) total += counts[state] || 0;
  return total;
}

// stateMatrixMaxTotal finds the busiest single cell across the whole visible
// grid. The matrix's bar-height scale is global (comparable busyness across
// projects), not normalized per row.
export function stateMatrixMaxTotal(data) {
  const projects = data?.projects || [];
  const buckets = data?.bucket_starts || [];
  let max = 0;
  for (const project of projects) {
    for (let i = 0; i < buckets.length; i++) {
      const t = stateCellTotal(data, project, i);
      if (t > max) max = t;
    }
  }
  return max;
}

// stateBucketLabel formats one column header, coarsening the format as the
// granularity widens — a "60m" column needs a time-of-day, a "1y" column
// just needs the year.
export function stateBucketLabel(ts, granularity) {
  const d = new Date(ts * 1000);
  if (granularity === '1y') return String(d.getFullYear());
  if (granularity === '1mo' || granularity === '6mo') return d.toLocaleDateString(undefined, { month: 'short', year: '2-digit' });
  if (granularity === '7d' || granularity === '24h' || granularity === '8h') return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

function buildStateCornerCell() {
  const el = document.createElement('div');
  el.className = 'hsm-corner';
  return el;
}

function buildStateColLabel(text) {
  const el = document.createElement('div');
  el.className = 'hsm-col-label';
  el.textContent = text;
  return el;
}

function buildStateRowLabel(project) {
  const el = document.createElement('div');
  el.className = 'hsm-row-label';
  el.textContent = project;
  el.title = project;
  return el;
}

// buildStateCell renders one grid cell: a bottom-anchored stacked mini bar
// (one per STATE_STACK_ORDER entry, in that fixed bottom-to-top order) sized
// against maxTotal, with a hover/focus tooltip and an aria-label carrying the
// exact counts for anyone not using the tooltip.
function buildStateCell(project, ts, counts, maxTotal) {
  const cell = document.createElement('div');
  cell.className = 'hsm-cell';
  cell.tabIndex = 0;
  cell.setAttribute('role', 'img');
  cell.setAttribute('aria-label', project + ', ' + new Date(ts * 1000).toLocaleString() + ': ' +
    STATE_STACK.map(([state]) => counts[state] + ' ' + state).join(', '));

  const bar = document.createElement('div');
  bar.className = 'hsm-bar';
  const total = sumCounts(counts);
  if (total > 0) {
    bar.style.height = Math.max(3, Math.round((total / maxTotal) * STATE_CELL_INNER_H)) + 'px';
    let lastNonZero = null;
    for (const state of STATE_STACK_ORDER) if (counts[state] > 0) lastNonZero = state;
    for (const state of STATE_STACK_ORDER) {
      if (counts[state] <= 0) continue;
      const seg = document.createElement('div');
      seg.className = 'hsm-seg hsm-seg-' + state + (state === lastNonZero ? ' hsm-seg-cap' : '');
      seg.style.flexGrow = String(counts[state]);
      bar.appendChild(seg);
    }
  }
  cell.appendChild(bar);

  cell.addEventListener('pointerenter', () => showStateTooltip(cell, project, ts, counts));
  cell.addEventListener('focus', () => showStateTooltip(cell, project, ts, counts));
  cell.addEventListener('pointerleave', hideStateTooltip);
  cell.addEventListener('blur', hideStateTooltip);
  return cell;
}

// renderStateMatrix (re)builds the whole grid on every render — matrix sizes
// here (a handful of projects × tens of buckets) are small enough that a
// diffing update isn't worth the complexity every other History chart avoids
// too (paintHistoryChart also redraws from scratch each time).
function renderStateMatrix() {
  const data = historyState.data;
  const mount = document.getElementById('history-matrix');
  const scroll = document.getElementById('history-matrix-scroll');
  const wrap = document.getElementById('history-chart-wrap');
  if (!mount) return;
  const projects = data?.projects || [];
  const buckets = data?.bucket_starts || [];
  const hasData = projects.length > 0 && buckets.length > 0;
  if (wrap) wrap.classList.toggle('empty', !hasData);
  if (scroll) scroll.hidden = !hasData;
  mount.innerHTML = '';
  if (!hasData) return;

  mount.style.gridTemplateColumns = 'var(--hsm-row-label-w) repeat(' + buckets.length + ', var(--hsm-cell-w))';

  const maxTotal = stateMatrixMaxTotal(data) || 1;
  mount.appendChild(buildStateCornerCell());
  for (const ts of buckets) mount.appendChild(buildStateColLabel(stateBucketLabel(ts, historyState.granularity)));
  for (const project of projects) {
    mount.appendChild(buildStateRowLabel(project));
    buckets.forEach((ts, i) => mount.appendChild(buildStateCell(project, ts, stateCellCounts(data, project, i), maxTotal)));
  }
}

// renderStatePanel fills the side panel for the activity matrix: the same
// peak/avg/current summary shape the agents chart uses (working+waiting
// combined), plus a legend for the three-color stacked bar — the matrix has
// no separate contributor list since its rows already are the projects.
function renderStatePanel() {
  const data = historyState.data;
  const titleEl = document.getElementById('history-panel-title');
  const totalEl = document.getElementById('history-total');
  const fcEl = document.getElementById('history-forecast-line');
  const listEl = document.getElementById('history-contrib');
  if (titleEl) titleEl.textContent = 'Activity · ' + (GRANULARITY_LABELS[historyState.granularity] || historyState.granularity);
  const conc = data?.concurrency || { peak: 0, average: 0, current: 0 };
  if (totalEl) totalEl.textContent = histCount(conc.peak) + ' peak';
  if (fcEl) fcEl.textContent = 'avg ' + (Number(conc.average) || 0).toFixed(1) + ' · now ' + histCount(conc.current);
  if (!listEl) return;
  listEl.innerHTML = '';
  if (!(data?.projects || []).length) {
    appendHistoryEmpty(listEl, 'no agents in this range');
    return;
  }
  for (const [state, label] of STATE_STACK) {
    const li = document.createElement('li');
    const dot = document.createElement('span'); dot.className = 'dot hsm-seg-' + state;
    const lab = document.createElement('span'); lab.className = 'label'; lab.textContent = label;
    li.append(dot, lab);
    listEl.appendChild(li);
  }
}

function stateTooltipEl() {
  let el = document.getElementById('history-matrix-tooltip');
  if (!el) {
    el = document.createElement('div');
    el.id = 'history-matrix-tooltip';
    el.className = 'hsm-tooltip';
    document.body.appendChild(el);
  }
  return el;
}

// showStateTooltip shows the same detail on keyboard focus as on hover
// (per the interaction spec every hoverable chart mark should follow),
// anchored to the cell's own rect rather than pointer coordinates so focus
// (which carries no pointer position) positions identically to hover.
function showStateTooltip(cell, project, ts, counts) {
  const el = stateTooltipEl();
  el.innerHTML = '';
  const title = document.createElement('div'); title.className = 'hsm-tooltip-title'; title.textContent = project;
  const range = document.createElement('div'); range.className = 'hsm-tooltip-range'; range.textContent = new Date(ts * 1000).toLocaleString();
  el.append(title, range);
  for (const [state, label] of STATE_STACK) {
    const row = document.createElement('div'); row.className = 'hsm-tooltip-row';
    const key = document.createElement('span'); key.className = 'hsm-tooltip-key';
    const dot = document.createElement('i'); dot.className = 'hsm-tooltip-dot hsm-seg-' + state;
    key.appendChild(dot);
    key.appendChild(document.createTextNode(label));
    const val = document.createElement('span'); val.className = 'hsm-tooltip-val'; val.textContent = String(counts[state]);
    row.append(key, val);
    el.appendChild(row);
  }
  el.classList.add('show');
  positionStateTooltip(cell);
}

function positionStateTooltip(cell) {
  const el = document.getElementById('history-matrix-tooltip');
  if (!el || !el.classList.contains('show') || !cell) return;
  const rect = cell.getBoundingClientRect();
  const r = el.getBoundingClientRect();
  const pad = 6;
  let x = rect.right + pad, y = rect.top;
  if (x + r.width > window.innerWidth - 10) x = rect.left - r.width - pad;
  if (y + r.height > window.innerHeight - 10) y = window.innerHeight - r.height - 10;
  el.style.left = x + 'px';
  el.style.top = y + 'px';
}

function hideStateTooltip() {
  const el = document.getElementById('history-matrix-tooltip');
  if (el) el.classList.remove('show');
}

// buildHistoryStatRow builds one contributor list-item: colored dot, label,
// value — the shared shape behind every history-panel breakdown list.
function buildHistoryStatRow(i, label, value) {
  const li = document.createElement('li');
  const dot = document.createElement('span'); dot.className = 'dot'; dot.style.background = historyColorFor(i);
  const lab = document.createElement('span'); lab.className = 'label'; lab.textContent = label;
  const val = document.createElement('span'); val.className = 'val'; val.textContent = value;
  li.append(dot, lab, val);
  return li;
}

// renderAgentsPanel fills the side panel for the agents chart: concurrency
// summarizes as a peak headline + avg/current sub-line, and ranks the
// projects that ran the most agents at once. No forecast or drilldown —
// concurrency is reconstructed per project only.
function renderAgentsPanel(data, totalEl, fcEl, listEl) {
  const conc = data.concurrency || { peak: 0, average: 0, current: 0 };
  if (totalEl) totalEl.textContent = histCount(conc.peak) + ' peak';
  if (fcEl) fcEl.textContent = 'avg ' + (Number(conc.average) || 0).toFixed(1) + ' · now ' + histCount(conc.current);
  if (!listEl) return;
  listEl.innerHTML = '';
  const projects = data.top_contributors || [];
  if (!projects.length) {
    appendHistoryEmpty(listEl, 'no agents in this range');
    return;
  }
  projects.forEach((c, i) => listEl.appendChild(buildHistoryStatRow(i, c.label, histCount(c.value))));
}

// renderTokensPanel fills the side panel for the tokens chart: an
// input/output/cache breakdown, or — when grouping by token_type — the
// stacked bands themselves, listed with friendly labels.
function renderTokensPanel(data, listEl) {
  if (historyState.group === 'token_type') {
    const contribs = data.top_contributors || [];
    if (!contribs.length) {
      appendHistoryEmpty(listEl, 'no token usage in this range');
      return;
    }
    contribs.forEach((c, i) =>
      listEl.appendChild(buildHistoryStatRow(i, TOKEN_TYPE_LABEL[c.label] || c.label, histTokens(c.value))));
    return;
  }
  const split = data.token_split;
  if (!split || data.total <= 0) {
    appendHistoryEmpty(listEl, 'no token usage in this range');
    return;
  }
  [['Input', split.input], ['Output', split.output], ['Cache', split.cache]].forEach(([label, v], i) =>
    listEl.appendChild(buildHistoryStatRow(i, label, histTokens(v))));
}

// wireDrillableRow makes a contributor row clickable/keyboard-activatable to
// drill into it, scoping the view and re-grouping by the next finer axis.
function wireDrillableRow(li, drillField, label) {
  li.classList.add('drillable');
  li.tabIndex = 0;
  li.setAttribute('role', 'button');
  li.title = 'Drill into ' + label;
  const drill = () => drillInto(drillField, label);
  li.addEventListener('click', drill);
  li.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); drill(); } });
}

// renderContributorsPanel fills the default (cost/co2/models/providers)
// contributor ranking, wiring drilldown when the grouped axis supports it.
// The synthetic "unknown" bucket and leaf axes aren't drillable.
function renderContributorsPanel(data, listEl) {
  const contribs = data.top_contributors || [];
  if (!contribs.length) {
    appendHistoryEmpty(listEl, historyState.chart === 'co2' ? 'no CO2 estimate in this range' : 'no spend in this range');
    return;
  }
  const drillField = data.group;
  const drillable = !!DRILL_NEXT[drillField];
  contribs.forEach((c, i) => {
    const li = buildHistoryStatRow(i, c.label, histValue(c.value));
    if (drillable && c.label !== 'unknown') wireDrillableRow(li, drillField, c.label);
    listEl.appendChild(li);
  });
}

function renderHistoryPanel() {
  const data = historyState.data;
  if (!data) return;
  const titleEl = document.getElementById('history-panel-title');
  const totalEl = document.getElementById('history-total');
  const fcEl = document.getElementById('history-forecast-line');
  const listEl = document.getElementById('history-contrib');
  const chartLabel = CHART_LABELS[historyState.chart] || 'Total';
  if (titleEl) titleEl.textContent = chartLabel + ' · ' + (RANGE_LABELS[historyState.range] || historyState.range);

  if (historyState.chart === 'agents') {
    renderAgentsPanel(data, totalEl, fcEl, listEl);
    return;
  }

  if (totalEl) totalEl.textContent = histValue(data.total);
  if (fcEl) {
    // Forecast is USD-only; the daemon omits it for the tokens chart.
    fcEl.textContent = (historyState.forecast && data.forecast)
      ? '▲ projected ' + histDollar(data.forecast.projected) + ' (' + (data.forecast.basis || 'linear') + ')'
      : '';
  }
  if (!listEl) return;
  listEl.innerHTML = '';

  if (historyState.chart === 'tokens') {
    renderTokensPanel(data, listEl);
    return;
  }

  renderContributorsPanel(data, listEl);
}

function appendHistoryEmpty(listEl, text) {
  const li = document.createElement('li');
  li.className = 'history-empty-contrib';
  li.textContent = text;
  listEl.appendChild(li);
}

function historyFilterOptions(dim) {
  if (dim === 'token_type') return TOKEN_TYPE_OPTIONS;
  return (historyState.known[dim] || []).map(v => [v, v]);
}

// buildHistoryFilterOption renders one filter dropdown's checkbox row.
function buildHistoryFilterOption(dim, val, label, sel) {
  const lab = document.createElement('label');
  const cb = document.createElement('input');
  cb.type = 'checkbox'; cb.value = val; cb.checked = sel.has(val);
  cb.addEventListener('change', () => toggleHistoryFilter(dim, val, cb.checked));
  const span = document.createElement('span'); span.textContent = label;
  lab.append(cb, span);
  return lab;
}

// renderHistoryFilterDetail populates one dimension's <details> filter menu
// and its summary text.
function renderHistoryFilterDetail(det, dim, sel) {
  const menu = det.querySelector('.menu');
  if (menu) {
    menu.innerHTML = '';
    const opts = historyFilterOptions(dim);
    for (const [val, label] of opts) {
      menu.appendChild(buildHistoryFilterOption(dim, val, label, sel));
    }
    if (!opts.length) appendHistoryEmpty(menu, 'none seen yet');
  }
  const sum = det.querySelector('summary');
  const dimLabel = dim === 'token_type' ? 'Token type' : dim[0].toUpperCase() + dim.slice(1);
  if (sum) sum.textContent = dimLabel + ': ' + (sel.size ? sel.size + ' selected' : 'All');
}

// renderHistoryFilters repopulates the per-dimension filter dropdowns,
// hiding the dimension currently being grouped on (never both axis and
// filter) and the token_type filter outside the tokens metric.
function renderHistoryFilters() {
  const row = document.getElementById('history-filter-row');
  if (!row) return;
  for (const det of row.querySelectorAll('details.history-filter')) {
    const dim = det.dataset.dim;
    const hidden = dim === historyState.group || (dim === 'token_type' && historyState.chart !== 'tokens');
    det.hidden = hidden;
    if (hidden) { det.open = false; continue; }
    const sel = new Set(historyState.filters[dim] || []);
    renderHistoryFilterDetail(det, dim, sel);
  }
}

function toggleHistoryFilter(dim, val, on) {
  const cur = new Set(historyState.filters[dim] || []);
  if (on) cur.add(val); else cur.delete(val);
  historyState.filters[dim] = [...cur];
  historyState.scope = null; // a filter change invalidates any drilldown
  fetchHistory();
}

// drillInto re-scopes the view to one contributor and re-groups by the next
// finer axis (project → branch → session). Drilldown is cost-based, matching
// the "Cost · Day · grouped by Branch · scoped to X" example in #750.
function drillInto(field, value) {
  const next = DRILL_NEXT[field];
  if (!next) return;
  historyState.scope = { field, value };
  historyState.group = next;
  historyState.chart = 'cost';
  syncHistorySelectors();
  fetchHistory();
}

function renderHistoryBreadcrumb() {
  const el = document.getElementById('history-breadcrumb');
  if (!el) return;
  el.innerHTML = '';
  if (!historyState.scope) { el.hidden = true; return; }
  el.hidden = false;
  const all = document.createElement('button');
  all.type = 'button';
  all.className = 'history-crumb';
  all.textContent = 'All';
  all.addEventListener('click', clearHistoryDrilldown);
  const sep = document.createElement('span');
  sep.className = 'history-crumb-sep';
  sep.textContent = '›';
  const cur = document.createElement('span');
  cur.className = 'history-crumb current';
  cur.textContent = historyState.scope.field + ': ' + historyState.scope.value;
  el.append(all, sep, cur);
}

function clearHistoryDrilldown() {
  const field = historyState.scope ? historyState.scope.field : 'project';
  historyState.scope = null;
  historyState.group = field; // return to the axis we drilled from
  syncHistorySelectors();
  fetchHistory();
}

// syncActivityChartVisibility reflects the Activity beta toggle (#1075) onto the
// chart selector. The matrix is reconstructed from opt-in recordings, and a
// bucket with no recording renders identically to a genuinely idle one (see
// renderStateMatrix), so anyone not recording reads a grid of blanks as "idle"
// — misleading enough that it's off by default.
export function syncActivityChartVisibility(enabled) {
  const btn = document.querySelector('#history-chart-sel button[data-chart="state"]');
  if (btn) btn.hidden = !enabled;
}

// leaveActivityChartIfSelected backs out of chart=state when the gate is turned
// off underneath it — otherwise the view strands on a chart the setting says is
// off, with the matrix grid still up. No-op unless Activity is live, so it's
// only the toggle's own change handler that needs to call it. Same shape as
// drillInto.
export function leaveActivityChartIfSelected() {
  if (historyState.chart !== 'state') return;
  historyState.chart = 'cost';
  historyState.scope = null;
  syncHistorySelectors();
  fetchHistory();
}

// syncHistorySelectors reflects historyState.chart/group onto the segmented
// controls — drilldown and the models/providers presets change them
// programmatically, so the active classes must follow.
function syncHistorySelectors() {
  const chartSeg = document.getElementById('history-chart-sel');
  if (chartSeg) for (const b of chartSeg.querySelectorAll('button')) b.classList.toggle('active', b.dataset.chart === historyState.chart);
  const metricsSeg = document.getElementById('history-metrics-sel');
  if (metricsSeg) for (const b of metricsSeg.querySelectorAll('button')) b.classList.toggle('active', b.dataset.chart === historyState.chart);
  const autonomySeg = document.getElementById('history-autonomy-sel');
  if (autonomySeg) for (const b of autonomySeg.querySelectorAll('button')) b.classList.toggle('active', b.dataset.chart === historyState.chart);
  const groupSeg = document.getElementById('history-group-sel');
  if (groupSeg) for (const b of groupSeg.querySelectorAll('button')) b.classList.toggle('active', b.dataset.group === historyState.group);
}

// Yield chart (#373): one horizontal bar per project, split productive
// (green) vs reverted (red), bar length ∝ the project's attributable spend.
// Yield is a per-project aggregate, not a time series, so it draws its own
// shape on the shared canvas rather than reusing the stacked-area painter.
function histTruncate(s, n) { s = String(s); return s.length > n ? s.slice(0, n - 1) + '…' : s; }

function paintYieldChart() {
  const canvas = document.getElementById('history-chart');
  const wrap = document.getElementById('history-chart-wrap');
  if (!canvas || !wrap) return;
  const data = historyState.data;
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.offsetWidth || wrap.clientWidth || 600;
  const h = canvas.offsetHeight || 340;
  const pxW = Math.round(w * dpr), pxH = Math.round(h * dpr);
  if (canvas.width !== pxW || canvas.height !== pxH) { canvas.width = pxW; canvas.height = pxH; }
  const ctx = canvas.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);

  const projects = data?.projects || [];
  // Only projects with attributable (productive+reverted) spend get a bar;
  // unknown-only projects contribute nothing to the ratio.
  const rows = projects.filter(p => (p.total_cost || 0) > 0);
  const hasData = rows.length > 0;
  wrap.classList.toggle('empty', !hasData);
  if (!hasData) return;

  const cs = getComputedStyle(document.documentElement);
  const green = (cs.getPropertyValue('--ready') || '#34C759').trim();
  const red = (cs.getPropertyValue('--pressure-high') || '#FF3B30').trim();
  const muted = (cs.getPropertyValue('--muted') || '#888').trim();
  const bright = (cs.getPropertyValue('--text-bright') || '#fff').trim();

  const maxTotal = rows.reduce((m, p) => Math.max(m, p.total_cost), 0) || 1;
  const padL = 8, padR = 10, padT = 10, padB = 8;
  const labelH = 15, barH = 14, gap = 11;
  const blockH = labelH + barH + gap;
  const plotW = Math.max(1, w - padL - padR);
  const maxRows = Math.max(1, Math.floor((h - padT - padB) / blockH));
  const shown = rows.slice(0, maxRows);

  let y = padT;
  for (const p of shown) {
    const total = p.total_cost || 0;
    const fullW = plotW * (total / maxTotal);
    const prodW = total > 0 ? fullW * ((p.productive_cost || 0) / total) : 0;
    ctx.font = '11px ui-monospace, monospace';
    ctx.textBaseline = 'alphabetic';
    ctx.textAlign = 'left';
    ctx.fillStyle = bright;
    ctx.fillText(histTruncate(p.project, 26), padL, y + 11);
    ctx.textAlign = 'right';
    ctx.fillStyle = muted;
    ctx.fillText(Math.round((p.yield || 0) * 100) + '% · ' + histDollar(p.productive_cost) + ' / ' + histDollar(total), w - padR, y + 11);
    const by = y + labelH;
    ctx.fillStyle = green;
    ctx.fillRect(padL, by, prodW, barH);
    ctx.fillStyle = red;
    ctx.fillRect(padL + prodW, by, Math.max(0, fullW - prodW), barH);
    y += blockH;
  }
}

function renderYieldPanel() {
  const data = historyState.data;
  if (!data) return;
  const titleEl = document.getElementById('history-panel-title');
  const totalEl = document.getElementById('history-total');
  const fcEl = document.getElementById('history-forecast-line');
  const listEl = document.getElementById('history-contrib');
  if (titleEl) titleEl.textContent = 'Yield · ' + (RANGE_LABELS[historyState.range] || historyState.range);
  const hasSpend = (data.total_cost || 0) > 0 || (data.unknown_cost || 0) > 0;
  if (totalEl) totalEl.textContent = (data.total_cost || 0) > 0 ? Math.round((data.yield || 0) * 100) + '%' : '—';
  if (fcEl) {
    let line = histDollar(data.productive_cost) + ' productive of ' + histDollar(data.total_cost) + ' total';
    if ((data.unknown_cost || 0) > 0) line += ' · ' + histDollar(data.unknown_cost) + ' unattributed';
    fcEl.textContent = hasSpend ? line : '';
  }
  if (!listEl) return;
  listEl.innerHTML = '';
  const projects = (data.projects || []).filter(p => (p.total_cost || 0) > 0 || (p.unknown_cost || 0) > 0);
  if (!projects.length) {
    const li = document.createElement('li');
    li.className = 'history-empty-contrib';
    li.textContent = 'no completed sessions in this range';
    listEl.appendChild(li);
    return;
  }
  projects.forEach((p) => {
    const li = document.createElement('li');
    const dot = document.createElement('span');
    dot.className = 'dot';
    dot.style.background = (p.reverted_cost || 0) > 0 ? red() : green();
    const label = document.createElement('span');
    label.className = 'label';
    label.textContent = p.project + ((p.reverted_count || 0) > 0 ? ' ↩' + p.reverted_count : '');
    const val = document.createElement('span');
    val.className = 'val';
    val.textContent = (p.total_cost || 0) > 0 ? Math.round((p.yield || 0) * 100) + '%' : '—';
    li.appendChild(dot);
    li.appendChild(label);
    li.appendChild(val);
    listEl.appendChild(li);
  });
  function green() { return (getComputedStyle(document.documentElement).getPropertyValue('--ready') || '#34C759').trim(); }
  function red() { return (getComputedStyle(document.documentElement).getPropertyValue('--pressure-high') || '#FF3B30').trim(); }
}

// syncDoraProjectRow shows/hides the DORA project picker and refreshes its
// option list from known.project — called on every render so a project
// discovered after switching to DORA still shows up (#951).
function syncDoraProjectRow() {
  const row = document.getElementById('history-dora-row');
  if (row) row.hidden = historyState.chart !== 'dora';
  const sel = document.getElementById('history-dora-project');
  if (!sel) return;
  const known = historyState.known.project || [];
  const current = sel.value;
  sel.innerHTML = '<option value="">Select a project…</option>';
  for (const p of known) {
    const opt = document.createElement('option');
    opt.value = p;
    opt.textContent = p;
    sel.appendChild(opt);
  }
  sel.value = known.includes(current) ? current : (historyState.doraProject || '');
}

// DORA metrics (#951): a per-project period summary, not a time series — no
// canvas, no bucket series. All four metrics render as rows in the side
// panel's contributor list, mirroring the panel shape yield/cost already use.
function renderDoraPanel() {
  const titleEl = document.getElementById('history-panel-title');
  const totalEl = document.getElementById('history-total');
  const fcEl = document.getElementById('history-forecast-line');
  const listEl = document.getElementById('history-contrib');
  const project = historyState.doraProject;
  if (titleEl) titleEl.textContent = 'DORA' + (project ? ' · ' + project : '') + ' · ' + (RANGE_LABELS[historyState.range] || historyState.range);
  if (totalEl) totalEl.textContent = '';
  if (fcEl) fcEl.textContent = '';
  if (!listEl) return;
  listEl.innerHTML = '';

  const data = historyState.data;
  if (!project) {
    appendHistoryEmpty(listEl, 'select a project above');
    return;
  }
  if (!data) {
    appendHistoryEmpty(listEl, 'loading…');
    return;
  }
  if (!data.available) {
    appendHistoryEmpty(listEl, data.message || 'not enough data to compute DORA metrics');
    return;
  }
  const rows = [
    ['Deployment Frequency', data.deployment_frequency, histDoraPerWeek],
    ['Lead Time for Changes', data.lead_time, histDoraHours],
    ['Change Failure Rate', data.change_failure_rate, histDoraPercent],
    ['Mean Time to Restore', data.mttr, histDoraHours],
  ];
  for (const [label, metric, format] of rows) {
    const li = document.createElement('li');
    const dot = document.createElement('span');
    dot.className = 'dot';
    dot.style.background = 'transparent';
    const lbl = document.createElement('span');
    lbl.className = 'label';
    lbl.textContent = label;
    const val = document.createElement('span');
    val.className = 'val';
    val.textContent = metric?.available ? format(metric.value) : (metric?.message || 'n/a');
    li.appendChild(dot);
    li.appendChild(lbl);
    li.appendChild(val);
    listEl.appendChild(li);
  }
}

function historyDownload(filename, mime, text) {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}
function historyCsvCell(s) {
  s = String(s);
  return /[",\n]/.test(s) ? '"' + s.replaceAll('"', '""') + '"' : s;
}
// Each *CsvLines builder returns the full CSV for one chart family, header
// first. HISTORY_CSV_BUILDERS maps a chart to its builder; anything not listed
// exports the plain bucket/project/value time series.
function yieldCsvLines(d) {
  const lines = ['project,productive_cost,reverted_cost,unknown_cost,total_cost,yield,reverted_count'];
  for (const p of (d.projects || [])) {
    lines.push([
      historyCsvCell(p.project),
      (p.productive_cost || 0).toFixed(6), (p.reverted_cost || 0).toFixed(6),
      (p.unknown_cost || 0).toFixed(6), (p.total_cost || 0).toFixed(6),
      (p.yield || 0).toFixed(6), String(p.reverted_count || 0),
    ].join(','));
  }
  return lines;
}

// stateCsvLines exports the activity matrix.
//
// Header and rows both come from STATE_STACK_ORDER (#1801), so the columns
// cannot disagree with each other or with the chart. The header GAINS an
// `error` column, which is a deliberate change to what the export looks like:
// the alternative is an export that silently omits a state the chart shows,
// and a spreadsheet whose columns no longer sum to what the tooltip says.
function stateCsvLines(d) {
  const lines = [['bucket_start', 'project', ...STATE_STACK_ORDER].join(',')];
  const by = d.by_state || {};
  for (const project of (d.projects || [])) {
    (d.bucket_starts || []).forEach((ts, i) => {
      const cells = STATE_STACK_ORDER.map(state => by[state]?.[project]?.[i] || 0);
      lines.push([new Date(ts * 1000).toISOString(), historyCsvCell(project), ...cells].join(','));
    });
  }
  return lines;
}

function doraCsvLines(d) {
  const lines = ['metric,value,unit,sample_size,available,message'];
  const rows = [
    ['deployment_frequency', d.deployment_frequency],
    ['lead_time', d.lead_time],
    ['change_failure_rate', d.change_failure_rate],
    ['mttr', d.mttr],
  ];
  for (const [name, m] of rows) {
    lines.push([
      name, m ? String(m.value) : '', m ? historyCsvCell(m.unit) : '',
      m ? String(m.sample_size) : '', m ? String(!!m.available) : '',
      m?.message ? historyCsvCell(m.message) : '',
    ].join(','));
  }
  return lines;
}

function seriesCsvLines(d) {
  const lines = ['bucket_start,project,value'];
  for (const pt of (d.series || [])) {
    lines.push([new Date(pt.ts * 1000).toISOString(), historyCsvCell(pt.project), pt.value.toFixed(6)].join(','));
  }
  return lines;
}

// autonomyCsvLines exports the spans themselves — the raw rows both elements
// are derived from, so a spreadsheet can re-derive either.
function autonomyCsvLines() {
  const spans = historyState.autonomyData?.spans?.spans || [];
  const lines = ['start,end,duration_seconds,project,session,reason'];
  for (const sp of spans) {
    lines.push([
      new Date(sp.start * 1000).toISOString(), new Date(sp.end * 1000).toISOString(),
      String(Math.max(0, (sp.end || 0) - (sp.start || 0))),
      historyCsvCell(sp.project || ''), historyCsvCell(sp.session || ''),
      historyCsvCell(sp.reason || ''),
    ].join(','));
  }
  return lines;
}

const HISTORY_CSV_BUILDERS = {
  yield: yieldCsvLines,
  state: stateCsvLines,
  dora: doraCsvLines,
  autonomy: autonomyCsvLines,
};

function exportHistoryCSV() {
  const d = historyState.data;
  if (!d) return;
  const build = HISTORY_CSV_BUILDERS[historyState.chart] || seriesCsvLines;
  historyDownload('irrlicht-history-' + historyState.range + '-' + historyState.chart + '.csv', 'text/csv;charset=utf-8', build(d).join('\n') + '\n');
}
function exportHistoryJSON() {
  // Autonomy exports BOTH payloads: either alone is a partial answer.
  const d = historyState.chart === 'autonomy' ? historyState.autonomyData : historyState.data;
  if (!d) return;
  historyDownload('irrlicht-history-' + historyState.range + '-' + historyState.chart + '.json', 'application/json', JSON.stringify(d, null, 2));
}


// initHistoryTab wires the History tab's controls and restores the tab if it
// was active last session. Called once from irrlicht.js's top-level init, in
// the same relative position as this code used to run inline.
export function initHistoryTab() {
  const histToggleBtn = document.getElementById('history-tab-toggle');
  if (histToggleBtn) histToggleBtn.addEventListener('click', () => setHistoryTab(!historyTabOn()));

  const histRangeSeg = document.getElementById('history-range');
  if (histRangeSeg) histRangeSeg.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-range]');
    if (!b) return;
    for (const x of histRangeSeg.querySelectorAll('button')) x.classList.toggle('active', x === b);
    const r = b.dataset.range;
    const custom = document.getElementById('history-custom');
    if (r === 'custom') { if (custom) { custom.hidden = false; } return; } // wait for Apply
    if (custom) custom.hidden = true;
    historyState.range = r;
    historyState.start = null;
    historyState.end = null;
    fetchHistory();
  });

  const histApplyBtn = document.getElementById('history-custom-apply');
  if (histApplyBtn) histApplyBtn.addEventListener('click', () => {
    const sv = document.getElementById('history-start').value;
    const ev = document.getElementById('history-end').value;
    if (!sv || !ev) return;
    const start = Math.floor(new Date(sv + 'T00:00:00').getTime() / 1000);
    const end = Math.floor(new Date(ev + 'T00:00:00').getTime() / 1000) + 86400; // include the end day
    if (end <= start) return;
    historyState.range = 'custom';
    historyState.start = start;
    historyState.end = end;
    fetchHistory();
  });

  // Shared by both chart-button groups (#history-chart-sel and the
  // #history-metrics-sel Yield/DORA group, #951) — same data-chart
  // attribute, same effect either way.
  const handleChartClick = (e) => {
    const b = e.target.closest('button[data-chart]');
    if (!b || b.disabled) return;
    const c = b.dataset.chart;
    historyState.chart = c;
    // models/providers are presets that pin the stacking axis; agents is
    // reconstructed per project only.
    if (c === 'models') historyState.group = 'model';
    else if (c === 'providers') historyState.group = 'provider';
    else if (c === 'agents' || c === 'state') historyState.group = 'project'; // recordings carry no other axis
    else if (c === 'autonomy') historyState.group = 'project'; // spans carry no other axis
    else if (c !== 'tokens' && historyState.group === 'token_type') historyState.group = 'project'; // token_type needs the tokens metric
    historyState.scope = null; // a new metric resets any drilldown
    syncHistorySelectors();
    fetchHistory();
  };
  const histChartSeg = document.getElementById('history-chart-sel');
  if (histChartSeg) histChartSeg.addEventListener('click', handleChartClick);
  const histMetricsSeg = document.getElementById('history-metrics-sel');
  if (histMetricsSeg) histMetricsSeg.addEventListener('click', handleChartClick);
  const histAutonomySeg = document.getElementById('history-autonomy-sel');
  if (histAutonomySeg) histAutonomySeg.addEventListener('click', handleChartClick);

  // The two Autonomy pickers. Each re-fetches only because the section shows
  // both elements together; the daemon serves them as two independent charts.
  const wireAutonomyPicker = (id, attr, key) => {
    const seg = document.getElementById(id);
    if (!seg) return;
    seg.addEventListener('click', (e) => {
      const b = e.target.closest('button[data-' + attr + ']');
      if (!b) return;
      for (const x of seg.querySelectorAll('button')) x.classList.toggle('active', x === b);
      historyState[key] = b.dataset[attr === 'autonomy-range' ? 'autonomyRange' : 'autonomySpan'];
      fetchHistory();
    });
  };
  wireAutonomyPicker('history-autonomy-range-sel', 'autonomy-range', 'autonomyRange');
  wireAutonomyPicker('history-autonomy-span-sel', 'autonomy-span', 'autonomySpan');

  const histDoraProjectSel = document.getElementById('history-dora-project');
  if (histDoraProjectSel) histDoraProjectSel.addEventListener('change', () => {
    historyState.doraProject = histDoraProjectSel.value || null;
    fetchHistory();
  });

  const histGranularitySeg = document.getElementById('history-granularity-sel');
  if (histGranularitySeg) histGranularitySeg.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-granularity]');
    if (!b) return;
    for (const x of histGranularitySeg.querySelectorAll('button')) x.classList.toggle('active', x === b);
    historyState.granularity = b.dataset.granularity;
    fetchHistory();
  });

  const histGroupSeg = document.getElementById('history-group-sel');
  if (histGroupSeg) histGroupSeg.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-group]');
    if (!b || b.disabled) return;
    historyState.group = b.dataset.group;
    if (historyState.group === 'token_type') {
      historyState.chart = 'tokens'; // token bands require the tokens metric
    } else if (historyState.chart === 'models' || historyState.chart === 'providers' || historyState.chart === 'agents' || historyState.chart === 'state') {
      // Choosing a group explicitly leaves the metric-preset charts (and
      // agents/state, which are project-only) so the chosen axis sticks on a
      // cost breakdown.
      historyState.chart = 'cost';
    }
    // A dimension is never both the stacking axis and a filter.
    if (historyState.filters[historyState.group]) historyState.filters[historyState.group] = [];
    historyState.scope = null;
    syncHistorySelectors();
    fetchHistory();
  });

  const histForecastChk = document.getElementById('history-forecast');
  if (histForecastChk) histForecastChk.addEventListener('change', () => {
    historyState.forecast = histForecastChk.checked;
    fetchHistory();
  });

  const histCsvBtn = document.getElementById('history-export-csv');
  if (histCsvBtn) histCsvBtn.addEventListener('click', exportHistoryCSV);
  const histJsonBtn = document.getElementById('history-export-json');
  if (histJsonBtn) histJsonBtn.addEventListener('click', exportHistoryJSON);

  window.addEventListener('resize', () => {
    if (!historyTabOn() || !historyState.data) return;
    if (historyResizeRAF) cancelAnimationFrame(historyResizeRAF);
    historyResizeRAF = requestAnimationFrame(paintHistoryChart);
  });

  // Restore the History tab if it was active last session.
  if (localStorage.getItem(ACTIVE_TAB_KEY) === 'history') setHistoryTab(true);
}
