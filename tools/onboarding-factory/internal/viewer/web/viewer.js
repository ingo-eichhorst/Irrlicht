// Viewer SPA. Loads scenario list, fetches a recording on click, renders
// the timeline with a scrubber. Per #268 Phase 7 spec: speed presets
// 1×/2×/5×/10×/20×/25×/100×, adaptive fast-forward, state-change
// reason panel showing rule_id + signal_ref + evidence.

import {
  deriveEventOffsets, findOffsetBefore, findOffsetAfter, resolveDashboardIframeUrl,
} from './playbackTimeline.js';
import {
  paintStateBand, paintEventDots, paintTurns, paintExpectedLane,
} from './playbackView.js';
import {
  startReplay, replayStatus, setReplaySpeed, pauseReplay, resumeReplay, stopReplay, seekReplay,
} from './replayClient.js';

const SPEED_PRESETS = [1, 2, 5, 10, 25, 100];

// RECORDING_SLUG_RE mirrors the backend's /api/scenarios/{agent}/{subtree}/{id}
// validation (slugRE in internal/viewer/scenarios.go) — agent and id must be
// lowercase-alnum-dash-underscore slugs, never containing "/", "?", or "#".
const RECORDING_SLUG_RE = /^[a-z0-9][a-z0-9_-]*$/;

// pluralSuffix returns "" for a count of exactly 1, "s" otherwise — the
// English-plural suffix used by every "N recording(s)" label in this file.
function pluralSuffix(n) {
  return n === 1 ? "" : "s";
}

// Strip control characters and cap length before logging a server-provided
// string (SonarQube jssecurity:S5145 — fetch response fields are tainted
// regardless of same-origin trust). Uses String(), not `value || ""`, so
// null/undefined/empty/garbage stay distinguishable in the log instead of
// all collapsing to the same blank output.
function sanitizeForLog(value) {
  return String(value).replace(/[\r\n]+/g, " ").slice(0, 300);
}

// inferDriverLabel returns "Interactive (tmux REPL)" when the adapter
// entry has a non-empty script array, "Headless one-shot" otherwise.
// Pure function — exported for unit tests.
export function inferDriverLabel(a) {
  if (!a) return "Headless one-shot";
  if (Array.isArray(a.script) && a.script.length > 0) return "Interactive (tmux REPL)";
  return "Headless one-shot";
}

// --- Agent-column pagination (overview matrix) ---------------------------
// The 7-segment pipeline strip makes each agent column ~220px wide, so the
// coverage matrix overflows the main pane once more than a handful of agents
// are onboarded. The matrix therefore windows the agent columns: 2–4 at a
// time depending on pane width, with ◀ ▶ pager controls in the panel header.

// AGENT_COL_PX approximates one agent column (7 chips + gaps + cell
// padding); MATRIX_RESERVED_PX approximates the scenario-name column plus
// panel padding.
const AGENT_COL_PX = 220;
const MATRIX_RESERVED_PX = 240;

let agentPage = 0;     // current agent-column page (session-scoped)
let lastPerPage = 0;   // page size at last matrix render, for the resize listener

// agentsPerPage returns how many agent columns fit in availableWidth,
// clamped to 2..4 — never fewer than 2 (a comparison needs a pair), never
// more than 4 (the strip stays readable). Pure — exported for unit tests.
export function agentsPerPage(availableWidth) {
  const fit = Math.floor((availableWidth - MATRIX_RESERVED_PX) / AGENT_COL_PX);
  return Math.max(2, Math.min(4, fit));
}

// paginateAgents slices the agent list into the visible column window. The
// page index is clamped into range so a stale value (pane narrowed, agent
// list changed) degrades to the nearest valid page instead of an empty
// table. Pure — exported for unit tests.
export function paginateAgents(agents, page, perPage) {
  const pages = Math.max(1, Math.ceil(agents.length / perPage));
  const p = Math.min(Math.max(0, page), pages - 1);
  const start = p * perPage;
  const end = Math.min(start + perPage, agents.length);
  return {visible: agents.slice(start, end), page: p, pages, start, end};
}

// Module-level handles populated during init() and reused by the
// Overview button + scenario clicks to swap views in the main pane.
let scenariosList = [];   // live recordings from /api/scenarios
let catalog = null;       // /api/catalog payload (coverage or scenarios)
let catalogSource = "";   // "coverage" | "scenarios" — drives the matrix shape
let recipes = null;       // /api/recipes payload — scenarios.json verbatim
let recipesByCoverageId = new Map(); // coverage_id → recipe entry

// folderToCoverageId maps one agent's on-disk recording folder to its
// coverage_id. For most cells the folder IS the coverage_id; for variant-folder
// cells it differs (codex interrupted-turn → user-esc-interrupt, pi
// agent-question-pending → user-blocking-question). The match is PER-AGENT: a
// folder resolves only if THIS agent's shard cell records under it (or the
// folder name is itself a coverage_id). This is deliberate — the same variant
// folder name also exists as a regression fixture under OTHER agents (e.g.
// aider/regressions/interrupted-turn), and those must NOT borrow codex's
// coverage_id/recipe. Falls back to the folder name when nothing matches (an
// orphan / regression-only recording). Single reverse-lookup shared by the
// sidebar label, the detail-page title, and the recipe panel.
function folderToCoverageId(folder, agent) {
  for (const r of recipesByCoverageId.values()) {
    const folderForAgent = r.folder_by_agent?.[agent];
    if ((r.name === folder || folderForAgent === folder) && r.coverage_id) {
      return r.coverage_id;
    }
  }
  return folder;
}

// loadInitData fetches the three starting payloads (scenarios, catalog,
// recipes) in parallel, populates the module-level caches, and returns the
// raw scenarios list so init() can decide whether to render the sidebar
// tree or the empty-state note.
async function loadInitData() {
  const [scenarios, catResp, recipesResp] = await Promise.all([
    fetch("/api/scenarios").then(r => r.json()),
    fetch("/api/catalog").then(async r => {
      if (!r.ok) return {body: null, source: ""};
      return {body: await r.json(), source: r.headers.get("X-Catalog-Source") || ""};
    }).catch(() => ({body: null, source: ""})),
    fetch("/api/recipes").then(r => r.ok ? r.json() : null).catch(() => null),
  ]);
  scenariosList = scenarios || [];
  catalog = catResp.body;
  catalogSource = catResp.source;
  recipes = recipesResp;
  if (recipes && Array.isArray(recipes.scenarios)) {
    for (const r of recipes.scenarios) {
      if (r.coverage_id) recipesByCoverageId.set(r.coverage_id, r);
    }
  }
  return scenarios;
}

// registerOverviewResizeListener re-renders the overview when a window
// resize changes how many agent columns fit (debounced; no-op on detail
// pages and when the fit count is unchanged). Registered once, from init().
function registerOverviewResizeListener() {
  let resizeTimer = 0;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      const h = location.hash || "#/";
      if (h.startsWith("#/scenario/") || h.startsWith("#/recording/")) return;
      if (!catalog) return;
      const main = document.getElementById("main");
      if (agentsPerPage(main?.clientWidth || 1200) !== lastPerPage) loadOverview();
    }, 150);
  });
}

// buildCodeById maps scenario id → catalog code (e.g. "5.4") so the
// sidebar can prefix labels and sort in the same order as the overview
// matrix. Regression-subtree recordings aren't in the catalog and stay
// uncoded — they fall through to alphabetical at the end.
function buildCodeById() {
  const codeById = new Map();
  if (catalog && Array.isArray(catalog.scenarios)) {
    for (const sc of catalog.scenarios) {
      if (sc.code && sc.id) codeById.set(sc.id, sc.code);
    }
  }
  return codeById;
}

// buildOverviewButton creates the always-present "📊 Overview" sidebar
// button. Click sets the hash; the router hashchange handler does the
// actual view swap.
function buildOverviewButton() {
  const overviewBtn = document.createElement("button");
  overviewBtn.className = "scn overview-btn";
  overviewBtn.dataset.route = "overview";
  overviewBtn.textContent = "📊 Overview";
  overviewBtn.addEventListener("click", () => navigate("#/"));
  return overviewBtn;
}

// renderEmptySidebarNote shows the "no recordings" note in the sidebar
// when /api/scenarios comes back empty.
function renderEmptySidebarNote(sidebar) {
  const note = document.createElement("div");
  note.style.cssText = "padding: 8px; font-size: 12px; color: #888;";
  note.textContent = "No recordings found under replaydata/agents/.";
  sidebar.appendChild(note);
}

// groupScenariosBySubtree buckets scenarios by subtree (scenarios vs
// regressions), then by agent, preserving discovery order within each
// agent's list.
function groupScenariosBySubtree(scenarios) {
  const bySubtree = {scenarios: {}, regressions: {}};
  for (const s of scenarios) {
    if (!bySubtree[s.subtree]) bySubtree[s.subtree] = {};
    bySubtree[s.subtree][s.agent] ||= [];
    bySubtree[s.subtree][s.agent].push(s);
  }
  return bySubtree;
}

// buildRecordingButton builds one sidebar leaf button for a recording.
// <button> rather than <a> so the element is reliably click-triggerable
// from any input source (mouse, keyboard, accessibility tools, Chrome
// MCP). data-rec-key lets the router find this button when restoring
// active state from a deep link.
function buildRecordingButton(s, codeById) {
  const el = document.createElement("button");
  el.className = "scn";
  el.dataset.recKey = `${s.agent}/${s.subtree}/${s.id}`;
  // Label by the resolved coverage_id (+ catalog code) so variant-folder
  // recordings (e.g. agent-question-pending → user-blocking-question) read
  // like their catalog row instead of the raw folder name.
  const cid = folderToCoverageId(s.id, s.agent);
  const code = codeById.get(cid);
  // The on-disk folder is kept as a parenthetical ONLY when it's a genuine
  // variant. Folders are id-prefixed (<dashed-code>_<name>), so a standard
  // cell's folder is just the code + name already shown — appending it
  // would be redundant noise. Show it only when the folder isn't the
  // canonical <dashed-code>_<name> (the 2 real variants:
  // user-esc-interrupt→2-20_interrupted-turn, user-blocking-question→
  // 2-17_agent-question-pending); the detail breadcrumb shows it regardless.
  const canonicalFolder = code ? `${code.replaceAll(".", "-")}_${cid}` : cid;
  const labelId = s.id === canonicalFolder ? cid : `${cid} (${s.id})`;
  el.textContent = code ? `${code} ${labelId}` : labelId;
  el.addEventListener("click", () => navigate(`#/recording/${s.agent}/${s.subtree}/${s.id}`));
  return el;
}

// buildAgentGroup builds one agent's collapsible group within a subtree.
// Sort by catalog code (e.g. "5.4") so the order mirrors the overview
// matrix. Resolve the folder→coverage_id per-agent so a variant-folder
// cell sorts by the SAME code its label shows. Items without a code
// (regression-only / orphan folders) sort to the end, alphabetically.
function buildAgentGroup(subtree, agent, agentScenarios, codeById, activePath) {
  const agentDet = makeSidebarGroup(
    "agent-group", `sidebar.agent.${subtree}.${agent}`, agent, agentScenarios.length,
    activePath.subtree === subtree && activePath.agent === agent,
  );
  agentScenarios.sort((a, b) => {
    const [as, ai] = parseCatalogCode(codeById.get(folderToCoverageId(a.id, agent)));
    const [bs, bi] = parseCatalogCode(codeById.get(folderToCoverageId(b.id, agent)));
    if (as !== bs) return as - bs;
    if (ai !== bi) return ai - bi;
    return a.id.localeCompare(b.id);
  });
  for (const s of agentScenarios) {
    agentDet.appendChild(buildRecordingButton(s, codeById));
  }
  return agentDet;
}

// buildSubtreeGroup builds one subtree's ("scenarios"/"regressions")
// collapsible group, containing one agent group per agent with recordings
// in that subtree.
function buildSubtreeGroup(subtree, agentsMap, codeById, activePath) {
  const totalCount = Object.values(agentsMap).reduce((n, arr) => n + arr.length, 0);
  const subtreeDet = makeSidebarGroup("subtree-group", `sidebar.subtree.${subtree}`, subtree, totalCount, activePath.subtree === subtree);
  for (const agent of Object.keys(agentsMap).sort((a, b) => a.localeCompare(b))) {
    subtreeDet.appendChild(buildAgentGroup(subtree, agent, agentsMap[agent], codeById, activePath));
  }
  return subtreeDet;
}

// renderSidebarGroups builds the full scenarios/regressions × agent tree
// under the sidebar's overview button. Group by subtree (scenarios vs
// regressions) first, then by agent. Each level is a <details>/<summary>
// so the list stays scannable even when many recordings accumulate.
// Open/closed state persists in localStorage; the path leading to the
// currently-selected recording is force-expanded on render.
function renderSidebarGroups(sidebar, scenarios, codeById) {
  const bySubtree = groupScenariosBySubtree(scenarios);
  const activePath = sidebarActivePath();
  for (const subtree of ["scenarios", "regressions"]) {
    const agents = bySubtree[subtree];
    if (!agents || Object.keys(agents).length === 0) continue;
    sidebar.appendChild(buildSubtreeGroup(subtree, agents, codeById, activePath));
  }
}

(async function init() {
  const scenarios = await loadInitData();
  // Re-render the overview when a window resize changes how many agent
  // columns fit (debounced; no-op on detail pages and when the fit count
  // is unchanged). Registered once for both init paths below.
  registerOverviewResizeListener();

  const codeById = buildCodeById();
  const sidebar = document.getElementById("scenarios");
  sidebar.innerHTML = "";

  // Overview button — always present. Click sets the hash; the router
  // hashchange handler does the actual view swap.
  sidebar.appendChild(buildOverviewButton());

  if (!scenarios || scenarios.length === 0) {
    renderEmptySidebarNote(sidebar);
    // Wire router even without recordings — overview view still works.
    window.addEventListener("hashchange", route);
    route();
    return;
  }
  renderSidebarGroups(sidebar, scenarios, codeById);
  // Wire the router and dispatch the initial route. Deep links land
  // directly on the requested view; bare `/` falls through to overview.
  window.addEventListener("hashchange", route);
  route();
})();

// parseCatalogCode splits a catalog code ("5.4") into [section, index]
// for numeric sort. Missing/blank codes sort to the end.
function parseCatalogCode(code) {
  if (!code) return [Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER];
  const [s, i] = code.split(".").map(n => Number.parseInt(n, 10));
  return [Number.isFinite(s) ? s : Number.MAX_SAFE_INTEGER, Number.isFinite(i) ? i : Number.MAX_SAFE_INTEGER];
}

// makeSidebarGroup builds one collapsible <details> with a styled
// <summary> (chevron + label + count). Open state persists per
// storageKey in localStorage; forceOpen overrides closed when the
// active selection lives inside this group.
function makeSidebarGroup(className, storageKey, label, count, forceOpen) {
  const det = document.createElement("details");
  det.className = className;
  // localStorage stores "1" (open) / "0" (closed). Default open for
  // first-time users so they discover what's inside.
  const stored = localStorage.getItem(storageKey);
  const isOpen = forceOpen || stored === null || stored === "1";
  if (isOpen) det.open = true;
  const sum = document.createElement("summary");
  const chev = document.createElement("span");
  chev.className = "chev";
  chev.textContent = "▸";
  sum.appendChild(chev);
  const labelEl = document.createElement("span");
  labelEl.textContent = label;
  sum.appendChild(labelEl);
  const countEl = document.createElement("span");
  countEl.className = "group-count";
  countEl.textContent = count;
  sum.appendChild(countEl);
  det.appendChild(sum);
  det.addEventListener("toggle", () => {
    localStorage.setItem(storageKey, det.open ? "1" : "0");
  });
  return det;
}

// sidebarActivePath inspects the current hash and returns {subtree,
// agent} when on a recording route, so makeSidebarGroup can
// auto-expand the path leading to the selection. {null, null}
// otherwise.
function sidebarActivePath() {
  const m = /^#\/recording\/([^/]+)\/([^/]+)\/([^/]+)/.exec(location.hash || "");
  if (!m) return {subtree: null, agent: null};
  return {agent: decodeURIComponent(m[1]), subtree: decodeURIComponent(m[2])};
}

// navigate updates location.hash and lets the hashchange listener do
// the dispatch. Centralizing through this single helper makes sure
// every click adds an entry to browser history so back/forward work.
// Setting `location.hash` to the same value is a no-op (no event,
// no history entry), which is what we want for re-clicks.
function navigate(hash) {
  if (location.hash === hash) {
    // Already there — force a re-render in case state went stale.
    route();
    return;
  }
  location.hash = hash;
}

// route parses location.hash and dispatches to the matching view.
// Hash shapes:
//   ""              → overview
//   "#/"            → overview
//   "#/scenario/<id>"                                  → scenario coverage detail
//   "#/recording/<agent>/<subtree>/<id>"               → recording playback (latest)
//   "#/recording/<agent>/<subtree>/<id>/<archive>"     → recording playback (specific archive)
//
// Any recording URL may carry a "?focus=<key>" suffix where key is a
// section anchor (supports, observes, recipe, spec, recordings,
// validation). Used by the pipeline-strip segments to scroll the
// matching panel into view on entry.
//
// Unknown hashes fall back to overview.
function route() {
  const hash = location.hash || "#/";
  // Peel off the optional ?focus=<key> suffix before path matching.
  const focusMatch = /\?focus=([a-z]+)$/.exec(hash);
  const focus = focusMatch ? focusMatch[1] : "";
  const pathPart = focus ? hash.slice(0, hash.lastIndexOf("?")) : hash;

  let m = /^#\/scenario\/([^/]+)\/?$/.exec(pathPart);
  if (m) {
    loadCoverageDetail(decodeURIComponent(m[1]));
    return;
  }
  m = /^#\/recording\/([^/]+)\/([^/]+)\/([^/]+)(?:\/([^/]+))?\/?$/.exec(pathPart);
  if (m) {
    const agent = decodeURIComponent(m[1]);
    const subtree = decodeURIComponent(m[2]);
    const id = decodeURIComponent(m[3]);
    const archive = m[4] ? decodeURIComponent(m[4]) : "";
    const rec = scenariosList.find(r => r.agent === agent && r.subtree === subtree && r.id === id);
    if (!rec) {
      console.warn("route: no recording for", hash, "— falling back to overview");
      navigate("#/");
      return;
    }
    loadScenario(rec, archive, focus);
    return;
  }
  // Default: overview. Strip any unknown hash content from the title.
  loadOverview();
}

