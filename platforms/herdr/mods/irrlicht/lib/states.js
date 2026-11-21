// A row's state: the matched Irrlicht session's, or Herdr's status read onto
// Irrlicht's states when no session matched. Each state has the hue and the
// hover label the macOS app gives it.

import { BRAND } from './palette.js';

// Each state's hue, as SessionState.State.color (SessionState.swift:1314-1322).
// `unknown` is a state this panel cannot read, such as one a newer daemon
// writes; the Swift app paints it IrrHex.unknown, which is value-equal to
// `cancelled` (Tokens.swift:18), and the CSS has no --unknown token, so it
// takes --cancelled. test/palette.parity.test.js checks that every state the
// daemon declares (canonicalStates in core/domain/session/session.go) is a
// key here and in STATE_LABEL.
export const STATE_HUE = Object.freeze({
  working: BRAND.working,
  waiting: BRAND.waiting,
  ready: BRAND.ready,
  error: BRAND.error,
  unknown: BRAND.cancelled,
});

// The state icon's hover text, as SessionState.State.label
// (SessionState.swift:1384-1392).
export const STATE_LABEL = Object.freeze({
  working: 'Working',
  waiting: 'Waiting for input',
  ready: 'Ready',
  error: 'Error — the session failed',
  unknown: 'Unknown state',
});

const own = (map, key) => typeof key === 'string' && Object.prototype.hasOwnProperty.call(map, key);

// irrlichtState is a daemon state as the panel draws it: one it knows, else
// `unknown`, never a guess (the Swift app's #1797 rule: an unreadable state is
// never painted green).
export function irrlichtState(raw) {
  return own(STATE_HUE, raw) ? raw : 'unknown';
}

// What a row no Irrlicht session matched shows: Herdr's agent status read onto
// Irrlicht's states. One entry per line, with the reason:
//   working: busy, as Irrlicht's working.
//   blocked: Herdr's "needs you", which is what Irrlicht's waiting means.
//   done:    finished while nobody looked; Irrlicht calls finished ready.
//   idle:    finished and seen; also ready.
// Herdr has no failure status, so no Herdr status maps to error. Anything
// else (null, or a word a newer Herdr adds) is unknown.
const FROM_HERDR = new Map([
  ['working', 'working'],
  ['blocked', 'waiting'],
  ['done', 'ready'],
  ['idle', 'ready'],
]);

export function herdrState(status) {
  return FROM_HERDR.get(status) ?? 'unknown';
}
