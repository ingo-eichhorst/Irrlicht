import { trimSlashes } from './sessions.js';

// Which Irrlicht session each Herdr agent is. A pid decides first, as it does
// for a TUI herdr pane in core/adapters/inbound/agents/processlifecycle/
// herdrpane.go. Unlike that file, a folder decides too, but only when exactly
// one agent and exactly one session share it with the same agent name: Codex's
// pid never matched (measured below), so without it Codex would get no gauge.
//
// Inputs:
//   agents     Herdr's agents() — { address, app, ... }
//   terminals  Herdr's terminals() — { address, pid, cwd, foreground: { pid, cwd } }
//   sessions   readSessions() facts — { id, adapter, pid, cwd, ... }
// Returns a Map from agent address to { session, via }, where via is 'pid' or
// 'folder'. An agent with no entry gets no gauge.
//
// What was measured on Herdr 0.1.16 / herdr-core 0.1.21 (2026-11-21), reading
// Herdr's terminals() and /api/v1/sessions side by side, once against a daemon
// with Claude Code hooks installed and once against one granted transcripts
// only (the same pids came back from both):
//   - Claude Code 2.1.330: terminals().foreground.pid is the claude process,
//     and it equals the session's pid. The terminal's own pid is the
//     `zsh -lic` wrapper Herdr starts agents in.
//   - Codex 0.163.0 (npm install): foreground.pid is the `node` launcher and
//     the session's pid is the native codex binary it spawned, so neither pid
//     matches and Codex is found by the folder rule below.
//   - foreground.cwd is the resolved path (/private/tmp/...) and matched the
//     session's cwd; the terminal's own `cwd` is the path as typed (/tmp/...)
//     and did not.

// agentKey is the folder rule's key: what the agent reported (OSC 7501 `app`)
// and the folder its foreground process runs in. Herdr's `app` and Irrlicht's
// `adapter` were the same string for both agents measured ("claude-code",
// "codex"); a pair that spells it differently matches nothing, never the
// wrong session.
function agentKey(agent, terminal) {
  const cwd = trimSlashes(terminal?.foreground?.cwd || terminal?.cwd || '');
  if (!agent?.app || !cwd) return null;
  return agent.app + '\u0000' + cwd;
}

function sessionKey(session) {
  return session.cwd ? session.adapter + '\u0000' + session.cwd : null;
}

function countBy(items, keyOf) {
  const counts = new Map();
  for (const item of items) {
    const k = keyOf(item);
    if (k !== null) counts.set(k, (counts.get(k) || 0) + 1);
  }
  return counts;
}

// candidatePids are the pids a terminal's agent can be known by: the
// foreground process group's leader first, then the terminal's own process.
function candidatePids(terminal) {
  const pids = [terminal?.foreground?.pid, terminal?.pid];
  return pids.filter((p, i) => Number.isInteger(p) && p > 0 && pids.indexOf(p) === i);
}

// pidMatch is the one session that carries one of the terminal's pids. Two
// sessions on one pid is ambiguous, and an ambiguous agent gets no gauge at
// all rather than a folder guess.
function pidMatch(terminal, sessionsByPid) {
  for (const pid of candidatePids(terminal)) {
    const found = sessionsByPid.get(pid) || [];
    if (found.length === 1) return { session: found[0] };
    if (found.length > 1) return { ambiguous: true };
  }
  return null;
}

export function matchAgents(agents, terminals, sessions) {
  const terminalOf = new Map(terminals.map((t) => [t.address, t]));
  const sessionsByPid = new Map();
  for (const s of sessions) {
    if (s.pid === null) continue;
    sessionsByPid.set(s.pid, [...(sessionsByPid.get(s.pid) || []), s]);
  }

  const matched = new Map();
  const claimed = new Set();
  const undecided = [];
  for (const a of agents) {
    const found = pidMatch(terminalOf.get(a.address), sessionsByPid);
    if (found?.session) {
      matched.set(a.address, { session: found.session, via: 'pid' });
      claimed.add(found.session.id);
    } else if (!found?.ambiguous) {
      undecided.push(a);
    }
  }

  // The folder rule, for agents no pid decided. It decides only when exactly
  // one Herdr agent and exactly one session share the pair, both counted over
  // everything, pid-matched ones included: an agent a pid already matched
  // that also runs in the folder still makes the folder ambiguous. Apart from
  // the counts, a session a pid already gave to a row (`claimed`) is never
  // handed to a second row.
  const agentKeyOf = (a) => agentKey(a, terminalOf.get(a.address));
  const agentsPerKey = countBy(agents, agentKeyOf);
  const sessionsPerKey = new Map();
  for (const s of sessions) {
    const k = sessionKey(s);
    if (k !== null) sessionsPerKey.set(k, [...(sessionsPerKey.get(k) || []), s]);
  }
  for (const a of undecided) {
    const k = agentKeyOf(a);
    const pair = k === null ? [] : sessionsPerKey.get(k) || [];
    const unique = agentsPerKey.get(k) === 1 && pair.length === 1;
    if (unique && !claimed.has(pair[0].id)) {
      matched.set(a.address, { session: pair[0], via: 'folder' });
      claimed.add(pair[0].id);
    }
  }
  return matched;
}