// scrollFocusInto scrolls the panel matching [data-anchor="<key>"]
// (or [data-anchor-alias~="<key>"]) into view. No-op if the key is
// empty or no such panel exists for this cell.
//
// The panels are appended asynchronously by renderRecordingPanels
// (inside the dropdown-selection-changed handler), so the target may
// not exist on the first scheduled tick. We poll up to ~800ms with
// setTimeout (rather than rAF — Chrome throttles rAF in background
// tabs and pauses it altogether when the tab isn't visible).
// Implemented imperatively (compute offset, set scrollTop) because
// Element.scrollIntoView is inconsistent inside grid items with
// internal overflow-y: auto.
function scrollFocusInto(focus) {
  if (!focus) return;
  const safe = CSS.escape(focus);
  const sel = `[data-anchor="${safe}"], [data-anchor-alias~="${safe}"]`;
  const start = Date.now();
  function tick() {
    const target = document.querySelector(sel);
    if (!target) {
      if (Date.now() - start < 800) {
        setTimeout(tick, 50);
      }
      return;
    }
    // Walk up to the nearest scrollable ancestor.
    let container = target.parentElement;
    while (container && container !== document.body) {
      const style = getComputedStyle(container);
      if (/(auto|scroll|overlay)/.test(style.overflowY)) break;
      container = container.parentElement;
    }
    if (!container || container === document.body) {
      window.scrollTo({top: target.getBoundingClientRect().top + window.scrollY, behavior: "auto"});
      return;
    }
    const cRect = container.getBoundingClientRect();
    const tRect = target.getBoundingClientRect();
    const offset = (tRect.top - cRect.top) + container.scrollTop - 8; // 8px slack
    container.scrollTo({top: offset, behavior: "auto"});
  }
  setTimeout(tick, 30);
}

// loadOverview swaps the main pane to the scenario coverage matrix.
// Two catalog shapes are supported:
//
//   coverage  (.claude/skills/ir:onboard-agent/agent-scenarios-coverage.json, source of truth):
//     38 scenarios × 5 agents. Each cell has agent_supports plus the
//     orthogonal daemon_capability + driver_capability verdicts, which
//     the daemon rolls up to a derived display_state, plus a notes field.
//     Cell badge colors reflect the display_state.
//
//   scenarios (.claude/skills/ir:onboard-agent/scenarios.json, fallback):
//     8 actively-driven cells with by_adapter prompts. Cell badge =
//     ✓/○/— based on whether a recording exists.
//
// In both modes, hovering a cell explains it, and where a recording
// exists for the (agent, scenario) pair the cell is clickable and
// jumps to that recording.
function loadOverview() {
  document.querySelectorAll(".scn").forEach(e => e.classList.remove("active"));
  const overviewBtn = document.querySelector(".scn.overview-btn");
  if (overviewBtn) overviewBtn.classList.add("active");
  document.title = "Irrlicht — Scenarios";
  document.getElementById("title").textContent = "Scenario coverage";
  document.getElementById("breadcrumb").textContent =
    catalog ? "from replaydata/agents/scenarios.json — refresh to pick up edits" : "catalog unavailable";
  const detail = document.getElementById("detail");
  detail.innerHTML = "";

  if (!catalog || !Array.isArray(catalog.scenarios)) {
    const p = document.createElement("p");
    p.textContent = "Catalog not loaded — /api/catalog returned no scenarios array.";
    detail.appendChild(p);
    return;
  }

  // Pick renderer by shape, not source tag. The matrix shape carries a
  // `coverage` map on each entry (added by the server-side assembler);
  // the legacy `scenarios.json` shape carries `by_adapter` instead.
  const hasMatrixShape = catalog.scenarios.length > 0 &&
    catalog.scenarios[0] && typeof catalog.scenarios[0].coverage === "object";
  if (hasMatrixShape) {
    renderCoverageMatrix(detail);
  } else {
    renderScenariosMatrix(detail);
  }
}

// renderCoverageMatrix paints the 38×5 maintainer coverage matrix.
// Each cell's segment-2 chip is colored by the derived display_state
// (observed / pending-record / blocked-daemon / blocked-driver /
// unobservable / n.a. / unknown), rolled up daemon-side from
// agent_supports + daemon_capability + driver_capability + the measured
// recording status. Notes (if any) show in the tooltip.
// buildCoverageHead builds the coverage panel's header (title + agent
// pager when there's more than one page of agent columns). Extracted from
// renderCoverageMatrix.
function buildCoverageHead(agents, pg) {
  const head = document.createElement("div");
  head.style.cssText = "display: flex; align-items: baseline; gap: 12px; justify-content: space-between; flex-wrap: wrap;";
  const h3 = document.createElement("h3");
  h3.textContent = `Scenario coverage — ${catalog.scenarios.length} scenarios × ${agents.length} agents`;
  head.appendChild(h3);
  if (pg.pages > 1) head.appendChild(renderAgentPager(pg, agents));
  return head;
}

// buildCoverageCell builds one scenario×agent cell: an em-dash when the
// agent has no coverage entry, otherwise the 7-segment pipeline strip.
// recIndex is keyed by on-disk folder name; sc.id is the coverage_id.
// Resolve coverage_id → folder via recipesByCoverageId so the
// pipeline-strip chip lands on the recording detail page (not
// /scenario/...) when folder name and coverage_id diverge. folder_by_agent
// is per-agent (variant-folder aware); fall back to the recipe name then
// the coverage_id. Extracted from renderCoverageMatrix.
function buildCoverageCell(sc, agent, recIndex) {
  const cov = sc.coverage?.[agent];
  const cell = document.createElement("td");
  cell.style.textAlign = "center";
  cell.style.padding = "4px";
  if (!cov) {
    cell.textContent = "—";
    cell.style.color = "#ccc";
    cell.title = `${agent}: no entry`;
    return cell;
  }
  const recipe = recipesByCoverageId.get(sc.id);
  const folder = recipe?.folder_by_agent?.[agent] || recipe?.name || sc.id;
  const rec = recIndex.get(`${agent}/${folder}`);
  const strip = renderPipelineStrip(agent, sc.id, cov, rec);
  cell.appendChild(strip);
  return cell;
}

// buildCoverageRow builds one scenario's table row: the name cell (code
// chip + coverage_id, clickable through to the scenario detail page) plus
// one cell per visible agent column. Extracted from renderCoverageMatrix.
function buildCoverageRow(sc, pg, recIndex) {
  const row = document.createElement("tr");
  const nameCell = document.createElement("td");
  nameCell.style.cssText = "cursor: pointer;";
  const nameLink = document.createElement("button");
  nameLink.style.cssText = "background: transparent; border: 0; padding: 0; text-align: left; cursor: pointer; font: inherit; color: inherit;";
  const codeChip = sc.code
    ? `<span style="display: inline-block; min-width: 28px; padding: 1px 5px; margin-right: 6px; background: #e8e6da; color: #555; border-radius: 3px; font-size: 10px; font-weight: 600; font-family: monospace; vertical-align: 1px;">${escapeHtml(sc.code)}</span>`
    : "";
  nameLink.innerHTML = `${codeChip}<span style="font-weight: 600; color: #1f56a8; text-decoration: underline;">${sc.id}</span>`;
  nameLink.addEventListener("click", () => navigate(`#/scenario/${sc.id}`));
  nameCell.appendChild(nameLink);
  row.appendChild(nameCell);
  for (const agent of pg.visible) {
    row.appendChild(buildCoverageCell(sc, agent, recIndex));
  }
  return row;
}

// buildCoverageTable builds the full scenario × agent matrix table (header
// row + one body row per catalog scenario). Extracted from
// renderCoverageMatrix.
function buildCoverageTable(pg, recIndex) {
  const table = document.createElement("table");
  table.className = "overview-matrix";
  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  ["Scenario", ...pg.visible].forEach(h => {
    const th = document.createElement("th");
    th.textContent = h;
    headRow.appendChild(th);
  });
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  for (const sc of catalog.scenarios) {
    tbody.appendChild(buildCoverageRow(sc, pg, recIndex));
  }
  table.appendChild(tbody);
  return table;
}

// computeCoverageStages counts cells by where they are in the workflow
// (blocked / awaiting recipe / awaiting spec / awaiting recording /
// recorded), plus divergence between the capability verdict and the
// measured outcome (mirrors renderPipelineStrip's outline logic).
// Extracted from renderCoverageMatrix.
// classifyCoverageCell resolves one (scenario, agent) cell's pipeline stage
// — the same decision chain computeCoverageStages' loop used to run inline,
// pulled out so the loop's own branching isn't compounded by it (SonarQube
// javascript:S3776). Mirrors the original exactly: a "recorded" cell is
// still tagged divergent on top of (not instead of) being recorded.
function classifyCoverageCell(cov) {
  if (cov.agent_supports === "no") return {stage: "blocked"};
  const pipe = cov.pipeline || {};
  if (!pipe.recipe?.authored) return {stage: "awaitingRecipe"};
  if (!pipe.spec?.authored) return {stage: "awaitingSpec"};
  const recCount = (pipe.recordings?.latest ? 1 : 0) + (pipe.recordings?.archive_count || 0);
  if (recCount === 0) return {stage: "awaitingRecording"};
  return {stage: "recorded", divergent: isCoverageCellDivergent(cov)};
}

// isCoverageCellDivergent reports whether a recorded cell's capability
// verdict (daemon/driver) disagrees with its measured outcome — mirrors
// renderPipelineStrip's outline logic.
function isCoverageCellDivergent(cov) {
  const daemon = cov.daemon_capability || "unknown";
  const driver = cov.driver_capability || "ready";
  const meas = cov.measurement || {};
  const capable = (daemon === "full" && driver === "ready");
  const verdictBlocks = (daemon === "incapable" || daemon === "bug" ||
    String(driver).startsWith("gap:"));
  return (
    meas.status === "fail" ||
    (meas.status === "known_failing" && capable) ||
    (meas.status === "pass" && verdictBlocks) ||
    meas.status === "known_failing_now_passing"
  );
}

function computeCoverageStages(agents) {
  const stages = {blocked: 0, awaitingRecipe: 0, awaitingSpec: 0, awaitingRecording: 0, recorded: 0, divergent: 0};
  let total = 0, withEntry = 0;
  for (const sc of catalog.scenarios) {
    for (const agent of agents) {
      total++;
      const cov = sc.coverage?.[agent];
      if (!cov) continue;
      withEntry++;
      const result = classifyCoverageCell(cov);
      stages[result.stage]++;
      if (result.divergent) stages.divergent++;
    }
  }
  return {stages, total, withEntry};
}

// buildCoverageSummary renders the "Pipeline: N blocked → N awaiting
// recipe → …" status line beneath the matrix. Extracted from
// renderCoverageMatrix.
function buildCoverageSummary(stages, total, withEntry) {
  const sum = document.createElement("div");
  sum.style.cssText = "margin-top: 10px; display: flex; gap: 12px; font-size: 11px; color: #555; flex-wrap: wrap; align-items: center;";
  sum.innerHTML = `
    <span style="font-weight:600;">Pipeline:</span>
    <span><b>${stages.blocked}</b> blocked (sup=no)</span>
    <span>→</span>
    <span><b>${stages.awaitingRecipe}</b> awaiting recipe</span>
    <span>→</span>
    <span><b>${stages.awaitingSpec}</b> awaiting spec</span>
    <span>→</span>
    <span><b>${stages.awaitingRecording}</b> awaiting recording</span>
    <span>→</span>
    <span style="background:#d6f0d4;color:#1f5a1d;padding:1px 6px;border-radius:8px;"><b>${stages.recorded}</b> recorded</span>
    ${stages.divergent > 0 ? `<span style="margin-left:14px;color:#c0392b;font-weight:600;background:#fff5f5;padding:1px 6px;border-radius:8px;">⚠ <b>${stages.divergent}</b> divergent</span>` : ""}
    <span style="margin-left:14px;color:#888;">${withEntry}/${total} cells assessed</span>
  `;
  return sum;
}

// buildCoverageLegend renders the static explainer describing the
// 7-segment strip and the full state vocabulary for each segment. Each
// row lists one segment, what it tests, and every glyph that can appear
// in it with its meaning. Pure (no inputs). Extracted from
// renderCoverageMatrix.
function buildCoverageLegend() {
  const legend = document.createElement("div");
  legend.style.cssText = "margin-top: 10px; padding: 10px 12px; background: #fafaf2; border: 1px solid #e8e6da; border-radius: 4px; font-size: 11px; color: #444;";
  // Helper: render one inline state chip + its label.
  const stateChip = (label, bg, fg, meaning) =>
    `<span style="display:inline-flex;align-items:center;gap:4px;margin-right:10px;white-space:nowrap;">` +
    `<span style="background:${bg};color:${fg};padding:1px 5px;border-radius:3px;font-weight:600;min-width:18px;text-align:center;display:inline-block;">${label}</span>` +
    `<span>${meaning}</span></span>`;
  // Helper: render one segment row (segment name + states).
  const row = (idx, name, blurb, states) =>
    `<div style="display:flex;gap:10px;align-items:flex-start;padding:4px 0;border-top:1px solid #ece9dd;">` +
    `<div style="min-width:120px;color:#222;"><b>${idx}. ${name}</b><div style="color:#777;font-weight:400;">${blurb}</div></div>` +
    `<div style="flex:1;">${states}</div></div>`;
  legend.innerHTML = `
    <div style="font-weight:600;margin-bottom:6px;color:#222;">How to read each cell — 7-segment pipeline (3 assessment pillars + 4 workflow stages)</div>
    ${row("1", "Supports", "agent CLI implements the feature (agent_supports) — glyph ⚙, color = state",
      stateChip("⚙", "#d6f0d4", "#1f5a1d", "yes — fully supported") +
      stateChip("⚙", "#fde7c1", "#8a4500", "partial — partially supported") +
      stateChip("⚙", "#f8c8c8", "#8a0000", "no — freezes the pipeline") +
      stateChip("⚙", "#e5e5e5", "#555",    "unknown — needs assessment") +
      stateChip("⚙", "#eeece4", "#999",    "n/a"))}
    ${row("2", "Daemon", "daemon sensor capture (daemon_capability) — glyph ◉, color = state",
      stateChip("◉", "#d6f0d4", "#1f5a1d", "full — trace exists and the daemon handles it") +
      stateChip("◉", "#f8c8c8", "#8a0000", "bug — trace exists but mis-handled (record known_failing)") +
      stateChip("◉", "#ffcda3", "#a8480a", "incapable — leaves no trace the daemon can see") +
      stateChip("◉", "#eeece4", "#999",    "n/a — agent doesn't support the feature") +
      stateChip("◉", "#e5e5e5", "#555",    "unknown — needs assessment"))}
    ${row("3", "Driver", "recording harness can drive it (driver_capability) — glyph ▷, color = state",
      stateChip("▷", "#d6f0d4", "#1f5a1d", "ready — driver has every step the recipe needs") +
      stateChip("▷", "#fde7c1", "#8a4500", "gap:&lt;primitive&gt; — driver lacks a step type") +
      stateChip("▷", "#e5e5e5", "#555",    "unknown — needs assessment"))}
    ${row("4", "Recipe", "driver script in scenarios.json",
      stateChip("✎", "#d6f0d4", "#1f5a1d", "authored") +
      stateChip("·", "transparent", "#bbb",  "not yet authored"))}
    ${row("5", "Spec", "expected.jsonl phase assertions",
      stateChip("§", "#d6f0d4", "#1f5a1d", "authored") +
      stateChip("·", "transparent", "#bbb",  "not yet authored"))}
    ${row("6", "Recordings", "count of fixtures captured",
      stateChip("N", "#d6f0d4", "#1f5a1d", "N total (latest + archived)") +
      stateChip("·", "transparent", "#bbb",  "none yet"))}
    ${row("7", "Validation", "latest recording vs spec",
      stateChip("✓", "#d6f0d4", "#1f5a1d", "pass — all phases matched") +
      stateChip("✗", "#f8c8c8", "#8a0000", "fail — at least one phase failed") +
      stateChip("⚠", "#fff7d6", "#8a4500", "known_failing — documented gap") +
      stateChip("↑", "#cfe7ff", "#1c3f7a", "known_failing now passing — flag stale") +
      stateChip("!", "#e5e5e5", "#555",    "validator error") +
      stateChip("·", "transparent", "#bbb",  "no recording / no spec"))}
    <div style="margin-top:8px;padding-top:6px;border-top:1px solid #ece9dd;color:#666;">
      <b>Cell outlines</b> (drift between capability verdict and measurement):
      <span style="display:inline-block;border:1px solid #c0392b;background:#fff5f5;padding:1px 6px;border-radius:3px;margin:0 4px;">red</span> daemon full + driver ready but recording fails
      <span style="display:inline-block;border:1px solid #d68a2a;background:#fffaf0;padding:1px 6px;border-radius:3px;margin:0 4px;">amber</span> marked blocked/unobservable but recording passes (stale verdict)
      <span style="display:inline-block;border:1px solid #1c3f7a;background:#f0f5ff;padding:1px 6px;border-radius:3px;margin:0 4px;">blue</span> daemon=bug / known_failing but now passes — drop the flag.
      <div style="margin-top:4px;color:#888;">Click a cell to open the recording (or scenario detail if none).</div>
    </div>
  `;
  return legend;
}

