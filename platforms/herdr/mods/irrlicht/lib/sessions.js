// The /api/v1/sessions payload reduced to what the panel draws, one entry per
// top-level session. The payload is a tree: `groups[]` (one per project), each
// with `agents[]` and, for an orchestrator, nested `groups[]`; each agent may
// carry `children[]` (subagents and background agents). Field names are the
// JSON tags of session.SessionState and session.SessionMetrics in
// core/domain/session.

// An empty adapter means Claude Code (SessionState.Adapter's doc comment,
// core/domain/session/session.go).
const DEFAULT_ADAPTER = 'claude-code';

// The daemon names a session with no project_name's group "unknown"
// (addRegularAgent, core/domain/session/grouped.go:212-225); a group with no
// name at all is read the same way.
const UNNAMED_GROUP = 'unknown';

function arrayOf(value) {
  return Array.isArray(value) ? value : [];
}

function isSession(value) {
  return value !== null && typeof value === 'object' && typeof value.session_id === 'string';
}

function textOf(value) {
  return typeof value === 'string' ? value : '';
}

// topLevelSessions lists every group's agents, nested groups included, each
// with the top-level group it sits under, in payload order: a group's own
// agents, then its nested groups', as GroupView draws them
// (GroupView.swift:144-161). It leaves out `children`. A child is a subagent
// or background agent of a session: it runs inside its parent's process or
// detached from any terminal, so it never is the program a Herdr terminal
// runs. On the live payload read for this mod (2026-11-21), the one Claude
// Code subagent carried no `pid` at all.
function topLevelSessions(payload) {
  const out = [];
  const visit = (groups, top) => {
    for (const g of arrayOf(groups)) {
      const owner = top || g;
      for (const a of arrayOf(g?.agents)) if (isSession(a)) out.push({ session: a, group: owner });
      visit(g?.groups, owner);
    }
  };
  visit(payload?.groups, null);
  return out;
}

// trimSlashes drops trailing slashes so "/a/b/" and "/a/b" compare equal; the
// root stays "/".
export function trimSlashes(path) {
  if (typeof path !== 'string' || path === '') return '';
  let end = path.length;
  while (end > 1 && path[end - 1] === '/') end--;
  return path.slice(0, end);
}

function positiveInt(n) {
  return Number.isInteger(n) && n > 0 ? n : null;
}

function finiteOrNull(n) {
  return Number.isFinite(n) ? n : null;
}

// metricFacts is what the context gauge reads.
function metricFacts(m, session) {
  return {
    model: textOf(m.model_name) || textOf(session.model),
    percent: finiteOrNull(m.context_utilization_percentage),
    pressure: textOf(m.pressure_level),
    windowUnknown: m.context_window_unknown === true,
    contextWindow: finiteOrNull(m.context_window),
    tokens: finiteOrNull(m.total_tokens),
  };
}

// activeSubagents is the Swift row's badge count, SessionState
// .activeSubagentCount (SessionState.swift:1457-1459): the daemon's subagent
// summary's working plus waiting.
function activeSubagents(summary) {
  const s = summary || {};
  return (positiveInt(s.working) || 0) + (positiveInt(s.waiting) || 0);
}

// noteFacts is what the lines under a row read (lib/notes.js): the waiting
// question, and the failure the daemon recorded.
function noteFacts(m) {
  const failure = m.session_error || {};
  return {
    questionHeadline: textOf(m.question_headline),
    lastAssistantText: textOf(m.last_assistant_text),
    errorMessage: textOf(failure.message),
    errorClass: textOf(failure.class),
  };
}

// sessionFacts is one session as the panel uses it.
export function sessionFacts(session) {
  const m = session.metrics || {};
  return {
    id: session.session_id,
    adapter: session.adapter || DEFAULT_ADAPTER,
    state: textOf(session.state),
    pid: positiveInt(session.pid),
    cwd: trimSlashes(session.cwd),
    branch: textOf(session.git_branch),
    subagents: activeSubagents(session.subagents),
    ...metricFacts(m, session),
    ...noteFacts(m),
  };
}

// placedFacts adds where the session sits in the payload: its top-level
// group's name and type, and its position in payload order.
function placedFacts({ session, group }, order) {
  return {
    ...sessionFacts(session),
    group: textOf(group.name) || UNNAMED_GROUP,
    gastown: group.type === 'gastown',
    order,
  };
}

// readSessions is the payload as the panel keeps it, or null when it is not
// the shape the daemon serves. `groups` is null when the daemon has no
// session at all: handlers.go tags it without omitempty, and BuildDashboard
// (core/domain/session/grouped.go) starts from `var result []*AgentGroup`.
export function readSessions(payload) {
  if (!isSessionsPayload(payload)) return null;
  return topLevelSessions(payload).map((placed, order) => placedFacts(placed, order));
}

function isSessionsPayload(payload) {
  return Boolean(payload) && (payload.groups === null || Array.isArray(payload.groups));
}
