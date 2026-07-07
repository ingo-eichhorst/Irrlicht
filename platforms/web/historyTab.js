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
export const CHART_LABELS = { cost: 'Cost', tokens: 'Tokens', co2: 'CO2', models: 'Models', providers: 'Providers', agents: 'Agents' };
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
  filters: { provider: [], token_type: [], project: [] },
  known: { provider: [], project: [] },
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
// The value formatter for the active chart — dollars for cost/models/providers,
// token counts for tokens, integer agent counts for agents, grams for co2.
function histValue(v) {
  if (historyState.chart === 'tokens') return histTokens(v);
  if (historyState.chart === 'agents') return histCount(v);
  if (historyState.chart === 'co2') return histCO2(v);
  return histDollar(v);
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
  if (state.range === 'custom' && state.start != null && state.end != null) {
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
  return p.toString();
}

function fetchHistory() {
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

function renderHistory() {
  renderHistoryBreadcrumb();
  renderHistoryFilters();
  const isYield = historyState.chart === 'yield';
  // Yield counts completed sessions; agents are reconstructed from opt-in
  // recordings — each gets its own empty caption.
  const emptyEl = document.getElementById('history-chart-empty');
  if (emptyEl) emptyEl.textContent =
    isYield ? 'no completed sessions in this range yet'
      : historyState.chart === 'agents' ? 'no recordings in this range yet'
        : 'no cost data in this range yet';
  if (!historyState.data) {
    const wrap = document.getElementById('history-chart-wrap');
    if (wrap) wrap.classList.add('empty');
    return;
  }
  if (isYield) {
    paintYieldChart();
    renderYieldPanel();
  } else {
    paintHistoryChart();
    renderHistoryPanel();
  }
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
function drawHistoryGridlines(ctx, w, padL, padR, muted, gridColor, maxY, yAt) {
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
function drawHistoryForecastLine(ctx, B, H, xAt, yAt, cumulative, grandTotal, fcY, waiting) {
  if (H <= 0) return;
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

// drawHistoryXAxisLabels draws up to 6 evenly-spaced time labels.
function drawHistoryXAxisLabels(ctx, buckets, B, bucketSeconds, muted, h, padB, xAt) {
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
  for (let r = 0; r < matrix.length; r++) grandTotal += matrix[r][B - 1] || 0;
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

  // Y gridlines + dollar labels (drawn first, behind the areas).
  drawHistoryGridlines(ctx, w, padL, padR, muted, gridColor, maxY, yAt);

  // Stacked areas, bottom-up.
  drawHistoryStackedAreas(ctx, projects, matrix, B, xAt, yAt);

  // Forecast: a dashed line into the future.
  drawHistoryForecastLine(ctx, B, H, xAt, yAt, cumulative, grandTotal, fcY, waiting);

  // X axis time labels.
  drawHistoryXAxisLabels(ctx, buckets, B, data.bucket_seconds, muted, h, padB, xAt);
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

// syncHistorySelectors reflects historyState.chart/group onto the segmented
// controls — drilldown and the models/providers presets change them
// programmatically, so the active classes must follow.
function syncHistorySelectors() {
  const chartSeg = document.getElementById('history-chart-sel');
  if (chartSeg) for (const b of chartSeg.querySelectorAll('button')) b.classList.toggle('active', b.dataset.chart === historyState.chart);
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

  const histChartSeg = document.getElementById('history-chart-sel');
  if (histChartSeg) histChartSeg.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-chart]');
    if (!b || b.disabled) return;
    const c = b.dataset.chart;
    historyState.chart = c;
    // models/providers are presets that pin the stacking axis; agents is
    // reconstructed per project only.
    if (c === 'models') historyState.group = 'model';
    else if (c === 'providers') historyState.group = 'provider';
    else if (c === 'agents') historyState.group = 'project';
    else if (c !== 'tokens' && historyState.group === 'token_type') historyState.group = 'project'; // token_type needs the tokens metric
    historyState.scope = null; // a new metric resets any drilldown
    syncHistorySelectors();
    fetchHistory();
  });

  const histGroupSeg = document.getElementById('history-group-sel');
  if (histGroupSeg) histGroupSeg.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-group]');
    if (!b || b.disabled) return;
    historyState.group = b.dataset.group;
    if (historyState.group === 'token_type') {
      historyState.chart = 'tokens'; // token bands require the tokens metric
    } else if (historyState.chart === 'models' || historyState.chart === 'providers' || historyState.chart === 'agents') {
      // Choosing a group explicitly leaves the metric-preset charts (and
      // agents, which is project-only) so the chosen axis sticks on a cost
      // breakdown.
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
