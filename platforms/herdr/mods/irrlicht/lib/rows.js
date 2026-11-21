// The panel's rows and status line as plain data, so the decisions main.js
// draws can be tested without Herdr's runtime. Each row is one Herdr agent;
// lib/layout.js groups and orders them.

import { gaugeOf } from './gauge.js';
import { isLocal, matchAgents } from './match.js';
import { rowNotes } from './notes.js';
import { STATE_LABEL, herdrState, irrlichtState } from './states.js';

// cleanTitle drops what an agent puts before its title (a status glyph or
// spinner and the separator after it), as Herdr's own agents mod does.
function cleanTitle(s) {
  return (s ?? '').replace(/^[^\p{L}\p{N}\p{Ps}\p{Pi}]+/u, '').replace(/^\[ [!.] \] /, '').trim();
}

// rowLabel is the agent's own title, else its tab's name. Its space is in the
// row's tooltip: under a project header it mostly repeats it.
function rowLabel(a) {
  return cleanTitle(a.title) || a.tab_name || '';
}

// shortModelName is the Swift row's model label, SessionState.shortModelName
// (SessionState.swift:1502-1516): the part after the last "/", without
// "claude-", and a trailing "-<digits>" read as a version, "opus-5-5" →
// "opus-5.5". (The web dashboard's shortModel differs; the Swift app is the
// one this panel follows.)
export function shortModelName(model) {
  const tail = String(model || '').split('/').pop();
  return tail.split('claude-').join('').replace(/-(\d+)$/, '.$1');
}

// unmatchedReason says, in the tooltip, why a row has no context line.
function unmatchedReason(a) {
  return isLocal(a)
    ? 'no Irrlicht session matched'
    : 'on another machine: Irrlicht reads this machine\'s sessions only';
}

// MATCHED_BY says, in the row's tooltip, how its numbers were found.
const MATCHED_BY = { pid: 'matched by pid', folder: 'matched by agent and folder' };

// baseRow is what every row has, matched or not: Herdr's side of it.
function baseRow(a) {
  const label = rowLabel(a);
  return {
    address: a.address,
    label,
    where: [a.space_name, label].filter(Boolean).join(' · '),
    program: a.program || null,
    focused: a.focused === true,
  };
}

// matchedRow draws the session: its state, model, gauge, subagent count and
// the lines under the row.
function matchedRow(a, match) {
  const f = match.session;
  const base = baseRow(a);
  const state = irrlichtState(f.state);
  const gauge = gaugeOf(f);
  const branch = f.branch ? 'branch ' + f.branch : '';
  return {
    ...base,
    matched: true,
    group: f.group,
    gastown: f.gastown,
    order: f.order,
    state,
    stateTip: STATE_LABEL[state],
    model: shortModelName(f.model),
    subagents: f.subagents,
    gauge,
    notes: rowNotes(f),
    tooltip: [base.where, STATE_LABEL[state], gauge.detail, f.model, branch, MATCHED_BY[match.via]].filter(Boolean).join(' · '),
  };
}

// unmatchedRow draws Herdr's own status in Irrlicht's palette (states.js
// herdrState) and nothing the daemon would have said.
function unmatchedRow(a) {
  const base = baseRow(a);
  const state = herdrState(a.status);
  const herdr = 'Herdr: ' + (a.status || 'no status');
  return {
    ...base,
    matched: false,
    state,
    stateTip: STATE_LABEL[state] + ' (' + herdr + ')',
    model: '',
    subagents: 0,
    gauge: gaugeOf(null),
    notes: [],
    tooltip: [base.where, herdr, unmatchedReason(a)].filter(Boolean).join(' · '),
  };
}

// panelRows is one row per Herdr agent, in Herdr's order, each matched to the
// session the daemon reports for it when there is one. `sessions` is null
// until the daemon answered once; every row is unmatched then.
export function panelRows(agents, terminals, sessions) {
  const matches = sessions ? matchAgents(agents, terminals, sessions) : new Map();
  return agents.map((a) => {
    const m = matches.get(a.address);
    return m ? matchedRow(a, m) : unmatchedRow(a);
  });
}

const STARTING = { text: 'Looking for the Irrlicht daemon…', tone: 'dim' };

const STATUS_LINES = new Map([
  ['ok', (s) => ({ text: 'Irrlicht at ' + s.where, tone: 'dim' })],
  ['unreachable', (s) => ({ text: 'Irrlicht daemon not reachable at ' + s.where, tone: 'dim' })],
  ['stalled', (s) => ({ text: 'Irrlicht daemon ' + (s.where ? 'at ' + s.where + ' ' : '') + 'is not answering', tone: 'dim' })],
  ['unexpected', (s) => ({ text: 'Irrlicht at ' + s.where + ' sent an unexpected answer', tone: 'warn' })],
  ['error', (s) => ({ text: 'Irrlicht panel error: ' + (s.message || 'unknown'), tone: 'warn' })],
]);

// statusLine is which daemon the panel reads, or why it reads none: the
// connection dot's tooltip, and the line under the header whenever the
// daemon is not answering. An unreachable daemon and a failure inside the
// panel are told apart, so a broken panel never passes for a stopped daemon.
//   state: { phase: 'starting' | 'ok' | 'unreachable' | 'stalled' | 'unexpected' | 'error',
//            where?: 'localhost:7837', message?: string }
export function statusLine(state) {
  const line = STATUS_LINES.get(state ? state.phase : undefined);
  return line ? line(state) : STARTING;
}

// signature is what the panel compares to skip a redraw after a poll: the
// status line and the whole layout as drawn (every group, its name, count and
// place, and every row in it with each field main.js draws). It is the drawn
// data itself rather than a list of fields, so a field drawn later cannot be
// left out of it. A session no Herdr agent matched is in no row, so its
// numbers moving does not redraw.
export function signature(state, layout) {
  return JSON.stringify([statusLine(state), layout ? layout.groups : []]);
}

// focusAddress is the agent the mod command "focus-<n>" names: row n of the
// panel as it is drawn (panelLayout's rows, which are numbered in display
// order), counting from 1. Null when the command is another one or the row
// does not exist.
export function focusAddress(command, rows) {
  const n = /^focus-(\d+)$/.exec(String(command));
  return (n && rows[Number(n[1]) - 1]?.address) || null;
}
