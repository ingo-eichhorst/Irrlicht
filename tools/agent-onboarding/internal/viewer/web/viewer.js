// Viewer SPA. Loads scenario list, fetches a recording on click, renders
// the timeline with a scrubber. Per #268 Phase 7 spec: speed presets
// 1×/2×/5×/10×/20×/25×/100×, adaptive fast-forward, state-change
// reason panel showing rule_id + signal_ref + evidence.

const SPEED_PRESETS = [1, 2, 5, 10, 20, 25, 100];

// Module-level handles populated during init() and reused by the
// Overview button + scenario clicks to swap views in the main pane.
let scenariosList = [];   // live recordings from /api/scenarios
let catalog = null;       // /api/catalog payload (coverage or scenarios)
let catalogSource = "";   // "coverage" | "scenarios" — drives the matrix shape
let recipes = null;       // /api/recipes payload — scenarios.json verbatim
let recipesByCoverageId = new Map(); // coverage_id → recipe entry

(async function init() {
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
  const sidebar = document.getElementById("scenarios");
  sidebar.innerHTML = "";

  // Overview button — always present. Click sets the hash; the router
  // hashchange handler does the actual view swap.
  const overviewBtn = document.createElement("button");
  overviewBtn.className = "scn overview-btn";
  overviewBtn.dataset.route = "overview";
  overviewBtn.textContent = "📊 Overview";
  overviewBtn.addEventListener("click", () => navigate("#/"));
  sidebar.appendChild(overviewBtn);

  if (!scenarios || scenarios.length === 0) {
    const note = document.createElement("div");
    note.style.cssText = "padding: 8px; font-size: 12px; color: #888;";
    note.textContent = "No recordings found under replaydata/agents/.";
    sidebar.appendChild(note);
    // Wire router even without recordings — overview view still works.
    window.addEventListener("hashchange", route);
    route();
    return;
  }
  // Group by subtree (scenarios vs regression) first, then by agent.
  // Each top-level h1 splits the sidebar into two visually distinct
  // sections so pipeline-managed recordings and regression captures
  // don't sit next to each other unannounced.
  const bySubtree = {scenarios: {}, regression: {}};
  for (const s of scenarios) {
    if (!bySubtree[s.subtree]) bySubtree[s.subtree] = {};
    (bySubtree[s.subtree][s.agent] ||= []).push(s);
  }
  for (const subtree of ["scenarios", "regression"]) {
    const agents = bySubtree[subtree];
    if (!agents || Object.keys(agents).length === 0) continue;
    const h1 = document.createElement("h1");
    h1.textContent = subtree;
    sidebar.appendChild(h1);
    for (const agent of Object.keys(agents).sort()) {
      const h2 = document.createElement("h2");
      h2.textContent = agent;
      sidebar.appendChild(h2);
      for (const s of agents[agent]) {
        // <button> rather than <a> so the element is reliably
        // click-triggerable from any input source (mouse, keyboard,
        // accessibility tools, Chrome MCP). data-rec-key lets the
        // router find this button when restoring active state from
        // a deep link.
        const el = document.createElement("button");
        el.className = "scn" + (s.has_ground_truth ? " has-gt" : "");
        el.dataset.recKey = `${s.agent}/${s.subtree}/${s.id}`;
        el.textContent = s.id;
        el.addEventListener("click", () => navigate(`#/recording/${s.agent}/${s.subtree}/${s.id}`));
        sidebar.appendChild(el);
      }
    }
  }
  // Wire the router and dispatch the initial route. Deep links land
  // directly on the requested view; bare `/` falls through to overview.
  window.addEventListener("hashchange", route);
  route();
})();

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
//   "#/scenario/<id>"                       → scenario coverage detail
//   "#/recording/<agent>/<subtree>/<id>"    → recording playback
// Unknown hashes fall back to overview.
function route() {
  const hash = location.hash || "#/";
  let m;
  if ((m = hash.match(/^#\/scenario\/([^/]+)\/?$/))) {
    loadCoverageDetail(decodeURIComponent(m[1]));
    return;
  }
  if ((m = hash.match(/^#\/recording\/([^/]+)\/([^/]+)\/([^/]+)\/?$/))) {
    const agent = decodeURIComponent(m[1]);
    const subtree = decodeURIComponent(m[2]);
    const id = decodeURIComponent(m[3]);
    const rec = scenariosList.find(r => r.agent === agent && r.subtree === subtree && r.id === id);
    if (!rec) {
      console.warn("route: no recording for", hash, "— falling back to overview");
      navigate("#/");
      return;
    }
    loadScenario(rec);
    return;
  }
  // Default: overview. Strip any unknown hash content from the title.
  loadOverview();
}

// loadOverview swaps the main pane to the scenario coverage matrix.
// Two catalog shapes are supported:
//
//   coverage  (.specs/agent-scenarios-coverage.json, source of truth):
//     38 scenarios × 5 agents. Each cell has agent_supports +
//     irrlicht_observes verdicts (yes/no/partial/unknown) plus a notes
//     field. Cell badge colors reflect the verdict combo.
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
  const sourceLabel = catalogSource === "coverage"
    ? ".specs/agent-scenarios-coverage.json (source of truth)"
    : ".claude/skills/ir:onboard-agent/scenarios.json (fallback)";
  document.getElementById("breadcrumb").textContent =
    catalog ? `from ${sourceLabel} — refresh to pick up edits` : "catalog unavailable";
  const detail = document.getElementById("detail");
  detail.innerHTML = "";

  if (!catalog || !Array.isArray(catalog.scenarios)) {
    const p = document.createElement("p");
    p.textContent = "Catalog not loaded — /api/catalog returned no scenarios array.";
    detail.appendChild(p);
    return;
  }

  if (catalogSource === "coverage") {
    renderCoverageMatrix(detail);
  } else {
    renderScenariosMatrix(detail);
  }
}

// renderCoverageMatrix paints the 38×5 maintainer coverage matrix.
// Each cell colored by the (agent_supports, irrlicht_observes) combo:
//   both yes        → green
//   both partial    → amber
//   either no       → red
//   any unknown     → gray
//   either partial  → light amber
// Notes (if any) show in the tooltip.
function renderCoverageMatrix(detail) {
  // catalog.agents is [{id, onboarded}, …] — extract ids for column iteration.
  const agents = (catalog.agents || []).map(a => typeof a === "string" ? a : a.id);
  // Recording lookup: only "scenarios" subtree counts here; regression
  // captures are not part of the coverage matrix.
  const recIndex = new Map();
  for (const r of scenariosList) {
    if (r.subtree === "scenarios") recIndex.set(`${r.agent}/${r.id}`, r);
  }

  // Group by section so the table visually breaks at "Session
  // lifecycle", "Tool calls", etc.
  const bySection = new Map();
  for (const sc of catalog.scenarios) {
    const sec = sc.section || "(other)";
    if (!bySection.has(sec)) bySection.set(sec, []);
    bySection.get(sec).push(sc);
  }

  const panel = document.createElement("div");
  panel.className = "panel";
  const h3 = document.createElement("h3");
  h3.textContent = `Scenario coverage — ${catalog.scenarios.length} scenarios × ${agents.length} agents`;
  panel.appendChild(h3);

  const table = document.createElement("table");
  table.className = "overview-matrix";
  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  ["Scenario", ...agents].forEach(h => {
    const th = document.createElement("th");
    th.textContent = h;
    headRow.appendChild(th);
  });
  thead.appendChild(headRow);
  table.appendChild(thead);

  const tbody = document.createElement("tbody");
  let currentSection = "";
  for (const sc of catalog.scenarios) {
    if (sc.section && sc.section !== currentSection) {
      currentSection = sc.section;
      const sectionRow = document.createElement("tr");
      const td = document.createElement("td");
      td.colSpan = 1 + agents.length;
      td.style.cssText = "background: #f5f4ee; font-size: 11px; font-weight: 600; color: #555; padding: 6px 8px;";
      td.textContent = sc.section;
      sectionRow.appendChild(td);
      tbody.appendChild(sectionRow);
    }
    const row = document.createElement("tr");
    const nameCell = document.createElement("td");
    nameCell.style.cssText = "cursor: pointer;";
    const nameLink = document.createElement("button");
    nameLink.style.cssText = "background: transparent; border: 0; padding: 0; text-align: left; cursor: pointer; font: inherit; color: inherit;";
    const codeChip = sc.code
      ? `<span style="display: inline-block; min-width: 28px; padding: 1px 5px; margin-right: 6px; background: #e8e6da; color: #555; border-radius: 3px; font-size: 10px; font-weight: 600; font-family: monospace; vertical-align: 1px;">${escapeHtml(sc.code)}</span>`
      : "";
    nameLink.innerHTML = `${codeChip}<span style="font-weight: 600; color: #1f56a8; text-decoration: underline;">${sc.id}</span><br>` +
      `<span style="font-weight: normal; color: #666; font-size: 11px; margin-left: ${sc.code ? '34px' : '0'};">${sc.feature || ""}</span>`;
    nameLink.addEventListener("click", () => navigate(`#/scenario/${sc.id}`));
    nameCell.appendChild(nameLink);
    row.appendChild(nameCell);
    for (const agent of agents) {
      const cov = sc.coverage && sc.coverage[agent];
      const cell = document.createElement("td");
      cell.style.textAlign = "center";
      if (!cov) {
        cell.textContent = "—";
        cell.style.color = "#ccc";
        cell.title = `${agent}: no entry`;
        row.appendChild(cell);
        continue;
      }
      const sup = cov.agent_supports || "unknown";
      const obs = cov.irrlicht_observes || "unknown";
      const {label, bg, fg} = coverageBadge(sup, obs);
      const rec = recIndex.get(`${agent}/${sc.id}`);
      const badge = document.createElement(rec ? "button" : "span");
      badge.textContent = label;
      // font: inherit on the button branch — without it, the user-agent
      // stylesheet swaps the page font for a platform-specific UI font
      // whose glyph metrics shrink "●●" relative to the span branch.
      // line-height:1 keeps the pill height consistent across the two.
      badge.style.cssText = `display: inline-block; padding: 2px 8px; border-radius: 10px; ` +
        `font: inherit; font-size: 13px; font-weight: 600; line-height: 1; ` +
        `background: ${bg}; color: ${fg}; ` +
        `border: 0; cursor: ${rec ? "pointer" : "default"};`;
      // Build a multi-line tooltip
      const lines = [`${agent}: agent_supports=${sup}, irrlicht_observes=${obs}`];
      if (cov.notes) lines.push(cov.notes);
      lines.push(rec ? `↻ click to open recording` : `(no recording committed)`);
      badge.title = lines.join("\n");
      if (rec) {
        badge.addEventListener("click", () => {
          navigate(`#/recording/${rec.agent}/${rec.subtree}/${rec.id}`);
        });
      }
      cell.appendChild(badge);
      row.appendChild(cell);
    }
    tbody.appendChild(row);
  }
  table.appendChild(tbody);
  panel.appendChild(table);
  detail.appendChild(panel);

  // Summary chips
  const sum = document.createElement("div");
  sum.style.cssText = "margin-top: 8px; display: flex; gap: 14px; font-size: 11px; color: #555;";
  let recorded = 0, observableNow = 0, supported = 0, total = 0;
  for (const sc of catalog.scenarios) {
    for (const agent of agents) {
      total++;
      const cov = sc.coverage && sc.coverage[agent];
      if (!cov) continue;
      if (cov.agent_supports === "yes") supported++;
      if (cov.agent_supports === "yes" && cov.irrlicht_observes === "yes") observableNow++;
      if (recIndex.has(`${agent}/${sc.id}`)) recorded++;
    }
  }
  sum.innerHTML = `
    <span><b>${recorded}</b> recordings committed</span>
    <span><b>${observableNow}</b> fully observable now</span>
    <span><b>${supported}</b> agent-supported</span>
    <span><b>${total}</b> total cells</span>
  `;
  panel.appendChild(sum);

  // Legend
  const legend = document.createElement("div");
  legend.style.cssText = "margin-top: 8px; display: flex; gap: 12px; font-size: 11px; color: #555; flex-wrap: wrap;";
  legend.innerHTML = `
    <span>Legend:</span>
    <span><span style="background:#d6f0d4;color:#1f5a1d;padding:1px 6px;border-radius:8px;">●●</span> agent supports + irrlicht observes</span>
    <span><span style="background:#fde7c1;color:#8a4500;padding:1px 6px;border-radius:8px;">●◐</span> partial somewhere</span>
    <span><span style="background:#f8c8c8;color:#8a0000;padding:1px 6px;border-radius:8px;">✗</span> no</span>
    <span><span style="background:#e5e5e5;color:#555;padding:1px 6px;border-radius:8px;">?</span> unknown</span>
  `;
  panel.appendChild(legend);
}

// loadCoverageDetail shows the per-agent testing plan for one
// scenario from the coverage catalog. Combines:
//   - Coverage data (.specs/agent-scenarios-coverage.json) — verdicts
//     and maintainer notes per agent.
//   - Recording recipe (scenarios.json) — joined by coverage_id —
//     showing the actual driver (interactive tmux vs headless print),
//     step-script or prompt, settings, and which committed recordings
//     exist for each agent.
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
  document.title = `Irrlicht — ${codePrefix}${sc.feature || sc.id}`;
  document.getElementById("title").textContent = sc.feature || sc.id;
  document.getElementById("breadcrumb").textContent = `${sc.section || ""} → ${sc.id}`;
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
    <h3 style="margin-top:0;">${codeBadge}${sc.feature || sc.id}</h3>
    <div style="font-size: 11px; color: #888; margin-bottom: 6px;">
      <code>${sc.id}</code> · ${sc.section || ""}
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
    if (spec && Array.isArray(spec.scenarios) && spec.scenarios.length > 0) {
      detail.appendChild(renderSpecPanel(spec));
    }
  } catch (_) { /* spec unavailable — show recipe-only */ }

  // Recipe lookup by coverage_id
  const recipe = recipesByCoverageId.get(sc.id);
  if (recipe) {
    const recipePanel = document.createElement("div");
    recipePanel.className = "panel";
    let recHTML = `<h3>Recording recipe — <code>${recipe.name}</code></h3>`;
    if (recipe.description) {
      recHTML += `<p style="font-size: 12px; color: #444; margin: 0 0 8px;">${recipe.description}</p>`;
    }
    if (Array.isArray(recipe.requires) && recipe.requires.length) {
      recHTML += `<div style="font-size: 11px; color: #888;">requires: <code>${recipe.requires.join(", ")}</code></div>`;
    }
    if (recipe.verify && Object.keys(recipe.verify).length) {
      recHTML += `<div style="font-size: 11px; color: #888;">verify: <code>${escapeHtml(JSON.stringify(recipe.verify))}</code></div>`;
    }
    recipePanel.innerHTML = recHTML;
    detail.appendChild(recipePanel);
  } else {
    const stub = document.createElement("div");
    stub.className = "panel";
    stub.innerHTML = `
      <h3>Recording recipe</h3>
      <p style="font-size: 12px; color: #888; margin: 0;">
        No recording recipe configured yet for this coverage scenario.
        To add one, edit <code>.claude/skills/ir:onboard-agent/scenarios.json</code>
        and set <code>coverage_id: "${sc.id}"</code> on a new or existing
        entry under <code>scenarios[]</code>.
      </p>
    `;
    detail.appendChild(stub);
  }

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
  let html = `<h3 style="margin-top:0;">Scenario description <span style="font-weight: normal; color: #888; font-size: 11px;">— from <code>.specs/agent-scenarios.md</code></span></h3>`;
  if (spec.scenarios.length === 1) {
    const sc = spec.scenarios[0];
    html += `<div style="font-size: 12px; color: #333; margin-bottom: 8px;">${escapeHtml(sc.text)}</div>`;
    if (sc.expected && sc.expected.length) {
      html += `<div style="font-size: 11px; color: #666; margin-bottom: 4px;"><b>Expected (user-observable)</b></div>`;
      html += "<ul style=\"font-size: 12px; padding-left: 22px; margin: 0; color: #333;\">";
      for (const e of sc.expected) html += `<li>${escapeHtml(e)}</li>`;
      html += "</ul>";
    }
  } else {
    spec.scenarios.forEach((sc, i) => {
      html += `<div style="margin-bottom: 12px;">`;
      html += `<div style="font-size: 11px; color: #888; font-weight: 600; margin-bottom: 3px;">Variant ${i + 1}</div>`;
      html += `<div style="font-size: 12px; color: #333; margin-bottom: 4px;">${escapeHtml(sc.text)}</div>`;
      if (sc.expected && sc.expected.length) {
        html += "<ul style=\"font-size: 12px; padding-left: 22px; margin: 0; color: #333;\">";
        for (const e of sc.expected) html += `<li>${escapeHtml(e)}</li>`;
        html += "</ul>";
      }
      html += `</div>`;
    });
  }
  panel.innerHTML = html;
  return panel;
}

// buildAgentPlanPanel composes one card per agent showing how this
// scenario is (or would be) recorded for that agent: coverage verdict,
// notes, driver choice, step-script or prompt, and any existing
// recording.
function buildAgentPlanPanel(sc, agent, recipe) {
  const panel = document.createElement("div");
  panel.className = "panel";
  panel.style.marginBottom = "12px";

  const cov = sc.coverage && sc.coverage[agent];
  const sup = cov && cov.agent_supports || "unknown";
  const obs = cov && cov.irrlicht_observes || "unknown";
  const {label, bg, fg} = coverageBadge(sup, obs);

  const headerHTML = `
    <h3 style="margin-top:0; display: flex; align-items: center; gap: 8px;">
      ${agent}
      <span style="background: ${bg}; color: ${fg}; padding: 1px 8px; border-radius: 10px; font-size: 11px; font-weight: 600;">${label}</span>
    </h3>
    <div style="font-size: 11px; color: #555; margin-bottom: 6px;">
      agent_supports: <b>${sup}</b> · irrlicht_observes: <b>${obs}</b>
    </div>
  `;
  let html = headerHTML;
  if (cov && cov.notes) {
    html += `<div style="font-size: 12px; color: #444; padding: 6px 8px; background: #fafaf2; border-left: 3px solid #d8d6cc; margin-bottom: 8px;">${escapeHtml(cov.notes)}</div>`;
  }

  // Recipe section per agent. Two shapes in scenarios.json:
  //   - by_adapter.<agent>.prompt → headless driver (drive-<adapter>.sh)
  //   - by_adapter.<agent>.script → interactive tmux driver (drive-<adapter>-interactive.sh)
  if (recipe && recipe.by_adapter && recipe.by_adapter[agent]) {
    const a = recipe.by_adapter[agent];
    // Idle-only badge when the recipe is observation-only (no prompts sent).
    const idleTag = recipe.idle_only
      ? ` <span style="background: #e0eaff; color: #1f3d8a; padding: 1px 6px; border-radius: 8px; font-size: 10px; font-weight: 600; margin-left: 6px;">idle observation</span>`
      : "";
    if (Array.isArray(a.script)) {
      html += `<div style="font-size: 11px; color: #666; margin: 8px 0 4px;">
        <b>Driver:</b> Interactive (tmux REPL) — <code>drive-${agent}-interactive.sh</code>${idleTag}
      </div>`;
      html += renderStepScript(a.script);
    } else if (a.prompt) {
      html += `<div style="font-size: 11px; color: #666; margin: 8px 0 4px;">
        <b>Driver:</b> Headless (<code>--print</code>) — <code>drive-${agent}.sh</code>${idleTag}
      </div>`;
      html += `<pre style="background: #1e1e1e; color: #d4d4d4; padding: 8px; border-radius: 4px; font-size: 11px; white-space: pre-wrap; margin: 0;">${escapeHtml(a.prompt)}</pre>`;
    } else {
      html += `<div style="font-size: 12px; color: #888;">Recipe entry exists but has no prompt or script.</div>`;
    }
    const timeout = a.timeout_seconds;
    const settings = a.settings || {};
    const meta = [];
    if (typeof timeout === "number") meta.push(`timeout: ${timeout}s`);
    if (Object.keys(settings).length) meta.push(`settings: <code>${escapeHtml(JSON.stringify(settings))}</code>`);
    if (meta.length) {
      html += `<div style="font-size: 11px; color: #888; margin-top: 6px;">${meta.join(" · ")}</div>`;
    }
    // Preconditions / setup / verify — only present on recipes that
    // have been translated by the per-cell workflow (see translate/SKILL.md).
    if (Array.isArray(a.preconditions) && a.preconditions.length) {
      html += renderChecklistBlock("Preconditions", a.preconditions, "□");
    }
    if (Array.isArray(a.setup) && a.setup.length) {
      html += renderChecklistBlock("Setup (run-cell.sh handles this)", a.setup, "•");
    }
    if (Array.isArray(a.verify) && a.verify.length) {
      html += renderChecklistBlock("Verify after recording", a.verify, "□");
    }
  } else if (recipe) {
    html += `<div style="font-size: 12px; color: #888; padding: 6px 0;">
      No <code>by_adapter.${agent}</code> entry on the recipe — adapter doesn't
      currently drive this scenario. Either the capability is missing, or the
      recipe just hasn't been written yet.
    </div>`;
  } else {
    html += `<div style="font-size: 12px; color: #888; padding: 6px 0;">
      No recording recipe wired to this scenario (no <code>coverage_id: "${sc.id}"</code>
      in scenarios.json yet).
    </div>`;
  }

  // Existing recording link if one is committed
  const rec = scenariosList.find(r => r.subtree === "scenarios" && r.agent === agent && recipe && r.id === recipe.name);
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

function escapeHtml(s) {
  return String(s).replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"})[c]);
}

function coverageBadge(sup, obs) {
  if (sup === "no" || obs === "no") return {label: "✗", bg: "#f8c8c8", fg: "#8a0000"};
  if (sup === "yes" && obs === "yes") return {label: "●●", bg: "#d6f0d4", fg: "#1f5a1d"};
  if (sup === "unknown" || obs === "unknown") return {label: "?", bg: "#e5e5e5", fg: "#555"};
  if (sup === "partial" || obs === "partial") return {label: "●◐", bg: "#fde7c1", fg: "#8a4500"};
  return {label: "—", bg: "transparent", fg: "#ccc"};
}

// renderScenariosMatrix paints the older 8×5 by_adapter view from
// scenarios.json (fallback when .specs/agent-scenarios-coverage.json
// isn't reachable).
function renderScenariosMatrix(detail) {
  const adapterSet = new Set();
  for (const sc of catalog.scenarios) {
    for (const a of Object.keys(sc.by_adapter || {})) adapterSet.add(a);
  }
  const adapters = [...adapterSet].sort();

  const recIndex = new Map();
  for (const r of scenariosList) {
    if (r.subtree === "scenarios") recIndex.set(`${r.agent}/${r.id}`, r);
  }

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
      const cell = document.createElement("td");
      cell.style.textAlign = "center";
      const declares = sc.by_adapter && sc.by_adapter[adapter];
      if (!declares) {
        cell.textContent = "—";
        cell.style.color = "#ccc";
        cell.title = `${adapter}: not declared`;
      } else {
        const rec = recIndex.get(`${adapter}/${sc.name}`);
        if (rec) {
          const btn = document.createElement("button");
          btn.textContent = "✓";
          btn.title = `Open ${adapter}/${sc.name}`;
          btn.style.cssText = "background: transparent; border: 0; color: #2a8d4f; font-size: 16px; cursor: pointer; padding: 0;";
          btn.addEventListener("click", () => {
            navigate(`#/recording/${rec.agent}/${rec.subtree}/${rec.id}`);
          });
          cell.appendChild(btn);
        } else {
          cell.textContent = "○";
          cell.style.color = "#c08a00";
          cell.title = `${adapter}: declared but no recording committed`;
        }
      }
      row.appendChild(cell);
    }
    tbody.appendChild(row);
  }
  table.appendChild(tbody);
  panel.appendChild(table);
  detail.appendChild(panel);
}

