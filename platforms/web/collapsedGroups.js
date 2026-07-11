// collapsedGroups.js — the set of dashboard group names the user has collapsed,
// persisted across reloads. A small store with a narrow interface: rendering
// asks isGroupCollapsed(name); the group-header click calls
// toggleGroupCollapsed(name). The load/persist/Set mechanics live in
// collapsedSet.js (shared with collapsedSummaries.js) — nothing else touches
// the raw state, so the collapse/persist transitions are testable without the
// DOM.
import { makeCollapsedSet } from './collapsedSet.js';

const store = makeCollapsedSet('irrlicht_collapsedGroups');

// isGroupCollapsed reports whether the named group is currently collapsed.
export function isGroupCollapsed(name) {
  return store.has(name);
}

// toggleGroupCollapsed flips the named group's collapsed state and persists it.
export function toggleGroupCollapsed(name) {
  store.toggle(name);
}
