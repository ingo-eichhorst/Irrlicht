// The lines under a matched row, in the order SessionRowView stacks them
// under its main line (SessionRowView.swift:489-528): the error line, the
// waiting question, then the context-pressure alert. Each is
//   { kind, text, tooltip, hue }
// and is drawn on a wash of its hue.

import { BRAND } from './palette.js';

// The Swift row's pressure alert fires at the app's default threshold, 80%
// of the window (ContextPressureThreshold.swift:16-17, isExceeded at :46-53),
// and only for a session that is still going (SessionRowView.swift:508-510).
export const PRESSURE_ALERT_PERCENT = 80;

// Human label for an error class, used when the agent gave no message of its
// own: SessionError.classLabel (SessionState.swift:1034-1053). An unknown
// class is shown as it is.
const CLASS_LABEL = new Map([
  ['rate_limit', 'Rate limited by the provider'],
  ['quota', 'Usage quota exhausted'],
  ['auth', 'Authentication was rejected'],
  ['context_limit', 'Context window limit reached'],
  ['provider', 'The provider failed the request'],
  ['process_death', 'The agent process exited mid-turn'],
]);

// errorText is SessionError.displayMessage (SessionState.swift:1001-1005):
// the agent's wording, else the class's label, else a bare statement, never
// an empty line.
function errorText(facts) {
  if (facts.errorMessage) return facts.errorMessage;
  if (facts.errorClass) return CLASS_LABEL.get(facts.errorClass) || facts.errorClass;
  return 'The session failed';
}

function errorNote(facts) {
  if (facts.state !== 'error') return null;
  const text = errorText(facts);
  const tooltip = facts.errorClass && facts.errorClass !== text ? text + '\nclass: ' + facts.errorClass : text;
  return { kind: 'error', text, tooltip, hue: BRAND.error };
}

// The waiting question shows by default: the Swift app's summaryDisplayMode
// starts at `.waiting` (SessionManager.swift:104), which opens the pill for a
// waiting session only. The headline is the pill's text and the full text
// its tooltip (SessionRowView.swift:142-153).
function questionNote(facts) {
  if (facts.state !== 'waiting') return null;
  const text = facts.questionHeadline || facts.lastAssistantText;
  if (!text) return null;
  return { kind: 'question', text, tooltip: facts.lastAssistantText || text, hue: BRAND.waiting };
}

const STILL_GOING = new Set(['working', 'waiting']);

function pressureNote(facts) {
  if (!STILL_GOING.has(facts.state)) return null;
  // The Swift check also asks for a fill above 0 (an unknown window reads
  // 0); with a threshold above 0, as this fixed one is, `>=` excludes it too.
  if (!(facts.percent >= PRESSURE_ALERT_PERCENT)) return null;
  return {
    kind: 'pressure',
    text: 'Switch to a fresh session soon',
    tooltip: 'Context window nearing limit',
    hue: BRAND['pressure-high'],
  };
}

// rowNotes is every line a matched session's row carries, possibly none.
export function rowNotes(facts) {
  return [errorNote(facts), questionNote(facts), pressureNote(facts)].filter(Boolean);
}