async function loadScenario(s) {
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
  document.title = `Irrlicht — ${s.agent}/${s.subtree}/${s.id}`;
  document.getElementById("title").textContent = s.id;
  document.getElementById("breadcrumb").textContent = `${s.agent} / ${s.subtree} / ${s.id}`;
  const detail = document.getElementById("detail");
  detail.innerHTML = `<p>Loading…</p>`;

  const [data, archives] = await Promise.all([
    fetch(`/api/scenarios/${s.agent}/${s.subtree}/${s.id}`).then(r => r.json()),
    fetch(`/api/scenarios/${s.agent}/${s.subtree}/${s.id}/recordings`).then(r => r.ok ? r.json() : []).catch(() => []),
  ]);
  detail.innerHTML = "";
  detail.appendChild(renderPlayback(s, data));
  detail.appendChild(renderMeta(data));
  detail.appendChild(renderExpected(data));
  // Recording history picker — only render when there are archived
  // recordings (recordings/ dir is empty for first-time-recorded
  // scenarios; rendering an empty dropdown would just be noise).
  if (Array.isArray(archives) && archives.length > 0) {
    detail.appendChild(renderRecordingHistory(s, data, archives));
  }
  const gt = renderGroundTruth(data);
  gt.classList.add("ground-truth-host");
  detail.appendChild(gt);
  const tr = renderTransitions(data);
  tr.classList.add("transitions-host");
  detail.appendChild(tr);
  detail.appendChild(renderValidate(data));
  // Signals preview only makes sense for recordings made by Phase 1's
  // multi-sensor recorder. All committed pre-recorder recordings have
  // data.signals = []; rendering the panel for them is dead weight.
  if (Array.isArray(data.signals) && data.signals.length > 0) {
    detail.appendChild(renderSignalsPreview(data));
  }
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

// renderExpected paints the spec-grounded expected.jsonl validation
// report. Distinct from renderGroundTruth (which shows per-recording
// measured offsets): this panel asserts the daemon's behavior against
// the spec, so a regression shows up as a red ✗ pill rather than a
// silent rebase of the truth.
function renderExpected(data) {
  const p = panel("Spec expectations");
  if (!data.expected || !Array.isArray(data.expected.phases) || data.expected.phases.length === 0) {
    p.appendChild(text("No expected.jsonl for this scenario. Author one per the translate skill's Step 3.5 (.specs-grounded benchmark, distinct from ground_truth.jsonl)."));
    return p;
  }
  const rep = data.expected;
  const summaryColor = rep.pass ? "#d6f0d4" : "#f8c8c8";
  const summaryFg = rep.pass ? "#1f5a1d" : "#8a0000";
  const summary = document.createElement("div");
  summary.style.cssText = "margin-bottom: 8px; display: flex; gap: 10px; align-items: center; flex-wrap: wrap;";
  summary.innerHTML = `
    <span style="background: ${summaryColor}; color: ${summaryFg}; padding: 2px 10px; border-radius: 10px; font-size: 11px; font-weight: 600;">
      ${escapeHtml(rep.summary || "")}
    </span>
    <span style="font-size: 11px; color: #888;">
      source: <code>${escapeHtml(rep.meta && rep.meta.source || "")}</code>
    </span>
  `;
  p.appendChild(summary);

  const tbl = document.createElement("table");
  tbl.innerHTML = `<tr>
    <th>phase</th>
    <th>target</th>
    <th>anchor</th>
    <th>window</th>
    <th>result</th>
    <th>delta</th>
    <th>spec text</th>
  </tr>`;
  // Definitions and phases are same-length, same-order arrays from
  // the validator. Zip by index so the row shows full context.
  const defs = Array.isArray(rep.definitions) ? rep.definitions : [];
  for (let i = 0; i < rep.phases.length; i++) {
    const ph = rep.phases[i];
    const def = defs[i] || {};
    const target = def.expected_state
      ? `state=<span class="badge ${def.expected_state}">${def.expected_state}</span>`
      : (def.kind ? `kind=<code>${escapeHtml(def.kind)}</code>` : "—");
    const anchor = def.relative_to ? `<code>${escapeHtml(def.relative_to)}</code>` : "<code>start</code>";
    let win = "";
    if (def.max_delay_ms) win += `≤ ${def.max_delay_ms} ms`;
    if (def.duration_at_least_ms) win += (win ? " · " : "") + `≥ ${def.duration_at_least_ms} ms`;
    if (!win) win = "—";
    const resultPill = ph.pass
      ? `<span class="badge ready">✓ pass</span>`
      : `<span class="badge fail">✗ fail</span>`;
    const delta = ph.matched_ts ? `+${ph.delta_ms} ms` : "—";
    const specText = def.text || "";
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td><code>${escapeHtml(ph.phase)}</code></td>
      <td style="font-size: 11px;">${target}</td>
      <td style="font-size: 11px;">${anchor}</td>
      <td style="font-size: 11px; color: #555;">${win}</td>
      <td>${resultPill}</td>
      <td>${escapeHtml(delta)}</td>
      <td title="${escapeHtml(specText)}" style="font-size: 11px; color: #555;">${escapeHtml(truncate(specText, 90))}</td>`;
    tbl.appendChild(tr);
  }
  p.appendChild(tbl);

  // Failure detail block — surface the reason strings prominently so
  // the operator can scan failures without hovering each row.
  const failed = rep.phases.filter(ph => !ph.pass);
  if (failed.length > 0) {
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
  return p;
}

function truncate(s, n) {
  if (!s) return "";
  if (s.length <= n) return s;
  return s.slice(0, n - 1) + "…";
}

// renderRecordingHistory paints a picker for the scenario's archived
// recordings (under replaydata/.../recordings/). Default selection is
// "Latest" — the top-level events.jsonl that drives every other
// panel. Picking an archived recording re-fetches its
// events/transcript/ground_truth and updates the transitions +
// ground-truth panels in place. The state band on the playback view
// keeps showing latest for now — retargeting playback to an archive
// is a bigger lift.
function renderRecordingHistory(s, latestData, archives) {
  const p = panel("Recording history");
  const intro = document.createElement("div");
  intro.style.cssText = "margin-bottom: 8px; font-size: 12px; color: #555;";
  intro.innerHTML = `<b>${archives.length}</b> previous recording${archives.length === 1 ? "" : "s"} archived. ` +
    `Select an archive to view its captured behavior; expected.jsonl is the constant benchmark across all of them.`;
  p.appendChild(intro);

  const select = document.createElement("select");
  select.style.cssText = "padding: 4px 8px; font: inherit; font-size: 12px; border: 1px solid #c0bdb1; border-radius: 3px;";
  const latestOpt = document.createElement("option");
  latestOpt.value = "";
  latestOpt.textContent = "Latest (current events.jsonl)";
  select.appendChild(latestOpt);
  for (const a of archives) {
    const opt = document.createElement("option");
    opt.value = a.name;
    const verLabel = a.daemon_version ? ` · daemon ${a.daemon_version}` : "";
    const passLabel = a.expected_pass_rate ? ` · ${a.expected_pass_rate}` : "";
    opt.textContent = `${a.promoted_at || a.name}${verLabel}${passLabel}`;
    select.appendChild(opt);
  }
  p.appendChild(select);

  const manifestBox = document.createElement("div");
  manifestBox.style.cssText = "margin-top: 10px; font-size: 11px; color: #666;";
  p.appendChild(manifestBox);

  select.addEventListener("change", async () => {
    const name = select.value;
    // Remove any panels we previously injected so a re-selection
    // doesn't stack them.
    document.querySelectorAll(".archive-injected").forEach(el => el.remove());

    if (!name) {
      // Latest — restore the original panels.
      manifestBox.innerHTML = "<i>Latest — no archive metadata.</i>";
      reRenderForLatest(latestData);
      return;
    }
    const arch = archives.find(a => a.name === name);
    if (arch) {
      manifestBox.innerHTML = `
        <b>promoted_at:</b> ${escapeHtml(arch.promoted_at || "")}<br>
        <b>daemon_version:</b> ${escapeHtml(arch.daemon_version || "")}<br>
        <b>agent_cli_version:</b> ${escapeHtml(arch.agent_cli_version || "")}<br>
        <b>recipe_hash:</b> <code>${escapeHtml((arch.recipe_hash || "").slice(0, 16))}…</code><br>
        <b>expected_pass_rate (at promote):</b> ${escapeHtml(arch.expected_pass_rate || "—")}<br>
        <b>recording_started_at:</b> ${escapeHtml(arch.recording_started_at || "")}
      `;
    }
    const archDetail = await fetch(
      `/api/scenarios/${s.agent}/${s.subtree}/${s.id}/recordings/${encodeURIComponent(name)}`
    ).then(r => r.json());
    // archDetail has the archive's transitions + ground_truth. Build
    // a synthetic detail-like object so the existing render functions
    // work unchanged.
    const archData = {
      ...latestData,
      transitions: archDetail.transitions || [],
      ground_truth: archDetail.ground_truth || null,
    };
    reRenderForArchive(archData);
  });

  function reRenderForLatest(d) {
    swapPanel("ground-truth-host", renderGroundTruth(d));
    swapPanel("transitions-host", renderTransitions(d));
  }
  function reRenderForArchive(d) {
    swapPanel("ground-truth-host", renderGroundTruth(d));
    swapPanel("transitions-host", renderTransitions(d));
  }
  function swapPanel(hostClass, newPanel) {
    newPanel.classList.add(hostClass);
    const existing = document.querySelector("." + hostClass);
    if (existing) existing.replaceWith(newPanel);
  }

  return p;
}

function renderGroundTruth(data) {
  const p = panel("Ground truth");
  if (!data.ground_truth || !data.ground_truth.labels || data.ground_truth.labels.length === 0) {
    p.appendChild(text("No ground_truth.jsonl — this scenario is regression-only (replay-fixtures.sh exercises it but the validator skips it)."));
    return p;
  }
  const tbl = document.createElement("table");
  tbl.innerHTML = `<tr><th>+ms</th><th>marker</th><th>expected</th><th>tol</th><th>evidence_kind</th><th>notes</th></tr>`;
  for (const l of data.ground_truth.labels) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td><span class="gt-marker">${l.ts_offset_ms}</span></td>
      <td><code>${escapeHtml(l.marker)}</code></td>
      <td><span class="badge ${l.expected_state}">${l.expected_state}</span></td>
      <td>${l.tolerance_ms || 1000}</td>
      <td>${escapeHtml(l.evidence_kind || "")}</td>
      <td>${escapeHtml(l.notes || "")}</td>`;
    tbl.appendChild(tr);
  }
  p.appendChild(tbl);
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

function renderValidate(data) {
  if (!data.validate) return panelFiller();
  const p = panel("Validator result");
  try {
    const v = typeof data.validate === "string" ? JSON.parse(data.validate) : data.validate;
    const b = document.createElement("div");
    b.innerHTML = `<span class="badge ${v.pass ? 'pass' : 'fail'}">${v.pass ? 'PASS' : 'FAIL'}</span> &nbsp; <code>${v.scenario}</code>`;
    p.appendChild(b);
    const tbl = document.createElement("table");
    tbl.style.marginTop = "8px";
    tbl.innerHTML = `<tr><th>marker</th><th>expected</th><th>observed</th><th>Δms</th><th>tol</th><th>pass</th><th>note</th></tr>`;
    for (const l of v.labels || []) {
      const tr = document.createElement("tr");
      if (!l.pass) tr.className = "hl";
      tr.innerHTML = `
        <td>${escapeHtml(l.marker)}</td>
        <td><span class="badge ${l.expected_state}">${l.expected_state}</span></td>
        <td><span class="badge ${l.observed_state || 'none'}">${escapeHtml(l.observed_state || '∅')}</span></td>
        <td>${l.delta_ms}</td>
        <td>${l.tolerance_ms}</td>
        <td><span class="badge ${l.pass ? 'pass' : 'fail'}">${l.pass ? '✓' : '✗'}</span></td>
        <td>${escapeHtml(l.note || "")}</td>`;
      tbl.appendChild(tr);
    }
    p.appendChild(tbl);
  } catch (e) {
    p.appendChild(text("(could not parse validate result)"));
  }
  return p;
}

// renderPlayback wires the play/pause/scrubber UI and the dashboard
// iframe. Takes the scenario picker entry (NOT the full detail payload)
// because that's what we need to POST /api/replay/start.
function renderPlayback(s, detailData) {
  const p = panel("Playback");

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
      try { await fetch(`/api/replay/speed?speed=${sp}`, {method: "POST"}); } catch {}
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
    tip.textContent = el.getAttribute("data-tip") || "";
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
    const next = e.relatedTarget && e.relatedTarget.closest && e.relatedTarget.closest("[data-tip]");
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

  // State colors. Consolidated to 3 high-signal colors that match what
  // the dashboard uses for session badges, plus a neutral gap color.
  const STATE_COLOR = {
    ready:   "#4ade80", // green
    working: "#8b5cf6", // purple
    waiting: "#f59e0b", // amber
  };

  // STATE_PRIORITY drives the aggregation rule when multiple sessions
  // are alive at the same moment (parent + subagents). The band shows
  // the highest-priority state among active sessions, so the user sees
  // "working" for the entire span any session is working — matching how
  // the macOS app's state bar treats the whole run.
  const STATE_PRIORITY = {working: 3, waiting: 2, ready: 1};

  function renderStateBand() {
    stateBand.innerHTML = "";
    if (!totalMs) return;

    // 1. Build per-session timelines.
    //    aliveFrom[sid] = offset of first event for the session
    //    aliveUntil[sid] = offset of the EARLIEST session-end signal
    //                     (process_exited, transcript_removed, or
    //                     presession_removed). For SIGKILL/crash
    //                     scenarios, process_exited fires first and
    //                     transcript_removed may not appear at all,
    //                     so process_exited has to count.
    //    transitions[sid] = sorted [{offset, state}, ...]
    const aliveFrom = {};
    const aliveUntil = {};
    const transitionsBySid = {};
    for (const e of events) {
      if (!e.session_id) continue;
      if (aliveFrom[e.session_id] === undefined) aliveFrom[e.session_id] = e.offset_ms;
      if (e.kind === "process_exited" || e.kind === "transcript_removed" || e.kind === "presession_removed") {
        if (aliveUntil[e.session_id] === undefined || e.offset_ms < aliveUntil[e.session_id]) {
          aliveUntil[e.session_id] = e.offset_ms;
        }
      }
      if (e.kind === "state_transition" && e.new_state) {
        (transitionsBySid[e.session_id] = transitionsBySid[e.session_id] || [])
          .push({offset: e.offset_ms, state: e.new_state});
      }
    }
    for (const sid of Object.keys(transitionsBySid)) {
      transitionsBySid[sid].sort((a, b) => a.offset - b.offset);
    }

    // 2. Collect every distinct boundary offset. The aggregate state is
    //    constant between consecutive boundaries, so segments are bound
    //    by these points.
    const boundarySet = new Set([0, totalMs]);
    for (const sid of Object.keys(aliveFrom)) {
      boundarySet.add(aliveFrom[sid]);
      if (aliveUntil[sid] !== undefined) boundarySet.add(aliveUntil[sid]);
      for (const t of (transitionsBySid[sid] || [])) boundarySet.add(t.offset);
    }
    const boundaries = [...boundarySet].sort((a, b) => a - b);

    // 3. For each interval [boundaries[i], boundaries[i+1]), compute the
    //    aggregate state by taking the max-priority state across active
    //    sessions. A session's state at offset T is its last
    //    state_transition.new_state at or before T, defaulting to ready
    //    (sessions start in ready by convention).
    function sessionStateAt(sid, t) {
      const list = transitionsBySid[sid] || [];
      let st = "ready";
      for (const trans of list) {
        if (trans.offset <= t) st = trans.state;
        else break;
      }
      return st;
    }

    function aggregateAt(t) {
      let best = null;
      let bestPriority = 0;
      for (const sid of Object.keys(aliveFrom)) {
        if (t < aliveFrom[sid]) continue;
        if (aliveUntil[sid] !== undefined && t >= aliveUntil[sid]) continue;
        const st = sessionStateAt(sid, t);
        const pri = STATE_PRIORITY[st] || 0;
        if (pri > bestPriority) { bestPriority = pri; best = st; }
      }
      return best;
    }

    // 4. Walk boundaries and emit one segment per change.
    const segments = [];
    let curStart = boundaries[0];
    let curState = aggregateAt(curStart);
    for (let i = 1; i < boundaries.length; i++) {
      const t = boundaries[i];
      const st = aggregateAt(t);
      if (st !== curState) {
        if (curState !== null) segments.push({start: curStart, end: t, state: curState});
        curStart = t;
        curState = st;
      }
    }
    if (curState !== null && curStart < totalMs) {
      segments.push({start: curStart, end: totalMs, state: curState});
    }

    // 5. Render. Coalesce so the user sees a single working span instead
    //    of many short ones when subagents finish in sequence under a
    //    still-working parent.
    for (const seg of segments) {
      const color = STATE_COLOR[seg.state] || "#cfcdc0";
      const left = (seg.start / totalMs) * 100;
      const width = ((seg.end - seg.start) / totalMs) * 100;
      const region = document.createElement("div");
      region.style.cssText = `position: absolute; top: 0; bottom: 0; left: ${left}%; width: ${width}%; background: ${color};`;
      region.setAttribute("data-tip", `${seg.state}\n+${(seg.start/1000).toFixed(2)}s → +${(seg.end/1000).toFixed(2)}s (${((seg.end-seg.start)/1000).toFixed(2)}s)`);
      stateBand.appendChild(region);
    }
  }

  // isPresession returns true for "proc-<PID>" session ids — irrlicht's
  // convention for a sighting before any transcript file exists. A real
  // session gets a UUID id once its transcript appears. Distinguishing
  // the two matters for the tooltip language: a transcript_removed on a
  // pre-session is a *handoff* (the UUID transcript took over), not an
  // ended conversation.
  function isPresession(sid) {
    return typeof sid === "string" && sid.startsWith("proc-");
  }

  // eventStyle classifies an event into a (color, size, label) triple.
  // Takes the WHOLE event so it can disambiguate by session id — e.g.
  // pid_discovered on proc-* (initial PID sighting) vs. pid_discovered
  // on a UUID (the same PID being re-bound to the upgraded session).
  // Salience tiers — bumped so the cursor lands them easily in the 18px
  // lane:
  //
  //   lifecycle   (14px blue/gray)  — session/presession appear or vanish
  //   process     (14px green)      — process identity confirmed / parent linked
  //   transition  (14px purple)     — state_transition (overlays the state band)
  //   activity    (10px violet)     — transcript_activity (every transcript line)
  //   bookkeeping (7px slate, 60%)  — debounce_coalesced, hook_received, file_event
  //
  // Tooltip text is plain English — no raw event_kind strings — so the
  // user doesn't need to memorize colors.
  function eventStyle(ev) {
    const kind = ev.kind;
    const pre = isPresession(ev.session_id);
    switch (kind) {
      case "transcript_new":
        return pre
          ? {color: "#3b82f6", size: 14, opacity: 1, label: "Process detected — waiting for transcript"}
          : {color: "#3b82f6", size: 14, opacity: 1, label: "Session started — transcript created"};
      case "presession_created":
        return {color: "#3b82f6", size: 14, opacity: 1, label: "Pre-session opened — process matched before transcript"};
      case "presession_removed":
        return {color: "#64748b", size: 14, opacity: 1, label: "Pre-session handed off — UUID session took over"};
      case "transcript_removed":
        return pre
          ? {color: "#64748b", size: 14, opacity: 1, label: "Pre-session transcript dropped"}
          : {color: "#64748b", size: 14, opacity: 1, label: "Session ended — transcript closed"};
      case "process_exited":
        return {color: "#64748b", size: 14, opacity: 1, label: "Process exited"};
      case "process_spawned":
        return {color: "#22c55e", size: 14, opacity: 1, label: "Process spawned"};
      case "pid_discovered":
        return pre
          ? {color: "#22c55e", size: 14, opacity: 1, label: "PID identified for pre-session"}
          : {color: "#22c55e", size: 14, opacity: 1, label: "PID re-bound to UUID session (handoff)"};
      case "parent_linked":
        return {color: "#22c55e", size: 14, opacity: 1, label: "Linked to parent — child/subagent attached"};
      case "state_transition":
        return {color: "#8b5cf6", size: 14, opacity: 1, label: ev.new_state ? `State changed → ${ev.new_state}` : "State changed"};
      case "transcript_activity":
        return {color: "#a78bfa", size: 10, opacity: 0.95, label: "Transcript updated — new lines written"};
      case "debounce_coalesced":
        return {color: "#94a3b8", size: 7, opacity: 0.6, label: "Bookkeeping — multiple updates coalesced"};
      case "hook_received":
        return {color: "#94a3b8", size: 8, opacity: 0.7, label: "Hook event received"};
      case "file_event":
        return {color: "#94a3b8", size: 7, opacity: 0.6, label: "Filesystem event"};
      default:
        return {color: "#94a3b8", size: 7, opacity: 0.5, label: kind};
    }
  }

  function renderEventDots() {
    eventLane.innerHTML = "";
    if (!totalMs) return;
    for (const ev of events) {
      const st = eventStyle(ev);
      const pct = Math.max(0, Math.min(100, (ev.offset_ms / totalMs) * 100));
      const dot = document.createElement("div");
      const sz = st.size;
      dot.style.cssText = `position: absolute; left: ${pct}%; top: ${(18 - sz) / 2}px; ` +
        `width: ${sz}px; height: ${sz}px; background: ${st.color}; opacity: ${st.opacity}; ` +
        `border-radius: 50%; transform: translateX(-${sz/2}px); ` +
        `border: 1.5px solid white; box-shadow: 0 0 0 1px rgba(0,0,0,0.1); cursor: help;`;
      const lines = [st.label, `+${(ev.offset_ms / 1000).toFixed(2)}s`];
      if (ev.session_id && ev.kind !== "debounce_coalesced" && ev.kind !== "file_event") {
        lines.push(`session: ${ev.session_id}`);
      }
      dot.setAttribute("data-tip", lines.join("\n"));
      eventLane.appendChild(dot);
    }
  }

  function renderTurns() {
    turnLane.innerHTML = "";
    if (!totalMs || !turns.length) return;
    for (const t of turns) {
      const pct = Math.max(0, Math.min(100, (t.offset_ms / totalMs) * 100));
      const tick = document.createElement("div");
      const isUser = t.role === "user";
      const color = isUser ? "#2563eb" : "#0d9488";
      // User ticks anchor to top, assistant to bottom — same-instant
      // pairs stack without overlap. Width bumped to 5px, height to
      // 10px so the cursor can hit them easily.
      const top = isUser ? "1px" : "11px";
      tick.style.cssText = `position: absolute; left: ${pct}%; top: ${top}; ` +
        `width: 5px; height: 10px; background: ${color}; transform: translateX(-2.5px); ` +
        `border-radius: 2px; cursor: help;`;
      const roleLabel = isUser ? "User" : "Assistant";
      const offsetLabel = `+${(t.offset_ms / 1000).toFixed(2)}s`;
      tick.setAttribute("data-tip", `${roleLabel}\n${offsetLabel}\n${t.text || ""}`);
      turnLane.appendChild(tick);
    }
  }

  function renderMarkers() {
    renderStateBand();
    renderEventDots();
    renderTurns();
    renderExpectedLane();
  }

  // renderExpectedLane paints one marker per spec-grounded phase from
  // the validator report. Positions come from each phase's
  // matched_ts (passed) or anchor_ts + max_delay_ms (failed). Color
  // encodes pass/fail; shape encodes state-vs-lifecycle. Hover via
  // the shared tooltip overlay shows phase name, spec text, actual
  // vs target, and the validator's pass/fail reason.
  function renderExpectedLane() {
    expectedLane.innerHTML = "";
    if (!totalMs) return;
    const rep = detailData && detailData.expected;
    if (!rep || !Array.isArray(rep.phases) || rep.phases.length === 0) {
      // No expected.jsonl — render a thin grey hint instead of leaving the lane mysteriously empty.
      const note = document.createElement("div");
      note.style.cssText = "position: absolute; left: 0; top: 0; font-size: 10px; color: #aaa; padding: 2px 4px;";
      note.textContent = "expected: not configured";
      expectedLane.appendChild(note);
      return;
    }
    // The validator anchors matched_ts to events[0].Ts (the
    // recording's first event) and exposes it as
    // rep.recording_start. Use that to convert each phase's
    // absolute matched_ts into an offset_ms compatible with the
    // EventMarker positions on the scrubber.
    const startMs = rep.recording_start ? Date.parse(rep.recording_start) : NaN;
    const defs = Array.isArray(rep.definitions) ? rep.definitions : [];
    for (let i = 0; i < rep.phases.length; i++) {
      const ph = rep.phases[i];
      const def = defs[i] || {};
      const matchedMs = ph.matched_ts ? Date.parse(ph.matched_ts) : NaN;
      const offsetMs = Number.isFinite(matchedMs) && Number.isFinite(startMs)
        ? matchedMs - startMs
        : null;
      // Failed phases without a match still need positioning — they
      // anchor at the validator's "should-have-been-here" point,
      // which is anchor_ts + max_delay_ms. Without anchor info on
      // the wire (we don't pass anchor_ts in the result yet), we
      // park them at the lane's start with a "?" marker.
      const pos = offsetMs !== null ? Math.max(0, Math.min(100, (offsetMs / totalMs) * 100)) : null;
      const pass = ph.pass;
      const marker = document.createElement("div");
      const isState = !!def.expected_state;
      const baseColor = isState
        ? (def.expected_state === "working" ? "#8b5cf6"
           : def.expected_state === "waiting" ? "#f59e0b"
           : "#4ade80") // ready
        : "#3b82f6"; // lifecycle kind (blue)
      const rimColor = pass ? "#22c55e" : "#dc2626";
      if (pos === null) {
        // Failed AND unmatched — pin to left edge with a "?" so the
        // operator notices something is wrong but isn't misled into
        // thinking it's at offset 0.
        marker.style.cssText =
          `position: absolute; left: 2px; top: 1px; ` +
          `width: 12px; height: 12px; ` +
          `background: ${rimColor}; color: white; ` +
          `border-radius: 50%; ` +
          `font-size: 9px; font-weight: 700; text-align: center; line-height: 12px; ` +
          `cursor: help;`;
        marker.textContent = "?";
      } else if (isState) {
        marker.style.cssText =
          `position: absolute; left: ${pos}%; top: 2px; ` +
          `width: 10px; height: 10px; transform: translateX(-5px); ` +
          `background: ${baseColor}; ` +
          `border: 2px solid ${rimColor}; ` +
          `border-radius: 50%; ` +
          `cursor: help;`;
      } else {
        // Lifecycle marker — rectangular tag with the kind's first 2 chars.
        const label = (def.kind || "").slice(0, 3).toUpperCase();
        marker.style.cssText =
          `position: absolute; left: ${pos}%; top: 1px; ` +
          `padding: 0 3px; height: 12px; line-height: 12px; ` +
          `transform: translateX(-50%); ` +
          `background: ${baseColor}; color: white; ` +
          `border: 1.5px solid ${rimColor}; ` +
          `border-radius: 3px; ` +
          `font-size: 9px; font-weight: 700; ` +
          `cursor: help;`;
        marker.textContent = label;
      }
      const lines = [`${ph.phase} — ${pass ? "PASS" : "FAIL"}`];
      if (def.text) lines.push(def.text);
      if (offsetMs !== null) {
        let delta = `+${Math.round(offsetMs)} ms from recording start`;
        if (def.max_delay_ms) delta += ` (target ≤ ${def.max_delay_ms} ms from anchor)`;
        lines.push(delta);
      }
      if (ph.reason) lines.push(`reason: ${ph.reason}`);
      marker.setAttribute("data-tip", lines.join("\n"));
      expectedLane.appendChild(marker);
    }
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
    const resp = await fetch("/api/replay/start", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({agent: s.agent, subtree: s.subtree, scenario: s.id, speed: currentSpeed}),
    });
    if (!resp.ok) {
      alert(`start failed: ${resp.status} ${await resp.text()}`);
      return;
    }
    const body = await resp.json();
    totalMs = body.total_ms || 0;
    events = Array.isArray(body.events) ? body.events : [];
    turns = Array.isArray(body.turns) ? body.turns : [];
    // Deduplicate offsets so a cluster of same-instant events doesn't
    // ping the prev/next buttons multiple times in one click.
    eventOffsets = [...new Set(events.map(e => e.offset_ms))].sort((a, b) => a - b);
    // Append a cache-buster so re-clicking Play actually reloads the
    // dashboard inside the iframe (setting iframe.src to the same URL is
    // a no-op in browsers — the WebSocket inside stays open with stale
    // state). The query param is harmless to the dashboard's relative
    // fetches.
    const url = body.dashboard_url;
    iframe.src = url + (url.includes("?") ? "&" : "?") + "pb=" + body.playback_id;
    iframeWrap.style.display = "block";
    iframeLabel.textContent = `${body.dashboard_url}` +
      (totalMs ? ` — total ${(totalMs/1000).toFixed(1)}s` : "") +
      (events.length ? ` — ${events.length} events` : "");
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
      const resp = await fetch("/api/replay/status");
      const st = await resp.json();
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
    await fetch(isResume ? "/api/replay/resume" : "/api/replay/pause", {method: "POST"});
    updateStatus();
  };
  btnStop.onclick = async () => {
    await fetch("/api/replay/stop", {method: "POST"});
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
    await fetch(`/api/replay/seek?offset_ms=${target}`, {method: "POST"});
    // Snap the scrubber immediately so the next poll doesn't visually
    // bounce back.
    scrub.value = String(target);
  };
  btnNext.onclick = async () => {
    if (eventOffsets.length === 0) return;
    const cur = Number(scrub.value) || 0;
    const target = findOffsetAfter(eventOffsets, cur + 1);
    if (target == null) return;
    await fetch(`/api/replay/seek?offset_ms=${target}`, {method: "POST"});
    scrub.value = String(target);
  };
  scrub.oninput = async () => {
    await fetch(`/api/replay/seek?offset_ms=${scrub.value}`, {method: "POST"});
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

function renderSignalsPreview(data) {
  const p = panel("Signals preview (first 50)");
  if (!data.signals || data.signals.length === 0) {
    p.appendChild(text("No signals.jsonl — this recording predates the multi-sensor recorder."));
    return p;
  }
  const tbl = document.createElement("table");
  tbl.innerHTML = `<tr><th>ts</th><th>sensor</th><th>kind</th><th>payload preview</th></tr>`;
  for (let i = 0; i < Math.min(50, data.signals.length); i++) {
    const sRaw = data.signals[i];
    let s;
    try { s = typeof sRaw === "string" ? JSON.parse(sRaw) : sRaw; } catch { continue; }
    const tr = document.createElement("tr");
    const payload = typeof s.payload === "string" ? s.payload : JSON.stringify(s.payload || {});
    tr.innerHTML = `
      <td>${escapeHtml((s.ts || "").substring(11, 23))}</td>
      <td><code>${escapeHtml(s.sensor)}</code></td>
      <td><code>${escapeHtml(s.kind)}</code></td>
      <td><code>${escapeHtml(payload).substring(0, 120)}</code></td>`;
    tbl.appendChild(tr);
  }
  p.appendChild(tbl);
  return p;
}

function panel(title) {
  const p = document.createElement("div");
  p.className = "panel";
  const h = document.createElement("h3");
  h.textContent = title;
  p.appendChild(h);
  return p;
}
function panelFiller() {
  const p = document.createElement("div");
  p.style.display = "none";
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

// findOffsetBefore returns the greatest offset < `cur`, or null if none.
// Linear scan is fine — typical scenarios have <100 events.
function findOffsetBefore(sorted, cur) {
  let best = null;
  for (const v of sorted) {
    if (v < cur) best = v;
    else break;
  }
  return best;
}

// findOffsetAfter returns the smallest offset > `cur`, or null if none.
function findOffsetAfter(sorted, cur) {
  for (const v of sorted) {
    if (v > cur) return v;
  }
  return null;
}
