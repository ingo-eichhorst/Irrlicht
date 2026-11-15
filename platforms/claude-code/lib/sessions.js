// Session shaping: the /api/v1/sessions payload (sessionsResponse in
// core/cmd/irrlichd/handlers.go) turned into the rows the panel draws.
//
// The payload is a tree: `groups[]` (one per project), each with `agents[]`
// and, for an orchestrator, nested `groups[]`; each agent may carry
// `children[]` (subagents and background agents). Field names are the JSON
// tags of session.AgentGroup / session.Agent / session.SessionState /
// session.SessionMetrics in core/domain/session.

import { fmtDuration, formatPercent, formatTokens, formatUsd, oneLine, shortModel } from './format.js';

// The session-state vocabulary is declared once, in Go:
// session.CanonicalStates() (core/domain/session/session.go). It cannot be
// imported from JS, so it is restated here whole, on one line, where
// tools/state-vocabulary-lint.sh reads it as a complete enumeration and will
// flag it the day a fifth state makes it a partial one.
export const SESSION_STATES = ['working', 'waiting', 'ready', 'error'];

// The dashboard's state colours: the default (dark) palette's --<state>
// variables in platforms/web/irrlicht.css, one per canonical state.
const STATE_COLORS = { working: '#8B5CF6', waiting: '#FF9500', ready: '#34C759', error: '#FF3B30' };

// stateColor answers the colour a state's dot is drawn in. A state no build
// of this panel knows is drawn in the theme's neutral `inactive` colour: grey
// claims nothing, where any state's colour would claim something false.
export function stateColor(state) {
  return Object.hasOwn(STATE_COLORS, state) ? STATE_COLORS[state] : 'inactive';
}

// An empty adapter means Claude Code (SessionState.Adapter's doc comment,
// core/domain/session/session.go).
const DEFAULT_ADAPTER = 'claude-code';

export function adapterOf(session) {
  return session?.adapter || DEFAULT_ADAPTER;
}

// adapterTag is shown on rows of sessions that are not Claude Code's, so a
// mixed fleet reads at a glance: "[pi]", "[codex]".
export function adapterTag(session) {
  const adapter = adapterOf(session);
  return adapter === DEFAULT_ADAPTER ? '' : '[' + adapter + ']';
}

// metricsOf is a session's metrics, an empty object when it reports none.
function metricsOf(session) {
  return session?.metrics || {};
}

// modelOf prefers the transcript-derived metrics.model_name over the
// top-level model, as the dashboard does.
export function modelOf(session) {
  return metricsOf(session).model_name || session?.model || '';
}

// isHere is an exact id match and nothing else: the panel marks the session
// it runs in only when the daemon lists this Claude Code session's own id
// (for Claude Code, the transcript file's stem). No match, no marker: a guess
// (same cwd, newest session) would mark the wrong row as often as the right
// one once two sessions share a project.
export function isHere(session, hereSessionId) {
  return typeof hereSessionId === 'string' && hereSessionId !== '' &&
    session?.session_id === hereSessionId;
}

// The longest question or last-reply text a detail block shows; the daemon
// already caps last_assistant_text near this length.
export const DETAIL_TEXT_MAX = 200;

// sessionDetail is what pressing a row reveals.
export function sessionDetail(session) {
  const m = metricsOf(session);
  const { textKind, text } = detailText(m);
  return {
    state: session?.state || '',
    elapsed: fmtDuration(m.elapsed_seconds),
    cost: detailCost(m),
    textKind,
    text: oneLine(text, DETAIL_TEXT_MAX),
  };
}

// detailText is the text a detail block quotes, and what kind it is: the
// question the session waits on, else its last reply, else nothing.
function detailText(m) {
  if (m.question_headline) return { textKind: 'question', text: m.question_headline };
  if (m.last_assistant_text) return { textKind: 'last reply', text: m.last_assistant_text };
  return { textKind: '', text: '' };
}

// detailCost is the session's estimated cost, or nothing when it reports none.
function detailCost(m) {
  return m.estimated_cost_usd > 0 ? formatUsd(m.estimated_cost_usd) : '';
}

// sessionRow is one row of the list, as plain data.
export function sessionRow(session, depth, hereSessionId) {
  const m = metricsOf(session);
  return {
    id: session.session_id,
    depth,
    state: session.state || '',
    color: stateColor(session.state),
    branch: session.git_branch || '',
    tokens: formatTokens(m.total_tokens),
    context: formatPercent(m.context_utilization_percentage),
    model: shortModel(modelOf(session)),
    adapterTag: adapterTag(session),
    isHere: isHere(session, hereSessionId),
    detail: sessionDetail(session),
  };
}

