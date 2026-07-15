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
export const CHART_LABELS = { cost: 'Cost', tokens: 'Tokens', co2: 'CO2', models: 'Models', providers: 'Providers', agents: 'Agents', state: 'Activity', yield: 'Yield', dora: 'DORA' };
// Granularity steps for chart=state's activity matrix (issue #981) — each
// picks both the server's bucket width and the matrix's visible column
// count at once (see historyGranularitySpecs on the daemon side).
const GRANULARITY_LABELS = { '1m': '1 min', '10m': '10 min', '60m': '60 min', '8h': '8 hr', '24h': '24 hr', '7d': '7 day', '1mo': '1 mo', '6mo': '6 mo', '1y': '1 yr' };
// Fixed stack order for the activity matrix's per-cell mini bar, bottom to
// top — mirrors the canonical state order in core/domain/session/session.go.
const STATE_STACK_ORDER = ['working', 'waiting', 'ready'];
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

export function historyQuery(state = historyState) {
  const p = new URLSearchParams();
  p.set('chart', state.chart);
  p.set('group', state.group);
  p.set('forecast', state.forecast ? 'true' : 'false');
  if (state.scope) p.set('scope', state.scope.field + ':' + state.scope.value);
  if (state.chart === 'state') {
    // The activity matrix resolves its window from a granularity zoom-level
    // instead of range/start/end — see historyGranularitySpecs on the daemon.
    p.set('granularity', state.granularity);
  } else if (state.range === 'custom' && state.start != null && state.end != null) {
    p.set('start', String(state.start));
    p.set('end', String(state.end));
  } else {
    p.set('range', state.range);
  }
  // Orthogonal cross-filters: emit each non-empty dimension except the one
  // being grouped on. token_type only narrows the tokens metric.
  const filters = state.filters || {};
  for (const dim of HISTORY_FILTER_DIMS) {
    if (dim === state.group) continue;
    if (dim === 'token_type' && state.chart !== 'tokens') continue;
    const vals = filters[dim];
    if (vals?.length) p.set(dim, vals.join(','));
  }
  // DORA is repo-scoped — exactly one project, not the multi-select project
  // filter above (#951).
  if (state.chart === 'dora' && state.doraProject) p.set('project', state.doraProject);
  return p.toString();
}

