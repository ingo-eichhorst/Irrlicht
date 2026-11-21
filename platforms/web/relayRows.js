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
      const sub = g.groups && strip(g.groups);
      if (emptiedAway(g, all, agents, sub, g.unattached_workers)) continue;
      out.push({ ...g, agents, groups: sub });
    }
    return out;
  })(groups);
}

// emptiedAway: g lost every row it had (`before` → `after`) and nothing else
// keeps it on screen — not an orchestrator type, sub-groups, or unattached
// workers. A group that was already empty is not "emptied".
function emptiedAway(g, before, after, sub, workers) {
  return after.length === 0 && before.length > 0 && !g.type && !sub?.length && !workers?.length;
}

// remoteRootSessions lists the remote-origin rows that sit directly in a
// group, in tree order — what replacing the tree must carry into the new one.
// A relay child attached to its parent rides along in the parent's
// `children`: normalizeSourcedFrame folds the daemon id into a relay frame's
// parent_session_id too, so a relay child can only attach to a relay parent.
export function remoteRootSessions(groups) {
  const out = [];
  (function walk(gs) {
    for (const g of (gs || [])) {
      for (const a of (g.agents || [])) {
        if (sessionOrigin(a) === 'remote') out.push(a);
      }
      if (g.groups) walk(g.groups);
    }
  })(groups);
  return out;
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
// when nothing joins,
// and never writes into it or into a session.
export function joinRelayWorkers(groups, labelOf) {
  const workers = workerCensus(groups);
  if (!workers.size) return groups;
  const localIds = localBareIds(groups);
  const winners = new Map(); // worker name → relay session
  (function walk(gs) {
    for (const g of (gs || [])) {
      for (const a of (g.agents || [])) {
        if (sessionOrigin(a) !== 'remote' || a.parent_session_id || isShadowedRemote(a, localIds)) continue;
        const name = labelOf(sourceIdOf(a.session_id));
        const w = name ? workers.get(name) : null;
        if (!w || w.count !== 1 || !w.free) continue;
        const held = winners.get(name);
        if (!held || isNewer(a, held)) winners.set(name, a);
      }
      if (g.groups) walk(g.groups);
    }
  })(groups);
  if (!winners.size) return groups;

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
  return (function rebuild(gs) {
    const out = [];
    for (const g of gs) {
      const all = g.agents || [];
      const kept = all.filter(a => !joinedIds.has(a.session_id));
      const agents = kept.concat(into.get(g) || []);
      const sub = g.groups && rebuild(g.groups);
      const workersLeft = g.unattached_workers && g.unattached_workers.filter(w => !winners.has(w.worker_name));
      if (emptiedAway(g, all, agents, sub, workersLeft)) continue;
      out.push({ ...g, agents, groups: sub, unattached_workers: workersLeft });
    }
    return out;
  })(groups);
}

// workerCensus maps each worker name in the local tree to how many workers
// carry it and, for an unattached one, where it is: { count, free: { group,
// worker } | null }. Relay rows are not the local tree and are not counted.
function workerCensus(groups) {
  const byName = new Map();
  const entry = (name) => {
    let e = byName.get(name);
    if (!e) { e = { count: 0, free: null }; byName.set(name, e); }
    e.count++;
    return e;
  };
  (function walk(gs) {
    for (const g of (gs || [])) {
      (function rows(arr) {
        for (const a of (arr || [])) {
          if (a.worker_name && sessionOrigin(a) === 'local') entry(a.worker_name);
          rows(a.children);
        }
      })(g.agents);
      for (const w of (g.unattached_workers || [])) {
        if (w.worker_name) entry(w.worker_name).free = { group: g, worker: w };
      }
      if (g.groups) walk(g.groups);
    }
  })(groups);
  return byName;
}

// isNewer: a started after b, or at the same second with the lower id.
function isNewer(a, b) {
  const fa = Number(a.first_seen) || 0;
  const fb = Number(b.first_seen) || 0;
  return fa > fb || (fa === fb && a.session_id < b.session_id);
}
