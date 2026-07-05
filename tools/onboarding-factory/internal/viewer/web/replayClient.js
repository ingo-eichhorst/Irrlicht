// replayClient.js — thin transport wrappers over the daemon's
// /api/replay/* endpoints used by the Playback panel (#873, extracted
// from viewer.js's renderPlayback god-function). No DOM, no module state:
// each function issues one request and returns just what the caller needs,
// so the viewer's controller code stays about wiring rather than fetch
// shapes, and the whole replay endpoint surface lives in one place.

// startReplay POSTs the replay-start request. On success it returns the
// parsed body ({ok:true, body}); on failure it returns the status + error
// text so the caller can log non-blocking and bail (an un-recorded cell
// opened via a deep link simply has nothing to play — never a modal).
export async function startReplay(params) {
  const resp = await fetch("/api/replay/start", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify(params),
  });
  if (!resp.ok) {
    return {ok: false, status: resp.status, error: await resp.text(), body: null};
  }
  return {ok: true, status: resp.status, error: "", body: await resp.json()};
}

// replayStatus GETs the current playhead/paused/speed snapshot.
export async function replayStatus() {
  const resp = await fetch("/api/replay/status");
  return await resp.json();
}

// setReplaySpeed pushes a new speed multiplier to a running playback.
export function setReplaySpeed(speed) {
  return fetch(`/api/replay/speed?speed=${speed}`, {method: "POST"});
}

export function pauseReplay() {
  return fetch("/api/replay/pause", {method: "POST"});
}

export function resumeReplay() {
  return fetch("/api/replay/resume", {method: "POST"});
}

export function stopReplay() {
  return fetch("/api/replay/stop", {method: "POST"});
}

// seekReplay moves the playhead to an absolute offset (ms).
export function seekReplay(offsetMs) {
  return fetch(`/api/replay/seek?offset_ms=${offsetMs}`, {method: "POST"});
}
