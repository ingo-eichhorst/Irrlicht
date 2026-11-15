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

// modelOf prefers the transcript-derived metrics.model_name over the
// top-level model, as the dashboard does.
export function modelOf(session) {
  return session?.metrics?.model_name || session?.model || '';
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
  const m = session?.metrics || {};
  const text = m.question_headline || m.last_assistant_text || '';
  return {
    state: session?.state || '',
    elapsed: fmtDuration(m.elapsed_seconds),
    cost: m.estimated_cost_usd > 0 ? formatUsd(m.estimated_cost_usd) : '',
    textKind: m.question_headline ? 'question' : (m.last_assistant_text ? 'last reply' : ''),
    text: oneLine(text, DETAIL_TEXT_MAX),
  };
}

// sessionRow is one row of the list, as plain data.
export function sessionRow(session, depth, hereSessionId) {
  const m = session?.metrics || {};
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

function groupsOf(payload) {
  return Array.isArray(payload?.groups) ? payload.groups : [];
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

// flattenSessions lists every session in the payload once: every group's
// agents, every nested group's, and every agent's children at any depth.
export function flattenSessions(payload) {
  const out = [];
  const visitAgents = agents => {
    for (const a of agents) {
      if (!isSession(a)) continue;
      out.push(a);
      visitAgents(childrenOf(a));
    }
  };
  const visitGroups = groups => {
    for (const g of groups) {
      visitAgents(agentsOf(g));
      visitGroups(Array.isArray(g?.groups) ? g.groups : []);
    }
  };
  visitGroups(groupsOf(payload));
  return out;
}

// sessionSections turns the group tree into the panel's sections: one per
// group that holds sessions, nested groups as their own sections named
// "<parent> / <child>", children as indented rows under their parent.
export function sessionSections(payload, hereSessionId) {
  const sections = [];
  const visitGroups = (groups, prefix) => {
    for (const g of groups) {
      const name = (prefix ? prefix + ' / ' : '') + (g?.name || '(no project)');
      const rows = [];
      const visitAgents = (agents, depth) => {
        for (const a of agents) {
          if (!isSession(a)) continue;
          rows.push(sessionRow(a, depth, hereSessionId));
          visitAgents(childrenOf(a), depth + 1);
        }
      };
      visitAgents(agentsOf(g), 0);
      if (rows.length > 0) sections.push({ name, count: rows.length, rows });
      visitGroups(Array.isArray(g?.groups) ? g.groups : [], name);
    }
  };
  visitGroups(groupsOf(payload), '');
  return sections;
}

// dayCostUSD sums the trailing-24h spend over every provider key the daemon
// reports, `unattributed` included: provider_costs is providerKey → timeframe
// → USD, and its `day` is a rolling 24 hours (costTimeframeSeconds in
// core/cmd/irrlichd/handlers.go), not the calendar day.
export function dayCostUSD(payload) {
  const costs = payload?.provider_costs;
  if (!costs || typeof costs !== 'object') return 0;
  let total = 0;
  for (const byWindow of Object.values(costs)) {
    const day = byWindow?.day;
    if (Number.isFinite(day)) total += day;
  }
  return total;
}
