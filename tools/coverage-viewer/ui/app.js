// Coverage-viewer SPA: matrix → modals (drilldown / timeline / feature coverage).
// Vanilla JS, no framework.

const $ = (id) => document.getElementById(id);

let matrixData = null;
let featureIndex = {};

init();

async function init() {
  bindUI();
  try {
    matrixData = await fetchJSON("/api/matrix");
    featureIndex = Object.fromEntries(matrixData.features.map((f) => [f.id, f]));
    renderMatrix(matrixData);
    $("matrix-loading").hidden = true;
    $("matrix-wrap").hidden = false;
  } catch (err) {
    $("matrix-loading").innerHTML = `<div class="error">Failed to load matrix: ${escapeHtml(err.message)}</div>`;
  }
}

function bindUI() {
  // Close modal: button click, backdrop click, ESC.
  document.body.addEventListener("click", (e) => {
    const target = e.target.closest("[data-close]");
    if (target) {
      closeModal(target.dataset.close);
      return;
    }
    if (e.target.classList.contains("modal-backdrop")) {
      closeModal(e.target.id.replace(/-modal$/, ""));
    }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      // Close topmost open modal first (detail > features/timeline > drilldown).
      if (!$("timeline-detail").hidden) return closeModal("detail");
      for (const id of ["features", "timeline", "drilldown"]) {
        if (!$(`${id}-modal`).hidden) return closeModal(id);
      }
    }
  });
}

function openModal(name) {
  $(`${name}-modal`).hidden = false;
  document.body.style.overflow = "hidden";
}

function closeModal(name) {
  if (name === "detail") {
    $("timeline-detail").hidden = true;
    return;
  }
  $(`${name}-modal`).hidden = true;
  if (allModalsClosed()) document.body.style.overflow = "";
}

function allModalsClosed() {
  return ["drilldown", "timeline", "features"].every((n) => $(`${n}-modal`).hidden);
}

// ---------- matrix ----------

function renderMatrix(m) {
  const groups = groupByRequires(m.scenarios);
  const tbl = $("matrix");
  tbl.innerHTML = "";

  const head = tbl.appendChild(document.createElement("thead"));
  const headRow = head.appendChild(document.createElement("tr"));
  headRow.appendChild(th("Scenario"));
  for (const a of m.adapters) {
    const c = th(`${a}\n`);
    c.className = "adapter";
    c.innerHTML = `${escapeHtml(a)}<span class="hint">click for features</span>`;
    c.addEventListener("click", () => openFeatures(a));
    headRow.appendChild(c);
  }

  const body = tbl.appendChild(document.createElement("tbody"));
  for (const [reqKey, scenarios] of groups) {
    const groupRow = body.appendChild(document.createElement("tr"));
    groupRow.className = "group-head";
    const td = document.createElement("td");
    td.colSpan = m.adapters.length + 1;
    td.innerHTML =
      `<span>requires: ${escapeHtml(reqKey || "(none)")}</span>` +
      `<span class="req">${scenarios.length} scenario${scenarios.length === 1 ? "" : "s"}</span>`;
    groupRow.appendChild(td);

    for (const s of scenarios) {
      const row = body.appendChild(document.createElement("tr"));
      const sc = document.createElement("td");
      sc.className = "scenario";
      sc.innerHTML = `<div>${escapeHtml(s.name)}</div><div class="desc">${escapeHtml(s.description || "")}</div>`;
      row.appendChild(sc);
      for (const a of m.adapters) {
        const cellData = m.cells[a]?.[s.name] || { state: "n/a", reason: "" };
        const td = document.createElement("td");
        td.className = "cell";
        td.title = cellData.reason || "";
        td.innerHTML = `<span class="chip ${chipClass(cellData.state)}">${escapeHtml(cellData.state)}</span>`;
        td.addEventListener("click", () => openDrilldown(a, s.name));
        row.appendChild(td);
      }
    }
  }
}

