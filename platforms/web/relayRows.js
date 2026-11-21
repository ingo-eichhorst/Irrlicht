// relayRows.js — pure helpers over the relay (remote-origin) rows the web
// dashboard keeps in the same group tree it adopts from the local daemon's
// REST payload (#2149). No DOM, no fetch, no module state.
//
// A module of its own rather than more of sessionIdentity.js: that file holds
// a literal NUL inside its first 8000 bytes, so git treats it as binary and a
// change to it reviews as "Binary files differ" (`git log --numstat` prints
// `-  -` for it).
import { sessionOrigin, sourceIdOf, localBareIds, isShadowedRemote } from './sessionIdentity.js';

// withoutRelayOnlySessions is the tree without the relay rows no local row
// shadows — the rows a REST payload cannot hold — and so the shape a rehydrate
// compares that payload against. Comparing the full tree made every poll with
// such a row "see" a structure change and replace the tree.
//
// A shadowed relay row stays in. Its bare twin IS in the payload, and on a
// page whose local rows have no live socket of their own (one served by a
// relay, or with Local off) only that replace refreshes the twin; keeping the
// shadowed row keeps such a page adopting every poll, as it did before #2149.
//
// A group the removal empties is dropped too (a relay-only project group).
// Returns copies of the groups; the rows themselves are shared.
export function withoutRelayOnlySessions(groups) {
  const localIds = localBareIds(groups);
  return (function strip(gs) {
    const out = [];
    for (const g of (gs || [])) {
      const all = g.agents || [];
      const agents = all.filter(a => sessionOrigin(a) !== 'remote' || isShadowedRemote(a, localIds));
      const next = { ...g, agents, groups: g.groups && strip(g.groups) };
      if (!emptiedAway(all, next)) out.push(next);
    }
    return out;
  })(groups);
}

// emptiedAway: the rebuilt group `next` lost every row it had (`before`) and
// nothing else keeps it on screen — not an orchestrator type, sub-groups, or
// unattached workers. A group that was already empty is not "emptied".
function emptiedAway(before, next) {
  return before.length > 0 && next.agents.length === 0 &&
    !next.type && !next.groups?.length && !next.unattached_workers?.length;
}

// remoteRootSessions lists the remote-origin rows that sit directly in a
// group, in tree order — what replacing the tree must carry into the new one.
// A relay child attached to its parent rides along in the parent's
// `children`: normalizeSourcedFrame folds the daemon id into a relay frame's
// parent_session_id too, so a relay child can only attach to a relay parent.
export function remoteRootSessions(groups) {
  const out = [];
  forEachGroup(groups, (g) => {
    for (const a of (g.agents || [])) if (sessionOrigin(a) === 'remote') out.push(a);
  });
  return out;
}

// forEachGroup visits every group in tree order: a group, then its sub-groups.
function forEachGroup(groups, visit) {
  for (const g of (groups || [])) {
    visit(g);
    forEachGroup(g.groups, visit);
  }
}

// joinRelayWorkers returns the tree as displayed (#2149): a session that runs
// in a container and reaches the dashboard through the relay is shown in its
// orchestrator worker's row. The join key is the forwarding daemon's label
// (the container's hostname), which equals the worker's name. A relay session
// joins worker X only when
//   · `labelOf(daemonId)` names X — a connected daemon's own label, '' when
//     there is none;
//   · it is a relay root (no parent_session_id) that no local copy shadows;
//   · exactly one worker in the local tree is named X, counting session rows
//     that carry worker_name X and unattached_workers entries alike, and that
//     one is unattached. A worker with a local session therefore wins.
// Several candidates for one worker (an old and a new container overlap, each
// with its own daemon id): the newest first_seen joins, a tie goes to the
// lower session id, and the rest keep their place.
//
// A joined session moves to the end of its worker's group's session rows, as
// a copy carrying the worker's role, icon, description, worker_name and
// worker_id; the worker leaves that group's unattached_workers; a project
// group the move empties is dropped (emptiedAway). Returns `groups` itself
// when nothing joins, and never writes into it or into a session.
export function joinRelayWorkers(groups, labelOf) {
  const workers = workerCensus(groups);
  const winners = workers.size ? pickJoinWinners(groups, workers, labelOf) : new Map();
  return winners.size ? placeJoins(groups, winners, workers) : groups;
}

