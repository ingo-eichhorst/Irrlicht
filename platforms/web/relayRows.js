// relayRows.js — pure helpers over the relay (remote-origin) rows the web
// dashboard keeps in the same group tree it adopts from the local daemon's
// REST payload (#2149). No DOM, no fetch, no module state.
//
// A module of its own rather than more of sessionIdentity.js: that file holds
// a literal NUL inside its first 8000 bytes, so git treats it as binary and a
// change to it reviews as "Binary files differ" (`git log --numstat` prints
// `-  -` for it).
import { sessionOrigin } from './sessionIdentity.js';

// withoutRemoteSessions is the tree with every remote-origin row taken out of
// its group: the shape the local daemon's REST payload describes, and so the
// one a rehydrate compares that payload against. A relay row is never in the
// payload, so comparing the full tree made every poll "see" a structure
// change and replace the tree.
//
// A group the removal empties is dropped too (a relay-only project group),
// unless something else keeps it on screen: an orchestrator type, sub-groups,
// or unattached workers. A group that was already empty in the payload stays.
// Returns copies of the groups; the rows themselves are shared.
export function withoutRemoteSessions(groups) {
  const out = [];
  for (const g of (groups || [])) {
    const all = g.agents || [];
    const agents = all.filter(a => sessionOrigin(a) !== 'remote');
    const sub = g.groups && withoutRemoteSessions(g.groups);
    const emptied = agents.length === 0 && all.length > 0;
    if (emptied && !g.type && !sub?.length && !g.unattached_workers?.length) continue;
    out.push({ ...g, agents, groups: sub });
  }
  return out;
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
