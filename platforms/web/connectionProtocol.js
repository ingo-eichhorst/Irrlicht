// connectionProtocol.js — pure helpers for the multi-source WebSocket
// wire protocol (#537/#540/#600): classifying an incoming frame, detecting
// a dropped-frame sequence gap, reducing per-source connection states to
// one header dot, and normalizing a user-entered relay address into a
// ws(s):// stream URL. No DOM, no fetch, no module state.

// relayFrameKind classifies an incoming frame so the handler can branch.
// Pure; exported for tests.
export function relayFrameKind(msg) {
  if (!msg || typeof msg.type !== 'string') return 'raw';
  switch (msg.type) {
    case 'hello_ack': return 'hello_ack';
    case 'snapshot': return 'snapshot';
    case 'daemon_status': return 'daemon_status';
    case 'push': return 'push';
    default: return 'raw';
  }
}

// seqGap reports whether a stamped push seq skipped ahead of the last one
// received — the daemon (or relay) dropped frames for this client (#600).
// 0/absent means unstamped (connect snapshots, relay replays, older
// daemons) and never gaps; a backward jump is a daemon restart, not a
// gap. Pure; exported for tests.
export function seqGap(last, seq) {
  return seq > 0 && last > 0 && seq > last + 1;
}

// aggregateConnState reduces per-source states into the single header dot:
// connected wins (we're watching at least one source), then connecting,
// then reconnecting, else disconnected. Pure; exported for tests.
export function aggregateConnState(states) {
  if (!states || states.length === 0) return 'disconnected';
  if (states.includes('connected')) return 'connected';
  if (states.includes('connecting')) return 'connecting';
  if (states.includes('reconnecting')) return 'reconnecting';
  return 'disconnected';
}

// relayWsUrl normalizes a user-entered relay address into a ws(s):// stream
// URL. Accepts http(s)://, ws(s)://, or a bare host[:port], with or without
// the stream path. Pure; exported for tests. Returns '' for empty input.
export function relayWsUrl(raw) {
  let u = (raw || '').trim();
  if (!u) return '';
  u = u.replace(/^http:/i, 'ws:').replace(/^https:/i, 'wss:');
  // Defaulting a bare host to ws:// (not wss://) is intentional (SonarQube
  // javascript:S5332): the relay's documented default posture is an
  // unencrypted trusted-LAN deployment with TLS/auth as an opt-in the
  // operator adds themselves (see examples/relay/Dockerfile) — a caller who
  // wants TLS types `https://`/`wss://` and the mapping above already
  // honors that.
  if (!/^wss?:\/\//i.test(u)) u = 'ws://' + u;
  while (u.endsWith('/')) u = u.slice(0, -1);
  if (!/\/api\/v1\/sessions\/stream$/.test(u)) u += '/api/v1/sessions/stream';
  return u;
}
