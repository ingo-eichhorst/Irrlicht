// collapsedSummaries.js — drives visibility of each session's task-summary/
// question block (issue #738). Two independent, persisted concerns:
//
//   - mode ('collapsed' | 'waiting'): the header button's global setting
//     (issue #985). 'collapsed' hides every row's question; 'waiting' shows
//     the block only for sessions currently in state === 'waiting', so
//     sessions blocked on the user don't get buried among working/ready
//     rows. There is no third "expand all" state — a flat wall of summaries
//     nobody reliably reads.
//   - per-session manual override: the row-scoped "let me glance at this
//     one" chevron click, layered on top of the mode's baseline regardless
//     of which mode is active.
//
// Both are computed live off current session state on every call, never
// snapshotted — a session transitioning into 'waiting' while in 'waiting'
// mode pops open automatically, and one that gets answered collapses again.
// The load/persist/Set mechanics for the override live in collapsedSet.js
// (shared with collapsedGroups.js).
import { makeCollapsedSet } from './collapsedSet.js';

const MODE_KEY = 'irrlicht_summaryMode';
const VALID_MODES = new Set(['collapsed', 'waiting']);
const DEFAULT_MODE = 'waiting';

function loadMode() {
  try {
    const raw = localStorage.getItem(MODE_KEY);
    if (VALID_MODES.has(raw)) return raw;
  } catch (e) {
    console.debug(`${MODE_KEY}: failed to load, defaulting to '${DEFAULT_MODE}'`, e);
  }
  return DEFAULT_MODE;
}

let mode = loadMode();

// getSummaryMode returns the current global mode.
export function getSummaryMode() {
  return mode;
}

// toggleSummaryMode flips between 'collapsed' and 'waiting' and persists it.
export function toggleSummaryMode() {
  mode = mode === 'waiting' ? 'collapsed' : 'waiting';
  try {
    localStorage.setItem(MODE_KEY, mode);
  } catch (e) {
    console.debug(`${MODE_KEY}: failed to persist`, e);
  }
}

// A distinct key from the pre-#985 store: that one used to accumulate every
// known session id on "collapse all", so reusing it here would reinterpret
// leftover bulk state as manual per-row overrides after upgrade.
const overrides = makeCollapsedSet('irrlicht_summaryOverrides');

// isSummaryCollapsed reports whether the given session's summary block
// should render collapsed: the mode's baseline for this session's current
// state, flipped if the user manually overrode this one row.
export function isSummaryCollapsed(sessionId, sessionState) {
  const baseline = !(mode === 'waiting' && sessionState === 'waiting');
  return overrides.has(sessionId) ? !baseline : baseline;
}

// toggleSummaryCollapsed flips one session's manual override and persists it.
export function toggleSummaryCollapsed(sessionId) {
  overrides.toggle(sessionId);
}
