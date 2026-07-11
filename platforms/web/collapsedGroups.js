// collapsedGroups.js — the set of dashboard group names the user has collapsed,
// persisted across reloads. A small store with a narrow interface: rendering
// asks isGroupCollapsed(name); the group-header click calls
// toggleGroupCollapsed(name). The Set, its localStorage backing, and the
// defensive load/persist all live behind those two calls — nothing else touches
// the raw state, so the collapse/persist transitions are testable without the
// DOM.
const KEY = 'irrlicht_collapsedGroups';

function load() {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) {
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) return new Set(parsed.filter(s => typeof s === 'string'));
    }
  } catch (e) {
    console.debug('collapsedGroups: failed to load, starting empty', e);
  }
  return new Set();
}

let collapsed = load();

function persist() {
  try { localStorage.setItem(KEY, JSON.stringify([...collapsed])); } catch (e) {
    console.debug('collapsedGroups: failed to persist', e);
  }
}

// isGroupCollapsed reports whether the named group is currently collapsed.
export function isGroupCollapsed(name) {
  return collapsed.has(name);
}

// toggleGroupCollapsed flips the named group's collapsed state and persists it.
export function toggleGroupCollapsed(name) {
  if (collapsed.has(name)) {
    collapsed.delete(name);
  } else {
    collapsed.add(name);
  }
  persist();
}
