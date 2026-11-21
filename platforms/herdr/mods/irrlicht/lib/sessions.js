// The /api/v1/sessions payload reduced to what the panel draws, one entry per
// top-level session. The payload is a tree: `groups[]` (one per project), each
// with `agents[]` and, for an orchestrator, nested `groups[]`; each agent may
// carry `children[]` (subagents and background agents). Field names are the
// JSON tags of session.SessionState and session.SessionMetrics in
// core/domain/session.

// An empty adapter means Claude Code (SessionState.Adapter's doc comment,
// core/domain/session/session.go).
const DEFAULT_ADAPTER = 'claude-code';

function arrayOf(value) {
  return Array.isArray(value) ? value : [];
}

function isSession(value) {
  return value !== null && typeof value === 'object' && typeof value.session_id === 'string';
}

// topLevelSessions lists every group's agents, nested groups included, and
// leaves out `children`. A child is a subagent or background agent of a
// session: it runs inside its parent's process or detached from any terminal,
// so it never is the program a Herdr terminal runs. On the live payload read
// for this mod (2026-11-21), the one Claude Code subagent carried no `pid` at
// all.
function topLevelSessions(payload) {
  const out = [];
  const visit = (groups) => {
    for (const g of arrayOf(groups)) {
      for (const a of arrayOf(g?.agents)) if (isSession(a)) out.push(a);
      visit(g?.groups);
    }
  };
  visit(payload?.groups);
  return out;
}

// trimSlashes drops trailing slashes so "/a/b/" and "/a/b" compare equal; the
// root stays "/".
export function trimSlashes(path) {
  if (typeof path !== 'string' || path === '') return '';
  const trimmed = path.replace(/\/+$/, '');
  return trimmed === '' ? '/' : trimmed;
}

function positiveInt(n) {
  return Number.isInteger(n) && n > 0 ? n : null;
}

function finiteOrNull(n) {
  return Number.isFinite(n) ? n : null;
}

// sessionFacts is one session as the panel uses it.
export function sessionFacts(session) {
  const m = session?.metrics || {};
  return {
    id: session.session_id,
    adapter: session.adapter || DEFAULT_ADAPTER,
    pid: positiveInt(session.pid),
    cwd: trimSlashes(session.cwd),
    model: m.model_name || session.model || '',
    percent: finiteOrNull(m.context_utilization_percentage),
    pressure: m.pressure_level || '',
    windowUnknown: m.context_window_unknown === true,
    contextWindow: finiteOrNull(m.context_window),
    tokens: finiteOrNull(m.total_tokens),
  };
}

// readSessions is the payload as the panel keeps it, or null when it is not
// the shape the daemon serves. `groups` is null when the daemon has no
// session at all: handlers.go tags it without omitempty, and BuildDashboard
// (core/domain/session/grouped.go) starts from `var result []*AgentGroup`.
export function readSessions(payload) {
  if (!payload || (payload.groups !== null && !Array.isArray(payload.groups))) return null;
  return topLevelSessions(payload).map(sessionFacts);
}