function renderCoverageMatrix(detail) {
  // catalog.agents is [{id, onboarded}, …] — extract ids for column iteration.
  const agents = (catalog.agents || []).map(a => typeof a === "string" ? a : a.id);
  // Recording lookup: only "scenarios" subtree counts here; regression
  // captures are not part of the coverage matrix.
  const recIndex = new Map();
  for (const r of scenariosList) {
    if (r.subtree === "scenarios") recIndex.set(`${r.agent}/${r.id}`, r);
  }

  // Window the agent columns to what fits the pane (2–4); the pager in the
  // panel header walks through the rest. The clamped page is written back so
  // it survives re-renders.
  const main = document.getElementById("main");
  const perPage = agentsPerPage(main?.clientWidth || 1200);
  lastPerPage = perPage;
  const pg = paginateAgents(agents, agentPage, perPage);
  agentPage = pg.page;

  // Scenarios are agent-agnostic now (no section/feature) — list them flat in
  // catalog (code) order; the per-row code chip carries the "<section>.<index>".
  const panel = document.createElement("div");
  panel.className = "panel";
  panel.appendChild(buildCoverageHead(agents, pg));
  panel.appendChild(buildCoverageTable(pg, recIndex));
  detail.appendChild(panel);

  // Pipeline status — count cells by where they are in the workflow.
  const {stages, total, withEntry} = computeCoverageStages(agents);
  panel.appendChild(buildCoverageSummary(stages, total, withEntry));

  // Explainer / legend — describes the 7-segment strip and the full
  // state vocabulary for each segment. Each row lists one segment, what
  // it tests, and every glyph that can appear in it with its meaning.
  panel.appendChild(buildCoverageLegend());
}

// renderAgentPager builds the ◀ "1–4 of 6 agents" ▶ control for the
// coverage matrix's windowed agent columns. Buttons disable at the bounds;
// a click moves the page and re-renders the overview from the module-cached
// catalog (loadOverview fetches nothing).
function renderAgentPager(pg, agents) {
  const pager = document.createElement("div");
  pager.style.cssText = "display: inline-flex; gap: 6px; align-items: center; font-size: 11px; color: #555; white-space: nowrap;";
  pager.title = `Agents: ${agents.join(", ")}`;

  const btn = (label, delta, disabled) => {
    const b = document.createElement("button");
    b.textContent = label;
    b.disabled = disabled;
    b.style.cssText = `padding: 2px 8px; border-radius: 3px; border: 1px solid #d8d6cc; ` +
      `background: ${disabled ? "#f0efe9" : "#fff"}; color: ${disabled ? "#bbb" : "#333"}; ` +
      `font: inherit; font-weight: 600; cursor: ${disabled ? "default" : "pointer"};`;
    if (!disabled) {
      b.addEventListener("click", () => {
        agentPage = pg.page + delta;
        loadOverview();
      });
    }
    return b;
  };

  pager.appendChild(btn("◀", -1, pg.page === 0));
  const label = document.createElement("span");
  label.textContent = `${pg.start + 1}–${pg.end} of ${agents.length} agents`;
  pager.appendChild(label);
  pager.appendChild(btn("▶", +1, pg.page >= pg.pages - 1));
  return pager;
}

// loadCoverageDetail shows the per-agent testing plan for one scenario.
// Combines:
//   - the agent-agnostic scenario spec (description / process / acceptance),
//     from /api/scenario-spec, in the spec panel.
//   - per-agent plan panels (buildAgentPlanPanel) — the derived verdict +
//     the cell's own recipe (metadata.json → details.recipe) and which
//     committed recordings exist for each agent.
async function loadCoverageDetail(scenarioId) {
  if (!catalog || !Array.isArray(catalog.scenarios)) {
    navigate("#/");
    return;
  }
  const sc = catalog.scenarios.find(s => s.id === scenarioId);
  if (!sc) {
    console.warn("loadCoverageDetail: unknown scenario id", scenarioId);
    navigate("#/");
    return;
  }

  document.querySelectorAll(".scn").forEach(e => e.classList.remove("active"));
  const codePrefix = sc.code ? `${sc.code} ` : "";
  document.title = `Irrlicht — ${codePrefix}${sc.id}`;
  document.getElementById("title").textContent = sc.id;
  document.getElementById("breadcrumb").textContent = sc.code ? `${sc.code} · ${sc.id}` : sc.id;
  const detail = document.getElementById("detail");
  detail.innerHTML = "";

  // Back-to-matrix link — goes through navigate() so it adds a
  // history entry (forward then takes you back to the detail).
  const back = document.createElement("button");
  back.textContent = "← Back to overview";
  back.style.cssText = "background: transparent; border: 0; color: #1f56a8; padding: 0 0 10px; cursor: pointer; font-size: 12px;";
  back.addEventListener("click", () => navigate("#/"));
  detail.appendChild(back);

  // Header — what the scenario is + identifiers
  const header = document.createElement("div");
  header.className = "panel";
  const codeBadge = sc.code
    ? `<span style="display: inline-block; padding: 2px 8px; margin-right: 8px; background: #e8e6da; color: #555; border-radius: 3px; font-size: 12px; font-weight: 600; font-family: monospace; vertical-align: 4px;">${escapeHtml(sc.code)}</span>`
    : "";
  header.innerHTML = `
    <h3 style="margin-top:0;">${codeBadge}${sc.id}</h3>
    <div style="font-size: 11px; color: #888; margin-bottom: 6px;">
      <code>${sc.id}</code>
    </div>
  `;
  detail.appendChild(header);

  // Spec panel — straight from .specs/agent-scenarios.md if the file
  // is reachable. Shows the maintainer's prose scenario text + the
  // Expected bullets that any agent's recipe must satisfy. Always
  // re-fetched so edits to the spec land on next page refresh.
  try {
    const spec = await fetch("/api/scenario-spec/" + encodeURIComponent(sc.id))
      .then(r => r.ok ? r.json() : null);
    if (spec && (spec.description || spec.process || spec.acceptance_criteria)) {
      detail.appendChild(renderSpecPanel(spec));
    }
  } catch (e) { console.debug('scenario spec unavailable — showing recipe-only', e); }

  // Recipe lookup by coverage_id — used by the per-agent plan panels below.
  // The old scenario-level "Recording recipe" panel was removed: a scenario has
  // no recipe of its own under the agent-agnostic model (recipes live per
  // (scenario, agent) cell in metadata.json → details.recipe, surfaced in the
  // per-agent panels + the recording detail page), and the fields it rendered
  // (requires / verify) were dropped in the schema cutover. The scenario's
  // description / process / acceptance are already shown in the spec panel.
  const recipe = recipesByCoverageId.get(sc.id);

  // Per-agent plan panels
  const agents = (catalog.agents || []).map(a => typeof a === "string" ? a : a.id);
  for (const agent of agents) {
    detail.appendChild(buildAgentPlanPanel(sc, agent, recipe));
  }
}

// renderSpecPanel turns the parsed .specs/agent-scenarios.md block
// into a card showing the maintainer's prose scenario text + the
// Expected bullets. Multiple Scenario:/Expected: sub-blocks under one
// Feature (e.g. session-end has three) each get their own sub-heading.
function renderSpecPanel(spec) {
  const panel = document.createElement("div");
  panel.className = "panel";
  // process / acceptance_criteria are markdown-ish (numbered steps, "- "
  // bullets, `code`). Render as escaped pre-wrap so the structure stays
  // readable without pulling in a markdown engine.
  const block = (md) =>
    `<div style="font-size: 12px; color: #333; white-space: pre-wrap; line-height: 1.5;">${escapeHtml(md || "")}</div>`;
  let html = `<h3 style="margin-top:0;">Scenario <span style="font-weight: normal; color: #888; font-size: 11px;">— applies to all agents</span></h3>`;
  if (spec.description) {
    html += `<div style="font-size: 12px; color: #333; margin-bottom: 12px;">${escapeHtml(spec.description)}</div>`;
  }
  if (spec.process) {
    html += `<div style="font-size: 11px; color: #666; font-weight: 600; margin-bottom: 4px;">Process</div>`;
    html += block(spec.process);
  }
  if (spec.acceptance_criteria) {
    html += `<div style="font-size: 11px; color: #666; font-weight: 600; margin: 12px 0 4px;">Acceptance criteria</div>`;
    html += block(spec.acceptance_criteria);
  }
  panel.innerHTML = html;
  return panel;
}

// _agentPlanHeaderHTML builds the per-agent card heading: agent name,
// coverage badge, and the supports/daemon/driver summary line.
function _agentPlanHeaderHTML(agent, cov) {
  const sup = cov?.agent_supports || "unknown";
  const daemon = cov?.daemon_capability || "unknown";
  const driver = cov?.driver_capability || "ready";
  const display = cov?.display_state || "unknown";
  const {label, bg, fg} = coverageBadge(display);
  return `
    <h3 style="margin-top:0; display: flex; align-items: center; gap: 8px;">
      ${agent}
      <span style="background: ${bg}; color: ${fg}; padding: 1px 8px; border-radius: 10px; font-size: 11px; font-weight: 600;">${label}</span>
    </h3>
    <div style="font-size: 11px; color: #555; margin-bottom: 6px;">
      agent_supports: <b>${sup}</b> · daemon: <b>${daemon}</b> · driver: <b>${driver}</b>
    </div>
  `;
}

// _renderRecipeDriverHTML renders the driver-specific block (interactive
// script vs headless prompt vs neither) for one agent's by_adapter entry.
function _renderRecipeDriverHTML(agent, a, idleTag) {
  if (Array.isArray(a.script)) {
    let html = `<div style="font-size: 11px; color: #666; margin: 8px 0 4px;">
        <b>Driver:</b> Interactive (tmux REPL) — <code>drive-${agent}-interactive.sh</code>${idleTag}
      </div>`;
    html += renderStepScript(a.script);
    return html;
  }
  if (a.prompt) {
    return `<div style="font-size: 11px; color: #666; margin: 8px 0 4px;">
        <b>Driver:</b> Headless (<code>--print</code>) — <code>drive-${agent}.sh</code>${idleTag}
      </div>
      <pre style="background: #1e1e1e; color: #d4d4d4; padding: 8px; border-radius: 4px; font-size: 11px; white-space: pre-wrap; margin: 0;">${escapeHtml(a.prompt)}</pre>`;
  }
  return `<div style="font-size: 12px; color: #888;">Recipe entry exists but has no prompt or script.</div>`;
}

// _renderRecipeMetaHTML renders the optional timeout/settings footer line.
function _renderRecipeMetaHTML(a) {
  const timeout = a.timeout_seconds;
  const settings = a.settings || {};
  const meta = [];
  if (typeof timeout === "number") meta.push(`timeout: ${timeout}s`);
  if (Object.keys(settings).length) meta.push(`settings: <code>${escapeHtml(JSON.stringify(settings))}</code>`);
  if (!meta.length) return "";
  return `<div style="font-size: 11px; color: #888; margin-top: 6px;">${meta.join(" · ")}</div>`;
}

// _renderRecipeChecklistsHTML renders the preconditions/setup/verify
// checklists — only present on recipes authored by the per-cell workflow.
function _renderRecipeChecklistsHTML(a) {
  let html = "";
  if (Array.isArray(a.preconditions) && a.preconditions.length) {
    html += renderChecklistBlock("Preconditions", a.preconditions, "□");
  }
  if (Array.isArray(a.setup) && a.setup.length) {
    html += renderChecklistBlock("Setup (run-cell.sh handles this)", a.setup, "•");
  }
  if (Array.isArray(a.verify) && a.verify.length) {
    html += renderChecklistBlock("Verify after recording", a.verify, "□");
  }
  return html;
}

// _renderRecipeSectionHTML composes the full recipe section per agent. Two
// by_adapter shapes in scenarios.json:
//   - by_adapter.<agent>.prompt → headless driver (drive-<adapter>.sh)
//   - by_adapter.<agent>.script → interactive tmux driver (drive-<adapter>-interactive.sh)
// Falls back to explanatory copy when the agent (or the recipe itself)
// has no entry yet.
function _renderRecipeSectionHTML(sc, agent, recipe) {
  const entry = recipe?.by_adapter?.[agent];
  if (!entry) {
    return recipe
      ? `<div style="font-size: 12px; color: #888; padding: 6px 0;">
      No <code>by_adapter.${agent}</code> entry on the recipe — adapter doesn't
      currently drive this scenario. Either the capability is missing, or the
      recipe just hasn't been written yet.
    </div>`
      : `<div style="font-size: 12px; color: #888; padding: 6px 0;">
      No recording recipe wired to this scenario (no <code>coverage_id: "${sc.id}"</code>
      in scenarios.json yet).
    </div>`;
  }
  // Idle-only badge when the recipe is observation-only (no prompts sent).
  const idleTag = recipe.idle_only
    ? ` <span style="background: #e0eaff; color: #1f3d8a; padding: 1px 6px; border-radius: 8px; font-size: 10px; font-weight: 600; margin-left: 6px;">idle observation</span>`
    : "";
  return _renderRecipeDriverHTML(agent, entry, idleTag) +
    _renderRecipeMetaHTML(entry) +
    _renderRecipeChecklistsHTML(entry);
}

// _findAgentRecording resolves the on-disk recording for one (scenario,
// agent) pair. Variant-folder aware: falls back to the recipe name when
// folder_by_agent doesn't have an override for this agent.
function _findAgentRecording(recipe, agent) {
  const recFolder = recipe?.folder_by_agent?.[agent] || recipe?.name;
  return scenariosList.find(r => r.subtree === "scenarios" && r.agent === agent && recFolder && r.id === recFolder);
}

// buildAgentPlanPanel composes one card per agent showing how this
// scenario is (or would be) recorded for that agent: coverage verdict,
// notes, driver choice, step-script or prompt, and any existing
// recording.
function buildAgentPlanPanel(sc, agent, recipe) {
  const panel = document.createElement("div");
  panel.className = "panel";
  panel.style.marginBottom = "12px";

  const cov = sc.coverage?.[agent];
  let html = _agentPlanHeaderHTML(agent, cov);
  if (cov?.notes) {
    html += `<div style="font-size: 12px; color: #444; padding: 6px 8px; background: #fafaf2; border-left: 3px solid #d8d6cc; margin-bottom: 8px;">${escapeHtml(cov.notes)}</div>`;
  }

  html += _renderRecipeSectionHTML(sc, agent, recipe);

  // Existing recording link if one is committed.
  const rec = _findAgentRecording(recipe, agent);
  if (rec) {
    html += `<div style="margin-top: 8px;">`;
    html += `<button class="open-rec" data-agent="${agent}" data-id="${rec.id}" style="background: #1f56a8; color: white; border: 0; padding: 4px 10px; border-radius: 3px; cursor: pointer; font-size: 11px;">↻ Open recording: ${agent}/${rec.id}</button>`;
    html += `</div>`;
  }

  panel.innerHTML = html;
  // Wire button after innerHTML (can't pass closure through innerHTML).
  // Route through navigate() so the URL updates and back/forward work.
  panel.querySelectorAll(".open-rec").forEach(btn => {
    btn.addEventListener("click", () => {
      if (rec) navigate(`#/recording/${rec.agent}/${rec.subtree}/${rec.id}`);
    });
  });
  return panel;
}

// renderChecklistBlock paints a labelled bullet list of plain-English
// items. `glyph` is the bullet — use "□" for things the operator should
// tick off (preconditions, verify), "•" for plain facts (setup).
function renderChecklistBlock(label, items, glyph) {
  let html = `<div style="font-size: 11px; color: #666; margin: 12px 0 4px;"><b>${label}</b></div>`;
  html += `<ul style="font-size: 12px; padding-left: 22px; margin: 0; color: #333; list-style: none;">`;
  for (const it of items) {
    html += `<li style="margin-bottom: 4px;"><span style="display: inline-block; width: 16px; color: #888;">${glyph}</span>${escapeHtml(it)}</li>`;
  }
  html += `</ul>`;
  return html;
}

function renderStepScript(steps) {
  let html = `<ol style="font-size: 12px; padding-left: 22px; margin: 4px 0; color: #333;">`;
  for (const step of steps) {
    if (step.type === "send" || step.type === "slash") {
      html += `<li><b>${step.type === "slash" ? "Slash command" : "Send prompt"}:</b> <code style="background:#f5f4ee;padding:1px 4px;border-radius:2px;">${escapeHtml(step.text || "")}</code></li>`;
    } else if (step.type === "wait_turn") {
      html += `<li><b>Wait for turn</b> — block until the agent finishes the current LLM round</li>`;
    } else if (step.type === "sleep") {
      html += `<li><b>Sleep ${step.seconds}s</b> — let the state classifier settle / next turn settle</li>`;
    } else if (step.type === "interrupt") {
      html += `<li><b>Interrupt</b> — send Escape (claudecode/codex/pi) or Ctrl-C (aider) mid-turn</li>`;
    } else {
      html += `<li><code>${escapeHtml(JSON.stringify(step))}</code></li>`;
    }
  }
  html += `</ol>`;
  return html;
}