function fetchHistory() {
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

function renderHistory() {
  renderHistoryBreadcrumb();
  renderHistoryFilters();
  syncDoraProjectRow();
  syncGranularityRow();
  syncHistoryRangeRow();
  syncHistoryCO2Info();
  const isYield = historyState.chart === 'yield';
  const isDora = historyState.chart === 'dora';
  const isState = historyState.chart === 'state';
  // The activity matrix is a grid, not a time-series line — it replaces the
  // shared canvas with its own scrollable DOM grid (see history-matrix-scroll).
  syncHistoryMatrixVisibility(isState);
  // Yield counts completed sessions; agents/state are reconstructed from
  // opt-in recordings — each gets its own empty caption. DORA never paints
  // the canvas at all (it's a period summary, not a time series) — its
  // content lives entirely in the side panel (see renderDoraPanel).
  const emptyEl = document.getElementById('history-chart-empty');
  if (emptyEl) {
    let emptyText = 'no cost data in this range yet';
    if (isYield) emptyText = 'no completed sessions in this range yet';
    else if (isDora) emptyText = historyState.doraProject ? 'DORA metrics — see panel' : 'select a project to see DORA metrics';
    else if (historyState.chart === 'agents' || isState) emptyText = 'no recordings in this range yet';
    emptyEl.textContent = emptyText;
  }
  if (!historyState.data) {
    const wrap = document.getElementById('history-chart-wrap');
    if (wrap) wrap.classList.add('empty');
    if (isDora) renderDoraPanel();
    return;
  }
  if (isDora) {
    const wrap = document.getElementById('history-chart-wrap');
    if (wrap) wrap.classList.add('empty');
    renderDoraPanel();
  } else if (isYield) {
    paintYieldChart();
    renderYieldPanel();
  } else if (isState) {
    renderStateMatrix();
    renderStatePanel();
  } else {
    paintHistoryChart();
    renderHistoryPanel();
  }
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
  if (row) row.hidden = historyState.chart === 'state';
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
    let top = below ? labelY : labelY - TEXT_HEIGHT_PX;
    if (prevBottom !== null && top - prevBottom < MIN_LABEL_GAP_PX) {
      const shift = MIN_LABEL_GAP_PX - (top - prevBottom);
      labelY += shift;
      top += shift;
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

// stateCellCounts returns one (project, bucket-index) cell's
// {working, waiting, ready} counts, defaulting missing entries to 0.
export function stateCellCounts(data, project, i) {
  const by = data?.by_state || {};
  const out = {};
  for (const state of STATE_STACK_ORDER) out[state] = (by[state]?.[project]?.[i]) || 0;
  return out;
}

// stateCellTotal sums one cell's working+waiting+ready counts.
export function stateCellTotal(data, project, i) {
  const counts = stateCellCounts(data, project, i);
  return counts.working + counts.waiting + counts.ready;
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
// (working/waiting/ready, fixed stack order — see STATE_STACK_ORDER) sized
// against maxTotal, with a hover/focus tooltip and an aria-label carrying the
// exact counts for anyone not using the tooltip.
function buildStateCell(project, ts, counts, maxTotal) {
  const cell = document.createElement('div');
  cell.className = 'hsm-cell';
  cell.tabIndex = 0;
  cell.setAttribute('role', 'img');
  cell.setAttribute('aria-label', project + ', ' + new Date(ts * 1000).toLocaleString() + ': ' +
    counts.working + ' working, ' + counts.waiting + ' waiting, ' + counts.ready + ' ready');

  const bar = document.createElement('div');
  bar.className = 'hsm-bar';
  const total = counts.working + counts.waiting + counts.ready;
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
  for (const [state, label] of [['working', 'Working'], ['waiting', 'Waiting'], ['ready', 'Ready']]) {
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
  for (const [state, label] of [['working', 'Working'], ['waiting', 'Waiting'], ['ready', 'Ready']]) {
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

// setActivityChartEnabled gates the Activity chart (chart=state) behind the
// Advanced Settings beta toggle (#1075). The matrix is reconstructed from
// opt-in recordings, and a bucket with no recording renders identically to a
// genuinely idle one (see renderStateMatrix), so anyone not recording reads a
// grid of blanks as "idle" — misleading enough that it's off by default.
export function setActivityChartEnabled(enabled) {
  const btn = document.querySelector('#history-chart-sel button[data-chart="state"]');
  if (btn) btn.hidden = !enabled;
  // Turning it off while it's the live chart would otherwise strand the view on
  // a chart the setting says is off — the matrix grid stays up, and the
  // per-chart control rows only resync on a render. Fall back to the default
  // chart, same shape as drillInto.
  if (!enabled && historyState.chart === 'state') {
    historyState.chart = 'cost';
    historyState.scope = null;
    syncHistorySelectors();
    fetchHistory();
  }
}

// syncHistorySelectors reflects historyState.chart/group onto the segmented
// controls — drilldown and the models/providers presets change them
// programmatically, so the active classes must follow.
function syncHistorySelectors() {
  const chartSeg = document.getElementById('history-chart-sel');
  if (chartSeg) for (const b of chartSeg.querySelectorAll('button')) b.classList.toggle('active', b.dataset.chart === historyState.chart);
  const metricsSeg = document.getElementById('history-metrics-sel');
  if (metricsSeg) for (const b of metricsSeg.querySelectorAll('button')) b.classList.toggle('active', b.dataset.chart === historyState.chart);
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
    val.textContent = metric && metric.available ? format(metric.value) : (metric?.message || 'n/a');
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
function exportHistoryCSV() {
  const d = historyState.data;
  if (!d) return;
  let lines;
  if (historyState.chart === 'yield') {
    lines = ['project,productive_cost,reverted_cost,unknown_cost,total_cost,yield,reverted_count'];
    for (const p of (d.projects || [])) {
      lines.push([
        historyCsvCell(p.project),
        (p.productive_cost || 0).toFixed(6), (p.reverted_cost || 0).toFixed(6),
        (p.unknown_cost || 0).toFixed(6), (p.total_cost || 0).toFixed(6),
        (p.yield || 0).toFixed(6), String(p.reverted_count || 0),
      ].join(','));
    }
  } else if (historyState.chart === 'state') {
    lines = ['bucket_start,project,working,waiting,ready'];
    const by = d.by_state || {};
    for (const project of (d.projects || [])) {
      (d.bucket_starts || []).forEach((ts, i) => {
        const w = by.working?.[project]?.[i] || 0;
        const wt = by.waiting?.[project]?.[i] || 0;
        const r = by.ready?.[project]?.[i] || 0;
        lines.push([new Date(ts * 1000).toISOString(), historyCsvCell(project), w, wt, r].join(','));
      });
    }
  } else if (historyState.chart === 'dora') {
    lines = ['metric,value,unit,sample_size,available,message'];
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
  } else {
    lines = ['bucket_start,project,value'];
    for (const pt of (d.series || [])) {
      lines.push([new Date(pt.ts * 1000).toISOString(), historyCsvCell(pt.project), pt.value.toFixed(6)].join(','));
    }
  }
  historyDownload('irrlicht-history-' + historyState.range + '-' + historyState.chart + '.csv', 'text/csv;charset=utf-8', lines.join('\n') + '\n');
}
function exportHistoryJSON() {
  const d = historyState.data;
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
    else if (c !== 'tokens' && historyState.group === 'token_type') historyState.group = 'project'; // token_type needs the tokens metric
    historyState.scope = null; // a new metric resets any drilldown
    syncHistorySelectors();
    fetchHistory();
  };
  const histChartSeg = document.getElementById('history-chart-sel');
  if (histChartSeg) histChartSeg.addEventListener('click', handleChartClick);
  const histMetricsSeg = document.getElementById('history-metrics-sel');
  if (histMetricsSeg) histMetricsSeg.addEventListener('click', handleChartClick);

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
