// Viewer SPA. Loads scenario list, fetches a recording on click, renders
// the timeline with a scrubber. Per #268 Phase 7 spec: speed presets
// 1×/2×/5×/10×/20×/25×/100×, adaptive fast-forward, state-change
// reason panel showing rule_id + signal_ref + evidence.

const SPEED_PRESETS = [1, 2, 5, 10, 20, 25, 100];

// Module-level handles populated during init() and reused by the
// Overview button + scenario clicks to swap views in the main pane.
let scenariosList = [];   // live recordings from /api/scenarios
let catalog = null;       // canonical scenarios.json contents

(async function init() {
  const [scenarios, cat] = await Promise.all([
    fetch("/api/scenarios").then(r => r.json()),
    fetch("/api/catalog").then(r => r.ok ? r.json() : null).catch(() => null),
  ]);
  scenariosList = scenarios || [];
  catalog = cat;
  const sidebar = document.getElementById("scenarios");
  sidebar.innerHTML = "";

  // Overview button — always present, renders the catalog × recordings
  // matrix in the main pane. Reads from /api/catalog (which serves
  // scenarios.json verbatim) so the maintainer's edits to that file
  // show up on next refresh without a viewer rebuild.
  const overviewBtn = document.createElement("button");
  overviewBtn.className = "scn overview-btn";
  overviewBtn.textContent = "📊 Overview";
  overviewBtn.addEventListener("click", () => loadOverview(overviewBtn));
  sidebar.appendChild(overviewBtn);

  if (!scenarios || scenarios.length === 0) {
    const note = document.createElement("div");
    note.style.cssText = "padding: 8px; font-size: 12px; color: #888;";
    note.textContent = "No recordings found under replaydata/agents/.";
    sidebar.appendChild(note);
    // Still allow overview-only navigation against the catalog.
    loadOverview(overviewBtn);
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
        // accessibility tools, Chrome MCP).
        const el = document.createElement("button");
        el.className = "scn" + (s.has_ground_truth ? " has-gt" : "");
        el.textContent = s.id;  // subtree is implied by the section header
        el.addEventListener("click", () => loadScenario(s, el));
        sidebar.appendChild(el);
      }
    }
  }
  // Land on Overview by default — the matrix tells the user at a glance
  // what's covered and what's missing before they pick a recording.
  loadOverview(overviewBtn);
})();