// renderRecipePanel renders the by_adapter recipe entry for this
// cell on the recording-detail page — same shape used by the
// scenario-coverage page, just framed as a standalone panel so the
// pipeline-strip ✎ segment has a scroll target. `recipe` is the
// scenarios.json -> scenarios[].by_adapter[<agent>] block, or null
// if no recipe is authored.
function renderRecipePanel(recipe) {
  const p = panel("Recipe", "recipe");
  if (!recipe || !Array.isArray(recipe.script) || recipe.script.length === 0) {
    p.appendChild(text("No recipe authored for this cell. The /ir:onboard-agent recipe skill produces the by_adapter entry; pipeline stops here until that lands."));
    return p;
  }
  const intro = document.createElement("div");
  intro.style.cssText = "font-size: 11px; color: #666; margin-bottom: 8px;";
  const driver = inferDriverLabel(recipe);
  intro.innerHTML = `<b>Driver:</b> ${escapeHtml(driver)}` +
    (recipe.timeout_seconds ? ` · <b>Timeout:</b> ${recipe.timeout_seconds}s` : "");
  p.appendChild(intro);
  const stepsWrap = document.createElement("div");
  stepsWrap.innerHTML = renderStepScript(recipe.script);
  p.appendChild(stepsWrap);
  if (Array.isArray(recipe.preconditions) && recipe.preconditions.length > 0) {
    const pcWrap = document.createElement("div");
    pcWrap.innerHTML = renderChecklistBlock("Preconditions", recipe.preconditions, "□");
    p.appendChild(pcWrap);
  }
  if (Array.isArray(recipe.verify) && recipe.verify.length > 0) {
    const vWrap = document.createElement("div");
    vWrap.innerHTML = renderChecklistBlock("Verify after recording", recipe.verify, "□");
    p.appendChild(vWrap);
  }
  return p;
}

// _displayMeta is the single palette + label source for the #476 derived
// display state (computed daemon-side in catalog.go, attached to each
// coverage cell as `display_state`). Every chip/badge/outline that needs
// to show "where does this cell stand" reads through here so colors and
// wording stay consistent across the overview strip, the detail header,
// and the legend.
function _displayMeta(state) {
  switch (state) {
    case "observed":       return {bg: "#d6f0d4", fg: "#1f5a1d", text: "observed"};
    case "pending-record": return {bg: "#e7eef7", fg: "#33598a", text: "pending record"};
    case "blocked-driver": return {bg: "#fde7c1", fg: "#8a4500", text: "blocked: driver"};
    case "blocked-daemon": return {bg: "#f8c8c8", fg: "#8a0000", text: "blocked: daemon"};
    case "unobservable":   return {bg: "#ffcda3", fg: "#a8480a", text: "unobservable"};
    case "n.a.":           return {bg: "#eeece4", fg: "#999",    text: "n.a."};
    default:               return {bg: "#e5e5e5", fg: "#555",    text: "unknown"};
  }
}

// coverageBadge renders the detail-page header pill from the derived
// display state.
function coverageBadge(displayState) {
  const m = _displayMeta(displayState);
  return {label: m.text, bg: m.bg, fg: m.fg};
}

// _axisBadge returns chip data for the agent_supports axis (segment 1 of
// the strip). glyph ⚙ ("agent has this capability"); the chip color
// carries the state.
function _axisBadge(value) {
  const label = "⚙";
  switch (value) {
    case "yes":     return {label, bg: "#d6f0d4", fg: "#1f5a1d"};
    case "partial": return {label, bg: "#fde7c1", fg: "#8a4500"};
    case "no":      return {label, bg: "#f8c8c8", fg: "#8a0000"};
    case "n/a":     return {label, bg: "#eeece4", fg: "#999"};
    default:        return {label, bg: "#e5e5e5", fg: "#555"}; // unknown / undefined
  }
}

// _daemonBadge returns chip data for segment 2 — the daemon sensor-capture
// pillar (daemon_capability). glyph ◉; color carries the raw verdict (not the
// rolled-up display state) so the overview shows the three pillars individually.
function _daemonBadge(value) {
  const label = "◉";
  switch (value) {
    case "full":      return {label, bg: "#d6f0d4", fg: "#1f5a1d"};
    case "bug":       return {label, bg: "#f8c8c8", fg: "#8a0000"};
    case "incapable": return {label, bg: "#ffcda3", fg: "#a8480a"};
    case "n/a":       return {label, bg: "#eeece4", fg: "#999"};
    default:          return {label, bg: "#e5e5e5", fg: "#555"}; // unknown / undefined
  }
}

// _driverBadge returns chip data for segment 3 — the driver-capability pillar.
// glyph ▷ ("the harness can drive it"); color carries ready vs a gap:* primitive.
function _driverBadge(value) {
  const label = "▷";
  if (value === "ready") return {label, bg: "#d6f0d4", fg: "#1f5a1d"};
  if (String(value).startsWith("gap:")) return {label, bg: "#fde7c1", fg: "#8a4500"};
  return {label, bg: "#e5e5e5", fg: "#555"}; // unknown / undefined
}

// renderPipelineStrip paints a compact 7-segment indicator that
// summarizes where a single (agent × scenario) cell sits in the
// onboarding workflow:
//
//   [ Supports ][ Daemon ][ Driver ][ Recipe ][ Spec ][ N recordings ][ Validation ]
//
// Segments 1-3 are the three assessment pillars (agent_supports /
// daemon_capability / driver_capability), each shown individually.
//
// Each segment is its OWN clickable button that jumps to the
// corresponding section on the cell's detail page (via the
// ?focus=<key> hash suffix). Reads left-to-right as a progression.
// Filled segments = stage complete; dim = stage not reached.
//
// A cell-level outline highlights drift between the maintainer's
// verdict and the measured outcome (matrix-stale or regression).
//
// Inputs:
//   agent — adapter slug for tooltip labelling and the navigation target
//   scenarioID — coverage_id for the scenario detail link
//   cov   — one entry from coverage[<agent>] (assessment + pipeline + measurement)
//   rec   — recording lookup entry from recIndex (or undefined)
// _appendPipelineTailSegments appends the four right-hand segments
// (Recipe / Spec / Recordings / Validation) — or, when the cell is
// blocked (agent_supports=no), four dim disabled placeholders so the
// cell width stays consistent across rows.
function _appendPipelineTailSegments(wrap, blocked, pipe, meas, jump) {
  if (blocked) {
    wrap.appendChild(_pipeBtn("·", "transparent", "#bbb", null, true,
      "Pipeline frozen — agent_supports=no"));
    wrap.appendChild(_pipeBtn("·", "transparent", "#bbb", null, true, ""));
    wrap.appendChild(_pipeBtn("·", "transparent", "#bbb", null, true, ""));
    wrap.appendChild(_pipeBtn("·", "transparent", "#bbb", null, true, ""));
    return;
  }
  const recipe = pipe.recipe || {};
  const spec = pipe.spec || {};
  const rcs = pipe.recordings || {};
  // Recipe
  wrap.appendChild(recipe.authored
    ? _pipeBtn("✎", "#d6f0d4", "#1f5a1d", jump("recipe"), false,
        `Recipe authored (${recipe.step_count} steps)`)
    : _pipeBtn("·", "transparent", "#bbb", jump("recipe"), false,
        "Recipe — not authored yet"));
  // Spec
  wrap.appendChild(spec.authored
    ? _pipeBtn("§", "#d6f0d4", "#1f5a1d", jump("spec"), false,
        `Spec authored (${spec.phase_count} phases)`)
    : _pipeBtn("·", "transparent", "#bbb", jump("spec"), false,
        "Spec — not authored yet"));
  // Recordings count (latest counts as 1; archive_count is additional)
  const totalRecs = (rcs.latest ? 1 : 0) + (rcs.archive_count || 0);
  wrap.appendChild(totalRecs > 0
    ? _pipeBtn(String(totalRecs), "#d6f0d4", "#1f5a1d", jump("recordings"), false,
        `${totalRecs} recording${pluralSuffix(totalRecs)}`)
    : _pipeBtn("·", "transparent", "#bbb", jump("recordings"), false,
        "No recordings yet"));
  // Validation
  const v = _validationGlyph(meas.status);
  wrap.appendChild(v
    ? _pipeBtn(v.label, v.bg, v.fg, jump("validation"), false,
        `Validation: ${meas.status}`)
    : _pipeBtn("·", "transparent", "#bbb", jump("validation"), false,
        "Validation — no recording yet"));
}

// _DRIFT_STYLES / _DRIFT_TOOLTIP_NOTES key the same three drift kinds to
// the pipeline strip's outline color and its tooltip note, so the two
// stay in lockstep by construction instead of by re-deriving the kind
// from a rendered CSS string (as the pre-refactor code did).
const _DRIFT_STYLES = {
  regression: {border: "1px solid #c0392b", background: "#fff5f5"},
  flag_drop: {border: "1px solid #1c3f7a", background: "#f0f5ff"},
  stale_verdict: {border: "1px solid #d68a2a", background: "#fffaf0"},
};
const _DRIFT_TOOLTIP_NOTES = {
  regression: "⚠ regression: daemon=full/driver=ready but recording fails",
  flag_drop: "↑ flag drop: marked daemon=bug / known_failing but now passes",
  stale_verdict: "⚠ verdict may be stale: marked blocked/unobservable but recording passes",
};

// _computeDriftKind classifies drift between the assessed capability
// verdict and the measured recording outcome:
//   regression    = expected to observe cleanly (full+ready) but the recording fails
//   flag_drop     = marked daemon=bug or known_failing yet the recording now passes
//                   (the bug/known_failing verdict is stale — drop it)
//   stale_verdict = marked blocked/unobservable yet a recording passes clean
//                   (capability verdict looks stale)
// Returns null when there is no drift to flag.
function _computeDriftKind(daemon, driver, meas) {
  const capable = (daemon === "full" && driver === "ready");
  const verdictBlocks = (daemon === "incapable" || daemon === "bug" ||
    String(driver).startsWith("gap:"));
  if (meas.status === "fail" || (meas.status === "known_failing" && capable)) {
    return "regression";
  }
  if (meas.status === "known_failing_now_passing" ||
      (meas.status === "pass" && daemon === "bug")) {
    return "flag_drop";
  }
  if (meas.status === "pass" && verdictBlocks) {
    return "stale_verdict";
  }
  return null;
}

// _buildPipelineStageLines renders the Recipe/Spec/Recordings/Validation
// tooltip lines shown when the cell isn't blocked.
function _buildPipelineStageLines(pipe, meas) {
  const recipe = pipe.recipe || {};
  const spec = pipe.spec || {};
  const rcs = pipe.recordings || {};
  const recipeStatus = recipe.authored ? `authored (${recipe.step_count} steps)` : "not authored yet";
  const specStatus = spec.authored ? `authored (${spec.phase_count} phases)` : "not authored yet";
  const lines = [`Recipe: ${recipeStatus}`, `Spec:   ${specStatus}`];
  const totalRecs = (rcs.latest ? 1 : 0) + (rcs.archive_count || 0);
  if (totalRecs > 0) {
    const parts = [];
    if (rcs.latest) parts.push("1 latest");
    if (rcs.archive_count > 0) parts.push(`${rcs.archive_count} archived`);
    lines.push(`Recordings: ${totalRecs} (${parts.join(" + ")})`);
  } else {
    lines.push(`Recordings: none yet`);
  }
  if (meas.status && meas.status !== "no_recording" && meas.status !== "no_expected") {
    lines.push(`Validation: ${meas.status}${meas.summary ? " — " + meas.summary : ""}`);
  }
  return lines;
}

// _pipelineFields derives the individual assessment/pipeline/measurement
// fields renderPipelineStrip and _buildPipelineTooltip both need from one
// coverage record, so neither has to carry them as a long parallel-value
// parameter list (javascript:S107).
function _pipelineFields(cov) {
  const sup = cov.agent_supports || "unknown";
  const daemon = cov.daemon_capability || "unknown";
  const driver = cov.driver_capability || "ready";
  const display = cov.display_state || "unknown";
  const pipe = cov.pipeline || {};
  const meas = cov.measurement || {};
  // agent_supports=no freezes the whole pipeline — nothing downstream
  // matters. The Supports segment shows the state; the Daemon + Driver
  // pillars still render, and everything after them collapses to dim
  // placeholders.
  const blocked = (sup === "no");
  return { sup, daemon, driver, display, pipe, meas, blocked };
}

// _buildPipelineTooltip composes the full per-stage tooltip text for a
// pipeline strip.
function _buildPipelineTooltip(agent, scenarioID, cov, driftKind) {
  const { sup, daemon, driver, display, pipe, meas, blocked } = _pipelineFields(cov);
  const lines = [`${agent} × ${scenarioID}`];
  lines.push(`Assessment: supports=${sup}, daemon=${daemon}, driver=${driver} → ${_displayMeta(display).text}`);
  if (cov.notes) lines.push(`  ${cov.notes}`);
  if (blocked) {
    lines.push(`(pipeline frozen — agent_supports=no)`);
  } else {
    lines.push(..._buildPipelineStageLines(pipe, meas));
  }
  if (driftKind) lines.push(_DRIFT_TOOLTIP_NOTES[driftKind]);
  lines.push(`↻ click a segment to jump to its section`);
  return lines.join("\n");
}

function renderPipelineStrip(agent, scenarioID, cov, rec) {
  const { sup, daemon, driver, pipe, meas, blocked } = _pipelineFields(cov);

  // Outer container is a plain <div>; each segment is its own button.
  // This is the change from a single composite button to a true
  // toolbar of six controls.
  const wrap = document.createElement("div");
  wrap.style.cssText = "display: inline-flex; gap: 2px; padding: 2px; " +
    "background: transparent; border: 1px solid transparent; border-radius: 4px; " +
    "font: inherit; align-items: center;";

  // jump builds the navigation target for one segment. If rec exists
  // (cell has a recording), we land on the recording-detail page;
  // otherwise on the scenario-coverage page. Both routes honor
  // ?focus=<key>.
  const jump = (focusKey) => () => {
    if (rec) {
      navigate(`#/recording/${rec.agent}/${rec.subtree}/${rec.id}?focus=${focusKey}`);
    } else {
      navigate(`#/scenario/${scenarioID}?focus=${focusKey}`);
    }
  };

  // Build 7 segments. Segments 1-3 are the three assessment pillars, each its
  // own button; all jump to the Assessment panel (which renders the pillars in
  // full inside).
  const supChip = _axisBadge(sup);
  const daemonChip = _daemonBadge(daemon);
  const driverChip = _driverBadge(driver);
  wrap.appendChild(_pipeBtn(supChip.label, supChip.bg, supChip.fg,
    jump("supports"), false, `agent_supports: ${sup} — agent CLI implements this feature`));
  wrap.appendChild(_pipeBtn(daemonChip.label, daemonChip.bg, daemonChip.fg,
    jump("observes"), false, `daemon: ${daemon} — can the daemon observe it`));
  wrap.appendChild(_pipeBtn(driverChip.label, driverChip.bg, driverChip.fg,
    jump("observes"), false, `driver: ${driver} — can the harness drive it`));
  _appendPipelineTailSegments(wrap, blocked, pipe, meas, jump);

  // Drift outline — see _computeDriftKind for the three cases.
  const driftKind = _computeDriftKind(daemon, driver, meas);
  if (driftKind) {
    const style = _DRIFT_STYLES[driftKind];
    wrap.style.border = style.border;
    wrap.style.background = style.background;
  }

  wrap.title = _buildPipelineTooltip(agent, scenarioID, cov, driftKind);

  return wrap;
}

function _pipeSeg(label, bg, fg) {
  const seg = document.createElement("span");
  seg.textContent = label;
  seg.style.cssText = `display: inline-block; min-width: 18px; padding: 1px 4px; ` +
    `border-radius: 3px; font: inherit; font-size: 11px; font-weight: 600; ` +
    `line-height: 1; text-align: center; background: ${bg}; color: ${fg};`;
  return seg;
}

// _pipeBtn is the per-segment button. Same visual chip as _pipeSeg
// but wrapped in a <button> with its own onclick + tooltip.
// disabled=true grays out and makes it non-clickable (used for the
// blocked placeholders when supports=no).
function _pipeBtn(label, bg, fg, onclick, disabled, title) {
  const btn = document.createElement("button");
  btn.textContent = label;
  btn.style.cssText = `display: inline-block; min-width: 18px; padding: 2px 5px; ` +
    `border-radius: 3px; border: 0; font: inherit; font-size: 11px; font-weight: 600; ` +
    `line-height: 1; text-align: center; background: ${bg}; color: ${fg}; ` +
    `cursor: ${disabled ? "default" : "pointer"};` +
    (disabled ? " opacity: 0.6;" : "");
  if (title) btn.title = title;
  if (disabled) {
    btn.disabled = true;
  } else if (onclick) {
    btn.addEventListener("click", (e) => {
      e.stopPropagation();
      onclick();
    });
  }
  return btn;
}

function _validationGlyph(status) {
  switch (status) {
    case "pass":                      return {label: "✓", bg: "#d6f0d4", fg: "#1f5a1d"};
    case "known_failing":             return {label: "⚠", bg: "#fff7d6", fg: "#8a4500"};
    case "known_failing_now_passing": return {label: "↑", bg: "#cfe7ff", fg: "#1c3f7a"};
    case "fail":                      return {label: "✗", bg: "#f8c8c8", fg: "#8a0000"};
    case "validator_error":           return {label: "!", bg: "#e5e5e5", fg: "#555"};
    default:                          return null; // no_recording / no_expected
  }
}


// _collectDeclaredAdapters gathers every adapter slug that declares at
// least one by_adapter entry across all scenarios, sorted for a stable
// column order.
function _collectDeclaredAdapters(scenarios) {
  const adapterSet = new Set();
  for (const sc of scenarios) {
    for (const a of Object.keys(sc.by_adapter || {})) adapterSet.add(a);
  }
  return [...adapterSet].sort((a, b) => a.localeCompare(b));
}

