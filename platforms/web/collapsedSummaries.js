// collapsedSummaries.js — the set of session IDs whose task-summary/question
// block the user has collapsed (issue #738), persisted across reloads. Mirrors
// collapsedGroups.js: a small store with a narrow interface so the
// collapse/persist transitions are testable without the DOM. Default-absent →
// expanded; presence → collapsed. The load/persist/Set mechanics live in
// collapsedSet.js (shared with collapsedGroups.js).
import { makeCollapsedSet } from './collapsedSet.js';

const store = makeCollapsedSet('irrlicht_collapsedSummaries');

// isSummaryCollapsed reports whether the given session's summary block is collapsed.
export function isSummaryCollapsed(sessionId) {
  return store.has(sessionId);
}

// toggleSummaryCollapsed flips one session's collapsed state and persists it.
export function toggleSummaryCollapsed(sessionId) {
  store.toggle(sessionId);
}

// anySummaryCollapsed reports whether at least one summary is collapsed —
// drives the header collapse-all/expand-all affordance.
export function anySummaryCollapsed() {
  return store.size > 0;
}

// collapseAllSummaries collapses every given session id; expandAllSummaries
// clears the set. Together they back the header's one-shot toggle.
export function collapseAllSummaries(sessionIds) {
  store.setAll(sessionIds);
}

export function expandAllSummaries() {
  store.clear();
}
