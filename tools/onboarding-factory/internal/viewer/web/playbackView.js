// playbackView.js — DOM painters for the Playback panel's timeline lanes
// (#873, extracted from viewer.js's renderPlayback god-function). Each
// painter clears the passed-in lane element and repaints it from the pure
// model computed in playbackTimeline.js. No module state and no fetch: the
// geometry decisions all live in the timeline module (and are unit tested
// there); this file is only the compute-model → DOM-node translation.

import {
  computeStateBand, computeEventDots, computeTurns, computeExpectedLane,
} from './playbackTimeline.js';

// paintStateBand repaints the colored working/waiting/ready regions.
export function paintStateBand(el, events, totalMs) {
  el.innerHTML = "";
  for (const seg of computeStateBand(events, totalMs)) {
    const region = document.createElement("div");
    region.style.cssText = `position: absolute; top: 0; bottom: 0; left: ${seg.leftPct}%; width: ${seg.widthPct}%; background: ${seg.color};`;
    region.setAttribute("data-tip", seg.tip);
    el.appendChild(region);
  }
}

// paintEventDots repaints the discrete event-dot lane.
export function paintEventDots(el, events, totalMs) {
  el.innerHTML = "";
  for (const d of computeEventDots(events, totalMs)) {
    const dot = document.createElement("div");
    dot.style.cssText = `position: absolute; left: ${d.leftPct}%; top: ${(18 - d.size) / 2}px; ` +
      `width: ${d.size}px; height: ${d.size}px; background: ${d.color}; opacity: ${d.opacity}; ` +
      `border-radius: 50%; transform: translateX(-${d.size / 2}px); ` +
      `border: 1.5px solid white; box-shadow: 0 0 0 1px rgba(0,0,0,0.1); cursor: help;`;
    dot.setAttribute("data-tip", d.tip);
    el.appendChild(dot);
  }
}

// paintTurns repaints one tick per transcript turn.
export function paintTurns(el, turns, totalMs) {
  el.innerHTML = "";
  for (const t of computeTurns(turns, totalMs)) {
    const tick = document.createElement("div");
    tick.style.cssText = `position: absolute; left: ${t.leftPct}%; top: ${t.top}; ` +
      `width: 5px; height: 10px; background: ${t.color}; transform: translateX(-2.5px); ` +
      `border-radius: 2px; cursor: help;`;
    tick.setAttribute("data-tip", t.tip);
    el.appendChild(tick);
  }
}

// paintExpectedLane repaints the spec-grounded expected-phase markers, or
// a thin grey hint when no expected.jsonl is configured (so the lane
// isn't mysteriously empty). Marker shape follows the model's `type`:
// "unmatched" → left-pinned "?" chip, "state" → circle, "lifecycle" →
// rectangular tag with the kind's first 3 chars.
export function paintExpectedLane(el, rep, totalMs) {
  el.innerHTML = "";
  const model = computeExpectedLane(rep, totalMs);
  if (!model) return;
  if (model.note) {
    const note = document.createElement("div");
    note.style.cssText = "position: absolute; left: 0; top: 0; font-size: 10px; color: #aaa; padding: 2px 4px;";
    note.textContent = model.note;
    el.appendChild(note);
    return;
  }
  for (const m of model.markers) {
    const marker = document.createElement("div");
    if (m.type === "unmatched") {
      // Failed AND unmatched — pin to left edge with a "?" so the operator
      // notices something is wrong but isn't misled into thinking it's at
      // offset 0.
      marker.style.cssText =
        `position: absolute; left: 2px; top: 1px; ` +
        `width: 12px; height: 12px; ` +
        `background: ${m.rimColor}; color: white; ` +
        `border-radius: 50%; ` +
        `font-size: 9px; font-weight: 700; text-align: center; line-height: 12px; ` +
        `cursor: help;`;
      marker.textContent = "?";
    } else if (m.type === "state") {
      marker.style.cssText =
        `position: absolute; left: ${m.pos}%; top: 2px; ` +
        `width: 10px; height: 10px; transform: translateX(-5px); ` +
        `background: ${m.baseColor}; ` +
        `border: 2px solid ${m.rimColor}; ` +
        `border-radius: 50%; ` +
        `cursor: help;`;
    } else {
      // Lifecycle marker — rectangular tag with the kind's first chars.
      marker.style.cssText =
        `position: absolute; left: ${m.pos}%; top: 1px; ` +
        `padding: 0 3px; height: 12px; line-height: 12px; ` +
        `transform: translateX(-50%); ` +
        `background: ${m.baseColor}; color: white; ` +
        `border: 1.5px solid ${m.rimColor}; ` +
        `border-radius: 3px; ` +
        `font-size: 9px; font-weight: 700; ` +
        `cursor: help;`;
      marker.textContent = m.label;
    }
    marker.setAttribute("data-tip", m.tip);
    el.appendChild(marker);
  }
}
