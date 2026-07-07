// playbackTimeline.js — pure timeline computation for the Playback panel
// (#873, extracted from viewer.js's renderPlayback god-function). Every
// export here is a plain fold over the replay payload — no DOM, no fetch,
// no module state — so the timeline geometry (state-band aggregation,
// event-dot/turn/expected-lane positioning, prev/next seeking) is unit
// tested in isolation (playbackTimeline.test.js). The DOM painting that
// consumes these models lives in playbackView.js.

// State colors. Consolidated to 3 high-signal colors that match what the
// dashboard uses for session badges, plus a neutral gap color.
export const STATE_COLOR = {
  ready:   "#4ade80", // green
  working: "#8b5cf6", // purple
  waiting: "#f59e0b", // amber
};

// STATE_PRIORITY drives the aggregation rule when multiple sessions are
// alive at the same moment (parent + subagents). The band shows the
// highest-priority state among active sessions, so the user sees
// "working" for the entire span any session is working — matching how the
// macOS app's state bar treats the whole run.
export const STATE_PRIORITY = {working: 3, waiting: 2, ready: 1};

// isPresession returns true for "proc-<PID>" session ids — irrlicht's
// convention for a sighting before any transcript file exists. A real
// session gets a UUID id once its transcript appears. Distinguishing the
// two matters for the tooltip language: a transcript_removed on a
// pre-session is a *handoff* (the UUID transcript took over), not an
// ended conversation.
export function isPresession(sid) {
  return typeof sid === "string" && sid.startsWith("proc-");
}

// eventStyle classifies an event into a (color, size, label) triple.
// Takes the WHOLE event so it can disambiguate by session id — e.g.
// pid_discovered on proc-* (initial PID sighting) vs. pid_discovered on a
// UUID (the same PID being re-bound to the upgraded session). Salience
// tiers — sized so the cursor lands them easily in the 18px lane:
//
//   lifecycle   (14px blue/gray)  — session/presession appear or vanish
//   process     (14px green)      — process identity confirmed / parent linked
//   transition  (14px purple)     — state_transition (overlays the state band)
//   activity    (10px violet)     — transcript_activity (every transcript line)
//   bookkeeping (7px slate, 60%)  — debounce_coalesced, hook_received, file_event
//
// Tooltip text is plain English — no raw event_kind strings — so the user
// doesn't need to memorize colors.
export function eventStyle(ev) {
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

// computeStateBand folds the event list into the aggregate state-band
// segments, each already positioned (leftPct/widthPct), colored, and
// carrying its tooltip text. Returns [] when there's no duration to lay
// out against. Pure.
export function computeStateBand(events, totalMs) {
  if (!totalMs) return [];

  // 1. Build per-session timelines.
  //    aliveFrom[sid] = offset of first event for the session
  //    aliveUntil[sid] = offset of the EARLIEST session-end signal
  //                     (process_exited, transcript_removed, or
  //                     presession_removed). For SIGKILL/crash
  //                     scenarios, process_exited fires first and
  //                     transcript_removed may not appear at all, so
  //                     process_exited has to count.
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
      transitionsBySid[e.session_id] = transitionsBySid[e.session_id] || [];
      transitionsBySid[e.session_id].push({offset: e.offset_ms, state: e.new_state});
    }
  }
  for (const sid of Object.keys(transitionsBySid)) {
    transitionsBySid[sid].sort((a, b) => a.offset - b.offset);
  }

  // 2. Collect every distinct boundary offset. The aggregate state is
  //    constant between consecutive boundaries, so segments are bound by
  //    these points.
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
  const sessionStateAt = (sid, t) => {
    const list = transitionsBySid[sid] || [];
    let st = "ready";
    for (const trans of list) {
      if (trans.offset <= t) st = trans.state;
      else break;
    }
    return st;
  };

  const aggregateAt = (t) => {
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
  };

  // 4. Walk boundaries and emit one segment per change. Coalescing here
  //    means the user sees a single working span instead of many short
  //    ones when subagents finish in sequence under a still-working parent.
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

  // 5. Attach display attributes (position/color/tooltip) so the painter
  //    stays a trivial loop.
  return segments.map(seg => ({
    start: seg.start,
    end: seg.end,
    state: seg.state,
    color: STATE_COLOR[seg.state] || "#cfcdc0",
    leftPct: (seg.start / totalMs) * 100,
    widthPct: ((seg.end - seg.start) / totalMs) * 100,
    tip: `${seg.state}\n+${(seg.start / 1000).toFixed(2)}s → +${(seg.end / 1000).toFixed(2)}s (${((seg.end - seg.start) / 1000).toFixed(2)}s)`,
  }));
}

// computeEventDots positions every event as a dot on the event lane,
// carrying the style tier (color/size/opacity) from eventStyle and the
// hover tooltip text. Returns [] when there's no duration. Pure.
export function computeEventDots(events, totalMs) {
  if (!totalMs) return [];
  return events.map(ev => {
    const st = eventStyle(ev);
    const leftPct = Math.max(0, Math.min(100, (ev.offset_ms / totalMs) * 100));
    const lines = [st.label, `+${(ev.offset_ms / 1000).toFixed(2)}s`];
    if (ev.session_id && ev.kind !== "debounce_coalesced" && ev.kind !== "file_event") {
      lines.push(`session: ${ev.session_id}`);
    }
    return {leftPct, size: st.size, color: st.color, opacity: st.opacity, tip: lines.join("\n")};
  });
}