// _buildScenarioRecIndex indexes committed scenario recordings by
// "<agent>/<id>" for O(1) lookup while painting the matrix.
function _buildScenarioRecIndex(recordings) {
  const recIndex = new Map();
  for (const r of recordings) {
    if (r.subtree === "scenarios") recIndex.set(`${r.agent}/${r.id}`, r);
  }
  return recIndex;
}

// _buildScenarioMatrixCell paints one (scenario × adapter) cell: dim "—"
// when the adapter doesn't declare this scenario, an open-recording ✓
// button when it does and a recording is committed, or an amber "○"
// when declared but not yet recorded.
function _buildScenarioMatrixCell(sc, adapter, recIndex) {
  const cell = document.createElement("td");
  cell.style.textAlign = "center";
  const declares = sc.by_adapter?.[adapter];
  if (!declares) {
    cell.textContent = "—";
    cell.style.color = "#ccc";
    cell.title = `${adapter}: not declared`;
    return cell;
  }
  const rec = recIndex.get(`${adapter}/${sc.name}`);
  if (!rec) {
    cell.textContent = "○";
    cell.style.color = "#c08a00";
    cell.title = `${adapter}: declared but no recording committed`;
    return cell;
  }
  const btn = document.createElement("button");
  btn.textContent = "✓";
  btn.title = `Open ${adapter}/${sc.name}`;
  btn.style.cssText = "background: transparent; border: 0; color: #2a8d4f; font-size: 16px; cursor: pointer; padding: 0;";
  btn.addEventListener("click", () => {
    navigate(`#/recording/${rec.agent}/${rec.subtree}/${rec.id}`);
  });
  cell.appendChild(btn);
  return cell;
}

// _buildScenarioMatrixRow paints one scenario's row: name, requires, then
// one cell per adapter column.
function _buildScenarioMatrixRow(sc, adapters, recIndex) {
  const row = document.createElement("tr");
  const nameCell = document.createElement("td");
  nameCell.style.fontWeight = "600";
  nameCell.textContent = sc.name;
  if (sc.description) nameCell.title = sc.description;
  row.appendChild(nameCell);
  const reqCell = document.createElement("td");
  reqCell.style.color = "#888";
  reqCell.style.fontSize = "11px";
  reqCell.textContent = (sc.requires || []).join(", ");
  row.appendChild(reqCell);
  for (const adapter of adapters) {
    row.appendChild(_buildScenarioMatrixCell(sc, adapter, recIndex));
  }
  return row;
}

// renderScenariosMatrix paints the older 8×5 by_adapter view from
// scenarios.json (fallback when .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json
// isn't reachable).
function renderScenariosMatrix(detail) {
  const adapters = _collectDeclaredAdapters(catalog.scenarios);
  const recIndex = _buildScenarioRecIndex(scenariosList);

  const panel = document.createElement("div");
  panel.className = "panel";
  const h3 = document.createElement("h3");
  h3.textContent = `Agent scenarios (${catalog.scenarios.length})`;
  panel.appendChild(h3);

  const table = document.createElement("table");
  table.className = "overview-matrix";
  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  ["Scenario", "Requires", ...adapters].forEach(h => {
    const th = document.createElement("th");
    th.textContent = h;
    headRow.appendChild(th);
  });
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  for (const sc of catalog.scenarios) {
    tbody.appendChild(_buildScenarioMatrixRow(sc, adapters, recIndex));
  }
  table.appendChild(tbody);
  panel.appendChild(table);
  detail.appendChild(panel);
}

// loadScenario renders the detail page. `initialArchive` is the
// optional archive name from the URL (#/recording/.../.../<archive>).
// "" or unspecified → start on the latest recording; otherwise the
// archive selector starts pre-pointed at the named archive (silently
// falls back to latest if the archive doesn't exist for this cell).
// `focus` is the optional ?focus=<key> from the URL — scrolls the
// matching panel into view after render. Empty → no scroll.
async function loadScenario(s, initialArchive, focus) {
  document.querySelectorAll(".scn").forEach(e => e.classList.remove("active"));
  // Find the sidebar button by data-rec-key (set in init() when the
  // button was created). Deep links come through route() without a
  // click event, so the active state has to be restored here.
  const key = `${s.agent}/${s.subtree}/${s.id}`;
  const sidebarBtn = document.querySelector(`.scn[data-rec-key="${CSS.escape(key)}"]`);
  if (sidebarBtn) {
    sidebarBtn.classList.add("active");
    sidebarBtn.scrollIntoView({block: "nearest"});
  }
  // Resolve folder name → coverage_id so the heading matches the
  // overview matrix row label. Multiple recordings can share one
  // coverage_id (e.g. basic-turn + multi-turn-conversation both
  // map to basic-turn); the detail page shows the canonical
  // coverage_id, with the folder name relegated to the breadcrumb.
  // recipesByCoverageId is populated at init from /api/recipes, so
  // this resolution is synchronous. folderToCoverageId resolves variant-folder
  // recordings (e.g. agent-question-pending → user-blocking-question) so the
  // heading matches the overview row; the folder stays in the breadcrumb. Keyed
  // on s.agent so a regression copy under another adapter isn't mis-resolved.
  const coverageId = folderToCoverageId(s.id, s.agent);
  document.title = `Irrlicht — ${coverageId} (${s.agent})`;
  document.getElementById("title").textContent = coverageId;
  document.getElementById("breadcrumb").textContent = `${s.agent} / ${s.subtree} / ${s.id}`;
  const detail = document.getElementById("detail");
  detail.innerHTML = `<p>Loading…</p>`;

  // s.agent/s.subtree/s.id come from the URL hash (sidebarActivePath /
  // route()). Validate each segment against the same slug/subtree
  // contract the backend's /api/scenarios handler enforces, then encode —
  // belt-and-suspenders so a hash crafted with `/`, `?`, or `#` can't
  // retarget the request (SonarQube jssecurity:S7044 / S8476).
  if (!RECORDING_SLUG_RE.test(s.agent) || !RECORDING_SLUG_RE.test(s.id) ||
      (s.subtree !== "scenarios" && s.subtree !== "regressions")) {
    detail.innerHTML = `<p>Invalid recording path.</p>`;
    return;
  }
  const recordingPath = `${encodeURIComponent(s.agent)}/${encodeURIComponent(s.subtree)}/${encodeURIComponent(s.id)}`;
  // Recipe lookup uses recipesByCoverageId (populated once at init from
  // /api/recipes — see comment above), so no per-recording recipes fetch
  // is needed here.
  const [data, archives, catalog] = await Promise.all([
    fetch(`/api/scenarios/${recordingPath}`).then(r => r.json()),
    fetch(`/api/scenarios/${recordingPath}/recordings`).then(r => r.ok ? r.json() : []).catch(() => []),
    // Coverage catalog: lets us render a stub Assessment panel from
    // the matrix verdict + notes when no assessment.json exists.
    // Without this fallback the ⚙ / ◉ pipeline-strip jumps would
    // land nowhere for most cells.
    fetch(`/api/catalog`).then(r => r.ok ? r.json() : null).catch(() => null),
  ]);
  detail.innerHTML = "";

  // No daemon-recorded events.jsonl sidecar: the timeline shown here is
  // reconstructed from the transcript via the shared classifier engine,
  // not recorded. Badge it so a synthesized arc isn't read as ground truth.
  if (data.degraded) {
    detail.appendChild(degradedBanner());
  }

  // Page hierarchy (iteration 13):
  //   1. Recording history selector — TOP, decisive control. Owns
  //      the container of recording-derived panels below it.
  //   2. Spec expectations — ALWAYS visible; content depends on
  //      whether the dropdown is on (none)/Latest (validate
  //      latest's events) or an archive (re-evaluate spec against
  //      archive events).
  //   3. Container below — holds Playback / Meta / GT / Transitions
  //      / Tools / Validate / Signals. Rendered conditionally based
  //      on dropdown state; empty when "(none)" is selected.
  // Look up the per-cell recipe entry, joining on the resolved coverage_id
  // (coverageId, computed above via folderToCoverageId, already handles the
  // variant-folder cells) and this recording's adapter.
  let recipeEntry = null;
  const recipeRow = recipesByCoverageId.get(coverageId);
  if (recipeRow) {
    recipeEntry = recipeRow.by_adapter?.[s.agent];
  }
  // Look up the per-cell coverage entry for the Assessment-fallback
  // panel. Used when no assessment.json exists on disk — the panel
  // still renders so the ⚙ / ◉ pipeline-strip anchors have a target.
  // coverageId was resolved synchronously above (before the await)
  // so the heading could render immediately.
  let coverageEntry = null;
  let coverageFeature = "";
  if (catalog && Array.isArray(catalog.scenarios)) {
    for (const sc of catalog.scenarios) {
      if (sc.id === coverageId) {
        coverageEntry = sc.coverage?.[s.agent];
        coverageFeature = sc.feature || "";
        break;
      }
    }
  }
  // Now that the catalog has resolved, enrich the breadcrumb with the
  // human-friendly feature label (mirrors the overview matrix row,
  // which stacks the coverage_id over the feature name).
  if (coverageFeature) {
    document.getElementById("breadcrumb").textContent =
      `${coverageFeature} · ${s.agent} / ${s.subtree} / ${s.id}`;
  }

  // Cell-level panels rendered at the page top — independent of the
  // selected recording so they don't blink on dropdown changes.
  // Order mirrors the pipeline strip left-to-right:
  //   Assessment (⚙ ◉) → Recipe (✎) → Recording (N) → Spec/Validation (§ ✓) → recording-specific panels.
  if (data.assessment) {
    detail.appendChild(renderAssessment(data.assessment));
  } else {
    detail.appendChild(renderAssessmentFallback(coverageEntry));
  }
  detail.appendChild(renderRecipePanel(recipeEntry));
  detail.appendChild(renderRecordingHistory(s, data, archives, initialArchive || "", recipeEntry, coverageEntry));
  scrollFocusInto(focus || "");
}

// degradedBanner is shown on scenario detail pages that have no
// events.jsonl sidecar. The timeline for such scenarios is synthesized by
// replaying the transcript through core/application/replayengine (the same
// classifier that produces the goldens) — faithful in semantics but not a
// byte-exact recording, so we say so up front.
function degradedBanner() {
  const b = document.createElement("div");
  b.className = "degraded-banner";
  b.dataset.testid = "degraded-banner";
  b.style.cssText =
    "margin:8px 0;padding:8px 12px;border-left:3px solid #c90;" +
    "background:#332b00;color:#e8c84d;border-radius:4px;font-size:13px;";
  b.textContent =
    "No sidecar recorded — playback will synthesize the timeline from the " +
    "transcript via the shared classifier engine (degraded), so the " +
    "transitions below are empty until you press Play. Record with " +
    "`irrlichd --record` for a faithful events.jsonl.";
  return b;
}

function renderMeta(data) {
  const p = panel("Recording metadata");
  if (!data.meta) {
    p.appendChild(text("No recording-meta.json — this recording predates Phase 1's recorder."));
    return p;
  }
  let meta;
  try {
    meta = typeof data.meta === "string" ? JSON.parse(data.meta) : data.meta;
  } catch (e) {
    console.debug('viewer: failed to parse recording meta', e);
    p.appendChild(text("(could not parse meta)"));
    return p;
  }
  // Synthesized-from-events form: render a tidy two-column table with a
  // provenance tag so the maintainer knows the data isn't from the real
  // recorder.
  if (meta.synthesized === true) {
    const tag = document.createElement("div");
    tag.style.cssText = "display: inline-block; padding: 2px 8px; background: #f0efe9; border: 1px solid #d8d6cc; border-radius: 3px; font-size: 11px; color: #555; margin-bottom: 8px;";
    tag.textContent = "synthesized from events.jsonl";
    p.appendChild(tag);
    const tbl = document.createElement("table");
    tbl.innerHTML = "";
    const dur = (meta.duration_ms || 0) / 1000;
    const rows = [
      ["adapter", meta.adapter || "(unknown)"],
      ["started at", meta.started_at || "—"],
      ["ended at", meta.ended_at || "—"],
      ["duration", dur.toFixed(2) + "s"],
      ["total events", meta.total_events || 0],
      ["session count", `${(meta.session_count?.presession || 0)} presession, ${(meta.session_count?.real || 0)} real`],
    ];
    for (const [k, v] of rows) {
      const tr = document.createElement("tr");
      tr.innerHTML = `<td style="width: 140px; color: #666;">${escapeHtml(k)}</td><td><code>${escapeHtml(String(v))}</code></td>`;
      tbl.appendChild(tr);
    }
    // Kinds row — collapse the map into a tidy chip list.
    if (meta.kinds && Object.keys(meta.kinds).length > 0) {
      const tr = document.createElement("tr");
      const td = document.createElement("td");
      td.colSpan = 2;
      td.innerHTML = `<div style="margin-top: 8px; color: #666; font-size: 11px;">event kinds:</div>`;
      const chips = document.createElement("div");
      chips.style.cssText = "display: flex; flex-wrap: wrap; gap: 4px; margin-top: 4px;";
      const sorted = Object.entries(meta.kinds).sort((a, b) => b[1] - a[1]);
      for (const [k, n] of sorted) {
        const c = document.createElement("span");
        c.style.cssText = "padding: 2px 8px; background: #f5f4ee; border: 1px solid #ece9dd; border-radius: 10px; font-size: 11px; font-family: monospace;";
        c.textContent = `${k}: ${n}`;
        chips.appendChild(c);
      }
      td.appendChild(chips);
      tr.appendChild(td);
      tbl.appendChild(tr);
    }
    p.appendChild(tbl);
    return p;
  }
  // Real recording-meta.json: keep the raw JSON dump.
  const pre = document.createElement("pre");
  pre.className = "snapshot";
  pre.textContent = JSON.stringify(meta, null, 2);
  p.appendChild(pre);
  return p;
}

// renderAssessment paints the Stage 1 (Assessment) point-in-time
// record loaded from <scenarioDir>/assessment.json. Surfaces:
//   - dated subtitle (when the assessment was made)
//   - verdict chips for agent_supports + daemon_capability +
//     driver_capability (the orthogonal observability axes, #476)
//   - optional confidence pill
//   - prose body (markdown rendered as preformatted text — headings
//     read fine via the literal `##` prefix)
//   - sources list with URL anchors where applicable
// _renderAssessmentSubtitle builds the "assessed <date>" subtitle line.
function _renderAssessmentSubtitle(a) {
  const sub = document.createElement("div");
  sub.style.cssText = "font-size: 11px; color: #666; margin-bottom: 8px;";
  const when = a.assessed_at ? a.assessed_at.replace("T", " ").replace(/\.\d+Z?$/, "").replace(/Z$/, " UTC") : "date unknown";
  sub.textContent = `assessed ${when}`;
  return sub;
}

// _renderAssessmentVerdictRow builds the agent/daemon/driver verdict
// chips row, plus an optional confidence pill when present.
function _renderAssessmentVerdictRow(a) {
  const row = document.createElement("div");
  row.style.cssText = "display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-bottom: 10px;";
  row.appendChild(_assessmentChip("Agent", a.agent_supports));
  row.appendChild(_capabilityChip("Daemon", a.daemon_capability));
  row.appendChild(_capabilityChip("Driver", a.driver_capability));
  if (typeof a.confidence === "number") {
    const conf = document.createElement("span");
    conf.style.cssText = "padding: 2px 8px; background: #f5f4ee; border: 1px solid #ece9dd; border-radius: 10px; font-size: 11px; font-family: monospace; color: #555;";
    conf.textContent = `confidence ${a.confidence.toFixed(2)}`;
    row.appendChild(conf);
  }
  return row;
}

// _appendAssessmentBody appends the rendered-Markdown prose body, if any.
// renderMarkdown escapes the prose first, so it cannot inject HTML.
function _appendAssessmentBody(p, body) {
  if (!body) return;
  const el = document.createElement("div");
  el.className = "md-body";
  el.innerHTML = renderMarkdown(body);
  p.appendChild(el);
}

// _appendCaveatsBlock appends the labelled caveats box — known
// limitations / metric drifts the verdict doesn't capture but a reader
// should know about. No-op when there are no caveats.
function _appendCaveatsBlock(p, caveats) {
  if (!Array.isArray(caveats) || caveats.length === 0) return;
  const cavHead = document.createElement("div");
  cavHead.style.cssText = "font-size: 11px; color: #666; margin-bottom: 4px;";
  cavHead.textContent = "Caveats";
  p.appendChild(cavHead);
  const cavBox = document.createElement("ul");
  cavBox.style.cssText = "margin: 0 0 10px 0; padding: 8px 10px 8px 28px; font-size: 12px; line-height: 1.5; color: #5a4500; background: #fff7e6; border: 1px solid #f5d886; border-radius: 4px;";
  for (const c of caveats) {
    const li = document.createElement("li");
    li.textContent = c;
    li.style.marginBottom = "4px";
    cavBox.appendChild(li);
  }
  p.appendChild(cavBox);
}

// _buildSourceListItem builds one <li> for the Sources list: a kind
// label, a link (url sources) or code ref (everything else), and an
// optional trailing note.
function _buildSourceListItem(src) {
  const li = document.createElement("li");
  const kind = document.createElement("span");
  kind.style.cssText = "color: #888; margin-right: 6px; font-family: monospace; font-size: 11px;";
  kind.textContent = src.kind || "src";
  li.appendChild(kind);
  if (src.kind === "url" && src.ref) {
    const link = document.createElement("a");
    link.href = src.ref;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    link.textContent = src.ref;
    li.appendChild(link);
  } else if (src.ref) {
    const code = document.createElement("code");
    code.textContent = src.ref;
    li.appendChild(code);
  }
  if (src.note) {
    const note = document.createElement("span");
    note.style.cssText = "color: #555; margin-left: 6px;";
    note.textContent = `— ${src.note}`;
    li.appendChild(note);
  }
  return li;
}

