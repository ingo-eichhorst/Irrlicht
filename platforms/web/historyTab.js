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
// Autonomy (#1905). `autonomy` is a CLIENT-SIDE pseudo-chart name that fans out
// into the daemon's TWO real charts, both over the same window:
//
//   chart=autonomy_duration - the AGGREGATE percentile chart across every
//   project (p95/p50/p5, with the plane between p95 and p5 filled), drawn on
//   the shared canvas.
//   chart=autonomy_projects - the per-project panels, of which exactly ONE is
//   drawn at a time, picked from a dropdown in its own header row.
//
// ONE Range control moves both, because they are read together: two windows
// would let the chart show a month while the panel under it showed a year, with
// nothing on screen saying they disagreed. The run strip's separate Span picker
// was exactly that and went with the strip (chart=autonomy_spans stays deleted).
//
// WINDOW LENGTHS, not bucket widths. These keys overlap GRANULARITY_LABELS'
// textually and mean something else entirely: there a key names a bucket width
// that is multiplied by a count (so '24h' resolves to a THIRTY-DAY window),
// here a key IS the window. The two tables must never be merged - see
// autonomy window vocabularies in irrlicht.history.autonomy.test.js.
export const AUTONOMY_RANGE_LABELS = { '30d': '30 days', '1y': 'Year' };
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
  // Autonomy (#1905): its own window, which is not `range`. ONE key, shared by
  // both of the section's elements.
  autonomyRange: '30d',
  // Which project the single panel draws. null means "whatever the daemon ranks
  // first", so a fresh tab needs no stored name; once the reader picks one, the
  // NAME is held rather than the rank, and it SURVIVES a Range change. Holding
  // a rank instead would silently swap the project under the reader whenever a
  // longer window reordered the ranking.
  autonomyProject: null,
  // There is no run-scope key here (#1905 recording): the section counts every
  // run, subagent runs included, because Irrlicht recorded them. What each run
  // WAS still matters - the `kind` field is what splits a concurrency peak into
  // sessions and subagents - but which runs are counted is not a choice.
  //
  // Holds BOTH payloads: { duration, projects }.
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