// loadOverview swaps the main pane to a coverage matrix built from
// the canonical catalog (scenarios.json) joined against the live
// recording list. Cells:
//   ✓  scenario defines this adapter AND a recording exists
//   ○  scenario defines this adapter BUT no recording yet
//   —  scenario does not declare this adapter (N/A by design)
// Clicking ✓ jumps to that recording. Hovering shows the requires/
// description from the catalog so the maintainer can see why a cell
// is N/A without opening the file.
function loadOverview(btnEl) {
  document.querySelectorAll(".scn").forEach(e => e.classList.remove("active"));
  if (btnEl) btnEl.classList.add("active");
  document.getElementById("title").textContent = "Scenario coverage";
  document.getElementById("breadcrumb").textContent =
    catalog ? "from .claude/skills/ir:onboard-agent/scenarios.json — refresh to pick up edits" : "catalog unavailable";
  const detail = document.getElementById("detail");
  detail.innerHTML = "";

  if (!catalog || !Array.isArray(catalog.scenarios)) {
    const p = document.createElement("p");
    p.textContent = "Catalog not loaded — /api/catalog returned no scenarios array.";
    detail.appendChild(p);
    return;
  }

  // Union of all adapters declared anywhere in by_adapter, sorted.
  const adapterSet = new Set();
  for (const sc of catalog.scenarios) {
    for (const a of Object.keys(sc.by_adapter || {})) adapterSet.add(a);
  }
  const adapters = [...adapterSet].sort();

  // Recording lookup: key "<agent>/<subtree>/<id>" → entry, so a click
  // on a ✓ cell can reuse the existing loadScenario flow.
  const recIndex = new Map();
  for (const r of scenariosList) {
    recIndex.set(`${r.agent}/scenarios/${r.id}`, r);
  }

  // --- Agent scenarios table ---
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
        cell.title = `${adapter}: not declared (capability mismatch or missing-prompt)`;
      } else {
        const rec = recIndex.get(`${adapter}/scenarios/${sc.name}`);
        if (rec) {
          const btn = document.createElement("button");
          btn.textContent = "✓";
          btn.title = `Open ${adapter}/${sc.name}`;
          btn.style.cssText = "background: transparent; border: 0; color: #2a8d4f; font-size: 16px; cursor: pointer; padding: 0;";
          btn.addEventListener("click", () => {
            // Find the sidebar button for this recording and reuse
            // the existing click path so active state syncs up.
            const sidebar = document.getElementById("scenarios");
            for (const el of sidebar.querySelectorAll(".scn")) {
              if (el.textContent === sc.name) {
                el.click();
                el.scrollIntoView({block: "nearest"});
                return;
              }
            }
            // Fallback if the recording exists but the sidebar button
            // wasn't found (different subtree, etc.) — load directly.
            loadScenario(rec, null);
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

  // Coverage summary chip strip
  const sum = document.createElement("div");
  sum.style.cssText = "margin-top: 8px; display: flex; gap: 14px; font-size: 11px; color: #555;";
  let recorded = 0, declared = 0;
  for (const sc of catalog.scenarios) {
    for (const adapter of adapters) {
      if (sc.by_adapter && sc.by_adapter[adapter]) {
        declared++;
        if (recIndex.has(`${adapter}/scenarios/${sc.name}`)) recorded++;
      }
    }
  }
  sum.innerHTML = `<span><b>${recorded}</b> / ${declared} cells recorded</span>
    <span><b>${catalog.scenarios.length}</b> scenarios × <b>${adapters.length}</b> adapters</span>`;
  panel.appendChild(sum);

  // --- Orchestrator scenarios (if present) ---
  const orch = catalog.orchestrator_scenarios;
  if (Array.isArray(orch) && orch.length > 0) {
    const orchPanel = document.createElement("div");
    orchPanel.className = "panel";
    const h = document.createElement("h3");
    h.textContent = `Orchestrator scenarios (${orch.length})`;
    orchPanel.appendChild(h);
    const list = document.createElement("ul");
    list.style.margin = "0";
    list.style.paddingLeft = "20px";
    for (const o of orch) {
      const li = document.createElement("li");
      li.style.marginBottom = "4px";
      const adapters = Object.keys(o.by_orchestrator || {}).join(", ");
      li.innerHTML = `<b>${o.name}</b> — ${o.description || ""} <span style="color: #888;">(${adapters})</span>`;
      list.appendChild(li);
    }
    orchPanel.appendChild(list);
    detail.appendChild(orchPanel);
  }
}

async function loadScenario(s, linkEl) {
  document.querySelectorAll(".scn").forEach(e => e.classList.remove("active"));
  linkEl.classList.add("active");
  document.getElementById("title").textContent = s.id;
  document.getElementById("breadcrumb").textContent = `${s.agent} / ${s.subtree} / ${s.id}`;
  const detail = document.getElementById("detail");
  detail.innerHTML = `<p>Loading…</p>`;

  const data = await fetch(`/api/scenarios/${s.agent}/${s.subtree}/${s.id}`).then(r => r.json());
  detail.innerHTML = "";
  detail.appendChild(renderPlayback(s));
  detail.appendChild(renderMeta(data));
  detail.appendChild(renderGroundTruth(data));
  detail.appendChild(renderTransitions(data));
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
    `<span class="badge ${finalState}">${escapeHtml(finalState)}</span> ` +
    `<span style="color:#888;">${transitions.length} transition${transitions.length === 1 ? '' : 's'}${dur ? ', ' + dur : ''}</span>`;
  card.appendChild(summary);

  const tbl = document.createElement("table");
  tbl.style.cssText = "margin: 0;";
  tbl.innerHTML = `<tr><th>ts</th><th>prev → new</th><th>reason</th></tr>`;
  for (const t of transitions) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td>${escapeHtml(t.ts || "")}</td>
      <td><span class="badge ${t.prev_state || 'none'}">${t.prev_state || "∅"}</span> →
          <span class="badge ${t.new_state}">${t.new_state}</span></td>
      <td>${escapeHtml(t.reason || "")}</td>`;
    tbl.appendChild(tr);
  }
  card.appendChild(tbl);
  return card;
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
function renderPlayback(s) {
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
    //    aliveUntil[sid] = offset of transcript_removed/presession_removed (or +∞ if still alive)
    //    transitions[sid] = sorted [{offset, state}, ...]
    const aliveFrom = {};
    const aliveUntil = {};
    const transitionsBySid = {};
    for (const e of events) {
      if (!e.session_id) continue;
      if (aliveFrom[e.session_id] === undefined) aliveFrom[e.session_id] = e.offset_ms;
      if (e.kind === "transcript_removed" || e.kind === "presession_removed") {
        aliveUntil[e.session_id] = e.offset_ms;
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