// _appendSourcesBlock appends the Sources list. No-op when there are no
// sources.
function _appendSourcesBlock(p, sources) {
  if (!Array.isArray(sources) || sources.length === 0) return;
  const h = document.createElement("div");
  h.style.cssText = "font-size: 11px; color: #666; margin-bottom: 4px;";
  h.textContent = "Sources";
  p.appendChild(h);
  const ul = document.createElement("ul");
  ul.style.cssText = "margin: 0; padding-left: 18px; font-size: 12px; line-height: 1.5;";
  for (const src of sources) {
    ul.appendChild(_buildSourceListItem(src));
  }
  p.appendChild(ul);
}

function renderAssessment(a) {
  // anchor "supports" — the pipeline-strip pillar segments (⚙ ◉ ▷) all land
  // here (the panel's chips render the three pillars individually).
  const p = panel("Assessment", "supports");
  // Also tag with the observes alias so [data-anchor="observes"]
  // resolves to the same panel.
  p.dataset.anchorAlias = "observes";
  p.appendChild(_renderAssessmentSubtitle(a));
  p.appendChild(_renderAssessmentVerdictRow(a));
  _appendAssessmentBody(p, a.body);
  _appendCaveatsBlock(p, a.caveats);
  _appendSourcesBlock(p, a.sources);
  return p;
}

// renderAssessmentFallback paints a stub Assessment panel for cells
// without an assessment.json artifact. Just the matrix verdict
// (agent_supports + daemon/driver capability chips) + notes line. Keeps the
// ⚙ / ◉ pipeline-strip anchors landable even when no rich record
// exists. coverageEntry may be null if /api/catalog didn't return one
// for this cell.
function renderAssessmentFallback(coverageEntry) {
  // anchor "supports" + alias "observes" — matches renderAssessment so
  // both pipeline-strip segments resolve here.
  const p = panel("Assessment", "supports");
  p.dataset.anchorAlias = "observes";
  const subtitle = document.createElement("div");
  subtitle.style.cssText = "font-size: 11px; color: #666; margin-bottom: 8px;";
  subtitle.textContent = "from matrix verdict — no point-in-time assessment.json on disk yet";
  p.appendChild(subtitle);
  if (!coverageEntry) {
    p.appendChild(text("Coverage matrix has no entry for this cell. Add one in .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json."));
    return p;
  }
  const row = document.createElement("div");
  row.style.cssText = "display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-bottom: 10px;";
  row.appendChild(_assessmentChip("Agent", coverageEntry.agent_supports));
  row.appendChild(_capabilityChip("Daemon", coverageEntry.daemon_capability));
  row.appendChild(_capabilityChip("Driver", coverageEntry.driver_capability));
  p.appendChild(row);
  if (coverageEntry.notes) {
    const notes = document.createElement("div");
    notes.style.cssText = "font-size: 12px; line-height: 1.5; color: #333; padding: 8px 10px; background: #fafaf6; border: 1px solid #ece9dd; border-radius: 4px;";
    notes.textContent = coverageEntry.notes;
    p.appendChild(notes);
  }
  return p;
}

// _assessmentChip maps the agent_supports enum values ("yes" / "partial"
// / "no" / "unknown") to user-facing display labels ("full" / "partial" /
// "none" / "unknown") and a color palette. The daemon/driver axes use
// _capabilityChip instead. The schema stays on the enum values; the
// labels are presentation-only.
function _assessmentChip(prefix, value) {
  const v = String(value || "unknown");
  let label, bg, fg;
  switch (v) {
    case "yes":     label = "full";    bg = "#d6f0d4"; fg = "#1f5a1d"; break;
    case "partial": label = "partial"; bg = "#fde7c1"; fg = "#8a4500"; break;
    case "no":      label = "none";    bg = "#f8c8c8"; fg = "#8a0000"; break;
    case "n/a":     label = "n/a";     bg = "#eeece4"; fg = "#666";    break;
    default:        label = "unknown"; bg = "#e5e5e5"; fg = "#555";    break;
  }
  const chip = document.createElement("span");
  chip.style.cssText = `display: inline-flex; align-items: center; padding: 3px 10px; background: ${bg}; color: ${fg}; border-radius: 12px; font-size: 12px; font-weight: 500;`;
  chip.textContent = `${prefix}: ${label}`;
  return chip;
}

// _capabilityChip renders one orthogonal observability axis (#476):
// daemon_capability (full / bug / incapable / unknown / n/a) or
// driver_capability (ready / gap:<primitive>). The value shows verbatim
// — these are already display-ready — colored by severity.
function _capabilityChip(prefix, value) {
  const v = String(value || "unknown");
  let bg, fg;
  switch (true) {
    case v === "full" || v === "ready": bg = "#d6f0d4"; fg = "#1f5a1d"; break;
    case v === "bug":                   bg = "#f8c8c8"; fg = "#8a0000"; break;
    case v === "incapable":             bg = "#ffcda3"; fg = "#a8480a"; break;
    case v.startsWith("gap:"):          bg = "#fde7c1"; fg = "#8a4500"; break;
    case v === "n/a":                   bg = "#eeece4"; fg = "#999";    break;
    default:                            bg = "#e5e5e5"; fg = "#555";    break; // unknown
  }
  const chip = document.createElement("span");
  chip.style.cssText = `display: inline-flex; align-items: center; padding: 3px 10px; background: ${bg}; color: ${fg}; border-radius: 12px; font-size: 12px; font-weight: 500;`;
  chip.textContent = `${prefix}: ${v}`;
  return chip;
}

// renderExpected paints the spec-grounded expected.jsonl validation
// report. Two modes:
//   "validate" (default) — full UI with pass/fail summary, result + delta
//                          columns, and a failures detail block.
//   "spec"               — definitions only. Used when no recording is
//                          selected so the panel reads as "here is the
//                          spec" rather than "0/N passed against nothing".
// _buildExpectedValidationHeading builds the anchor heading for the
// pipeline-strip ✓ segment ("validation"). The summary chip just below
// carries the pass/fail signal; this small heading just gives the
// scroll-into-view a labelled landing spot.
function _buildExpectedValidationHeading() {
  const valHeading = document.createElement("h4");
  valHeading.dataset.anchor = "validation";
  valHeading.style.cssText = "font-size: 11px; text-transform: uppercase; letter-spacing: 0.05em; color: #666; margin: 0 0 6px 0; font-weight: 600;";
  valHeading.textContent = "Validation";
  return valHeading;
}

// _buildExpectedSummary builds the pass/fail (or spec-only) summary chip
// row shown above the phases table.
function _buildExpectedSummary(rep, specOnly) {
  const summary = document.createElement("div");
  summary.style.cssText = "margin-bottom: 8px; display: flex; gap: 10px; align-items: center; flex-wrap: wrap;";
  if (specOnly) {
    summary.innerHTML = `
      <span style="background: #eaeae0; color: #555; padding: 2px 10px; border-radius: 10px; font-size: 11px; font-weight: 600;">
        spec only — pick a recording to validate
      </span>
      <span style="font-size: 11px; color: #888;">
        source: <code>${escapeHtml(rep.meta?.source || "")}</code>
      </span>
    `;
    return summary;
  }
  const summaryColor = rep.pass ? "#d6f0d4" : "#f8c8c8";
  const summaryFg = rep.pass ? "#1f5a1d" : "#8a0000";
  summary.innerHTML = `
      <span style="background: ${summaryColor}; color: ${summaryFg}; padding: 2px 10px; border-radius: 10px; font-size: 11px; font-weight: 600;">
        ${escapeHtml(rep.summary || "")}
      </span>
      <span style="font-size: 11px; color: #888;">
        source: <code>${escapeHtml(rep.meta?.source || "")}</code>
      </span>
    `;
  return summary;
}

// _expectedTableHeaderHTML returns the <tr> header — the validate mode
// adds "result" and "delta" columns not shown in spec-only mode.
function _expectedTableHeaderHTML(specOnly) {
  return specOnly
    ? `<tr>
        <th>phase</th>
        <th>target</th>
        <th>anchor</th>
        <th>window</th>
        <th>spec text</th>
      </tr>`
    : `<tr>
        <th>phase</th>
        <th>target</th>
        <th>anchor</th>
        <th>window</th>
        <th>result</th>
        <th>delta</th>
        <th>spec text</th>
      </tr>`;
}

// _expectedTargetHTML renders one phase definition's "target" cell:
// expected state badge, else expected event kind, else a dash.
function _expectedTargetHTML(def) {
  if (def.expected_state) {
    return `state=<span class="badge ${def.expected_state}">${def.expected_state}</span>`;
  }
  return def.kind ? `kind=<code>${escapeHtml(def.kind)}</code>` : "—";
}

// _expectedWindowText renders one phase definition's timing-window cell
// (max delay and/or minimum duration constraints, joined with " · ").
function _expectedWindowText(def) {
  let win = "";
  if (def.max_delay_ms) win += `≤ ${def.max_delay_ms} ms`;
  if (def.duration_at_least_ms) win += (win ? " · " : "") + `≥ ${def.duration_at_least_ms} ms`;
  return win || "—";
}

// _buildExpectedRow builds one phase's <tr>. Definitions and phases are
// same-length, same-order arrays from the validator, so the row can show
// full context (target/anchor/window from the definition, pass/delta from
// the phase result — the latter only in validate mode).
function _buildExpectedRow(ph, def, specOnly) {
  const target = _expectedTargetHTML(def);
  const anchor = def.relative_to ? `<code>${escapeHtml(def.relative_to)}</code>` : "<code>start</code>";
  const win = _expectedWindowText(def);
  const specText = def.text || "";
  const tr = document.createElement("tr");
  if (specOnly) {
    tr.innerHTML = `
        <td><code>${escapeHtml(ph.phase)}</code></td>
        <td style="font-size: 11px;">${target}</td>
        <td style="font-size: 11px;">${anchor}</td>
        <td style="font-size: 11px; color: #555;">${win}</td>
        <td title="${escapeHtml(specText)}" style="font-size: 11px; color: #555;">${escapeHtml(truncate(specText, 90))}</td>`;
    return tr;
  }
  const resultPill = ph.pass
    ? `<span class="badge ready">✓ pass</span>`
    : `<span class="badge fail">✗ fail</span>`;
  // delta_ms may be 0 (phase matched exactly at its anchor) — treat
  // anything numeric as renderable, only fall back to "—" when the
  // phase never matched at all.
  const deltaMs = Number.isFinite(ph.delta_ms) ? ph.delta_ms : 0;
  const delta = ph.matched_ts ? `+${deltaMs} ms` : "—";
  tr.innerHTML = `
        <td><code>${escapeHtml(ph.phase)}</code></td>
        <td style="font-size: 11px;">${target}</td>
        <td style="font-size: 11px;">${anchor}</td>
        <td style="font-size: 11px; color: #555;">${win}</td>
        <td>${resultPill}</td>
        <td>${escapeHtml(delta)}</td>
        <td title="${escapeHtml(specText)}" style="font-size: 11px; color: #555;">${escapeHtml(truncate(specText, 90))}</td>`;
  return tr;
}

// _buildExpectedTable builds the full phases table (header + one row per
// phase).
function _buildExpectedTable(rep, specOnly) {
  const tbl = document.createElement("table");
  tbl.innerHTML = _expectedTableHeaderHTML(specOnly);
  // Definitions and phases are same-length, same-order arrays from
  // the validator. Zip by index so the row shows full context.
  const defs = Array.isArray(rep.definitions) ? rep.definitions : [];
  for (let i = 0; i < rep.phases.length; i++) {
    tbl.appendChild(_buildExpectedRow(rep.phases[i], defs[i] || {}, specOnly));
  }
  return tbl;
}

// _appendExpectedFailures appends the failure-detail block — surfaces the
// reason strings prominently so the operator can scan failures without
// hovering each row. No-op when nothing failed.
function _appendExpectedFailures(p, rep) {
  const failed = rep.phases.filter(ph => !ph.pass);
  if (failed.length === 0) return;
  const failBox = document.createElement("div");
  failBox.style.cssText = "margin-top: 10px; padding: 8px 10px; background: #fff7f7; border-left: 3px solid #8a0000; font-size: 12px; color: #444;";
  let html = "<b>Failures:</b><ul style=\"margin: 4px 0 0; padding-left: 20px;\">";
  for (const ph of failed) {
    html += `<li><code>${escapeHtml(ph.phase)}</code>: ${escapeHtml(ph.reason || "(no reason recorded)")}</li>`;
  }
  html += "</ul>";
  failBox.innerHTML = html;
  p.appendChild(failBox);
}

function renderExpected(data, mode) {
  const specOnly = mode === "spec";
  // anchor "spec" — pipeline-strip segment § lands here.
  const p = panel("Spec expectations", "spec");
  if (!data.expected || !Array.isArray(data.expected.phases) || data.expected.phases.length === 0) {
    p.appendChild(text("No expected.jsonl for this scenario. Author one via /ir:onboard-agent spec <agent> <scenario>."));
    return p;
  }
  const rep = data.expected;
  p.appendChild(_buildExpectedValidationHeading());
  p.appendChild(_buildExpectedSummary(rep, specOnly));
  p.appendChild(_buildExpectedTable(rep, specOnly));

  if (specOnly) return p;
  _appendExpectedFailures(p, rep);
  return p;
}

function truncate(s, n) {
  if (!s) return "";
  if (s.length <= n) return s;
  return s.slice(0, n - 1) + "…";
}

// Renders the manifestBox field list (promoted_at, daemon_version, ...) via
// DOM construction rather than an innerHTML template literal, so a manifest
// value can never be interpreted as markup regardless of what a recording's
// manifest.json contains. alwaysEllipsis matches the archive case, which
// appends "…" after recipe_hash unconditionally; the newest-recording case
// only appends it when a hash is present.
export function renderManifestFields(m, passRateLabel, alwaysEllipsis) {
  const frag = document.createDocumentFragment();
  const row = (label, ...valueParts) => {
    const b = document.createElement("b");
    b.textContent = `${label}:`;
    frag.append(b, " ", ...valueParts, document.createElement("br"));
  };
  row("promoted_at", m.promoted_at || "");
  row("daemon_version", m.daemon_version || "");
  row("agent_cli_version", m.agent_cli_version || "");
  const code = document.createElement("code");
  code.textContent = (m.recipe_hash || "").slice(0, 16);
  row("recipe_hash", code, (alwaysEllipsis || m.recipe_hash) ? "…" : "");
  row(passRateLabel, m.expected_pass_rate || "—");
  const last = document.createElement("b");
  last.textContent = "recording_started_at:";
  frag.append(last, " ", m.recording_started_at || "");
  return frag;
}

// renderRecordingHistory is the TOP-LEVEL controller for the scenario detail
// page. It owns:
//   - a selector with options [(none), ...recordings newest-first]. Every
//     recording lives under recordings/<name>/; there is no separate "Latest".
//   - the Spec expectations panel (always visible; content swaps with the
//     selected recording — the validator re-runs the current spec against that
//     recording's events to surface drift)
//   - a container of recording-derived panels (Playback, Meta, Transitions,
//     Tool calls) rendered only when a recording is selected
//
// State machine for the selector:
//   "(none)"       → only Spec expectations renders, in spec-only mode (no
//                    pass/fail badges — nothing to validate against).
//   newest name    → the newest recording; its data is already embedded in
//                    ScenarioDetail (latestData), rendered without a refetch.
//   other <name>   → an older recording: fetched via the recordings endpoint;
//                    Playback retargets to its events via /api/replay/start's
//                    recording field.
// fmtLabel formats one recording-history <option>'s label: recording_started_at,
// daemon version, fresh pass rate. Uses recording_started_at (not promoted_at)
// so the timestamps describe WHEN the recording was captured.
function fmtLabel(startedAt, daemonVer, passRate) {
  const ts = startedAt || "(no timestamp)";
  const ver = daemonVer ? ` · daemon ${daemonVer}` : "";
  const pass = passRate ? ` · ${passRate}` : "";
  return `${ts}${ver}${pass}`;
}