// pickJoinWinners maps each free worker's name to the relay session that
// joins it: of the candidates, the newest first_seen, a tie to the lower id.
function pickJoinWinners(groups, workers, labelOf) {
  const localIds = localBareIds(groups);
  const winners = new Map();
  for (const a of remoteRootSessions(groups)) {
    const name = joinableWorkerName(a, workers, localIds, labelOf);
    if (!name) continue;
    const held = winners.get(name);
    if (!held || isNewer(a, held)) winners.set(name, a);
  }
  return winners;
}

// joinableWorkerName is the name of the one free worker relay row `a` may
// join, or '': `a` is a relay root no local copy shadows, and its daemon's
// label names exactly one worker in the tree, an unattached one.
function joinableWorkerName(a, workers, localIds, labelOf) {
  if (a.parent_session_id || isShadowedRemote(a, localIds)) return '';
  const name = labelOf(sourceIdOf(a.session_id));
  const w = name ? workers.get(name) : null;
  return w && w.count === 1 && w.free ? name : '';
}

// placeJoins rebuilds the tree with each winner moved, decorated as its
// worker, to the end of that worker's group's session rows, and the worker
// out of that group's unattached_workers.
function placeJoins(groups, winners, workers) {
  const joinedIds = new Set();
  const into = new Map(); // worker's group → decorated sessions
  for (const [name, a] of winners) {
    const { group, worker } = workers.get(name).free;
    joinedIds.add(a.session_id);
    into.set(group, (into.get(group) || []).concat([{
      ...a,
      role: worker.role, icon: worker.icon, description: worker.description,
      worker_name: worker.worker_name, worker_id: worker.worker_id,
    }]));
  }
  const stillFree = (w) => !winners.has(w.worker_name);
  return (function rebuild(gs) {
    const out = [];
    for (const g of gs) {
      const all = g.agents || [];
      const next = {
        ...g,
        agents: all.filter(a => !joinedIds.has(a.session_id)).concat(into.get(g) || []),
        groups: g.groups && rebuild(g.groups),
        unattached_workers: g.unattached_workers && g.unattached_workers.filter(stillFree),
      };
      if (!emptiedAway(all, next)) out.push(next);
    }
    return out;
  })(groups);
}

// workerCensus maps each worker name in the local tree to how many workers
// carry it and, for an unattached one, where it is: { count, free: { group,
// worker } | null }. Relay rows are not the local tree and are not counted.
function workerCensus(groups) {
  const census = new Map();
  forEachGroup(groups, (g) => {
    countSessionRows(census, g.agents);
    countFreeWorkers(census, g);
  });
  return census;
}

// censusEntry counts one more worker named `name` and returns its entry.
function censusEntry(census, name) {
  const e = census.get(name) || { count: 0, free: null };
  census.set(name, e);
  e.count++;
  return e;
}

// countSessionRows counts the worker names local session rows carry,
// subagent rows included.
function countSessionRows(census, rows) {
  for (const a of (rows || [])) {
    if (a.worker_name && sessionOrigin(a) === 'local') censusEntry(census, a.worker_name);
    countSessionRows(census, a.children);
  }
}

// countFreeWorkers counts group g's unattached workers and records where each is.
function countFreeWorkers(census, g) {
  for (const w of (g.unattached_workers || [])) {
    if (w.worker_name) censusEntry(census, w.worker_name).free = { group: g, worker: w };
  }
}

// isNewer: a started after b, or at the same second with the lower id.
function isNewer(a, b) {
  const fa = Number(a.first_seen) || 0;
  const fb = Number(b.first_seen) || 0;
  return fa > fb || (fa === fb && a.session_id < b.session_id);
}
