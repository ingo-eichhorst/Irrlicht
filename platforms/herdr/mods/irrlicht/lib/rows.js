// The panel's rows and status line as plain data, so the decisions main.js
// draws can be tested without Herdr's runtime.

import { gaugeOf } from './gauge.js';
import { matchAgents } from './match.js';

// cleanTitle drops what an agent puts before its title (a status glyph or
// spinner and the separator after it), as Herdr's own agents mod does.
function cleanTitle(s) {
  return (s ?? '').replace(/^[^\p{L}\p{N}\p{Ps}\p{Pi}]+/u, '').replace(/^\[ [!.] \] /, '').trim();
}

// rowLabel reads "space · title": the agent's own title, else its tab's name.
function rowLabel(a) {
  const detail = cleanTitle(a.title) || a.tab_name || '';
  return [a.space_name, detail].filter(Boolean).join(' · ');
}

// unmatchedTooltip says why a row has no gauge.
function unmatchedTooltip(a) {
  return String(a.address).includes('/')
    ? 'on another machine: Irrlicht reads this machine\'s sessions only'
    : 'no Irrlicht session matched';
}

// MATCHED_BY says, in the row's tooltip, how its numbers were found.
const MATCHED_BY = { pid: 'matched by pid', folder: 'matched by agent and folder' };

// panelRows lists Herdr's agents in Herdr's own order, each with the gauge of
// the session it was matched to. `sessions` is null until the daemon answered
// once; the rows still list every agent then, without gauges.
export function panelRows(agents, terminals, sessions) {
  const matches = sessions ? matchAgents(agents, terminals, sessions) : new Map();
  return agents.map((a) => {
    const m = matches.get(a.address);
    const gauge = gaugeOf(m?.session);
    const tooltip = m
      ? [gauge.detail, m.session.model, MATCHED_BY[m.via]].filter(Boolean).join(' · ')
      : unmatchedTooltip(a);
    return {
      address: a.address,
      label: rowLabel(a),
      status: a.status,
      program: a.program,
      focused: a.focused === true,
      gauge,
      tooltip,
    };
  });
}

// statusLine is the line under the header: which daemon the panel reads, or
// why it reads none. An unreachable daemon and a failure inside the panel are
// told apart, so a broken panel never passes for a stopped daemon.
//   state: { phase: 'starting' | 'ok' | 'unreachable' | 'stalled' | 'unexpected' | 'error',
//            where?: 'localhost:7837', message?: string }
export function statusLine(state) {
  switch (state?.phase) {
    case 'ok':
      return { text: 'Irrlicht at ' + state.where, tone: 'dim' };
    case 'unreachable':
      return { text: 'Irrlicht daemon not reachable at ' + state.where, tone: 'dim' };
    case 'stalled':
      return { text: 'Irrlicht daemon ' + (state.where ? 'at ' + state.where + ' ' : '') + 'is not answering', tone: 'dim' };
    case 'unexpected':
      return { text: 'Irrlicht at ' + state.where + ' sent an unexpected answer', tone: 'warn' };
    case 'error':
      return { text: 'Irrlicht panel error: ' + (state.message || 'unknown'), tone: 'warn' };
    default:
      return { text: 'Looking for the Irrlicht daemon…', tone: 'dim' };
  }
}

// signature is what the panel compares to skip a redraw after a poll: the
// status line as drawn and each row's gauge and tooltip. A session no Herdr
// agent matched changes nothing on screen, so its numbers moving does not
// redraw. Herdr's own AGENTS.md says render runs again whenever its live data
// (agents, terminals, focus, theme) changes, so the rows' other fields need no
// place here.
export function signature(state, rows) {
  const gauges = (rows || []).map((r) => [r.address, r.gauge, r.tooltip]);
  return JSON.stringify([statusLine(state), gauges]);
}

// focusAddress is the agent the mod command "focus-<n>" names: row n of the
// panel, counting from 1, which is agent n in Herdr's order. Null when the
// command is another one or the row does not exist.
export function focusAddress(command, agents) {
  const n = /^focus-(\d+)$/.exec(String(command));
  return (n && agents[Number(n[1]) - 1]?.address) || null;
}
