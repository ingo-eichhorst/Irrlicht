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
function agentKey({ agent, terminal }) {
  const cwd = trimSlashes(terminal.foreground?.cwd || terminal.cwd || '');
  if (!agent.app || !cwd) return null;
  return agent.app + '\u0000' + cwd;
}

function sessionKey(session) {
  return session.cwd ? session.adapter + '\u0000' + session.cwd : null;
}

// groupBy collects items into a Map from key to the items with that key,
// leaving out items whose key is null.
function groupBy(items, keyOf) {
  const groups = new Map();
  for (const item of items) {
    const k = keyOf(item);
    if (k !== null) groups.set(k, (groups.get(k) || []).concat([item]));
  }
  return groups;
}

function countBy(items, keyOf) {
  return new Map([...groupBy(items, keyOf)].map(([k, list]) => [k, list.length]));
}

// isLocal is false for an agent on another machine: Herdr's AGENTS.md says
// remote ids carry a `<machine>/` prefix ("vps/t7"), and its shipped agents
// mod tells them apart the same way. The daemon only knows this machine's
// processes and folders, so a remote agent's pid or cwd matching a local
// session would be a coincidence.
export const isLocal = (agent) => !String(agent.address).includes('/');

// candidatePids are the pids a terminal's agent can be known by: the
// foreground process group's leader first, then the terminal's own process.
function candidatePids(terminal) {
  const pids = [terminal.foreground?.pid, terminal.pid];
  return pids;
}

// pidMatches are the sessions on the first of the terminal's pids that has
// any. One is a match; two or more are ambiguous, and an ambiguous agent gets
// no gauge at all rather than a folder guess.
function pidMatches(terminal, sessionsByPid) {
  for (const pid of candidatePids(terminal)) {
    const found = sessionsByPid.get(pid);
    if (found) return found;
  }
  return [];
}

// matchByPid runs the pid step over every agent. It answers the rows it
// matched, the session ids they claimed, and the agents no pid decided.
function matchByPid(herdr, sessions) {
  const sessionsByPid = groupBy(sessions, (s) => s.pid);
  const result = { matched: new Map(), claimed: new Set(), undecided: [] };
  for (const h of herdr) recordPidStep(result, h, pidMatches(h.terminal, sessionsByPid));
  return result;
}

// recordPidStep files one agent's pid step: no session leaves it to the
// folder rule, one is its match, more are ambiguous and decide "no gauge".
function recordPidStep(result, h, found) {
  if (found.length === 0) result.undecided.push(h);
  if (found.length !== 1) return;
  result.matched.set(h.agent.address, { session: found[0], via: 'pid' });
  result.claimed.add(found[0].id);
}

// matchByFolder is the folder rule, for agents no pid decided. It decides
// only when exactly one Herdr agent and exactly one session share the pair,
// both counted over everything, pid-matched ones included: an agent a pid
// already matched that also runs in the folder still makes the folder
// ambiguous. Apart from the counts, a session a pid already gave to a row
// (`claimed`) is never handed to a second row.
function matchByFolder(herdr, sessions, { matched, claimed, undecided }) {
  const agentsPerKey = countBy(herdr, agentKey);
  const sessionsPerKey = groupBy(sessions, sessionKey);
  for (const h of undecided) {
    const k = agentKey(h);
    const pair = sessionsPerKey.get(k) || [];
    const unique = agentsPerKey.get(k) === 1 && pair.length === 1;
    if (unique && !claimed.has(pair[0].id)) {
      matched.set(h.agent.address, { session: pair[0], via: 'folder' });
    }
  }
  return matched;
}

export function matchAgents(allAgents, terminals, sessions) {
  const terminalOf = new Map(terminals.map((t) => [t.address, t]));
  const herdr = allAgents.filter(isLocal).map((agent) => ({ agent, terminal: terminalOf.get(agent.address) || {} }));
  return matchByFolder(herdr, sessions, matchByPid(herdr, sessions));
}