// computeTurns positions one tick per transcript turn. User ticks anchor
// to the top of the lane, assistant ticks to the bottom, so same-instant
// pairs stack without overlap. Returns [] when there's no duration or no
// turns. Pure.
export function computeTurns(turns, totalMs) {
  if (!totalMs || !turns?.length) return [];
  return turns.map(t => {
    const leftPct = Math.max(0, Math.min(100, (t.offset_ms / totalMs) * 100));
    const isUser = t.role === "user";
    return {
      leftPct,
      color: isUser ? "#2563eb" : "#0d9488",
      top: isUser ? "1px" : "11px",
      tip: `${isUser ? "User" : "Assistant"}\n+${(t.offset_ms / 1000).toFixed(2)}s\n${t.text || ""}`,
    };
  });
}

// computeExpectedLane models one marker per spec-grounded phase from the
// validator report. Positions come from each phase's matched_ts (passed)
// or are left null (failed/unmatched, pinned to the lane's start). Color
// encodes pass/fail; `type` encodes the marker shape the painter draws
// (unmatched "?" / state circle / lifecycle tag). Returns:
//   - null                       when there's no duration to lay out against
//   - {note, markers: []}        when no expected.jsonl is configured
//   - {note: null, markers}      otherwise
// Pure (Date.parse is deterministic given the timestamps on the wire).
export function computeExpectedLane(rep, totalMs) {
  if (!totalMs) return null;
  if (!rep || !Array.isArray(rep.phases) || rep.phases.length === 0) {
    return {note: "expected: not configured", markers: []};
  }
  // The validator anchors matched_ts to events[0].Ts (the recording's
  // first event) and exposes it as rep.recording_start. Use that to
  // convert each phase's absolute matched_ts into an offset_ms compatible
  // with the EventMarker positions on the scrubber.
  const startMs = rep.recording_start ? Date.parse(rep.recording_start) : Number.NaN;
  const defs = Array.isArray(rep.definitions) ? rep.definitions : [];
  const markers = [];
  for (let i = 0; i < rep.phases.length; i++) {
    const ph = rep.phases[i];
    const def = defs[i] || {};
    const matchedMs = ph.matched_ts ? Date.parse(ph.matched_ts) : Number.NaN;
    const offsetMs = Number.isFinite(matchedMs) && Number.isFinite(startMs)
      ? matchedMs - startMs
      : null;
    // Failed phases without a match still need positioning — they anchor
    // at the validator's "should-have-been-here" point. Without anchor
    // info on the wire we park them at the lane's start with a "?" marker.
    const pos = offsetMs !== null ? Math.max(0, Math.min(100, (offsetMs / totalMs) * 100)) : null;
    const pass = ph.pass;
    const isState = !!def.expected_state;
    const baseColor = isState
      ? (def.expected_state === "working" ? "#8b5cf6"
         : def.expected_state === "waiting" ? "#f59e0b"
         : "#4ade80") // ready
      : "#3b82f6"; // lifecycle kind (blue)
    const rimColor = pass ? "#22c55e" : "#dc2626";
    const type = pos === null ? "unmatched" : (isState ? "state" : "lifecycle");
    const label = (def.kind || "").slice(0, 3).toUpperCase();
    const lines = [`${ph.phase} — ${pass ? "PASS" : "FAIL"}`];
    if (def.text) lines.push(def.text);
    if (offsetMs !== null) {
      let delta = `+${Math.round(offsetMs)} ms from recording start`;
      if (def.max_delay_ms) delta += ` (target ≤ ${def.max_delay_ms} ms from anchor)`;
      lines.push(delta);
    }
    if (ph.reason) lines.push(`reason: ${ph.reason}`);
    markers.push({type, pos, baseColor, rimColor, label, tip: lines.join("\n")});
  }
  return {note: null, markers};
}

// deriveEventOffsets returns the sorted, de-duplicated offset_ms values so
// a cluster of same-instant events doesn't ping the prev/next buttons
// multiple times in one click. Pure.
export function deriveEventOffsets(events) {
  return [...new Set(events.map(e => e.offset_ms))].sort((a, b) => a - b);
}

// findOffsetBefore returns the greatest offset < `cur`, or null if none.
// Linear scan is fine — typical scenarios have <100 events.
export function findOffsetBefore(sorted, cur) {
  let best = null;
  for (const v of sorted) {
    if (v < cur) best = v;
    else break;
  }
  return best;
}

// findOffsetAfter returns the smallest offset > `cur`, or null if none.
export function findOffsetAfter(sorted, cur) {
  for (const v of sorted) {
    if (v > cur) return v;
  }
  return null;
}

// resolveDashboardIframeUrl validates a server-provided dashboard_url before
// it's assigned to an iframe's src. Rejects anything that isn't an http(s)
// URL on the viewer's own origin (e.g. a `javascript:` scheme, or a URL
// pointing at a third-party host) by returning null. On success, appends
// `pb=<playbackId>` as a cache-buster so re-clicking Play reloads the
// dashboard even though setting iframe.src to an unchanged URL is normally
// a no-op. Pure.
export function resolveDashboardIframeUrl(dashboardUrl, playbackId, origin) {
  if (typeof dashboardUrl !== "string" || dashboardUrl.trim() === "") return null;
  let parsed;
  try {
    parsed = new URL(dashboardUrl, origin);
  } catch {
    return null;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return null;
  if (parsed.origin !== origin) return null;
  parsed.searchParams.set("pb", playbackId);
  return parsed.toString();
}