function groupByRequires(scenarios) {
  const map = new Map();
  for (const s of scenarios) {
    const key = (s.requires || []).join(", ");
    if (!map.has(key)) map.set(key, []);
    map.get(key).push(s);
  }
  return map;
}

// ---------- feature coverage modal ----------

function openFeatures(adapter) {
  const body = $("features-body");
  const caps = matrixData.capabilities[adapter] || {};
  const cells = matrixData.cells[adapter] || {};
  const scenariosByName = Object.fromEntries(matrixData.scenarios.map((s) => [s.name, s]));

  // For each feature, find scenarios that require it AND their cell state for this adapter.
  const rows = matrixData.features.map((f) => {
    const cap = caps[f.id];
    const scenariosRequiring = matrixData.scenarios.filter((s) =>
      (s.requires || []).includes(f.id),
    );
    const coveringScenarios = scenariosRequiring.filter(
      (s) => (cells[s.name] || {}).state === "covered",
    );
    return {
      feature: f,
      cap,
      scenariosRequiring,
      coveringScenarios,
      coverageCount: coveringScenarios.length,
    };
  });

  const stats = {
    capTrue: rows.filter((r) => r.cap === true).length,
    capFalse: rows.filter((r) => r.cap === false).length,
    capUnknown: rows.filter((r) => r.cap === "unknown").length,
    capMissing: rows.filter((r) => r.cap === undefined).length,
    coveredAtLeastOnce: rows.filter((r) => r.coverageCount > 0).length,
    coveredMultiple: rows.filter((r) => r.coverageCount > 1).length,
    declaredButUntested: rows.filter((r) => r.cap === true && r.coverageCount === 0).length,
  };

  body.innerHTML = `
    <h2>${escapeHtml(adapter)} <span style="color:var(--text-dim);font-weight:400">— feature coverage</span></h2>
    <p class="crumb">Which features <strong>${escapeHtml(adapter)}</strong> declares, and which scenarios actually exercise them.</p>
    <div class="summary">
      <span class="stat"><strong>${stats.coveredAtLeastOnce}</strong>/${matrixData.features.length} features covered ≥ 1×</span>
      <span class="stat"><strong>${stats.coveredMultiple}</strong> covered multiple times</span>
      <span class="stat"><strong>${stats.declaredButUntested}</strong> declared but untested</span>
      <span class="stat"><strong>${stats.capTrue}</strong> capable · <strong>${stats.capUnknown + stats.capMissing}</strong> unknown · <strong>${stats.capFalse}</strong> not capable</span>
    </div>
    <table class="features">
      <thead><tr>
        <th>Feature</th>
        <th>Declared</th>
        <th>Scenarios testing it</th>
        <th>Times covered</th>
      </tr></thead>
      <tbody>
        ${rows.map((r) => renderFeatureRow(adapter, r, scenariosByName)).join("")}
      </tbody>
    </table>
  `;
  // Wire scenario links → drilldown modal (replaces features modal).
  body.querySelectorAll("[data-scenario]").forEach((el) => {
    el.addEventListener("click", () => {
      closeModal("features");
      openDrilldown(adapter, el.dataset.scenario);
    });
  });
  openModal("features");
}

function renderFeatureRow(adapter, r, scenariosByName) {
  const capChip = renderCapChip(r.cap);
  const scnLinks = r.scenariosRequiring.length === 0
    ? `<span style="color:var(--text-dim)">— no scenario requires this feature —</span>`
    : r.scenariosRequiring.map((s) => {
        const state = (matrixData.cells[adapter]?.[s.name] || {}).state || "n/a";
        return `<span class="scn-link ${chipClass(state)}" data-scenario="${escapeHtml(s.name)}" title="${escapeHtml(state)}">${escapeHtml(s.name)}</span>`;
      }).join("");
  const countCls = r.coverageCount === 0 ? "zero" : "covered";
  return `
    <tr class="${r.coverageCount === 0 ? "uncovered" : ""}">
      <td class="feature-id">
        ${escapeHtml(r.feature.id)}
        ${r.feature.description ? `<span class="desc">${escapeHtml(r.feature.description)}</span>` : ""}
      </td>
      <td class="cap">${capChip}</td>
      <td class="scenarios">${scnLinks}</td>
      <td class="count ${countCls}">${r.coverageCount}</td>
    </tr>
  `;
}