function renderRecordingHistory(s, latestData, archives, initialArchive, recipeEntry, coverageEntry) {
  const wrap = document.createElement("div");

  // 1. The selector panel (top, controls everything below).
  //    anchor "recordings" — pipeline-strip segment N jumps here.
  const selPanel = panel("Recording", "recordings");
  const intro = document.createElement("div");
  intro.style.cssText = "margin-bottom: 8px; font-size: 12px; color: #555;";
  const recCount = (archives || []).length;
  intro.innerHTML = `Select which recording to inspect — all live under <code>recordings/</code>, newest first. <b>expected.jsonl</b> is the constant benchmark across all of them; picking an older recording re-evaluates the current spec against its events (drift signal).` +
    (recCount > 0
      ? ` <b>${recCount}</b> recording${pluralSuffix(recCount)} available.`
      : ` No recordings yet.`);
  selPanel.appendChild(intro);

  const select = document.createElement("select");
  select.style.cssText = "padding: 4px 8px; font: inherit; font-size: 12px; border: 1px solid #c0bdb1; border-radius: 3px;";
  const noneOpt = document.createElement("option");
  noneOpt.value = "__none__";
  noneOpt.textContent = "— No recording (spec only) —";
  select.appendChild(noneOpt);

  // Every recording lives under recordings/<name>/ — there is no separate
  // "Latest" entry. The list is newest-first by name; the newest (the one the
  // ScenarioDetail's recording-derived fields describe) is latestData.latest_recording.
  const newestName = latestData.latest_recording || ((archives || [])[0] && archives[0].name) || "";
  for (const a of (archives || [])) {
    const opt = document.createElement("option");
    opt.value = a.name;
    let label = fmtLabel(a.recording_started_at || a.name, a.daemon_version, a.expected_pass_rate);
    if (a.name === newestName) label = "● " + label + " (newest)";
    opt.textContent = label;
    select.appendChild(opt);
  }
  // Default = the newest recording. A URL deep-link (#/recording/.../.../<name>)
  // that exists opens pre-pointed at it; otherwise the newest is autoselected.
  // With no recordings at all, fall back to the spec-only view.
  const archMatch = initialArchive && (archives || []).some(a => a.name === initialArchive);
  select.value = archMatch ? initialArchive : (newestName || "__none__");
  selPanel.appendChild(select);

  const manifestBox = document.createElement("div");
  manifestBox.style.cssText = "margin-top: 10px; font-size: 11px; color: #666;";
  selPanel.appendChild(manifestBox);
  wrap.appendChild(selPanel);

  // 2. Spec expectations panel — always visible. Re-rendered on
  //    selection change. Initial render against latest.
  const expHost = document.createElement("div");
  expHost.appendChild(renderExpected(latestData));
  wrap.appendChild(expHost);

  // 3. Container for recording-derived panels — populated on
  //    selection. Empty when "(none)" is chosen.
  const below = document.createElement("div");
  wrap.appendChild(below);

  async function selectionChanged() {
    const value = select.value;

    // Reset the spec panel and the below-container before deciding
    // what to render. The Spec expectations panel always re-renders;
    // below-container is conditionally populated.
    manifestBox.replaceChildren();
    below.innerHTML = "";

    if (value === "__none__") {
      // Spec-only view: render the panel WITHOUT pass/fail badges, the
      // result/delta columns, or the summary chip. The point is "here's
      // the spec" — there's no recording to validate against.
      expHost.replaceChildren(renderExpected(latestData, "spec"));
      const i = document.createElement("i");
      i.textContent = "No recording selected — only Spec expectations rendered. Pick a recording to see captured behavior.";
      manifestBox.appendChild(i);
      return;
    }

    if (value === newestName) {
      // The newest recording — its data is already embedded in ScenarioDetail
      // (latestData), so render directly without a second fetch.
      expHost.replaceChildren(renderExpected(latestData));
      const lm = latestData.latest_manifest;
      if (lm) {
        manifestBox.append(renderManifestFields(lm, "expected_pass_rate", /*alwaysEllipsis=*/false));
      } else {
        const i = document.createElement("i");
        const code = document.createElement("code");
        code.textContent = `recordings/${newestName}/`;
        i.append("Showing the newest recording (", code, ").");
        manifestBox.appendChild(i);
      }
      renderRecordingPanels(latestData, /*recordingName=*/newestName);
      return;
    }

    // An older recording selected.
    const arch = (archives || []).find(a => a.name === value);
    if (arch) {
      manifestBox.append(renderManifestFields(arch, "expected_pass_rate (at promote)", /*alwaysEllipsis=*/true));
    }
    // s.agent/s.subtree/s.id come from the URL hash (see loadScenario) —
    // encode each segment before it lands in a fetch path (SonarQube
    // jssecurity:S7044 / S8476).
    const archDetail = await fetch(
      `/api/scenarios/${encodeURIComponent(s.agent)}/${encodeURIComponent(s.subtree)}/${encodeURIComponent(s.id)}/recordings/${encodeURIComponent(value)}`
    ).then(r => r.json());
    const archData = {
      ...latestData,
      transitions: archDetail.transitions || [],
      expected: archDetail.expected || null,
      tools: archDetail.tools || [],
    };
    // Drift annotation: archive's frozen pass rate (manifest) vs
    // fresh eval (current spec re-run on archived events).
    const frozenRate = arch?.expected_pass_rate || "";
    const freshRate = archDetail.expected?.summary || "";
    if (frozenRate && freshRate) {
      const driftNote = document.createElement("div");
      driftNote.style.cssText = "margin-top: 8px; padding: 6px 9px; font-size: 11px; border-radius: 3px;";
      if (frozenRate === freshRate) {
        driftNote.style.background = "#fafaf2";
        driftNote.style.color = "#555";
        const b = document.createElement("b");
        b.textContent = "No drift:";
        driftNote.append(b, ` archive's frozen pass rate (${frozenRate}) matches a fresh evaluation against today's spec.`);
      } else {
        driftNote.style.background = "#fff7d6";
        driftNote.style.color = "#8a4500";
        const b = document.createElement("b");
        b.textContent = "Drift detected:";
        const codeFrozen = document.createElement("code");
        codeFrozen.textContent = frozenRate;
        const codeFresh = document.createElement("code");
        codeFresh.textContent = freshRate;
        driftNote.append(b, " at promote time the archive showed ", codeFrozen, "; today's spec rates the same archive as ", codeFresh, ".");
      }
      manifestBox.appendChild(driftNote);
    }
    expHost.replaceChildren(renderExpected(archData));
    renderRecordingPanels(archData, /*archiveName=*/value);
  }

  function renderRecordingPanels(d, archiveName) {
    below.innerHTML = "";
    // Assessment and Recipe are now rendered at the page level in
    // loadScenario (above this dropdown) — they're cell-level info
    // and shouldn't re-render on dropdown changes. The panels here
    // are only the recording-specific ones.
    //
    // Playback retargets to the archive when archiveName is set —
    // /api/replay/start accepts a `recording` field that resolves to
    // <scenarioDir>/recordings/<name>.
    below.appendChild(renderPlayback(s, d, archiveName));
    below.appendChild(renderMeta(d));
    below.appendChild(renderTransitions(d));
    if (Array.isArray(d.tools) && d.tools.length > 0) {
      below.appendChild(renderToolCalls(d));
    }
  }

  select.addEventListener("change", () => {
    // Keep the URL in sync with the dropdown so the link can be
    // copy-pasted to share a specific recording. Latest collapses to
    // the bare cell URL (no archive segment); "(none)" stays on the
    // bare cell URL too — it's a UI-only sub-state. Use
    // history.replaceState rather than navigate() to avoid spamming
    // browser history with every dropdown click.
    const v = select.value;
    const base = `#/recording/${s.agent}/${s.subtree}/${s.id}`;
    const next = (v && v !== "__none__") ? `${base}/${encodeURIComponent(v)}` : base;
    if (location.hash !== next) {
      history.replaceState(null, "", next);
    }
    selectionChanged();
  });
  // Initial render reflects the default selection (Latest or the
  // archive named in the URL).
  selectionChanged();

  return wrap;
}

// _buildToolCallsIntro builds the explanatory paragraph above the chips
// and table.
function _buildToolCallsIntro(tools) {
  const intro = document.createElement("div");
  intro.style.cssText = "font-size: 11px; color: #666; margin-bottom: 8px;";
  intro.innerHTML = `<b>${tools.length}</b> tool call${tools.length === 1 ? "" : "s"} ` +
    `extracted from <code>transcript.jsonl</code>. ` +
    `Note: irrlicht's <code>events.jsonl</code> has no first-class <code>tool_use</code> Kind today; ` +
    `this view is derived client-side from the transcript content. Promotion to a lifecycle Kind is future work.`;
  return intro;
}

// _toolCallsStartMs anchors the +ms offsets to the first tool call's
// timestamp, so the table reads in the same time base as the timeline
// lanes. Returns null when there's nothing to anchor to.
function _toolCallsStartMs(tools) {
  if (tools.length > 0 && tools[0].ts) return Date.parse(tools[0].ts);
  return null;
}

// _countToolsByName groups tool calls by name for the summary chips.
function _countToolsByName(tools) {
  const byName = {};
  for (const t of tools) {
    byName[t.name] = (byName[t.name] || 0) + 1;
  }
  return byName;
}

// _buildToolNameChip builds one summary chip. Special-cases the "Agent"
// tool name (claudecode's Task tool) with a distinct color + tooltip
// since spawning subagents is the headline case.
function _buildToolNameChip(name, count) {
  const chip = document.createElement("span");
  const isAgent = name === "Agent";
  chip.style.cssText = `padding: 2px 8px; border-radius: 10px; font-size: 11px; font-weight: 600; ` +
    (isAgent
      ? "background: #e0eaff; color: #1f3d8a;"
      : "background: #eaeae0; color: #555;");
  chip.textContent = `${name} · ${count}`;
  if (isAgent) chip.title = "Task tool — spawns subagents. See coverage_id=foreground-subagent (3.1).";
  return chip;
}

// _buildToolNameChips builds the summary chip row, one chip per distinct
// tool name, sorted alphabetically.
function _buildToolNameChips(byName) {
  const chips = document.createElement("div");
  chips.style.cssText = "display: flex; gap: 6px; margin-bottom: 8px; flex-wrap: wrap;";
  for (const name of Object.keys(byName).sort((a, b) => a.localeCompare(b))) {
    chips.appendChild(_buildToolNameChip(name, byName[name]));
  }
  return chips;
}

// _buildToolCallRow builds one `+ms · session · tool · id` row.
function _buildToolCallRow(t, startMs) {
  const offset = (startMs && t.ts) ? (Date.parse(t.ts) - startMs) : null;
  const offsetCell = offset !== null ? `+${offset} ms` : "—";
  const sidShort = (t.session_id || "").slice(0, 14);
  const isAgent = t.name === "Agent";
  const toolCell = isAgent
    ? `<span style="background: #e0eaff; color: #1f3d8a; padding: 1px 6px; border-radius: 8px; font-weight: 600; font-size: 11px;">${escapeHtml(t.name)}</span>`
    : `<code>${escapeHtml(t.name)}</code>`;
  const tr = document.createElement("tr");
  tr.innerHTML = `
      <td>${escapeHtml(offsetCell)}</td>
      <td><code style="font-size: 11px; color: #666;">${escapeHtml(sidShort)}</code></td>
      <td>${toolCell}</td>
      <td><code style="font-size: 11px; color: #888;">${escapeHtml((t.id || "").slice(0, 16))}</code></td>`;
  return tr;
}

// _buildToolCallsTable builds the header + one row per tool call.
function _buildToolCallsTable(tools, startMs) {
  const tbl = document.createElement("table");
  tbl.innerHTML = `<tr><th>+ms</th><th>session</th><th>tool</th><th>id</th></tr>`;
  for (const t of tools) {
    tbl.appendChild(_buildToolCallRow(t, startMs));
  }
  return tbl;
}

// renderToolCalls shows the tool_use blocks the server extracted
// from transcript.jsonl. Today this is the only signal irrlicht has
// for "agent invoked a tool" — events.jsonl has no first-class
// tool_use Kind, so the viewer derives this client-side from the
// transcript content. Each row is `+ms · session · ToolName · id`.
function renderToolCalls(data) {
  const p = panel("Tool calls");
  p.appendChild(_buildToolCallsIntro(data.tools));
  const startMs = _toolCallsStartMs(data.tools);
  p.appendChild(_buildToolNameChips(_countToolsByName(data.tools)));
  p.appendChild(_buildToolCallsTable(data.tools, startMs));
  return p;
}

function renderTransitions(data) {
  const p = panel("Emitted state transitions (from events.jsonl)");
  if (!data.transitions || data.transitions.length === 0) {
    p.appendChild(text("No state_transition entries in events.jsonl."));
    return p;
  }

  // Group transitions by session_id so the daemon's presession→session
  // handoff doesn't read as duplicate ∅→ready spam. Presession rows
  // (session_id matches ^proc-\d+$) collapse by default — they reflect
  // daemon internals that aren't useful for replay debugging.
  const groups = new Map(); // sessionID → [transitions]
  const order = []; // sessionIDs in first-appearance order
  for (const tRaw of data.transitions) {
    const t = typeof tRaw === "string" ? JSON.parse(tRaw) : tRaw;
    const sid = t.session_id || "(unknown)";
    if (!groups.has(sid)) {
      groups.set(sid, []);
      order.push(sid);
    }
    groups.get(sid).push(t);
  }

  const presessionRE = /^proc-\d+$/;
  const procOnly = order.length > 0 && order.every(sid => presessionRE.test(sid));
  const hasPresession = !procOnly && order.some(sid => presessionRE.test(sid));
  // For adapters like aider that don't use UUIDs, every session_id is
  // proc-XXXX. Hiding all of them leaves an empty panel — so we only
  // hide them when a non-proc UUID session also exists (the
  // claudecode/codex presession→session handoff case).

  // Toggle for showing daemon internals.
  let showInternals = false;
  const toggleWrap = document.createElement("div");
  toggleWrap.style.cssText = "margin-bottom: 8px; font-size: 11px; color: #666;";
  const rerender = () => {
    container.innerHTML = "";
    for (const sid of order) {
      if (!showInternals && presessionRE.test(sid)) continue;
      container.appendChild(renderSessionGroup(sid, groups.get(sid)));
    }
  };
  if (hasPresession) {
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.id = "show-internals";
    cb.onchange = () => { showInternals = cb.checked; rerender(); };
    const label = document.createElement("label");
    label.htmlFor = "show-internals";
    label.textContent = " show daemon-internal presession rows (proc-XXXX)";
    toggleWrap.appendChild(cb);
    toggleWrap.appendChild(label);
    p.appendChild(toggleWrap);
  }

  const container = document.createElement("div");
  p.appendChild(container);
  rerender();
  return p;
}

