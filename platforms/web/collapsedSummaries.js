// collapsedSummaries.js — the set of session IDs whose task-summary/question
// block the user has collapsed (issue #738), persisted across reloads. Mirrors
// collapsedGroups.js: a small store with a narrow interface so the
// collapse/persist transitions are testable without the DOM. Default-absent →
// expanded; presence → collapsed.
const KEY = 'irrlicht_collapsedSummaries';

function load() {
  try {
    const raw = localStorage.getItem(KEY);
    if (raw) {
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) return new Set(parsed.filter(s => typeof s === 'string'));
    }
  } catch (e) {}
  return new Set();
}

let collapsed = load();

function persist() {
  try { localStorage.setItem(KEY, JSON.stringify([...collapsed])); } catch (e) {}
}

// isSummaryCollapsed reports whether the given session's summary block is collapsed.
export function isSummaryCollapsed(sessionId) {
  return collapsed.has(sessionId);
}

// toggleSummaryCollapsed flips one session's collapsed state and persists it.
export function toggleSummaryCollapsed(sessionId) {
  if (collapsed.has(sessionId)) {
    collapsed.delete(sessionId);
  } else {
    collapsed.add(sessionId);
  }
  persist();
}

// anySummaryCollapsed reports whether at least one summary is collapsed —
// drives the header collapse-all/expand-all affordance.
export function anySummaryCollapsed() {
  return collapsed.size > 0;
}

// collapseAllSummaries collapses every given session id; expandAllSummaries
// clears the set. Together they back the header's one-shot toggle.
export function collapseAllSummaries(sessionIds) {
  collapsed = new Set(sessionIds);
  persist();
}

export function expandAllSummaries() {
  collapsed = new Set();
  persist();
}
