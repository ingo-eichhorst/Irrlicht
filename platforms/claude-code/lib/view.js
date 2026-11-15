// The panel's view model: one poll's outcome turned into plain, JSON-safe
// data the hooks module keeps in `$.state` and draws from. Every way a poll
// can fail ends in the same "not reachable" view, and nothing here throws.

import { formatUsd } from './format.js';
import { bucketChips, quotaView, selectBucket } from './quota.js';
import { dayCostUSD, flattenSessions, sessionSections } from './sessions.js';

// unreachableView is what the panel shows when it could not get a usable
// answer from the daemon at `hostPort`, with the reason in a few words.
export function unreachableView(hostPort, reason) {
  return { status: 'unreachable', hostPort, reason };
}

// buildView shapes a parsed /api/v1/sessions payload. `hereSessionId` is this
// Claude Code session's id (`$.session.id()`), `nowMs` the clock the quota
// staleness and reset countdowns are measured against.
export function buildView(payload, { hostPort, hereSessionId, nowMs }) {
  const sessions = flattenSessions(payload);
  const current = hereSessionId ? sessions.find(s => s.session_id === hereSessionId) : undefined;
  const bucket = selectBucket(bucketChips(sessions, nowMs), current);
  return {
    status: 'ok',
    hostPort,
    quota: quotaView(bucket, nowMs),
    sections: sessionSections(payload, hereSessionId),
    sessionCount: sessions.length,
    dayCost: formatUsd(dayCostUSD(payload)),
  };
}

// viewFromResponse turns one `$.http.fetch` outcome into a view: `response`
// is what the fetch resolved with, `error` what it rejected with (a refused
// connection, an organization's web-fetch policy saying no). Non-2xx, a body
// that is not JSON, and JSON that is not a sessions payload all read as "not
// reachable": the panel only draws what it can trust came from the daemon.
export function viewFromResponse({ response, error, hostPort, hereSessionId, nowMs }) {
  if (error !== undefined || !response) {
    return unreachableView(hostPort, 'no connection');
  }
  if (!response.ok) {
    return unreachableView(hostPort, 'HTTP ' + response.status);
  }
  let payload;
  try {
    payload = JSON.parse(response.text);
  } catch {
    return unreachableView(hostPort, 'not a JSON answer');
  }
  // sessionsResponse.Groups is tagged `json:"groups"` without omitempty
  // (core/cmd/irrlichd/handlers.go), so the key is always there: an array, or
  // null for a nil slice. Anything else is not this endpoint's answer.
  if (payload === null || typeof payload !== 'object' || Array.isArray(payload) ||
      !(payload.groups === null || Array.isArray(payload.groups))) {
    return unreachableView(hostPort, 'not an Irrlicht sessions answer');
  }
  try {
    return buildView(payload, { hostPort, hereSessionId, nowMs });
  } catch {
    // A payload shaped in a way this panel never anticipated. Say so rather
    // than draw half of it.
    return unreachableView(hostPort, 'unexpected sessions answer');
  }
}