// renderSessionGroup builds one collapsible card per session_id with the
// transitions in chronological order under a header that summarizes the
// final state + count + total duration.
function renderSessionGroup(sessionID, transitions) {
  const card = document.createElement("details");
  card.style.cssText = "border: 1px solid #d8d6cc; border-radius: 4px; margin-bottom: 6px; background: #fff;";
  card.open = transitions.length > 0; // open by default

  const final = transitions[transitions.length - 1];
  const finalState = final ? final.new_state : "unknown";
  let dur = "";
  if (transitions.length >= 2 && transitions[0].ts && final.ts) {
    const ms = new Date(final.ts) - new Date(transitions[0].ts);
    dur = ms >= 1000 ? `${(ms/1000).toFixed(1)}s` : `${ms}ms`;
  }

  const summary = document.createElement("summary");
  summary.style.cssText = "padding: 8px 10px; cursor: pointer; font-family: monospace; font-size: 12px;";
  summary.innerHTML = `<code>${escapeHtml(sessionID)}</code> ` +
    `<span class="badge ${badgeClass(finalState)}">${escapeHtml(finalState)}</span> ` +
    `<span style="color:#888;">${transitions.length} transition${transitions.length === 1 ? '' : 's'}${dur ? ', ' + dur : ''}</span>`;
  card.appendChild(summary);

  const tbl = document.createElement("table");
  tbl.style.cssText = "margin: 0;";
  tbl.innerHTML = `<tr><th>ts</th><th>prev → new</th><th>reason</th></tr>`;
  for (const t of transitions) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td>${escapeHtml(t.ts || "")}</td>
      <td><span class="badge ${badgeClass(t.prev_state || 'none')}">${t.prev_state || "∅"}</span> →
          <span class="badge ${badgeClass(t.new_state)}">${t.new_state}</span></td>
      <td>${escapeHtml(t.reason || "")}</td>`;
    tbl.appendChild(tr);
  }
  card.appendChild(tbl);
  return card;
}

// badgeClass maps a state name to a CSS-safe class. Synthetic
// session-end events arrive as the ∅ symbol — map them to .badge.ended
// (neutral grey italic). Other states strip to alnum for class safety.
function badgeClass(state) {
  if (!state) return "none";
  if (state === "∅") return "ended";
  return String(state).replace(/[^a-zA-Z0-9_-]/g, "") || "none";
}

// renderPlayback wires the play/pause/scrubber UI and the dashboard
// iframe. Takes the scenario picker entry (NOT the full detail payload)
// because that's what we need to POST /api/replay/start.
function renderPlayback(s, detailData, archiveName) {
  const p = panel("Playback");

  // When replaying an archive, surface which one above the controls so
  // the operator doesn't confuse it with the live "Latest" timeline.
  if (archiveName) {
    const chip = document.createElement("div");
    chip.style.cssText = "margin-bottom: 8px; font-size: 11px;";
    chip.innerHTML = `<span style="background: #fff7d6; color: #8a4500; padding: 2px 10px; border-radius: 10px; font-weight: 600;">Playing archive: <code>${escapeHtml(archiveName)}</code></span>`;
    p.appendChild(chip);
  }

  // Play / Pause / Stop / Prev / Next / Speed.
  const ctl = document.createElement("div");
  ctl.className = "controls";
  const btnPlay = mkButton("▶ Play");
  const btnPause = mkButton("⏸ Pause"); btnPause.disabled = true;
  const btnStop = mkButton("⏹ Stop"); btnStop.disabled = true;
  const btnPrev = mkButton("⏮"); btnPrev.disabled = true; btnPrev.title = "previous event";
  const btnNext = mkButton("⏭"); btnNext.disabled = true; btnNext.title = "next event";
  ctl.appendChild(btnPlay);
  ctl.appendChild(btnPause);
  ctl.appendChild(btnStop);
  ctl.appendChild(btnPrev);
  ctl.appendChild(btnNext);
  const speedSpan = document.createElement("span");
  speedSpan.style.marginLeft = "12px";
  speedSpan.innerHTML = `<strong>Speed:</strong> `;
  ctl.appendChild(speedSpan);
  let currentSpeed = 1;
  const speedButtons = [];
  for (const sp of SPEED_PRESETS) {
    const b = document.createElement("button");
    b.className = "speed";
    b.textContent = `${sp}×`;
    if (sp === 1) b.style.fontWeight = "700";
    b.onclick = async () => {
      currentSpeed = sp;
      for (const x of speedButtons) x.style.fontWeight = "400";
      b.style.fontWeight = "700";
      // If a playback is running, push the new speed live.
      try { await setReplaySpeed(sp); } catch {}
    };
    ctl.appendChild(b);
    speedButtons.push(b);
  }
  p.appendChild(ctl);

  // Timeline: state band (colored regions by working/waiting/ready) +
  // event-dot lane (discrete non-state events like transcript_new) +
  // the actual <input type="range"> scrubber (kept for keyboard /
  // accessibility / drag-seek). Styled to look like the macOS state
  // bar so the user reads "what was the session doing when" at a glance.
  const scrubWrap = document.createElement("div");
  scrubWrap.style.cssText = "margin-top: 8px; position: relative; padding-top: 4px;";

  // Shared tooltip overlay — one DOM node reused across every marker so
  // we avoid the browser's ~1.5s `title`-attribute delay. Each marker
  // stores its hover text in data-tip and a delegated listener on
  // scrubWrap shows/positions/hides this element on mouseenter/leave.
  const tip = document.createElement("div");
  tip.style.cssText = "position: absolute; display: none; max-width: 360px; " +
    "padding: 6px 9px; background: #1f2937; color: #f9fafb; font-size: 11px; " +
    "line-height: 1.4; border-radius: 4px; box-shadow: 0 4px 12px rgba(0,0,0,0.2); " +
    "pointer-events: none; white-space: pre-wrap; z-index: 10;";
  scrubWrap.appendChild(tip);

  // Turn lane sits ABOVE the state band. Renders one tick per user
  // prompt or assistant response from the recording's transcript so the
  // user can see WHERE in the timeline each turn landed. User ticks pin
  // to the top half, assistant ticks to the bottom half — both can
  // co-exist at the same x without overlap. Lane is taller now (22px)
  // and ticks are wider (5px) so they're easier to land the cursor on.
  // Expected lane — markers at each spec-grounded phase's
  // matched-or-target timestamp. Sits above the turn lane so the
  // operator reads top-to-bottom: expected (spec) → turns
  // (transcript) → state (irrlicht) → events (irrlicht).
  const expectedLane = document.createElement("div");
  expectedLane.style.cssText = "position: relative; height: 16px; margin-bottom: 2px;";
  scrubWrap.appendChild(expectedLane);

  const turnLane = document.createElement("div");
  turnLane.style.cssText = "position: relative; height: 22px; margin-bottom: 2px;";
  scrubWrap.appendChild(turnLane);

  // Band + scrubber are layered: the band IS the visual track; the
  // <input type="range"> sits on top with a transparent track so only
  // its thumb shows (the seek handle).
  const bandWrap = document.createElement("div");
  bandWrap.style.cssText = "position: relative; height: 18px;";
  const stateBand = document.createElement("div");
  stateBand.style.cssText = "position: absolute; inset: 0; background: #eaeae0; border-radius: 4px; overflow: hidden;";
  bandWrap.appendChild(stateBand);

  const scrub = document.createElement("input");
  scrub.type = "range";
  scrub.min = "0";
  scrub.max = "100";
  scrub.value = "0";
  scrub.className = "timeline-scrubber";
  scrub.setAttribute("aria-label", "Playback position");
  scrub.disabled = true;
  bandWrap.appendChild(scrub);
  scrubWrap.appendChild(bandWrap);

  const eventLane = document.createElement("div");
  eventLane.style.cssText = "position: relative; height: 18px; margin-top: 4px;";
  scrubWrap.appendChild(eventLane);

  // Delegated hover handler: any descendant with a data-tip attribute
  // shows the shared tooltip immediately on mouseenter (no browser
  // delay) and hides on mouseleave. Position is computed relative to
  // scrubWrap so the tooltip sticks near the hovered marker even when
  // the page scrolls. Clamped to the wrap's width so wide tooltips
  // don't overflow the panel edge.
  scrubWrap.addEventListener("mouseover", (e) => {
    const el = e.target.closest("[data-tip]");
    if (!el) return;
    tip.textContent = el.dataset.tip || "";
    tip.style.display = "block";
    const wrapRect = scrubWrap.getBoundingClientRect();
    const elRect = el.getBoundingClientRect();
    const tipRect = tip.getBoundingClientRect();
    // Anchor below the marker by default; flip above when there's no
    // room below (rare, but keeps it visible at the page bottom).
    let top = elRect.bottom - wrapRect.top + 4;
    if (top + tipRect.height > wrapRect.height + 200) {
      top = elRect.top - wrapRect.top - tipRect.height - 4;
    }
    let left = elRect.left - wrapRect.left + elRect.width / 2 - tipRect.width / 2;
    if (left < 0) left = 0;
    if (left + tipRect.width > wrapRect.width) left = wrapRect.width - tipRect.width;
    tip.style.left = `${left}px`;
    tip.style.top = `${top}px`;
  });
  scrubWrap.addEventListener("mouseout", (e) => {
    const el = e.target.closest("[data-tip]");
    if (!el) return;
    // Only hide when leaving to a non-marker; mouseover on a sibling
    // tooltip-bearing element will re-show immediately anyway.
    const next = e.relatedTarget?.closest?.("[data-tip]");
    if (next) return;
    tip.style.display = "none";
  });

  const offsetReadout = document.createElement("div");
  offsetReadout.id = "playhead-info";
  offsetReadout.textContent = "—";
  offsetReadout.style.cssText = "margin-top: 4px;";
  scrubWrap.appendChild(offsetReadout);

  // Legend — labels for the colors. Cheaper than memorizing a palette.
  const legend = document.createElement("div");
  legend.style.cssText = "margin-top: 6px; display: flex; gap: 14px; flex-wrap: wrap; font-size: 11px; color: #555; align-items: center;";
  const swatch = (color, label) => {
    const span = document.createElement("span");
    span.style.cssText = "display: inline-flex; align-items: center; gap: 4px;";
    span.innerHTML = `<span style="display:inline-block; width:10px; height:10px; background:${color}; border-radius:2px;"></span>${label}`;
    return span;
  };
  const dotSwatch = (color, label) => {
    const span = document.createElement("span");
    span.style.cssText = "display: inline-flex; align-items: center; gap: 4px;";
    span.innerHTML = `<span style="display:inline-block; width:9px; height:9px; background:${color}; border-radius:50%;"></span>${label}`;
    return span;
  };
  legend.appendChild(document.createTextNode("State: "));
  legend.appendChild(swatch("#4ade80", "ready"));
  legend.appendChild(swatch("#8b5cf6", "working"));
  legend.appendChild(swatch("#f59e0b", "waiting"));
  const sep = document.createElement("span");
  sep.style.color = "#bbb"; sep.textContent = "·";
  legend.appendChild(sep);
  legend.appendChild(document.createTextNode("Events: "));
  legend.appendChild(dotSwatch("#3b82f6", "lifecycle"));
  legend.appendChild(dotSwatch("#22c55e", "process"));
  legend.appendChild(dotSwatch("#a78bfa", "activity"));
  legend.appendChild(dotSwatch("#94a3b8", "bookkeeping"));
  const sep2 = document.createElement("span");
  sep2.style.color = "#bbb"; sep2.textContent = "·";
  legend.appendChild(sep2);
  legend.appendChild(document.createTextNode("Turns: "));
  legend.appendChild(swatch("#2563eb", "user"));
  legend.appendChild(swatch("#0d9488", "assistant"));
  scrubWrap.appendChild(legend);

  p.appendChild(scrubWrap);

  // Local state shared by the prev/next buttons + state-band + event-dot
  // renderer.
  let eventOffsets = []; // sorted, dedup'd offset_ms values
  let events = [];       // raw EventMarker list from /api/replay/start
  let turns = [];        // raw TurnMarker list from /api/replay/start

  function renderMarkers() {
    paintStateBand(stateBand, events, totalMs);
    paintEventDots(eventLane, events, totalMs);
    paintTurns(turnLane, turns, totalMs);
    paintExpectedLane(expectedLane, detailData?.expected, totalMs);
  }

  // Dashboard iframe (hidden until playback starts).
  const iframeWrap = document.createElement("div");
  iframeWrap.style.cssText = "margin-top: 12px; display: none; border: 1px solid #d8d6cc; border-radius: 4px; overflow: hidden;";
  const iframe = document.createElement("iframe");
  iframe.style.cssText = "width: 100%; height: 540px; border: 0;";
  iframeWrap.appendChild(iframe);
  const iframeLabel = document.createElement("div");
  iframeLabel.style.cssText = "padding: 4px 10px; background: #f0efe9; font-size: 11px; color: #666;";
  iframeWrap.appendChild(iframeLabel);
  p.appendChild(iframeWrap);

  let pollTimer = null;
  let totalMs = 0;

  async function startPlayback() {
    const res = await startReplay({
      agent: s.agent,
      subtree: s.subtree,
      scenario: s.id,
      speed: currentSpeed,
      recording: archiveName || "",
    });
    if (!res.ok) {
      // Never pop a blocking modal — a scenario with no events.jsonl/usable
      // transcript (e.g. an un-recorded cell opened via a deep link) just has
      // nothing to play. Log non-blocking for debugging and bail quietly.
      console.warn("replay start failed:", res.status, sanitizeForLog(res.error));
      return;
    }
    const body = res.body;
    totalMs = body.total_ms || 0;
    events = Array.isArray(body.events) ? body.events : [];
    turns = Array.isArray(body.turns) ? body.turns : [];
    // Deduplicate offsets so a cluster of same-instant events doesn't
    // ping the prev/next buttons multiple times in one click.
    eventOffsets = deriveEventOffsets(events);
    // resolveDashboardIframeUrl rejects anything but an http(s), same-origin
    // URL (dashboard_url is server-provided) and appends a cache-buster so
    // re-clicking Play actually reloads the dashboard inside the iframe
    // (setting iframe.src to the same URL is normally a no-op in browsers —
    // the WebSocket inside stays open with stale state). The replay itself
    // is already running server-side by this point, so a rejected URL only
    // blanks the iframe — it doesn't stop the rest of the timeline (scrubber,
    // event markers, status polling) from wiring up below.
    const safeUrl = resolveDashboardIframeUrl(body.dashboard_url, body.playback_id, window.location.origin);
    if (safeUrl) {
      iframe.src = safeUrl;
      iframeWrap.style.display = "block";
      iframeLabel.textContent = `${body.dashboard_url}` +
        (totalMs ? ` — total ${(totalMs/1000).toFixed(1)}s` : "") +
        (events.length ? ` — ${events.length} events` : "");
    } else {
      console.warn(`replay start: rejected unsafe dashboard_url ${sanitizeForLog(body.dashboard_url)}`);
      iframe.src = "about:blank";
      iframeWrap.style.display = "none";
    }
    btnPlay.disabled = true;
    btnPause.disabled = false;
    btnStop.disabled = false;
    btnPrev.disabled = eventOffsets.length === 0;
    btnNext.disabled = eventOffsets.length === 0;
    scrub.disabled = false;
    scrub.max = String(totalMs || 100);
    renderMarkers();
    if (pollTimer) clearInterval(pollTimer);
    pollTimer = setInterval(updateStatus, 500);
  }

  async function updateStatus() {
    try {
      const st = await replayStatus();
      if (!st.active) {
        clearInterval(pollTimer); pollTimer = null;
        return;
      }
      if (st.offset_ms !== undefined) {
        scrub.value = String(st.offset_ms);
        offsetReadout.textContent = `playhead: ${(st.offset_ms/1000).toFixed(2)}s / ${(st.total_ms/1000).toFixed(2)}s   (speed ${st.speed}×, ${st.paused ? "paused" : "playing"})`;
      }
      btnPause.textContent = st.paused ? "▶ Resume" : "⏸ Pause";
    } catch {}
  }

  btnPlay.onclick = startPlayback;
  btnPause.onclick = async () => {
    const isResume = btnPause.textContent.startsWith("▶");
    await (isResume ? resumeReplay() : pauseReplay());
    updateStatus();
  };
  btnStop.onclick = async () => {
    await stopReplay();
    btnPlay.disabled = false;
    btnPause.disabled = true;
    btnStop.disabled = true;
    btnPrev.disabled = true;
    btnNext.disabled = true;
    scrub.disabled = true;
    scrub.value = "0";
    offsetReadout.textContent = "—";
    eventLane.innerHTML = "";
    stateBand.innerHTML = "";
    turnLane.innerHTML = "";
    events = [];
    eventOffsets = [];
    turns = [];
    iframeWrap.style.display = "none";
    iframe.src = "about:blank";
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  };

  // Jump-to-event handlers. Read the current playhead from the scrubber
  // (which the poller keeps in sync) and find the closest offset that's
  // strictly less / greater. Seek there via the existing API.
  btnPrev.onclick = async () => {
    if (eventOffsets.length === 0) return;
    const cur = Number(scrub.value) || 0;
    // Subtract a small epsilon so clicking Prev when sitting exactly ON
    // an event lands on the one BEFORE it, not the same one.
    const target = findOffsetBefore(eventOffsets, cur - 1);
    if (target == null) return;
    await seekReplay(target);
    // Snap the scrubber immediately so the next poll doesn't visually
    // bounce back.
    scrub.value = String(target);
  };
  btnNext.onclick = async () => {
    if (eventOffsets.length === 0) return;
    const cur = Number(scrub.value) || 0;
    const target = findOffsetAfter(eventOffsets, cur + 1);
    if (target == null) return;
    await seekReplay(target);
    scrub.value = String(target);
  };
  scrub.oninput = async () => {
    await seekReplay(scrub.value);
  };

  // Auto-start playback when the scenario opens. The user can still
  // Stop and re-Play; they just don't have to click Play to see the
  // dashboard for the first time.
  startPlayback();

  return p;
}

function mkButton(label) {
  const b = document.createElement("button");
  b.textContent = label;
  b.style.cssText = "padding: 4px 10px; border: 1px solid #c0bdb1; background: #fff; border-radius: 3px; cursor: pointer;";
  return b;
}

// panel makes a standard panel <div class="panel"><h3>title</h3></div>.
// anchorKey: optional — sets data-anchor so the pipeline-strip
// segments (and other deep-link consumers) can find this panel via
// [data-anchor="<key>"] and scrollIntoView.
function panel(title, anchorKey) {
  const p = document.createElement("div");
  p.className = "panel";
  if (anchorKey) p.dataset.anchor = anchorKey;
  const h = document.createElement("h3");
  h.textContent = title;
  p.appendChild(h);
  return p;
}
function text(s) {
  const e = document.createElement("p");
  e.textContent = s;
  e.style.color = "#888";
  e.style.fontSize = "12px";
  return e;
}
function escapeHtml(s) {
  if (s == null) return "";
  return String(s).replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
}

// renderMarkdown converts the small Markdown subset the assessment bodies use
// (## / ### / #### headings, "- " and "1. " lists, **bold**, `code`, and
// blank-line-separated paragraphs) into safe HTML, returned as a string for
// innerHTML. The input is escaped FIRST so any literal HTML in the prose stays
// inert — only the tags this function inserts are live. No external engine: the
// subset is deliberately small (it's what the assess skill authors), so a few
// regexes beat pulling a markdown library into a no-build SPA. Style lives in
// the `.md-body` rules in index.html.
//
// The heading/list-item regexes below (jssecurity:S8786) each start with a
// mandatory literal ("#", "-", or a digit run + ".") that a non-matching line
// fails on immediately — the engine never backtracks into the trailing
// `\s+`/`.*` pair because it never gets past the first character class. Each
// is also run per-line (pre-split on "\n", no embedded newlines) against
// markdown the assess skill itself authors, not arbitrary/attacker input.
// Verified no exponential blowup on adversarial inputs (long runs of spaces,
// no trailing match) before leaving these as-is.
export function renderMarkdown(md) {
  const esc = escapeHtml(String(md || ""));
  // Inline pass — one alternation, left-to-right: a `code` span is matched
  // whole (so any ** inside it stays literal), otherwise **bold**. One pass,
  // no placeholder sentinels in the source.
  const inline = (s) =>
    s.replace(/`([^`]+)`|\*\*([^*]+)\*\*/g,
      (_, code, bold) => code !== undefined ? `<code>${code}</code>` : `<strong>${bold}</strong>`);
  const out = [];
  let list = null;   // {tag, items:[]}
  let para = [];     // accumulated plain lines → one <p>
  const flushPara = () => { if (para.length) { out.push(`<p>${inline(para.join(" "))}</p>`); para = []; } };
  const flushList = () => {
    if (list) {
      const itemsHtml = list.items.map(i => `<li>${inline(i)}</li>`).join("");
      out.push(`<${list.tag}>${itemsHtml}</${list.tag}>`);
      list = null;
    }
  };
  for (const raw of esc.split("\n")) {
    const line = raw.trimEnd();
    if (line === "") { flushPara(); flushList(); continue; }
    let m = /^(#{2,4})\s+(.*)$/.exec(line); // NOSONAR jssecurity:S8786 — no backtracking risk, verified: mandatory "#" prefix fails immediately on any non-heading line before reaching \s+/.*
    if (m) {
      flushPara(); flushList();
      out.push(`<h${m[1].length + 2}>${inline(m[2])}</h${m[1].length + 2}>`); // ## → h4, ### → h5, #### → h6
      continue;
    }
    m = /^\s*-\s+(.*)$/.exec(line); // NOSONAR jssecurity:S8786 — no backtracking risk, verified: mandatory "-" prefix fails immediately on any non-list line before reaching \s+/.*
    if (m) {
      flushPara();
      if (list?.tag !== "ul") { flushList(); list = {tag: "ul", items: []}; }
      list.items.push(m[1]);
      continue;
    }
    m = /^\s*\d+\.\s+(.*)$/.exec(line); // NOSONAR jssecurity:S8786 — no backtracking risk, verified: mandatory digit+"." prefix fails immediately on any non-list line before reaching \s+/.*
    if (m) {
      flushPara();
      if (list?.tag !== "ol") { flushList(); list = {tag: "ol", items: []}; }
      list.items.push(m[1]);
      continue;
    }
    flushList();
    para.push(line);
  }
  flushPara(); flushList();
  return out.join("\n");
}