// groupsOf answers the `groups` of the payload or of a group: the payload's
// top-level groups, or a group's nested ones.
function groupsOf(payloadOrGroup) {
  return Array.isArray(payloadOrGroup?.groups) ? payloadOrGroup.groups : [];
}

function agentsOf(group) {
  return Array.isArray(group?.agents) ? group.agents : [];
}

function childrenOf(agent) {
  return Array.isArray(agent?.children) ? agent.children : [];
}

function isSession(value) {
  return value !== null && typeof value === 'object' && typeof value.session_id === 'string';
}

// visitSessions calls `visit(session, depth)` for every session among
// `agents`, each followed by its children at any depth, a child one level
// deeper than its parent. Whatever is not a session (isSession) is passed
// over, and its children with it. The one walk of an agent list that
// flattenSessions, quotaSessions and sessionSections share.
function visitSessions(agents, depth, visit) {
  for (const a of agents) {
    if (!isSession(a)) continue;
    visit(a, depth);
    visitSessions(childrenOf(a), depth + 1, visit);
  }
}

// flattenSessions lists every session in the payload once: every group's
// agents, every nested group's, and every agent's children at any depth.
export function flattenSessions(payload) {
  const out = [];
  const visitGroups = groups => {
    for (const g of groups) {
      visitSessions(agentsOf(g), 0, a => out.push(a));
      visitGroups(groupsOf(g));
    }
  };
  visitGroups(groupsOf(payload));
  return out;
}

// quotaSessions is the set of sessions the quota is bucketed over, which must
// be the dashboard's so the two show the same bucket: every TOP-LEVEL group's
// agents and their children at any depth, and no nested group's
// (collectFlatSessions, platforms/web/irrlicht.js:600-615, visits
// `dashboardGroups[].agents` and `children` only). The session list itself
// walks nested groups too (flattenSessions, sessionSections).
//
// The macOS app draws its set differently again: every session it holds,
// nested groups included (flattenAgents, SessionManager+GroupComposition
// .swift), minus children whose parent it holds (SessionManager+WebSocket
// .swift). The panel follows the dashboard, the one the parity test pins.
export function quotaSessions(payload) {
  const out = [];
  for (const g of groupsOf(payload)) visitSessions(agentsOf(g), 0, a => out.push(a));
  return out;
}

// sessionSections turns the group tree into the panel's sections: one per
// group that holds sessions, nested groups as their own sections named
// "<parent> / <child>", children as indented rows under their parent.
export function sessionSections(payload, hereSessionId) {
  const sections = [];
  const visitGroups = (groups, prefix) => {
    for (const g of groups) {
      const name = sectionName(prefix, g);
      const rows = [];
      visitSessions(agentsOf(g), 0, (a, depth) => rows.push(sessionRow(a, depth, hereSessionId)));
      if (rows.length > 0) sections.push({ name, rows });
      visitGroups(groupsOf(g), name);
    }
  };
  visitGroups(groupsOf(payload), '');
  return sections;
}

// sectionName is a group's section heading: its name, or "(no project)"
// when it has none, after its parent's heading and " / " when nested.
function sectionName(prefix, group) {
  return (prefix ? prefix + ' / ' : '') + (group?.name || '(no project)');
}

// dayCostUSD sums the trailing-24h spend over every provider key the daemon
// reports, `unattributed` included: provider_costs is providerKey → timeframe
// → USD, and its `day` is a rolling 24 hours (costTimeframeSeconds in
// core/cmd/irrlichd/handlers.go), not the calendar day.
export function dayCostUSD(payload) {
  const costs = payload?.provider_costs;
  if (!costs || typeof costs !== 'object') return 0;
  let total = 0;
  for (const byWindow of Object.values(costs)) total += dayCostOf(byWindow);
  return total;
}

// dayCostOf is one provider key's `day` figure, 0 when it is not a finite
// number. Adding that 0 is the same as skipping the key: x + 0 is x for
// every x but -0, and a sum that starts at 0 is never -0.
function dayCostOf(byWindow) {
  const day = byWindow?.day;
  return Number.isFinite(day) ? day : 0;
}