// autonomyQuery builds one of the two Autonomy requests. `element` picks which
// chart; both send the SAME window, which is what makes one Range control
// honest.
export function autonomyQuery(element, state = historyState) {
  const p = new URLSearchParams();
  p.set('chart', element === 'projects' ? 'autonomy_projects' : 'autonomy_duration');
  p.set('window', state.autonomyRange);
  // No run-scope parameter (#1905 recording). Every run counts, so there is
  // nothing to ask for - and sending the retired one would leave an OLD daemon
  // serving a filtered payload while this panel's sentence said otherwise,
  // which is the exact "wrong number, nothing on screen saying so" the section
  // is built to avoid.
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

// fetchAutonomy fetches BOTH elements. Either failing marks the whole section
// unloaded rather than drawing one chart beside an empty frame that never
// resolves - the two are read together, so half of them is not a partial answer
// but a misleading one.
function fetchAutonomy() {
  const seq = ++historyFetchSeq;
  const get = (element) => fetch('/api/v1/history?' + autonomyQuery(element))
    .then(r => (r.ok ? r.json() : null))
    .catch(() => null);
  return Promise.all([get('duration'), get('projects')]).then(([duration, projects]) => {
    if (seq !== historyFetchSeq) return; // superseded by a newer request
    historyState.autonomyData = (duration && projects) ? { duration, projects } : null;
    // `data` drives the shared empty/export paths, and the aggregate chart is
    // what the shared canvas draws, so it is the one that goes there.
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
      renderAutonomyPanel();
      renderAutonomySidePanel();
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
  const isAutonomy = historyState.chart === 'autonomy';
  // Autonomy KEEPS the shared canvas — that is where its aggregate percentile
  // chart is drawn — and adds its project panel underneath, inside the same
  // card. Only chart=state replaces the canvas outright.
  if (canvas) canvas.hidden = isState;
  if (matrixScroll) matrixScroll.hidden = !isState;
  const panels = document.getElementById('history-autonomy-panels');
  if (panels) panels.hidden = !isAutonomy;
  // The card GROWS for the two elements rather than scrolling inside its own
  // fixed height (QA-2, #1919). The rows an inner scrollbar cut off first were
  // the x axis and the "+N more" line — the row that says what the view left
  // out. The class is scoped to this chart, so every other chart keeps its
  // fixed-height card; it also gives the canvas an explicit height, which a
  // percentage cannot supply inside an auto-height parent.
  const wrap = document.getElementById('history-chart-wrap');
  if (wrap) wrap.classList.toggle('autonomy', isAutonomy);
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

// syncAutonomyRows shows the Autonomy controls only while the section is
// active, mirroring syncGranularityRow's per-chart row toggle.
//
// ONE ROW, one control: Range. The strip's Span picker went with the strip, so
// there is no second window vocabulary on screen to confuse with this one.
function syncAutonomyRows(isAutonomy) {
  const ctl = document.getElementById('history-autonomy-range-row');
  if (ctl) ctl.hidden = !isAutonomy;
  const groupRow = document.getElementById('history-group-sel')?.closest('.history-ctl-row');
  // Group and the cross-filters do not apply to the panels - the section has no
  // stacking axis at all, and its own axis is the project.
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
// TWO ELEMENTS over the always-on span log, one above the other inside the
// shared chart card, sharing one Range control:
//
//   THE AGGREGATE CHART, on the shared canvas — p95/p50/p5 of run duration
//   across EVERY project, with the plane between p95 and p5 filled as a band.
//   "Is autonomy getting better across the machine."
//   ONE PROJECT PANEL below it — the longest run in each time bucket as a line,
//   and how many runs were working at the same time under it as a histogram.
//   "What did THIS project do." The project is picked from a dropdown in the
//   panel's own header row.
//
// WHY BOTH. A maximum over one project is that project's story; a maximum
// charted across every project is a chart of whoever ran longest that day. A
// percentile over the whole population is a trend; a percentile over one
// project's four runs is not a percentile. Neither figure can stand in for the
// other, which is why #1919's five-panels-only build lost something real and
// this restores it.
//
// WHY ONE PANEL AND NOT FIVE. Five panels squeezed each plot to 40px, and the
// reader could still only compare the five the daemon happened to rank highest.
// One panel gets 110px for its line and 44px for its bars, and the dropdown
// reaches EVERY project the window holds.
//
// LINEAR Y ON BOTH, and the cost is stated where each domain is chosen
// (autonomyYDomain, autonomyAggregateDomain) rather than left for the next
// reader to rediscover.
//
// The panel's scales are THAT PROJECT'S OWN. They were shared across the five
// so the five were comparable; with one panel drawn there is nothing to share
// with, and a domain stretched to fit projects that are not on screen would
// flatten the one that is.

// autonomyEverRecorded reports whether the span log holds anything at all,
// anywhere — the fact that separates "no runs in this range" from "this
// feature has not collected anything yet". Both are empty views; only one of
// them means the user did nothing.
export function autonomyEverRecorded(state = historyState) {
  return (Number(state.autonomyData?.projects?.total_recorded) || 0) > 0;
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

// autonomyDateLabel formats the day the provenance line names. A helper of its
// own so a caller can never render the same instant two different ways.
function autonomyDateLabel(ts) {
  return new Date((Number(ts) || 0) * 1000)
    .toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

// autonomyProvenanceLine states when collection started, so an empty or short
// history is never read as "you did nothing" (#1905). This sentence is part of
// the feature, not decoration — which is why it is the one line here that is
// always drawn.
//
// THE RECONSTRUCTED SUFFIX IS THE LAST SURVIVOR of a three-sentence paragraph
// (#1905 prose cut). `tools/autonomy-backfill` rebuilds pre-feature runs from
// logs a machine already had, and a reconstructed figure rendered as a measured
// one is exactly the "wrong number with nothing on screen saying so" this
// section was built to prevent — so the count cannot go. What it costs the
// reader it is written for is nothing: a machine that was never back-filled
// sends `reconstructed: 0` and the clause never appears at all. Four words is
// the smallest thing that still says it, and it hangs off the line that was
// already there rather than earning a line of its own.
export function autonomyProvenanceLine(data) {
  const earliest = Number(data?.earliest_span) || 0;
  const total = Number(data?.total_recorded) || 0;
  if (!earliest) {
    return 'No autonomous runs recorded yet. Irrlicht began measuring them with this update — '
      + 'an empty chart means "nothing recorded", not "nothing happened".';
  }
  const reconstructed = Number(data?.provenance?.reconstructed) || 0;
  return 'Collecting since ' + autonomyDateLabel(earliest)
    + ' · ' + total + ' run' + (total === 1 ? '' : 's') + ' recorded'
    + (reconstructed > 0 ? ' · ' + reconstructed + ' in view reconstructed' : '')
    + '.';
}

// AUTONOMY_CONCURRENCY_CAVEAT is the sentence beside the `at once` figure, and
// it is not optional: without it the number is read as "N independent agents"
// and it is not that.
//
// The daemon deliberately holds a PARENT session in `working` while its
// subagents run, so one agent with three subagents overlaps as four. Four things
// really were working — the figure is not wrong — but a reader who takes it for
// four independent agents has been misled by a true number, which is the exact
// failure mode this sentence exists to prevent.
export const AUTONOMY_CONCURRENCY_CAVEAT =
  'A parent counts as working while its subagents run.';

// AUTONOMY_ERA_LABELS describes what lies to the LEFT of a source boundary —
// the era the data before the line came from, and at what resolution.
//
// Resolution is the whole point (#1905 back-fill, QA-2). The cost log writes at
// most every 60s and only when a value changed, so it cannot see a run shorter
// than that; the event log records one-second runs. Across that boundary both
// elements step at the same instant, and two simultaneous steps read as two
// findings when they are one change of INSTRUMENT.
const AUTONOMY_ERA_LABELS = {
  cost: 'cost log · 60s resolution',
  log: 'event log · rebuilt',
  live: 'measured',
};

// autonomyBoundaryLabel is the marker's caption. The arrow is load-bearing: it
// is what makes the label describe the data BEFORE the line rather than the
// line itself.
export function autonomyBoundaryLabel(boundary) {
  const from = boundary?.from || '';
  return '← ' + (AUTONOMY_ERA_LABELS[from] || from || 'a different source');
}

// autonomyVisibleBoundaries returns the source boundaries that fall inside the
// drawn domain, each with the x fraction (0..1) it sits at.
//
// STRICTLY inside: a boundary at the very first or very last bucket would draw
// a rule on the axis itself, marking nothing and reading as a panel border. A
// range that does not straddle a boundary gets none — which is every range on
// a machine that was never back-filled, and most ranges on one that was.
//
// Pure, so both halves of the rule — draws one when it should, draws nothing
// when it should not — are testable without a canvas. Both elements read it,
// and both share a window, so the rule lands at the same instant in each.
export function autonomyVisibleBoundaries(data) {
  const starts = data?.bucket_starts || [];
  if (starts.length < 2) return [];
  const first = starts[0];
  const last = starts[starts.length - 1];
  if (!(last > first)) return [];
  const out = [];
  for (const b of (data?.provenance?.boundaries || [])) {
    const ts = Number(b?.ts) || 0;
    if (ts <= first || ts >= last) continue;
    out.push({ ts, from: b.from, to: b.to, fraction: (ts - first) / (last - first) });
  }
  return out;
}

// AUTONOMY_BOUNDARY_CAPTION_ON names which of the section's two elements writes
// a boundary's words.
//
// THE RULE READS ONCE ACROSS THE SECTION. The dashed rule is drawn on BOTH
// elements — it has to be, or it would annotate the aggregate line and leave the
// panel below it stepping for no stated reason — but the caption is drawn on the
// AGGREGATE chart only, which is the top one. Two copies of "← cost log · 60s
// resolution" down one x position are two competing captions where the reader
// needs one, and at 9px the lower one would collide with the panel's header.
export const AUTONOMY_BOUNDARY_CAPTION_ON = 'aggregate';
export function autonomyBoundaryCaptionShown(element) {
  return element === AUTONOMY_BOUNDARY_CAPTION_ON;
}

// autonomyMoreProjectsLabel names what the daemon's safety cap left out, or ''
// when nothing was left out — which is every machine seen so far, since the cap
// is 200 and the busiest one on record holds 93 projects in a year.
//
// It says WHY those projects are the ones missing — they have less autonomous
// time — so the reader knows the payload took the tail rather than an arbitrary
// slice. An omission nothing mentions is indistinguishable from a project that
// never ran.
export function autonomyMoreProjectsLabel(data) {
  const more = Number(data?.more_projects) || 0;
  if (more <= 0) return '';
  return '+' + more + ' more project' + (more === 1 ? '' : 's') + ' past the payload cap, '
    + 'each with less autonomous time';
}

// autonomyPanelChoice resolves which project the single panel draws.
//
// THREE OUTCOMES, and the third is the one that has to be written down rather
// than fallen into:
//
//   - nothing selected → rank 1, the project with the most autonomous time in
//     the window. A fresh tab needs no stored name.
//   - the selection is in this range → that panel.
//   - the selection is NOT in this range → `missing`, and the selection is
//     KEPT. Silently falling back to rank 1 would answer a question the reader
//     did not ask, and would make a Range change look like a click they never
//     made; the panel says "no runs for <project> in this range" instead, and
//     the dropdown still carries the name so one Range change back restores it.
export function autonomyPanelChoice(data, selected) {
  const panels = data?.panels || [];
  if (!selected) {
    return { panel: panels[0] || null, project: panels[0]?.project || '', missing: false };
  }
  const found = panels.find(p => p.project === selected);
  if (found) return { panel: found, project: selected, missing: false };
  return { panel: null, project: selected, missing: true };
}

// autonomyProjectOptions is the dropdown's contents: every project the window
// holds, ranked, plus the selected one when this range does not hold it.
//
// The absent project goes LAST and says so, which is the same rule the rest of
// the list follows rather than an exception to it: the order is most autonomous
// time first, and a project with no runs in this range has none of it. Its label
// carries the reason, so a reader who opens the dropdown is not left wondering
// why one entry draws nothing.
export function autonomyProjectOptions(data, selected) {
  const out = (data?.panels || []).map(p => ({ value: p.project, label: p.project, missing: false }));
  if (selected && !out.some(o => o.value === selected)) {
    out.push({ value: selected, label: selected + ' — no runs in this range', missing: true });
  }
  return out;
}

// autonomyEmptyProjectNote is what the panel draws instead of an empty frame
// when the selected project has nothing in this range.
//
// A SENTENCE, not a blank plot. An empty plot with axes on it is the same
// picture a broken request would draw, and the reader cannot tell which they are
// looking at.
export function autonomyEmptyProjectNote(project) {
  return 'no runs for ' + (project || 'this project') + ' in this range';
}

// autonomyConcurrencyLabel renders one "at once" figure, with its split only
// when the daemon says the split is derivable.
//
// NO SPLIT IS SHOWN OVER DATA THAT CANNOT SUPPORT ONE. Most rows written before
// 18 Aug 2026 carry `kind: unknown` — they predate the classification, or the
// back-fill rebuilt them from a source with no parent information — and there is
// no way to recover which they were. The total alone is the honest answer there;
// "5 (5 + 0 sub)" would be an invented one.
export function autonomyConcurrencyLabel(peak, top, sub, splitKnown) {
  const n = Number(peak) || 0;
  if (n <= 0) return '';
  if (!splitKnown) return n + ' at once';
  return n + ' at once (' + (Number(top) || 0) + ' + ' + (Number(sub) || 0) + ' sub)';
}

// autonomyPanelHeadline is one panel's figure line: its longest run and its
// widest moment, for the WHOLE window.
//
// A still-running longest run is marked rather than dropped: "already lasted
// 3h" is a true statement about the longest run, and hiding it would make the
// longest run on the machine the one thing the section cannot show.
export function autonomyPanelHeadline(panel) {
  let longest = 'longest ' + autonomyDuration(panel?.longest);
  if (panel?.longest_running) longest += ' (still going)';
  const concurrency = autonomyConcurrencyLabel(panel?.peak, panel?.peak_top, panel?.peak_sub, panel?.peak_split);
  return concurrency ? longest + ' · ' + concurrency : longest;
}

// autonomyPanelPoints aligns one panel's sparse bucket list to bucket_starts,
// with a null for every bucket the daemon OMITTED.
//
// The null is the honesty rule, not a convenience: an empty bucket is a GAP. A
// day with no runs must not pull the line down to the axis, and must not draw a
// zero-height bar that looks measured.
export function autonomyPanelPoints(panel, bucketStarts) {
  const byTs = new Map();
  for (const b of (panel?.buckets || [])) byTs.set(b.ts, b);
  return (bucketStarts || []).map(ts => byTs.get(ts) || null);
}

// autonomyChartPoints does the same for the AGGREGATE chart's buckets. Same
// rule, same reason, a different payload — one helper each rather than one
// helper branching on shape, so neither can silently start reading the other's
// fields.
export function autonomyChartPoints(duration) {
  const starts = duration?.bucket_starts || [];
  const byTs = new Map();
  for (const b of (duration?.buckets || [])) byTs.set(b.ts, b);
  return starts.map(ts => byTs.get(ts) || null);
}

// autonomyLineSegments returns the index ranges the line may be stroked over.
//
// TWO KINDS OF GAP, and they are not the same gap. A bucket can be absent
// entirely (nothing happened), or present with `longest === 0` — a run passed
// THROUGH it without finishing in it, so something was working and nothing
// finished. Both break the line; only the first also removes the bar. Drawing a
// point at zero in the second case would claim a run of no length.
export function autonomyLineSegments(points) {
  const out = [];
  let run = null;
  for (let i = 0; i < (points?.length || 0); i++) {
    const p = points[i];
    if (p && Number(p.longest) > 0) {
      if (run) run.to = i; else run = { from: i, to: i };
    } else if (run) {
      out.push(run);
      run = null;
    }
  }
  if (run) out.push(run);
  return out;
}

// autonomyYDomain is the panel's LINEAR Y domain — that project's own, since
// exactly one panel is drawn. null when nothing in the window has a length to
// plot, which is the empty state.
//
// LINEAR, AND WHAT THAT COSTS. This is the maintainer's explicit call (#1905),
// recorded here so the next reader knows it was a decision and not an oversight.
// On a linear axis one 11-hour run sets the whole domain, and the typical
// 10-minute runs beneath it collapse into the bottom 1.5% of the plot — visually
// indistinguishable from the floor. A log axis kept them apart, at the cost of
// making a doubling look like a small step wherever the eye landed. The panel
// header states the exact longest as a NUMBER for exactly this reason, so the
// figure never depends on reading the axis.
//
// ZERO IS THE FLOOR, which the log axis could not offer: a linear axis whose
// origin is not zero exaggerates every difference above it, and there is no
// longer any "a log scale cannot plot 0" reason to lift it.
export function autonomyYDomain(panel) {
  const values = (panel?.buckets || []).map(b => Number(b.longest) || 0).filter(v => v > 0);
  if (!values.length) return null;
  return { lo: 0, hi: Math.max(...values) * 1.1 };
}

// autonomyAggregateDomain is the aggregate chart's LINEAR Y domain: the highest
// p95 it DRAWS, plus a tenth of headroom, from zero.
//
// THE DOMAIN FITS WHAT IS DRAWN, and `max` is not drawn. The chart draws p95,
// p50, p5 and the plane between p95 and p5; a bucket's true maximum is a FIGURE
// in the summary row, and it is a figure precisely so it cannot redraw the axis
// — "one four-hour run left going overnight would otherwise redraw the whole Y
// scale and flatten every other bucket into the floor" (#1905). Folding it back
// into the domain re-created exactly that, and the first round of this restore
// shipped it.
//
// MEASURED on the reference machine's live span log (30-day window, 27 of 30
// buckets with data, 2735 runs, reduced the way the daemon buckets them):
//
//     highest p95 across buckets : 1h37m
//     highest max across buckets : 11h39m
//     domain inflation           : 7.19x
//     tallest p50, domain to p95 : 6.60% of plot height
//     tallest p50, domain to max : 0.92%
//     median  p50, domain to max : 0.46%   (i.e. on the axis)
//     empty plot above the band  : 87.4%
//
// Linear still costs what autonomyYDomain says it costs — a long p95 flattens
// the short buckets under it — but that is a cost paid to a line the reader can
// SEE. Paying it to one the chart never draws buys nothing.
export function autonomyAggregateDomain(points) {
  const drawn = (points || []).filter(Boolean);
  if (!drawn.length) return null;
  const hi = Math.max(1, ...drawn.map(b => Number(b.p95) || 0));
  return { lo: 0, hi: hi * 1.1 };
}

// autonomyPeakScale is the histogram's full-height value: the panel's own
// highest peak. 0 when nothing overlapped anywhere in it, in which case no bar
// is drawn at all rather than every bar being drawn full height.
export function autonomyPeakScale(panel) {
  let max = 0;
  for (const b of (panel?.buckets || [])) {
    const v = Number(b.peak) || 0;
    if (v > max) max = v;
  }
  return max;
}

// autonomyAxisLabel is THE Autonomy section's date formatter — the ONE both x
// axes and the tooltip go through.
//
// ONE FORMATTER, and it is a correctness rule rather than tidiness. The two
// charts are stacked, share one window and one x domain, and are read against
// each other: a reader lines a spike in the aggregate chart up with a bar in the
// panel below it. Labelled by two different functions they said the same instant
// two ways — the aggregate axis went through histAxisLabel and read `8/7`
// (US numeric, hardcoded), while the panel's bounds and the tooltip read
// `7. Aug.` — so lining the two up meant translating between them.
//
// histAxisLabel stays where it is for every OTHER chart; what it must not do is
// label one half of this section. `both autonomy axes are labelled by the same
// function` pins that, by provenance and not only by output.
//
// MONTH-NAME BASED, matching what macOS reads (AutonomyFormat.axisBound /
// .axisDate produce `Aug 24`) rather than inventing a third shape — the sibling
// of `the two surfaces name the key the same way`. The locale stays the
// reader's, so `Aug 24` and `24. Aug.` are the same label in two languages,
// where `8/7` was a different thing entirely.
//
// It coarsens with the window, the way stateBucketLabel does for the activity
// matrix: a time of day under 36h, a date under 60 days, a month beyond that.
export function autonomyAxisLabel(ts, windowSeconds) {
  const d = new Date((Number(ts) || 0) * 1000);
  if (windowSeconds <= 36 * 3600) return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  if (windowSeconds <= 60 * 86400) return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  return d.toLocaleDateString(undefined, { month: 'short', year: 'numeric' });
}

// autonomyMeasurementNote marks the runs in view whose duration is a FLOOR
// rather than a measurement (#1905 recording). '' when nothing is running,
// which is the quiet case on a machine between sessions.
//
// A run that has not ended has no length yet — only how long it has lasted SO
// FAR. That floor still COUNTS towards the panel's longest (that figure is a
// maximum, and "already lasted 3h" is true), and is deliberately not a sample
// for the aggregate chart's percentiles, where a floor shortens the longest
// runs hardest. Both halves are in the four words after the dash.
//
// THE SECOND SENTENCE WENT (#1905 prose cut). It named the runs already going
// when Irrlicht started watching, whose start is a lower bound rather than a
// beginning. That is a real limit, but it is a restart artefact a reader can do
// nothing with, and it was the longest sentence in the section.
// `measurement.start_lower_bound` stays on the wire; only the sentence went.
export function autonomyMeasurementNote(payload) {
  const running = Number(payload?.measurement?.running) || 0;
  if (running <= 0) return '';
  return running + ' run' + (running === 1 ? '' : 's') + ' still going — '
    + (running === 1 ? 'length' : 'lengths') + ' so far.';
}

// autonomyThinNote explains the aggregate chart's thin-bucket marking, in
// words, because a fainter plane means nothing on its own. '' when every bucket
// in view clears the floor.
//
// It has to say what p95 and p5 ARE in a thin bucket, not merely that the
// bucket is thin: with two samples they are the longest and the shortest run,
// and a reader who takes them for percentiles reads a two-run week as a spread.
export function autonomyThinNote(duration) {
  const buckets = duration?.buckets || [];
  const thin = buckets.filter(b => b?.thin).length;
  if (thin <= 0) return '';
  const floor = Number(duration?.sample_floor) || 0;
  return thin + ' of ' + buckets.length + ' bucket' + (buckets.length === 1 ? '' : 's')
    + (thin === 1 ? ' has' : ' have') + ' under ' + floor + ' run' + (floor === 1 ? '' : 's')
    + ' (drawn fainter): p95 and p5 there are just the longest and shortest.';
}

// The side panel's key: FOUR entries, because the section draws four marks —
// the aggregate chart's p50 line and p5–p95 plane, and the panel's longest-run
// line and concurrency bars.
//
// LENGTH IS A CONSTRAINT HERE, not a style preference. This panel is
// `flex: 0 0 260px` with 16px padding, so a key row's label gets 204px once its
// swatch and gap are taken out — about 28 monospace characters at 12px. A label
// that will not fit ellipsises the very row whose job is to explain the mark;
// `autonomyLayout.test.js` computes that budget from the stylesheet and fails
// one that is too long.
//
// All four strings are duplicated in AutonomyPalette.keyEntries on macOS, and
// `the two surfaces name the key the same way` reads this table against that one
// — two clients must not explain one chart differently.
const AUTONOMY_KEY = [
  ['p50', 'p50 · the typical run'],
  ['band', 'p5–p95 · the usual spread'],
  ['line', 'longest run in a bucket'],
  ['bars', 'working at once (peak)'],
];

// autonomyKeyColor resolves one mark's colour against the live theme,
// falling back to the literal when the custom property is unset (an unstyled
// document, or a test's stub). '' for a name the section has no mark for.
export function autonomyKeyColor(kind, cs) {
  const token = AUTONOMY_MARK_TOKENS[kind];
  if (!token) return '';
  const [cssVar, fallback] = token;
  return ((cs && cs.getPropertyValue(cssVar)) || fallback).trim();
}

// Every mark's colour. ONE HUE throughout: the band, its edges and the bars are
// all the line's hue at lower alphas, because they are further readings of the
// same activity and not further subjects. `the marks are one hue, not four
// colours` pins that.
export const AUTONOMY_MARK_TOKENS = {
  p50: ['--working', '#8B5CF6'],
  band: ['--autonomy-band', 'rgba(139, 92, 246, 0.20)'],
  bandThin: ['--autonomy-band-thin', 'rgba(139, 92, 246, 0.08)'],
  edge: ['--autonomy-edge', 'rgba(139, 92, 246, 0.45)'],
  line: ['--working', '#8B5CF6'],
  bars: ['--autonomy-bar', 'rgba(139, 92, 246, 0.45)'],
};

// autonomyKeyEntries is the key the side panel draws, resolved through the same
// calls the canvas painters make so a swatch cannot disagree with the ink.
// `fill` is '' for everything but the band: a line has no area.
export function autonomyKeyEntries(cs) {
  return AUTONOMY_KEY.map(([kind, label]) => ({
    kind,
    label,
    color: autonomyKeyColor(kind === 'band' ? 'edge' : kind, cs),
    fill: kind === 'band' ? autonomyKeyColor('band', cs) : '',
  }));
}

// Per-role stroke weights for the aggregate chart. The p50 line carries the
// chart: full alpha, the heavier stroke. The two edges are present enough to
// bound the plane and quiet enough not to compete with it.
const AUTONOMY_ROLE_STYLE = {
  line: { width: 1.8, alpha: 1, thinAlpha: 0.6, marker: 2.6 },
  edge: { width: 1, alpha: 0.5, thinAlpha: 0.32, marker: 2 },
};

// The three drawn percentile lines, in draw order: series key, then the ROLE it
// plays. ONE HUE, THREE WEIGHTS — the chart says one thing (here is the typical
// run, here is the spread around it), and a hue per percentile would make p95
// and p5 read as two independent measurements rather than the boundary of one
// range.
export const AUTONOMY_SERIES = [
  ['p95', 'edge'],
  ['p50', 'line'],
  ['p5', 'edge'],
];

// autonomySeriesRole reports whether a key is the headline `line` or one of the
// band's `edge`s. '' for anything the chart does not draw.
export function autonomySeriesRole(key) {
  const row = AUTONOMY_SERIES.find(([k]) => k === key);
  return row ? row[1] : '';
}

// autonomyBandSegments splits the gap-aligned point list into the areas the
// band may actually be filled over.
//
// TWO RULES, both of them honesty rules the smooth shape would otherwise
// erase, and both of them the reason this is a pure function rather than a
// loop inside the painter:
//
//   - A FILLED AREA WANTS TO CLOSE ACROSS A GAP. The daemon omits empty
//     buckets and the line breaks there (autonomyChartPoints); a polygon
//     spanning the gap would draw a plane over days that hold no runs at all,
//     which is a stronger false claim than the interpolated line #1905 already
//     refuses. A segment therefore never crosses a null.
//   - A THIN BUCKET IS NOT A PERCENTILE. Under sample_floor, p95 is that
//     bucket's longest run and p5 its shortest. The stroke already dashes
//     across such a bucket; the fill splits at the same place so the thin
//     stretch can be painted in its own fainter plane.
//
// Returns index ranges into `points`, inclusive at both ends. Adjacent
// segments SHARE their boundary index, so a thin→solid handover has no seam.
// `from === to` is an isolated bucket: it has no neighbour to make an area
// with, and the painter draws its spread as a whisker instead.
export function autonomyBandSegments(points) {
  const out = [];
  const n = (points || []).length;
  let i = 0;
  while (i < n) {
    if (!points[i]) { i++; continue; }
    let last = i;
    while (last + 1 < n && points[last + 1]) last++;
    if (last === i) {
      out.push({ from: i, to: i, thin: !!points[i].thin });
      i = last + 1;
      continue;
    }
    // Thinness belongs to the INTERVAL, not the bucket — matching
    // drawAutonomySeries, which dashes a segment either of whose ends is thin.
    let start = i;
    let thin = !!(points[i].thin || points[i + 1].thin);
    for (let j = i + 1; j < last; j++) {
      const next = !!(points[j].thin || points[j + 1].thin);
      if (next !== thin) {
        out.push({ from: start, to: j, thin });
        start = j;
        thin = next;
      }
    }
    out.push({ from: start, to: last, thin });
    i = last + 1;
  }
  return out;
}

// --- Rendering: the aggregate chart -----------------------------------------

// AUTONOMY_CHART is the aggregate chart's plot inset, in CSS pixels.
export const AUTONOMY_CHART = { padL: 52, padR: 12, padT: 12, padB: 22 };

function paintAutonomyChart() {
  const canvas = document.getElementById('history-chart');
  const wrap = document.getElementById('history-chart-wrap');
  if (!canvas || !wrap) return;
  const duration = historyState.autonomyData?.duration;
  const { ctx, w, h } = setupHistoryCanvas(canvas, wrap);
  const points = autonomyChartPoints(duration);
  const domain = autonomyAggregateDomain(points);
  wrap.classList.toggle('empty', !domain);
  if (!domain) return;

  const cs = getComputedStyle(document.documentElement);
  const muted = (cs.getPropertyValue('--muted') || '#888').trim();
  const gridColor = 'rgba(128,140,170,0.18)';

  const { padL, padR, padT, padB } = AUTONOMY_CHART;
  const plotW = Math.max(1, w - padL - padR);
  const plotH = Math.max(1, h - padT - padB);
  const n = Math.max(1, points.length);
  const xAt = (i) => (n <= 1 ? padL : padL + plotW * (i / (n - 1)));
  const yAt = (v) => padT + autonomyLinearY(v, domain, plotH);

  // Draw order IS the visual hierarchy, cheapest thing first:
  //
  //   gridlines → band → boundary rules → edges → p50 → axis labels
  //
  // The band goes over the gridlines rather than under them. A gridline is
  // furniture; drawn on top of the plane it would cut the band into slices
  // that read as structure in the data, which is the one thing a soft fill
  // must never do. Over it, the fill's own translucency dims each gridline
  // where it crosses — the line stays legible, and stays furniture.
  drawAutonomyChartGridlines(ctx, { domain, yAt, padL, padR, w, muted, gridColor });
  drawAutonomyBand(ctx, {
    points,
    segments: autonomyBandSegments(points),
    xAt,
    yAt,
    fill: autonomyKeyColor('band', cs),
    fillThin: autonomyKeyColor('bandThin', cs),
    edge: autonomyKeyColor('edge', cs),
  });
  // Under the lines, deliberately: the marker explains the data, it is not
  // part of it, and a rule drawn over a curve competes with what it annotates.
  drawAutonomyBoundaries(ctx, {
    boundaries: autonomyVisibleBoundaries(duration),
    padL, plotW, top: padT, height: plotH, muted, element: 'aggregate',
  });
  // Edges before the line, whatever order the table lists them in: the
  // headline curve is the last ink down, so nothing crosses over it.
  for (const role of ['edge', 'line']) {
    for (const [key, seriesRole] of AUTONOMY_SERIES) {
      if (seriesRole !== role) continue;
      drawAutonomySeries(ctx, { points, key, role, color: autonomyKeyColor('p50', cs), xAt, yAt });
    }
  }
  drawAutonomyXLabels(ctx, { duration, xAt, muted, h, padB });
}

// drawAutonomyBand fills the plane between p5 and p95 — the spread around the
// typical run — one polygon per autonomyBandSegments entry.
//
// It fills SEGMENT BY SEGMENT rather than as one path so the two rules that
// function encodes survive the paint: a gap leaves real empty canvas (no
// plane over days with no runs), and a thin stretch is filled from the
// fainter token, so a range that is not a percentile spread does not look
// like one.
function drawAutonomyBand(ctx, { points, segments, xAt, yAt, fill, fillThin, edge }) {
  ctx.save();
  for (const seg of segments) {
    if (seg.from === seg.to) {
      // An isolated bucket has no neighbour to make an area with. Drawn as a
      // whisker rather than dropped, or it would be the one bucket whose
      // spread is invisible — and a lone bucket is exactly where the reader
      // most needs to see how wide the range was.
      const only = points[seg.from];
      ctx.setLineDash(seg.thin ? [3, 3] : []);
      ctx.strokeStyle = edge;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(xAt(seg.from), yAt(only.p95));
      ctx.lineTo(xAt(seg.from), yAt(only.p5));
      ctx.stroke();
      continue;
    }
    ctx.beginPath();
    for (let i = seg.from; i <= seg.to; i++) {
      const x = xAt(i), y = yAt(points[i].p95);
      if (i === seg.from) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    for (let i = seg.to; i >= seg.from; i--) ctx.lineTo(xAt(i), yAt(points[i].p5));
    ctx.closePath();
    ctx.fillStyle = seg.thin ? fillThin : fill;
    ctx.fill();
  }
  ctx.restore();
}

// drawAutonomySeries strokes one percentile line, breaking at every omitted
// bucket and DASHING any segment that touches a thin bucket — so a bucket
// under the sample floor is visibly different rather than hidden or smoothed.
//
// `role` decides the weight, not the colour: all three lines are one hue, and
// what separates the headline p50 from the band's two edges is stroke width
// and alpha (AUTONOMY_ROLE_STYLE).
function drawAutonomySeries(ctx, { points, key, role, color, xAt, yAt }) {
  const style = AUTONOMY_ROLE_STYLE[role] || AUTONOMY_ROLE_STYLE.line;
  ctx.strokeStyle = color;
  ctx.lineWidth = style.width;
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1], b = points[i];
    if (!a || !b) continue; // a gap is a gap: never interpolate across it
    const thin = a.thin || b.thin;
    ctx.save();
    ctx.setLineDash(thin ? [3, 3] : []);
    ctx.globalAlpha = thin ? style.thinAlpha : style.alpha;
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
    ctx.save();
    ctx.beginPath();
    ctx.arc(xAt(i), yAt(b[key]), style.marker, 0, Math.PI * 2);
    if (b.thin) {
      ctx.strokeStyle = color;
      ctx.globalAlpha = style.alpha * 0.8;
      ctx.stroke();
    } else {
      ctx.fillStyle = color;
      ctx.globalAlpha = style.alpha;
      ctx.fill();
    }
    ctx.restore();
  }
}

// drawAutonomyChartGridlines draws the aggregate chart's linear Y gridlines,
// labelled in the same duration units the summary row uses, so an axis tick and
// a headline figure can never be read in different units.
function drawAutonomyChartGridlines(ctx, { domain, yAt, padL, padR, w, muted, gridColor }) {
  ctx.strokeStyle = gridColor;
  ctx.fillStyle = muted;
  ctx.font = '10px ui-monospace, monospace';
  ctx.textAlign = 'right';
  ctx.textBaseline = 'middle';
  for (const v of autonomyTickValues(domain, 4)) {
    const y = yAt(v);
    ctx.beginPath();
    ctx.moveTo(padL, y);
    ctx.lineTo(w - padR, y);
    ctx.stroke();
    ctx.fillText(autonomyDuration(v), padL - 6, y);
  }
}

// drawAutonomyXLabels labels the aggregate chart's x axis — through
// autonomyAxisLabel, the section's one formatter, NOT through the shared
// histAxisLabel every other chart uses. See autonomyAxisLabel for why: the panel
// directly beneath this axis shares its window and its domain, and the two must
// not name one instant two ways.
//
// The whole WINDOW is what coarsens the label, not this chart's bucket width —
// so the aggregate axis and the panel's bounds coarsen together at the same
// range rather than at two different thresholds.
function drawAutonomyXLabels(ctx, { duration, xAt, muted, h, padB }) {
  ctx.fillStyle = muted;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'top';
  const starts = duration?.bucket_starts || [];
  if (!starts.length) return;
  const windowSeconds = (Number(duration.end) || 0) - (Number(duration.start) || 0);
  const labels = Math.min(5, starts.length);
  for (let i = 0; i < labels; i++) {
    const c = Math.round(i * (starts.length - 1) / Math.max(1, labels - 1));
    ctx.fillText(autonomyAxisLabel(starts[c], windowSeconds), xAt(c), h - padB + 5);
  }
}

// --- Rendering: the project panel -------------------------------------------

// Panel geometry, in CSS pixels. The line plot and the histogram share one
// canvas so their x axes cannot drift apart and one boundary rule can run
// through both.
//
// TICK LABELS ON THE RIGHT, and the insets that go with them. Both are QA fixes
// carried over from #1919 (QA-1):
//
//   - On the LEFT, the y-axis gutter sat directly under the panel header's
//     project name, and the header row and the axis column were one column of
//     text doing two jobs. On the right the header row is free.
//   - `padT` is what stops the TOPMOST label being sliced. A label is drawn
//     centred on its gridline; the top gridline is at the plot's own top edge,
//     so without an inset half the glyph falls outside the canvas and reads as
//     text cut off by the element above. The inset has to be at least half the
//     tick font — `autonomyTickPlacement` computes that and a test asserts it.
//   - `padB` is the same rule at the other end, and it is NEW: the histogram
//     labels its own baseline `0` now, drawn centred on the baseline, so
//     without an inset half of that glyph would fall off the bottom.
//   - `gap` is the same rule a THIRD time, and it is what QA caught: the line
//     axis's lowest label sits at the foot of the line plot, and the histogram's
//     peak label at the head of the bar band. Both are in the same right-hand
//     gutter, both drawn centred on their own line. At the 3px gap the
//     five-panel stack used — where the bar band carried NO labels at all —
//     "0s" and "15" overprinted into an unreadable smudge. The gap has to clear
//     one whole tick font, and `autonomyGutterLabels` is where that is asserted.
//
// The plot is TALLER than the 40px five stacked panels could afford, because
// only one is drawn: 110px for the line, 44px for the bars.
export const AUTONOMY_PANEL = {
  padL: 4,      // a hair of left margin; the labels are on the RIGHT
  padR: 46,     // the y axes' label gutter, shared by the line and the bars
  padT: 6,      // clearance for the topmost tick label's upper half
  lineH: 110,   // the longest-run plot
  gap: 14,      // clearance between the two axes' innermost labels
  barsH: 44,    // the concurrency histogram
  padB: 5,      // clearance for the baseline label's lower half
  tickFont: 9,  // px, the y-axis tick label — the SAME font on both axes
  tickGap: 5,   // px between the plot's right edge and its labels
};
export const AUTONOMY_PANEL_CANVAS_H =
  AUTONOMY_PANEL.padT + AUTONOMY_PANEL.lineH + AUTONOMY_PANEL.gap
  + AUTONOMY_PANEL.barsH + AUTONOMY_PANEL.padB;

// AUTONOMY_BAR_MIN_H is the floor on a drawn bar (QA-3). A bucket where one
// agent worked is a real reading and must be visible; at a fraction of the band
// it was a single device pixel and read as grit. The floor applies only to a bar
// that is drawn at all — a bucket with nobody working still draws NOTHING.
export const AUTONOMY_BAR_MIN_H = 2;

// renderAutonomyPanel draws the single project panel: its header row (the
// project dropdown and that project's figures), its canvas, the "+N more" line
// when the daemon's cap bit, and the window's own bounds under it.
function renderAutonomyPanel() {
  const host = document.getElementById('history-autonomy-panels');
  if (!host) return;
  const data = historyState.autonomyData?.projects;
  host.innerHTML = '';
  const panels = data?.panels || [];
  if (!panels.length) return;

  const choice = autonomyPanelChoice(data, historyState.autonomyProject);
  const cs = getComputedStyle(document.documentElement);
  host.appendChild(buildAutonomyPanel(choice, data, {
    domain: autonomyYDomain(choice.panel),
    peakMax: autonomyPeakScale(choice.panel),
    boundaries: autonomyVisibleBoundaries(data),
    bucketStarts: data.bucket_starts || [],
    windowSeconds: (data.end || 0) - (data.start || 0),
    cs,
  }));
  const more = autonomyMoreProjectsLabel(data);
  if (more) {
    const el = document.createElement('div');
    el.className = 'history-autonomy-more';
    el.textContent = more;
    host.appendChild(el);
  }
  host.appendChild(buildAutonomyAxis(data));
}

// buildAutonomyAxis puts the window's start on the left and "now" on the right,
// once under the panel.
export function buildAutonomyAxis(data) {
  const axis = document.createElement('div');
  axis.className = 'history-autonomy-axis';
  const from = document.createElement('i');
  from.textContent = autonomyAxisLabel(data?.start, (data?.end || 0) - (data?.start || 0));
  const to = document.createElement('i');
  to.textContent = 'now';
  axis.appendChild(from);
  axis.appendChild(to);
  return axis;
}

// buildAutonomyProjectSelect is the picker, in the panel's OWN header row —
// beside the figures it changes, not up in the tab's control row with Range.
// Range moves both elements; this moves one of them, and a control's place is
// what says which.
function buildAutonomyProjectSelect(data, choice) {
  const sel = document.createElement('select');
  sel.className = 'history-autonomy-project';
  sel.id = 'history-autonomy-project';
  sel.setAttribute('aria-label', 'Project');
  for (const opt of autonomyProjectOptions(data, historyState.autonomyProject)) {
    const o = document.createElement('option');
    o.value = opt.value;
    o.textContent = opt.label;
    if (opt.missing) o.dataset.missing = 'true';
    sel.appendChild(o);
  }
  sel.value = choice.project;
  sel.addEventListener('change', () => {
    // The NAME is held, not the rank — see historyState.autonomyProject. No
    // refetch: the payload already carries every project, so switching is a
    // repaint rather than a round trip.
    historyState.autonomyProject = sel.value || null;
    renderAutonomyPanel();
  });
  return sel;
}

function buildAutonomyPanel(choice, data, opts) {
  const el = document.createElement('div');
  el.className = 'history-autonomy-panel';

  const head = document.createElement('div');
  head.className = 'history-autonomy-panel-head';
  head.appendChild(buildAutonomyProjectSelect(data, choice));
  const figs = document.createElement('span');
  figs.className = 'history-autonomy-panel-figs';
  figs.textContent = choice.panel ? autonomyPanelHeadline(choice.panel) : '';
  head.appendChild(figs);
  el.appendChild(head);

  if (!choice.panel) {
    // A SENTENCE, not an empty frame: an empty plot with axes on it is the same
    // picture a broken request draws, and the reader cannot tell them apart.
    const note = document.createElement('div');
    note.className = 'history-autonomy-panel-empty';
    note.textContent = autonomyEmptyProjectNote(choice.project);
    el.appendChild(note);
    return el;
  }

  const panel = choice.panel;
  const canvas = document.createElement('canvas');
  canvas.className = 'history-autonomy-panel-canvas';
  canvas.setAttribute('role', 'img');
  canvas.setAttribute('aria-label', panel.project + ': ' + autonomyPanelHeadline(panel)
    + ', ' + (Number(panel.runs) || 0) + ' runs');
  el.appendChild(canvas);

  const tip = document.createElement('div');
  tip.className = 'history-autonomy-tip';
  tip.hidden = true;
  el.appendChild(tip);
  wireAutonomyPanelTooltip(el, canvas, tip, panel, opts);

  // Painted on the next frame: the canvas has no layout width until it is in
  // the document, and a zero-width canvas would collapse every bucket into
  // nothing.
  requestAnimationFrame(() => paintAutonomyPanel(canvas, panel, opts));
  return el;
}

// autonomyBucketAtX is the tooltip's hit test: which bucket index the pointer is
// over, as a value.
//
// IT IS THE INVERSE OF THE PAINTER'S OWN `xAt`, and that is the whole point —
// derived from the same padL/plotW/n, so the tooltip cannot name a different
// bucket from the one drawn under the pointer. A hit test built from its own
// arithmetic drifts the moment either inset changes, and a tooltip that points
// at the wrong bucket is worse than none: it is a wrong figure with nothing on
// screen saying so.
//
// null outside the plot, so the gutters do not report the end buckets.
export function autonomyBucketAtX(x, { padL, plotW, n }) {
  if (!(n > 0) || !(plotW > 0)) return null;
  if (x < padL || x > padL + plotW) return null;
  if (n === 1) return 0;
  const i = Math.round(((x - padL) / plotW) * (n - 1));
  return Math.min(n - 1, Math.max(0, i));
}

// autonomyPanelTooltip is what the tooltip says about one bucket. '' for a
// bucket the daemon omitted, which draws nothing and therefore says nothing.
export function autonomyPanelTooltip(point, ts, windowSeconds) {
  if (!point) return '';
  const parts = [autonomyAxisLabel(ts, windowSeconds)];
  const longest = Number(point.longest) || 0;
  // A bucket a long run merely crossed carries a bar and no line point. Saying
  // "longest 0s" there would claim a run of no length; the honest reading is
  // that something was working and nothing finished.
  parts.push(longest > 0
    ? 'longest ' + autonomyDuration(longest) + (point.running ? ' (still going)' : '')
    : 'nothing finished');
  const atOnce = autonomyConcurrencyLabel(point.peak, point.peak_top, point.peak_sub, point.peak_split);
  if (atOnce) parts.push(atOnce);
  return parts.join(' · ');
}

// AUTONOMY_TIP_OFFSET is how far below the pointer the tooltip sits, in CSS
// pixels — far enough that the cursor does not sit on its own label.
export const AUTONOMY_TIP_OFFSET = 12;

// autonomyTipPlacement is where the tooltip is drawn, as a value, relative to
// the panel's own box.
//
// TWO RULES, and the first is what QA caught. The tip used to be pinned to the
// panel's TOP edge (`bottom: 100%`), which put it OUTSIDE the panel and over the
// aggregate chart above — hovering to read one chart hid the other, covering its
// x-axis labels and its lowest data. It stays INSIDE its own panel now: below
// the pointer by default, flipped above it when that would overflow the bottom,
// and never at a negative top.
//
// The second rule is the right-hand gutter. Both y axes label themselves there
// (autonomyBarAxisLabels, autonomyTickPlacement), so the tip's right edge is
// held clear of `padR` — otherwise reading a bucket covers the figures the
// gutter exists to show.
export function autonomyTipPlacement(x, y, box, panel = AUTONOMY_PANEL) {
  const width = Math.max(0, Number(box?.width) || 0);
  const height = Math.max(0, Number(box?.height) || 0);
  const tipW = Math.max(0, Number(box?.tipW) || 0);
  const tipH = Math.max(0, Number(box?.tipH) || 0);
  const half = tipW / 2;
  // `left` is the tip's CENTRE (it is translated by -50%), so both bounds are
  // half a tip in from the edge they protect.
  const rightBound = Math.max(half, width - panel.padR - half);
  const left = Math.min(rightBound, Math.max(half, Number(x) || 0));
  const below = (Number(y) || 0) + AUTONOMY_TIP_OFFSET;
  const above = (Number(y) || 0) - AUTONOMY_TIP_OFFSET - tipH;
  // Below the pointer unless that runs past the panel's foot, in which case
  // above it — either way inside the panel, never over the chart above it.
  const top = below + tipH <= height ? below : above;
  return { left, top: Math.max(0, Math.min(top, Math.max(0, height - tipH))) };
}

// wireAutonomyPanelTooltip makes the concurrency figure readable per bucket.
// The header states the window's peak; without this the individual bars are the
// one number on the panel a reader cannot get at.
function wireAutonomyPanelTooltip(host, canvas, tip, panel, opts) {
  const points = autonomyPanelPoints(panel, opts.bucketStarts);
  const hide = () => { tip.hidden = true; };
  canvas.addEventListener('mouseleave', hide);
  canvas.addEventListener('mousemove', (ev) => {
    const rect = canvas.getBoundingClientRect();
    const w = rect.width || canvas.offsetWidth || 0;
    const { padL, padR } = AUTONOMY_PANEL;
    const geo = { padL, plotW: Math.max(1, w - padL - padR), n: Math.max(1, points.length) };
    const i = autonomyBucketAtX(ev.clientX - rect.left, geo);
    const text = i == null ? '' : autonomyPanelTooltip(points[i], opts.bucketStarts[i], opts.windowSeconds);
    if (!text) { hide(); return; }
    tip.textContent = text;
    tip.hidden = false;
    // Measured against the PANEL, not the canvas: the tip is the panel's child
    // and has to stay inside it, header row included.
    const box = host.getBoundingClientRect();
    const at = autonomyTipPlacement(ev.clientX - box.left, ev.clientY - box.top, {
      width: box.width || w,
      height: box.height || 0,
      tipW: tip.offsetWidth || 0,
      tipH: tip.offsetHeight || 0,
    });
    tip.style.left = at.left + 'px';
    tip.style.top = at.top + 'px';
  });
}

function paintAutonomyPanel(canvas, panel, opts) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.offsetWidth || 400;
  const h = AUTONOMY_PANEL_CANVAS_H;
  const pxW = Math.max(1, Math.round(w * dpr)), pxH = Math.max(1, Math.round(h * dpr));
  if (canvas.width !== pxW || canvas.height !== pxH) { canvas.width = pxW; canvas.height = pxH; }
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);

  const { padL, padR, padT, lineH } = AUTONOMY_PANEL;
  const plotW = Math.max(1, w - padL - padR);
  const points = autonomyPanelPoints(panel, opts.bucketStarts);
  const n = Math.max(1, points.length);
  const xAt = (i) => (n <= 1 ? padL : padL + plotW * (i / (n - 1)));

  const cs = opts.cs;
  const muted = (cs.getPropertyValue('--muted') || '#888').trim();
  const gridColor = 'rgba(128,140,170,0.18)';
  const lineColor = autonomyKeyColor('line', cs);
  const barColor = autonomyKeyColor('bars', cs);

  drawAutonomyGridlines(ctx, { domain: opts.domain, padL, padR, padT, lineH, w, muted, gridColor });
  // Under the marks, deliberately: a boundary explains the data, it is not part
  // of it, and a rule drawn over a line competes with what it annotates.
  drawAutonomyBoundaries(ctx, {
    boundaries: opts.boundaries, padL, plotW, top: 0, height: h, muted, element: 'panel',
  });
  drawAutonomyBars(ctx, {
    points, xAt, plotW, n, peakMax: opts.peakMax,
    color: barColor, gridColor, muted, padL, padR, w,
  });
  drawAutonomyLine(ctx, { points, xAt, domain: opts.domain, padT, lineH, color: lineColor });
}

// drawAutonomyGridlines draws the line plot's linear Y gridlines, labelled in
// the same duration units the panel header uses, so an axis tick and a headline
// figure can never be read in different units.
//
// LABELS TO THE RIGHT OF THE PLOT (QA-1). On the left they shared a column with
// the panel header, and the topmost one was drawn half outside the canvas.
// `autonomyTickPlacement` is where the position is decided, so both halves of
// the fix are testable without a canvas.
function drawAutonomyGridlines(ctx, { domain, padL, padR, padT, lineH, w, muted, gridColor }) {
  if (!domain) return;
  ctx.save();
  ctx.strokeStyle = gridColor;
  ctx.fillStyle = muted;
  ctx.font = AUTONOMY_PANEL.tickFont + 'px ui-monospace, monospace';
  ctx.textAlign = 'left';
  ctx.textBaseline = 'middle';
  for (const v of autonomyTickValues(domain)) {
    const at = autonomyTickPlacement(v, domain, w);
    ctx.beginPath();
    ctx.moveTo(padL, at.y);
    ctx.lineTo(w - padR, at.y);
    ctx.stroke();
    ctx.fillText(autonomyDuration(v), at.x, at.y);
  }
  ctx.restore();
}

// autonomyTickValues returns the durations an axis is labelled at, EVENLY
// SPACED — which on a linear scale means evenly spaced in value too, where on
// the log scale this replaced it meant evenly spaced in ratio.
export function autonomyTickValues(domain, steps = 2) {
  if (!domain) return [];
  const out = [];
  for (let i = 0; i <= steps; i++) {
    out.push(domain.lo + (domain.hi - domain.lo) * (i / steps));
  }
  return out;
}

// autonomyTickPlacement is where one tick's label is drawn, as a value.
//
// It carries the two properties QA-1 was about, so both can be asserted without
// a canvas: the label sits in the gutter to the RIGHT of the plot (so the panel
// header's row is free), and its upper edge is inside the canvas (so the
// topmost figure is not sliced by the element above). `top`/`bottom` are the
// label's own extent, since it is drawn centred on its gridline.
export function autonomyTickPlacement(v, domain, width, panel = AUTONOMY_PANEL) {
  const y = panel.padT + autonomyYAt(v, domain, panel.lineH);
  return {
    y,
    x: width - panel.padR + panel.tickGap,
    top: y - panel.tickFont / 2,
    bottom: y + panel.tickFont / 2,
    plotRight: width - panel.padR,
  };
}

// autonomyLinearY maps a value onto a LINEAR axis of the given height, top
// down. One helper for both elements, so the aggregate chart and the panel
// cannot end up on different mappings.
//
// See autonomyYDomain for what linear costs and why it was chosen anyway.
export function autonomyLinearY(v, domain, height) {
  if (!domain) return height;
  const span = domain.hi - domain.lo;
  if (!(span > 0)) return height;
  const clamped = Math.min(domain.hi, Math.max(domain.lo, Number(v) || 0));
  return height * (1 - (clamped - domain.lo) / span);
}

// autonomyYAt maps a duration onto the panel's line axis.
export function autonomyYAt(v, domain, lineH) {
  return autonomyLinearY(v, domain, lineH);
}

// drawAutonomyLine strokes the longest-run line, BREAKING at every gap rather
// than interpolating across it (autonomyLineSegments), and marking an isolated
// bucket with a dot so the one bucket with no neighbour is not invisible.
function drawAutonomyLine(ctx, { points, xAt, domain, padT, lineH, color }) {
  if (!domain) return;
  const yAt = (v) => padT + autonomyYAt(v, domain, lineH);
  ctx.save();
  ctx.strokeStyle = color;
  ctx.fillStyle = color;
  ctx.lineWidth = 1.6;
  ctx.lineJoin = 'round';
  for (const seg of autonomyLineSegments(points)) {
    if (seg.from === seg.to) {
      ctx.beginPath();
      ctx.arc(xAt(seg.from), yAt(points[seg.from].longest), 1.8, 0, Math.PI * 2);
      ctx.fill();
      continue;
    }
    ctx.beginPath();
    for (let i = seg.from; i <= seg.to; i++) {
      const x = xAt(i), y = yAt(points[i].longest);
      if (i === seg.from) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.stroke();
  }
  // A bucket whose longest run has not ended is hollow rather than solid: its
  // length is a floor, and the header says "still going" beside it.
  for (let i = 0; i < points.length; i++) {
    const p = points[i];
    if (!p || !(Number(p.longest) > 0) || !p.running) continue;
    ctx.beginPath();
    ctx.arc(xAt(i), yAt(p.longest), 2.4, 0, Math.PI * 2);
    ctx.stroke();
  }
  ctx.restore();
}

// autonomyBaselineY is the histogram's foot: the line every bar stands on,
// directly beneath the plot and clear of the canvas's own bottom edge by padB,
// which is what leaves room for the `0` label drawn centred on it.
export function autonomyBaselineY(panel = AUTONOMY_PANEL) {
  return panel.padT + panel.lineH + panel.gap + panel.barsH;
}

// autonomyBarRect is one bar's vertical extent, as a value (QA-3). null for a
// bucket that draws no bar at all.
//
// EVERY BAR STANDS ON autonomyBaselineY. They always did arithmetically, but at
// two or three pixels with nothing to stand on they scanned as dashes scattered
// under the line rather than as a distribution — so the baseline is DRAWN, and
// the band is tall enough that a bar of 1 and a bar of 15 are visibly different
// heights rather than both being a smudge.
export function autonomyBarRect(peak, peakMax, panel = AUTONOMY_PANEL) {
  const n = Number(peak) || 0;
  const max = Number(peakMax) || 0;
  if (n <= 0 || max <= 0) return null;
  const height = Math.max(AUTONOMY_BAR_MIN_H, panel.barsH * (n / max));
  const bottom = autonomyBaselineY(panel);
  return { top: bottom - height, bottom, height };
}

// autonomyBarAxisLabels is the histogram's OWN y axis, as values.
//
// TWO LABELS: the band's full-height value at the top, and `0` at the baseline.
// Two is what the axis actually means — a bar's height is its peak as a fraction
// of the highest peak, so the top and the floor are the only two readings that
// are exact — and a ladder of intermediate ticks in a 44px band would be four
// numbers 10px apart.
//
// Before this the gutter beside the bars was deliberately BLANK on both
// surfaces, which is what "the number of agents running in parallel is not
// visible" was about: the bars carried the only figure on the panel with no
// scale of any kind. They share the line chart's gutter (padR) and tick font, so
// the two axes read as one column rather than as two competing ones.
//
// [] when nothing overlapped anywhere in the panel: with no bar drawn, an axis
// labelled 0 to 0 would be furniture claiming a measurement.
export function autonomyBarAxisLabels(peakMax, panel = AUTONOMY_PANEL) {
  const max = Math.round(Number(peakMax) || 0);
  if (max <= 0) return [];
  const baseline = autonomyBaselineY(panel);
  return [
    { value: max, text: String(max), y: baseline - panel.barsH },
    { value: 0, text: '0', y: baseline },
  ];
}

// autonomyGutterLabels is EVERY label drawn in the panel's right-hand gutter —
// the line axis's ticks and the histogram's two — each with the vertical extent
// it occupies, top to bottom.
//
// It exists because the two axes share one gutter and neither knows about the
// other. QA of the shipped build caught the consequence: at the 3px gap the
// five-panel stack used (where the bar band carried no labels at all) the line
// axis's floor tick "0s" and the histogram's peak "15" were drawn 3px apart and
// overprinted into an unreadable smudge — a wrong figure with nothing on screen
// saying so, in the very gutter this change added to make the concurrency
// figure legible.
//
// As a value, so "no two labels overlap" is one assertion over the real
// geometry rather than a constant somebody has to keep in their head.
export function autonomyGutterLabels(domain, peakMax, width, panel = AUTONOMY_PANEL) {
  const half = panel.tickFont / 2;
  const out = autonomyTickValues(domain).map((v) => {
    const at = autonomyTickPlacement(v, domain, width, panel);
    return { axis: 'line', text: autonomyDuration(v), y: at.y, top: at.top, bottom: at.bottom };
  });
  for (const label of autonomyBarAxisLabels(peakMax, panel)) {
    out.push({ axis: 'bars', text: label.text, y: label.y, top: label.y - half, bottom: label.y + half });
  }
  return out.sort((a, b) => a.y - b.y);
}

// drawAutonomyBars draws the concurrency histogram: one bar per bucket, scaled
// against this panel's own peak, all of them standing on one drawn baseline,
// with the band's own y axis labelled in the right-hand gutter.
//
// A BUCKET WITH NO ONE WORKING DRAWS NOTHING — no bar, however short. The
// BASELINE is not a mark on that rule: it is drawn uniformly across the whole
// plot in the gridline colour, exactly like the y gridlines above it, so it
// reads as furniture rather than as a per-bucket measurement of zero.
function drawAutonomyBars(ctx, { points, xAt, plotW, n, peakMax, color, gridColor, muted, padL, padR, w }) {
  const baseline = autonomyBaselineY();
  ctx.save();
  // The foot first, so a bar sits ON it rather than the rule cutting across.
  ctx.strokeStyle = gridColor;
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(padL, baseline + 0.5);
  ctx.lineTo(w - padR, baseline + 0.5);
  ctx.stroke();
  if (peakMax > 0) {
    const barW = Math.max(1.5, Math.min(6, plotW / Math.max(1, n) - 1));
    ctx.fillStyle = color;
    for (let i = 0; i < points.length; i++) {
      const rect = autonomyBarRect(points[i]?.peak, peakMax);
      if (!rect) continue;
      ctx.fillRect(xAt(i) - barW / 2, rect.top, barW, rect.height);
    }
  }
  // …and the band's own scale, in the line chart's gutter and font.
  ctx.fillStyle = muted;
  ctx.font = AUTONOMY_PANEL.tickFont + 'px ui-monospace, monospace';
  ctx.textAlign = 'left';
  ctx.textBaseline = 'middle';
  for (const label of autonomyBarAxisLabels(peakMax)) {
    ctx.fillText(label.text, w - padR + AUTONOMY_PANEL.tickGap, label.y);
  }
  ctx.restore();
}

// drawAutonomyBoundaries marks each instant where the data's source changes: a
// dashed hairline down the whole plot, so the rule reads as one vertical through
// the section. The caption is written by the aggregate chart only
// (autonomyBoundaryCaptionShown).
function drawAutonomyBoundaries(ctx, { boundaries, padL, plotW, top, height, muted, element }) {
  if (!boundaries?.length) return;
  ctx.save();
  ctx.font = '9px ui-monospace, monospace';
  ctx.textBaseline = 'top';
  for (const b of boundaries) {
    const x = padL + plotW * b.fraction;
    ctx.globalAlpha = 0.45;
    ctx.strokeStyle = muted;
    ctx.lineWidth = 1;
    ctx.setLineDash([2, 3]);
    ctx.beginPath();
    ctx.moveTo(x, top);
    ctx.lineTo(x, top + height);
    ctx.stroke();
    if (!autonomyBoundaryCaptionShown(element)) continue;
    const label = autonomyBoundaryLabel(b);
    ctx.setLineDash([]);
    if (x - 4 - ctx.measureText(label).width >= padL) {
      ctx.globalAlpha = 0.7;
      ctx.fillStyle = muted;
      ctx.textAlign = 'right';
      ctx.fillText(label, x - 4, top + 1);
      ctx.textAlign = 'left';
    }
  }
  ctx.restore();
}

// autonomyPanelRows builds the side panel's rows, INCLUDING each row's swatch
// colour, as a pure function.
//
// The swatch belongs here rather than at the DOM call site because that is
// exactly where the defect was once: the panel built its rows and then blanked
// every dot, leaving unlabelled curves on the canvas with no key at all. As a
// value it is testable; as a `style.background =` buried in a render loop it
// was not.
//
// The first FOUR rows are the key, and they are the key because they are what
// the section draws: two lines, one plane and one row of bars. The two elements'
// rows sit in the order they appear on screen — the aggregate chart's p50 and
// band first, then the panel's longest and peak. runs/projects are FIGURES and
// stay unswatched on purpose: a swatch would claim ink nothing lays down.
export function autonomyPanelRows(data, cs) {
  const d = data?.duration?.summary || {};
  const p = data?.projects?.summary || {};
  const [p50Key, bandKey, lineKey, barsKey] = autonomyKeyEntries(cs);
  const key = (entry, value) => ({ kind: entry.kind, label: entry.label, value, swatch: entry.color, fill: entry.fill });
  const figure = (label, value) => ({ kind: null, label, value, swatch: 'transparent', fill: '' });
  let longest = autonomyDuration(p.longest);
  if (p.longest_running) longest += ' · still going';
  return [
    key(p50Key, autonomyDuration(d.p50)),
    // Both ends of the band, in the order they read on the chart: bottom edge
    // to top edge. One row, two figures still visible.
    key(bandKey, autonomyDuration(d.p5) + ' – ' + autonomyDuration(d.p95)),
    key(lineKey, longest),
    key(barsKey, String(Number(p.peak) || 0)),
    figure('runs', String(Number(p.runs) || 0)),
    figure('projects', String(Number(p.projects) || 0)),
  ];
}

// renderAutonomySidePanel fills the shared side panel with the window's own
// figures and every sentence that qualifies them.
function renderAutonomySidePanel() {
  const titleEl = document.getElementById('history-panel-title');
  const totalEl = document.getElementById('history-total');
  const fcEl = document.getElementById('history-forecast-line');
  const listEl = document.getElementById('history-contrib');
  const data = historyState.autonomyData;
  const projects = data?.projects;
  const duration = data?.duration;
  if (titleEl) titleEl.textContent = 'Autonomy · ' + (AUTONOMY_RANGE_LABELS[historyState.autonomyRange] || historyState.autonomyRange);
  const s = projects?.summary || {};
  const runs = Number(s.runs) || 0;
  // The headline is the TYPICAL run, which is the aggregate chart's subject and
  // the first thing on screen; the longest is a key row below it.
  if (totalEl) totalEl.textContent = runs > 0 ? autonomyDuration(duration?.summary?.p50) : '—';
  if (fcEl) {
    fcEl.textContent = runs > 0
      ? 'median run · ' + runs + ' runs across ' + (Number(s.projects) || 0) + ' projects'
      : '';
  }
  if (!listEl) return;
  listEl.innerHTML = '';
  if (!(runs > 0)) {
    appendHistoryEmpty(listEl, autonomyEverRecorded() ? 'no runs in this range' : 'nothing recorded yet');
  } else {
    for (const row of autonomyPanelRows(data, getComputedStyle(document.documentElement))) {
      const li = document.createElement('li');
      // The key rows lay out differently from the figure rows below them: they
      // carry a sentence AND a figure, which do not fit one 228px line.
      if (row.kind) li.className = 'autonomy-key';
      const dot = document.createElement('span');
      // Each key swatch takes the SHAPE of what it stands for — a rule for a
      // line, a bounded plane for the band, a pair of bars for the histogram.
      // Four identical dots in one hue would be a key that says the same thing
      // four times.
      dot.className = row.kind ? 'dot autonomy-' + row.kind : 'dot';
      if (row.kind === 'band') {
        dot.style.background = row.fill;
        dot.style.borderColor = row.swatch;
      } else if (row.kind) {
        dot.style.color = row.swatch;
      } else {
        dot.style.background = row.swatch;
      }
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
    // The caveat belongs beside the number it qualifies, not in a tooltip: the
    // `at once` figure is otherwise read as "N independent agents".
    appendHistoryEmpty(listEl, AUTONOMY_CONCURRENCY_CAVEAT);
    // …and the thin-bucket marking is explained in words, because a fainter
    // plane means nothing on its own.
    const thin = autonomyThinNote(duration);
    if (thin) appendHistoryEmpty(listEl, thin);
  }
  // Which of the figures are floors rather than measurements (#1905 recording).
  // Silent when nothing is running, which is most of the time.
  const measurement = autonomyMeasurementNote(projects);
  if (measurement) appendHistoryEmpty(listEl, measurement);
  // The provenance line is part of the feature: "no data" must never read as
  // "you did nothing" — and it carries the back-fill count, so a reconstructed
  // figure is never rendered as a measured one. Always drawn; the two lines it
  // replaced (the run census and the reconstruction paragraph) are gone (#1905
  // prose cut).
  appendHistoryEmpty(listEl, autonomyProvenanceLine(projects));
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

// autonomyCsvLines exports EVERY project's per-bucket row, not just the panel
// on screen — the dropdown shows one project at a time, and a CSV that carried
// only the visible one would silently be a per-project export wearing a
// section-wide name.
//
// The aggregate chart's own percentiles are not repeated here: they are
// derivable from nothing in this file, so the JSON export (which carries BOTH
// payloads) is the place that answers for them.
function autonomyCsvLines() {
  const data = historyState.autonomyData?.projects;
  const lines = ['bucket_start,project,longest_seconds,running,peak,peak_top,peak_sub,peak_split'];
  for (const panel of (data?.panels || [])) {
    for (const b of (panel.buckets || [])) {
      lines.push([
        new Date((b.ts || 0) * 1000).toISOString(), historyCsvCell(panel.project || ''),
        String(Number(b.longest) || 0), String(!!b.running),
        String(Number(b.peak) || 0), String(Number(b.peak_top) || 0),
        String(Number(b.peak_sub) || 0), String(!!b.peak_split),
      ].join(','));
    }
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

  // The Autonomy segmented control. `dataKey` is passed rather than derived
  // from `attr`, so a second control would be one more line instead of another
  // branch in a ternary that silently defaulted every unrecognised attribute to
  // one particular picker.
  const wireAutonomyPicker = (id, attr, dataKey, stateKey) => {
    const seg = document.getElementById(id);
    if (!seg) return;
    seg.addEventListener('click', (e) => {
      const b = e.target.closest('button[data-' + attr + ']');
      if (!b) return;
      for (const x of seg.querySelectorAll('button')) x.classList.toggle('active', x === b);
      historyState[stateKey] = b.dataset[dataKey];
      fetchHistory();
    });
  };
  wireAutonomyPicker('history-autonomy-range-sel', 'autonomy-range', 'autonomyRange', 'autonomyRange');

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
    // Autonomy repaints BOTH of its elements: the panel is its own
    // width-dependent canvas, so the shared painter alone would leave a stale
    // plot at the old width under a freshly redrawn aggregate chart.
    const repaint = historyState.chart === 'autonomy'
      ? () => { paintAutonomyChart(); renderAutonomyPanel(); }
      : paintHistoryChart;
    historyResizeRAF = requestAnimationFrame(repaint);
  });

  // Restore the History tab if it was active last session.
  if (localStorage.getItem(ACTIVE_TAB_KEY) === 'history') setHistoryTab(true);
}
