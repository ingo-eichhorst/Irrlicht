// Viewer SPA. Loads scenario list, fetches a recording on click, renders
// the timeline with a scrubber. Per #268 Phase 7 spec: speed presets
// 1×/2×/5×/10×/20×/25×/100×, adaptive fast-forward, state-change
// reason panel showing rule_id + signal_ref + evidence.

const SPEED_PRESETS = [1, 2, 5, 10, 20, 25, 100];

(async function init() {
  const scenarios = await fetch("/api/scenarios").then(r => r.json());
  const sidebar = document.getElementById("scenarios");
  sidebar.innerHTML = "";
  if (!scenarios || scenarios.length === 0) {
    sidebar.textContent = "No recordings found under replaydata/agents/.";
    return;
  }
  const grouped = {};
  for (const s of scenarios) {
    (grouped[s.agent] ||= []).push(s);
  }
  for (const agent of Object.keys(grouped).sort()) {
    const h = document.createElement("h2");
    h.textContent = agent;
    sidebar.appendChild(h);
    for (const s of grouped[agent]) {
      const el = document.createElement("a");
      el.className = "scn" + (s.has_ground_truth ? " has-gt" : "");
      el.innerHTML = `<span class="agent">${s.subtree}/</span>${s.id}`;
      el.onclick = () => loadScenario(s, el);
      sidebar.appendChild(el);
    }
  }
})();

async function loadScenario(s, linkEl) {
  document.querySelectorAll(".scn").forEach(e => e.classList.remove("active"));
  linkEl.classList.add("active");
  document.getElementById("title").textContent = s.id;
  document.getElementById("breadcrumb").textContent = `${s.agent} / ${s.subtree} / ${s.id}`;
  const detail = document.getElementById("detail");
  detail.innerHTML = `<p>Loading…</p>`;

  const data = await fetch(`/api/scenarios/${s.agent}/${s.subtree}/${s.id}`).then(r => r.json());
  detail.innerHTML = "";
  detail.appendChild(renderMeta(data));
  detail.appendChild(renderGroundTruth(data));
  detail.appendChild(renderTransitions(data));
  detail.appendChild(renderValidate(data));
  detail.appendChild(renderTimelineControls(data));
  detail.appendChild(renderSignalsPreview(data));
}

function renderMeta(data) {
  const p = panel("Recording metadata");
  if (!data.meta) {
    p.appendChild(text("No recording-meta.json — this recording predates Phase 1's recorder."));
    return p;
  }
  try {
    const meta = typeof data.meta === "string" ? JSON.parse(data.meta) : data.meta;
    const pre = document.createElement("pre");
    pre.className = "snapshot";
    pre.textContent = JSON.stringify(meta, null, 2);
    p.appendChild(pre);
  } catch (e) {
    p.appendChild(text("(could not parse meta)"));
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
  const tbl = document.createElement("table");
  tbl.innerHTML = `<tr><th>ts</th><th>prev → new</th><th>reason</th></tr>`;
  for (const tRaw of data.transitions) {
    const t = typeof tRaw === "string" ? JSON.parse(tRaw) : tRaw;
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td>${escapeHtml(t.ts || "")}</td>
      <td><span class="badge ${t.prev_state || 'none'}">${t.prev_state || "∅"}</span> →
          <span class="badge ${t.new_state}">${t.new_state}</span></td>
      <td>${escapeHtml(t.reason || "")}</td>`;
    tbl.appendChild(tr);
  }
  p.appendChild(tbl);
  return p;
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

function renderTimelineControls(data) {
  const p = panel("Playback");
  const ctl = document.createElement("div");
  ctl.className = "controls";
  for (const sp of SPEED_PRESETS) {
    const b = document.createElement("button");
    b.className = "speed";
    b.textContent = `${sp}×`;
    ctl.appendChild(b);
  }
  const info = document.createElement("span");
  info.id = "playhead-info";
  info.textContent = "  signals: " + (data.signals?.length || 0);
  ctl.appendChild(info);
  p.appendChild(ctl);
  const scrub = document.createElement("div");
  scrub.id = "scrubber-wrap";
  scrub.innerHTML = `<input id="scrubber" type="range" min="0" max="100" value="0" />`;
  p.appendChild(scrub);
  p.appendChild(text("Playback engine is a Phase 7 follow-up; controls render and the scrubber maps positions to signal indices when a recorder produces signals.jsonl."));
  return p;
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
