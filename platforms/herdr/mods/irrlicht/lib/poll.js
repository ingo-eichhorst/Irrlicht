// One poll of the daemon, with every read injected so vitest can drive it.
//
// The address is resolved on the first poll and again after any poll that
// did not get an answer, and kept while the daemon answers. A daemon that
// restarts on another port therefore fails one poll and is found on the next;
// a second daemon that publishes a new addr file while the first still
// answers is not looked for until the first stops answering.

import { hostPortOf, resolveDaemonAddress, sessionsURL } from './address.js';
import { readSessions } from './sessions.js';

// pollOnce resolves the next panel state from the previous one:
//   { phase: 'ok', where, resolved, sessions }      the daemon answered;
//   { phase: 'unreachable', where }                 the request itself failed;
//   { phase: 'unexpected', where }                  something answered, but not
//                                                   with a sessions payload.
// `io.ladder` is resolveDaemonAddress's io; `io.fetch(url)` is Herdr's fetch.
// Only the request and the body parse are caught here: an exception anywhere
// else is the panel's own fault and propagates, so main.js can show it as one
// instead of as a stopped daemon.
export async function pollOnce(prev, io) {
  const resolved = prev?.phase === 'ok' && prev.resolved ? prev.resolved : await resolveDaemonAddress(io.ladder);
  const where = hostPortOf(resolved);

  let response;
  try {
    response = await io.fetch(sessionsURL(resolved));
  } catch {
    return { phase: 'unreachable', where };
  }
  if (!response?.ok) return { phase: 'unexpected', where };

  let payload;
  try {
    payload = await response.json();
  } catch {
    return { phase: 'unexpected', where };
  }
  const sessions = readSessions(payload);
  if (sessions === null) return { phase: 'unexpected', where };
  return { phase: 'ok', where, resolved, sessions };
}

// signature is what the panel compares to skip a redraw after a poll: the
// status line and each row's gauge and tooltip. A session no Herdr agent
// matched changes nothing on screen, so its numbers moving does not redraw.
// Herdr's own AGENTS.md says render runs again whenever its live data
// (agents, terminals, focus, theme) changes, so the rows' other fields need no
// place here.
export function signature(state, rows) {
  const gauges = (rows || []).map((r) => [r.address, r.gauge, r.tooltip]);
  return JSON.stringify([state?.phase, state?.where, state?.message, gauges]);
}
