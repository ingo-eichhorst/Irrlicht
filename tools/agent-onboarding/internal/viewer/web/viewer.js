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

  // Mode toggle.
  const modeWrap = document.createElement("div");
  modeWrap.className = "controls";
  modeWrap.style.marginBottom = "8px";
  modeWrap.innerHTML = `
    <strong>Mode:</strong>
    <label><input type="radio" name="mode" value="viewer-internal" checked> Viewer-internal (in this tab)</label>
    <label><input type="radio" name="mode" value="isolated-daemon"> Isolated subprocess (separate port)</label>
  `;
  p.appendChild(modeWrap);

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

  // Scrubber + event-marker overlay + offset readout.
  const scrubWrap = document.createElement("div");
  scrubWrap.style.cssText = "margin-top: 8px; position: relative;";
  // The marker lane sits ON TOP of the slider track. Each tick is an
  // absolutely-positioned 2px-wide div colored by event kind. Tooltips
  // (title attribute) show kind + session_id on hover.
  const markerLane = document.createElement("div");
  markerLane.style.cssText = "position: absolute; left: 0; right: 0; top: 4px; height: 14px; pointer-events: none;";
  const scrub = document.createElement("input");
  scrub.type = "range";
  scrub.min = "0";
  scrub.max = "100";
  scrub.value = "0";
  scrub.style.cssText = "width: 100%; position: relative; z-index: 2;";
  scrub.disabled = true;
  scrubWrap.appendChild(markerLane);
  scrubWrap.appendChild(scrub);
  const offsetReadout = document.createElement("div");
  offsetReadout.id = "playhead-info";
  offsetReadout.textContent = "—";
  scrubWrap.appendChild(offsetReadout);
  p.appendChild(scrubWrap);

  // Local state shared by the prev/next buttons and the marker renderer.
  let eventOffsets = []; // sorted, dedup'd offset_ms values
  let events = [];       // raw EventMarker list from /api/replay/start

  // colorForKind maps a lifecycle.Event kind to a tick color. The
  // common high-signal kinds (state_transition, transcript_new/removed,
  // presession_*) get distinct hues; everything else is a neutral gray.
  function colorForKind(kind, newState) {
    if (kind === "state_transition") {
      if (newState === "working") return "#d97757"; // orange
      if (newState === "waiting") return "#a04545"; // red
      return "#2a8d4f";                              // green (ready / unknown)
    }
    if (kind === "transcript_new" || kind === "presession_created") return "#4a6fa5";
    if (kind === "transcript_removed" || kind === "presession_removed") return "#777";
    if (kind === "pid_discovered") return "#6b6";
    return "#bbb";
  }

  function renderMarkers() {
    markerLane.innerHTML = "";
    if (!totalMs || events.length === 0) return;
    for (const ev of events) {
      const pct = Math.max(0, Math.min(100, (ev.offset_ms / totalMs) * 100));
      const tick = document.createElement("div");
      tick.style.cssText = `position: absolute; left: ${pct}%; top: 0; width: 2px; height: 14px; ` +
        `background: ${colorForKind(ev.kind, ev.new_state)}; transform: translateX(-1px);`;
      tick.title = `${ev.kind}${ev.new_state ? " → " + ev.new_state : ""}\n+${(ev.offset_ms/1000).toFixed(2)}s` +
        (ev.session_id ? `\nsession: ${ev.session_id}` : "");
      markerLane.appendChild(tick);
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
    const mode = document.querySelector('input[name="mode"]:checked').value;
    const resp = await fetch("/api/replay/start", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({agent: s.agent, subtree: s.subtree, scenario: s.id, mode, speed: currentSpeed}),
    });
    if (!resp.ok) {
      alert(`start failed: ${resp.status} ${await resp.text()}`);
      return;
    }
    const body = await resp.json();
    totalMs = body.total_ms || 0;
    events = Array.isArray(body.events) ? body.events : [];
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
    iframeLabel.textContent = `${body.mode} — ${body.dashboard_url}` + (totalMs ? ` — total ${(totalMs/1000).toFixed(1)}s` : "") +
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
    markerLane.innerHTML = "";
    events = [];
    eventOffsets = [];
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
