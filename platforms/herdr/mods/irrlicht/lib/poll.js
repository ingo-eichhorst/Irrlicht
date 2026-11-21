// Polling the daemon, with every read and the clock injected so vitest can
// drive it. main.js only starts the timer and draws what a step returns.

import { hostPortOf, resolveDaemonAddress, sessionsURL } from './address.js';
import { readSessions } from './sessions.js';

// STALL_MS is how long a request may stay unanswered before the panel stops
// showing its last numbers. Herdr's `fetch` takes no timeout or abort signal
// (ShellFetchOptions in gpui-kit.d.ts has only method, headers and body), so a
// daemon that accepts the connection and never answers (stopped with SIGSTOP,
// or stuck in the handler) would otherwise leave the old gauges up as if
// current. The Claude Code panel uses the same 10 s.
export const STALL_MS = 10_000;

// RERESOLVE_MS bounds how often the address is looked up again while the
// daemon does not answer. Each lookup runs a /bin/sh test of the addr file,
// and a panel left open with Irrlicht stopped would otherwise spawn one every
// poll. The last address keeps being asked every poll meanwhile, so a daemon
// back on the same port is found as fast as before; one back on another port
// is found within RERESOLVE_MS.
export const RERESOLVE_MS = 30_000;

// pollOnce resolves the next panel state from the previous one:
//   { phase: 'ok', where, resolved, resolvedAt, sessions }  the daemon answered;
//   { phase: 'unreachable', where, resolved, resolvedAt }   the request failed;
//   { phase: 'unexpected', where, resolved, resolvedAt }    something answered,
//                                                           but not with a
//                                                           sessions payload.
// The address is kept while the daemon answers. Once a poll has failed, the
// next one looks it up again if the last lookup is RERESOLVE_MS old, and later
// ones at most every RERESOLVE_MS.
// `io.ladder` is resolveDaemonAddress's io, `io.fetch(url)` Herdr's fetch,
// `io.now()` the clock. Only the request and the body parse are caught here:
// an exception anywhere else is the panel's own fault and propagates, so it is
// shown as one instead of as a stopped daemon.
export async function pollOnce(prev, io) {
  const kept = await addressFor(prev, io);
  let response;
  try {
    response = await io.fetch(sessionsURL(kept.resolved));
  } catch {
    return { phase: 'unreachable', ...kept };
  }
  const sessions = readSessions(await bodyOf(response));
  if (sessions === null) return { phase: 'unexpected', ...kept };
  return { phase: 'ok', ...kept, sessions };
}

// addressFor is the address this poll asks, kept from `prev` or looked up
// again (lookUpAgain), with when it was looked up.
async function addressFor(prev, io) {
  const now = io.now();
  if (!lookUpAgain(prev, now)) return { where: prev.where, resolved: prev.resolved, resolvedAt: prev.resolvedAt };
  const resolved = await resolveDaemonAddress(io.ladder);
  return { where: hostPortOf(resolved), resolved, resolvedAt: now };
}

function lookUpAgain(prev, now) {
  if (!prev || !prev.resolved) return true;
  return prev.phase !== 'ok' && now - prev.resolvedAt >= RERESOLVE_MS;
}

// bodyOf is a successful response's JSON, or null for an error status or a
// body that is not JSON (readSessions then answers null: "unexpected").
async function bodyOf(response) {
  if (!response || !response.ok) return null;
  try {
    return await response.json();
  } catch {
    return null;
  }
}

// stallState is the state to show while a request is still in flight:
// `{ phase: 'stalled', where }` once it has run STALL_MS, else null (keep
// what is shown).
export function stallState(state, startedAt, now) {
  if (state.phase === 'stalled' || !(now - startedAt >= STALL_MS)) return null;
  return { phase: 'stalled', where: state.where };
}

// createPoller owns the one request allowed in flight. Each timer tick calls
// step(), which answers one of:
//   { request }  a request went out; the promise resolves to the next state
//                (a fault inside the panel becomes { phase: 'error', message });
//   { stalled }  the request in flight just passed STALL_MS;
//   {}           nothing to do this tick.
// No second request is sent behind a pending one; when a stalled request
// settles, its result replaces the stalled state as usual.
export function createPoller(io) {
  let state = { phase: 'starting' };
  let since = null;
  return {
    step() {
      const now = io.now();
      if (since !== null) {
        const stalled = stallState(state, since, now);
        if (stalled) state = stalled;
        return stalled ? { stalled } : {};
      }
      since = now;
      const request = pollOnce(state, io)
        .catch((e) => ({ phase: 'error', message: String(e?.message ?? e) }))
        .then((next) => {
          since = null;
          state = next;
          return next;
        });
      return { request };
    },
  };
}