function renderCapChip(cap) {
  if (cap === true) return `<span class="chip cap-true">true</span>`;
  if (cap === false) return `<span class="chip cap-false">false</span>`;
  if (cap === "unknown") return `<span class="chip cap-unknown">unknown</span>`;
  return `<span class="chip cap-unknown">—</span>`;
}

// ---------- drilldown ----------

async function openDrilldown(adapter, scenario) {
  const dd = $("drilldown");
  dd.innerHTML = `<p class="crumb">Loading ${escapeHtml(adapter)} / ${escapeHtml(scenario)}…</p>`;
  openModal("drilldown");
  try {
    const d = await fetchJSON(`/api/scenario/${adapter}/${scenario}`);
    renderDrilldown(d);
  } catch (err) {
    dd.innerHTML = `<div class="error">${escapeHtml(err.message)}</div>`;
  }
}

function renderDrilldown(d) {
  const dd = $("drilldown");
  const verifyHtml = renderVerify(d.verify);
  const meta = [];
  if (d.prompt) meta.push(`<dt>Prompt</dt><dd><pre>${escapeHtml(d.prompt)}</pre></dd>`);
  if (d.timeout_seconds) meta.push(`<dt>Timeout</dt><dd>${d.timeout_seconds}s</dd>`);
  if (d.requires?.length) meta.push(`<dt>Requires</dt><dd>${d.requires.map((r) => `<code>${escapeHtml(r)}</code>`).join(" ")}</dd>`);
  if (d.settings && Object.keys(d.settings).length > 0) {
    meta.push(`<dt>Settings</dt><dd><pre>${escapeHtml(JSON.stringify(d.settings, null, 2))}</pre></dd>`);
  }
  if (d.reason) meta.push(`<dt>Reason</dt><dd>${escapeHtml(d.reason)}</dd>`);

  dd.innerHTML = `
    <h2>${escapeHtml(d.adapter)} <span style="color:var(--text-dim)">/</span> ${escapeHtml(d.scenario)} <span class="chip ${chipClass(d.state)}">${escapeHtml(d.state)}</span></h2>
    <p class="crumb">${escapeHtml(d.description || "")}</p>
    <dl class="meta">${meta.join("")}</dl>
    <div class="steps">
      ${d.steps.map((s, i) => `
        <div class="step">
          <h4><span class="num">${i + 1}</span> ${escapeHtml(s.title)}</h4>
          <p>${escapeHtml(s.description)}</p>
          <a href="${escapeHtml(s.link)}" target="_blank" rel="noopener">${escapeHtml(s.link.replace(/^https:\/\/github\.com\/[^/]+\/[^/]+\/blob\/[^/]+\//, ""))}</a>
        </div>
      `).join("")}
    </div>
    ${verifyHtml}
    <button class="timeline-btn" id="show-timeline" ${d.has_fixture ? "" : "disabled"}>${d.has_fixture ? "View timeline →" : "No committed fixture"}</button>
  `;
  if (d.has_fixture) {
    $("show-timeline").addEventListener("click", () => openTimeline(d.adapter, d.scenario));
  }
}

function renderVerify(verify) {
  if (!verify || Object.keys(verify).length === 0) return "";
  const items = Object.entries(verify).map(([k, v]) => {
    return `<li><code>${escapeHtml(k)}</code> = ${escapeHtml(JSON.stringify(v))}</li>`;
  });
  return `<div class="verify"><h4>Verify block</h4><ul>${items.join("")}</ul></div>`;
}

// ---------- timeline ----------

const LANES = [
  ["driver", "Driver"],
  ["agent", "Agent"],
  ["tool_result", "Tool result"],
  ["hook", "Hook"],
  ["daemon", "Daemon state"],
  ["subagent", "Subagent"],
];

async function openTimeline(adapter, scenario) {
  $("timeline-meta").innerHTML = `Loading timeline for <strong>${escapeHtml(adapter)} / ${escapeHtml(scenario)}</strong>…`;
  $("timeline").innerHTML = "";
  openModal("timeline");
  try {
    const t = await fetchJSON(`/api/timeline/${adapter}/${scenario}`);
    renderTimeline(t);
  } catch (err) {
    $("timeline").innerHTML = `<div class="error">${escapeHtml(err.message)}</div>`;
  }
}

function renderTimeline(t) {
  const meta = [`<strong>${escapeHtml(t.adapter)} / ${escapeHtml(t.scenario)}</strong> · ${t.entries.length} events`];
  if (t.note) meta.push(`<span class="note">${escapeHtml(t.note)}</span>`);
  $("timeline-meta").innerHTML = meta.join(" ");

  const tl = $("timeline");
  tl.innerHTML = "";

  const byLane = Object.fromEntries(LANES.map(([k]) => [k, []]));
  for (const e of t.entries) {
    if (!byLane[e.lane]) byLane[e.lane] = [];
    byLane[e.lane].push(e);
  }

  const subsByParent = {};
  for (const e of byLane.subagent || []) {
    const key = e.parent_id || "_root";
    if (!subsByParent[key]) subsByParent[key] = [];
    subsByParent[key].push(e);
  }

  for (const [laneKey, laneLabel] of LANES) {
    if (laneKey === "subagent") continue;
    const label = document.createElement("div");
    label.className = "lane-label";
    label.textContent = laneLabel;
    tl.appendChild(label);

    const row = document.createElement("div");
    row.className = "lane-row lane-" + laneKey;
    for (const e of byLane[laneKey] || []) row.appendChild(blockEl(e));
    tl.appendChild(row);
  }

  for (const [parentSid, children] of Object.entries(subsByParent)) {
    const label = document.createElement("div");
    label.className = "lane-label";
    label.textContent = parentSid === "_root" ? "Subagent" : "↳ subagent";
    label.title = parentSid;
    tl.appendChild(label);

    const row = document.createElement("div");
    row.className = "lane-row lane-subagent";
    for (const e of children) row.appendChild(blockEl(e));
    tl.appendChild(row);
  }
}

function blockEl(e) {
  const b = document.createElement("div");
  b.className = `tl-block kind-${cssToken(e.kind)}`;
  b.innerHTML = `<div class="ts">${formatTime(e.ts)}</div><div class="title">${escapeHtml(e.title || e.kind)}</div>`;
  b.addEventListener("click", () => showDetail(e));
  return b;
}

function showDetail(e) {
  $("timeline-detail-body").textContent = JSON.stringify(e, null, 2);
  $("timeline-detail").hidden = false;
}

// ---------- helpers ----------

async function fetchJSON(url) {
  const r = await fetch(url);
  if (!r.ok) throw new Error(`${url} → ${r.status} ${await r.text()}`);
  return r.json();
}

function chipClass(state) {
  if (state === "n/a") return "n-a";
  return state.replace(/[^a-z0-9]+/g, "-");
}

function cssToken(s) {
  return (s || "").replace(/[^a-z0-9_]+/g, "_");
}

function escapeHtml(s) {
  return String(s ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function formatTime(ts) {
  if (!ts) return "—";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "—";
  return d.toISOString().slice(11, 23);
}

function th(text) {
  const el = document.createElement("th");
  el.textContent = text;
  return el;
}
