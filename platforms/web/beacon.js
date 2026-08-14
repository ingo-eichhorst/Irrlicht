// Irrlicht Beacon — pairing flow + push settings UI for the phone-facing PWA
// (docs/mobile-notifications-arc42.md §6.1, §8.1, §8.3, ADR-3).
//
// Everything here is feature-detected (arc42 §5.2 additivity contract): the
// Beacon section renders only when this page's own origin answers
// /api/v1/push/info with {enabled:true}. A daemon-served dashboard and an old
// relay both 404 that endpoint and show nothing new; an --auth off relay
// answers {enabled:false} and ALSO renders nothing in P1 — the operator-facing
// fix (enable auth) lives in the relay docs, and the dashboard stays clean
// rather than growing an error surface for a state only an operator can fix.
//
// This module is also the only place in the web tree allowed to register the
// service worker (arc42 §5.2: lazily, from the pairing flow and its §8.3
// self-heal only — plain dashboard usage never installs a worker), and the
// only one that talks to it. sw-contract.test.js pins both properties.

import { relayWsUrl } from './connectionProtocol.js';

const DEVICE_TOKEN_KEY = 'beaconDeviceToken';

// The vocabulary this module and sw.js exchange. sw.js is a classic script
// with nothing to import from (its own header says why), so it spells the same
// four values as literals and sw-contract.test.js pins the two copies
// together — a rename on one side would otherwise leave the other silently
// unheard, which is the failure mode arc42 §8.3 exists to forbid.
export const BEACON_MESSAGES = Object.freeze({
  liveSessions: 'beacon-live-sessions',
  ledgerGet: 'beacon-ledger-get',
  openSession: 'beacon-open-session',
  sessionHashKey: 'beacon-session',
});

// A worker that is asleep, or older than this page, may answer a ledger read
// with nothing at all. The caller is composing a "that session is not here"
// notice (R6), so waiting forever would reproduce the very tap-does-nothing
// failure the notice exists to prevent.
const LEDGER_REPLY_TIMEOUT_MS = 2000;

// base64url → Uint8Array for pushManager.subscribe's applicationServerKey.
// Handing subscribe the raw base64url string is the classic silent failure —
// some engines accept it and then mis-key the subscription — so the conversion
// is pinned byte-for-byte by test.
export function urlBase64ToUint8Array(base64url) {
  const s = String(base64url || '');
  const pad = '='.repeat((4 - (s.length % 4)) % 4);
  const b64 = (s + pad).replace(/-/g, '+').replace(/_/g, '/');
  const raw = atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// "ab12cd34" → "AB12-CD34" — the display form of a freshly minted pairing
// code (arc42 ADR-3: short-lived, possession-proving, typed on the phone).
export function formatPairingCode(code) {
  const c = normalizePairingCode(code);
  return c.length > 4 ? c.slice(0, 4) + '-' + c.slice(4) : c;
}

// What the user typed → what the relay minted: strip the display hyphen (and
// anything else stray), uppercase.
export function normalizePairingCode(entered) {
  return String(entered || '').replace(/[^0-9A-Za-z]/g, '').toUpperCase();
}

export function countdownText(seconds) {
  const s = Math.max(0, Math.floor(seconds));
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
}

let relayTokenAccessor = () => '';
let countdownTimer = null;
// The Settings → Sources seam (see initBeacon): { read, write } over the same
// `settings` object and the same persist + reconnect path the Settings panel
// uses, never localStorage keys of this module's own.
let liveView = null;
let openSessionHandler = null;
let notificationTargetsWired = false;

// Entry point, called from irrlicht.js at wiring time.
//   · `opts.relayToken` — accessor for the Settings → Sources relay token (the
//     authed client token that may mint pairing codes, arc42 §8.1); the phone
//     side needs none.
//   · `opts.liveView` — { read(), write(next) } over the dashboard's own
//     Sources settings. Pairing configures the phone's live view through it
//     (arc42 §6.2, ADR-9); going through the dashboard's mechanism rather than
//     writing localStorage by hand is what keeps a later change to how
//     settings are stored from orphaning it.
//   · `opts.openSession` — where a notification tap lands (R6).
export async function initBeacon(opts = {}) {
  relayTokenAccessor = opts.relayToken || (() => '');
  liveView = opts.liveView || null;
  openSessionHandler = opts.openSession || null;
  if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
  // Before the feature-detection fetch and before the section check: a tap
  // that opened this app cold is waiting on it, and it is not part of the
  // settings UI. Gated on the stored device token all the same — only a paired
  // phone can have been sent here by a notification, so an unpaired dashboard
  // wires nothing (arc42 §5.2).
  wireNotificationTargets();
  const section = document.getElementById('beacon-section');
  if (!section) return;
  const info = await fetchPushInfo();
  if (!info) return; // stays hidden: daemon-served, old relay, or push disabled
  await renderSection(section, info);
}

async function fetchPushInfo() {
  // Same-origin, relative URL: the push endpoints live on whichever relay
  // served this page — never on the Settings relayUrl, which may point at a
  // different host than the serving origin.
  let r = null;
  try {
    r = await fetch('api/v1/push/info');
  } catch (e) {
    return null;
  }
  if (!r || !r.ok) return null;
  let info = null;
  try {
    info = await r.json();
  } catch (e) {
    return null;
  }
  if (!info || info.enabled !== true) return null;
  return info;
}

async function renderSection(section, info) {
  section.innerHTML = '';
  section.hidden = false;
  const h = document.createElement('h2');
  h.textContent = 'Irrlicht Beacon';
  section.appendChild(h);
  renderMacSide(section, info);
  const phone = el('div', 'beacon-phone');
  phone.id = 'beacon-phone';
  section.appendChild(phone);
  const deviceToken = localStorage.getItem(DEVICE_TOKEN_KEY);
  // Awaited: the health panel runs the §8.3 self-heal, and a caller that
  // moved on before it finished would be looking at a pre-repair verdict.
  if (deviceToken) await renderHealthPanel(phone, info, deviceToken);
  else renderPairEntry(phone, info);
}

// ── Mac side: mint a one-time code for a phone to type (arc42 §6.1) ──────

function renderMacSide(section, info) {
  const clientToken = relayTokenAccessor() || '';
  // Minting requires an authed client token (arc42 §8.1: the code inherits
  // its workspace from the token) — without one there is nothing to offer.
  if (!clientToken) return;
  const row = el('div', 'settings-action-row');
  const text = el('span', 'settings-label-text');
  const title = el('span', 'settings-title');
  title.textContent = 'Pair a phone';
  const hint = el('span', 'settings-hint');
  hint.textContent = 'Mints a one-time code (10 min, single use) to type into this page on the phone.';
  text.appendChild(title);
  text.appendChild(hint);
  const btn = el('button', 'settings-action-btn');
  btn.type = 'button';
  btn.id = 'beacon-mint';
  btn.textContent = 'Pair a phone…';
  row.appendChild(text);
  row.appendChild(btn);
  const out = el('div', 'beacon-mint-out');
  out.id = 'beacon-mint-out';
  btn.addEventListener('click', () => mintPairingCode(out, clientToken));
  section.appendChild(row);
  section.appendChild(out);
}

async function mintPairingCode(out, clientToken) {
  out.textContent = 'Minting code…';
  let r = null;
  try {
    r = await fetch('api/v1/push/pairings', {
      method: 'POST',
      headers: { Authorization: 'Bearer ' + clientToken },
    });
  } catch (e) {
    r = null;
  }
  if (!r || !r.ok) {
    out.textContent = 'Could not mint a pairing code' + (r ? ' (relay answered ' + r.status + ')' : '') + '.';
    return;
  }
  let minted = null;
  try {
    minted = await r.json();
  } catch (e) {
    minted = null;
  }
  if (!minted || !minted.code) {
    // Anything in front of the relay can answer 200 with an HTML error page;
    // a throw here would leave the row reading "Minting code…" for good.
    out.textContent = 'Could not mint a pairing code — the relay answered something this page could not read.';
    return;
  }
  out.innerHTML = '';
  const codeEl = el('div', 'beacon-code');
  codeEl.id = 'beacon-code';
  codeEl.textContent = formatPairingCode(minted.code);
  const expiry = el('div', 'beacon-code-expiry');
  expiry.id = 'beacon-code-expiry';
  const url = el('div', 'beacon-code-url');
  // P1 is paste/type only — no QR, no vendored encoder (arc42 risk 7: an
  // 8-char ambiguity-free code beats auditing vendored encoder code; QR is a
  // follow-up). So the thing to carry to the phone is spelled out here.
  url.textContent = 'On the phone, open ' + location.origin + location.pathname + ' and enter this code.';
  out.appendChild(codeEl);
  out.appendChild(expiry);
  out.appendChild(url);
  startCountdown(expiry, minted.expires_in);
}

function startCountdown(expiryEl, expiresInSeconds) {
  if (countdownTimer) clearInterval(countdownTimer);
  const deadline = Date.now() + Number(expiresInSeconds || 0) * 1000;
  const tick = () => {
    const left = Math.round((deadline - Date.now()) / 1000);
    if (left <= 0) {
      expiryEl.textContent = 'expired — mint a new code';
      clearInterval(countdownTimer);
      countdownTimer = null;
      return;
    }
    expiryEl.textContent = 'expires in ' + countdownText(left);
  };
  tick();
  countdownTimer = setInterval(tick, 1000);
}

// ── Phone side: code entry → device token → subscription (arc42 §6.1) ────

function renderPairEntry(phone, info) {
  phone.innerHTML = '';
  // ADR-3: iOS partitions Safari-tab storage from installed-app storage — a
  // pairing done in the browser tab vanishes from the installed app, so steer
  // touch-device users to install first.
  if (isTouchDevice() && !isStandalone()) {
    const note = el('div', 'beacon-hint');
    note.id = 'beacon-standalone-hint';
    note.textContent = 'Add this page to your Home Screen first, then pair inside the installed app — iOS delivers push only there.';
    phone.appendChild(note);
  }
  const row = el('div', 'beacon-pair-row');
  const input = document.createElement('input');
  input.type = 'text';
  input.id = 'beacon-code-input';
  input.className = 'settings-text-input beacon-code-input';
  input.placeholder = 'XXXX-XXXX';
  input.autocomplete = 'off';
  input.spellcheck = false;
  const btn = el('button', 'settings-action-btn');
  btn.type = 'button';
  btn.id = 'beacon-pair-submit';
  btn.textContent = 'Pair this phone';
  row.appendChild(input);
  row.appendChild(btn);
  const status = el('div', 'beacon-status');
  status.id = 'beacon-pair-status';
  btn.addEventListener('click', () => pairThisPhone(phone, info, input.value, status));
  phone.appendChild(row);
  phone.appendChild(status);
}

async function pairThisPhone(phone, info, enteredCode, status) {
  const code = normalizePairingCode(enteredCode);
  if (!code) {
    status.textContent = 'Enter the code shown on the Mac.';
    return;
  }
  if (!('serviceWorker' in navigator) || typeof Notification === 'undefined') {
    status.textContent = 'This browser cannot receive push notifications — nothing to pair.';
    return;
  }
  // Permission is asked FIRST, still inside the click's transient user
  // activation. Downstream of a network round-trip plus a worker registration
  // the activation may already have expired, and on iOS — the one platform
  // where the installed-app path is mandatory (arc42 ADR-2, constraint C3) —
  // an expired activation means the prompt never appears at all. Asking here
  // also means a refusal costs nothing: the code is single-use (ADR-3) and is
  // not spent until the exchange below.
  let perm = 'denied';
  try {
    perm = await Notification.requestPermission();
  } catch (e) {
    perm = 'denied';
  }
  if (perm !== 'granted') {
    status.textContent = 'Notifications are not allowed for this app — turn them on in the phone’s settings, then press Pair again. Your code is still unused.';
    return;
  }
  status.textContent = 'Pairing…';
  let r = null;
  try {
    r = await fetch('api/v1/push/pair', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code, label: deviceLabel() }),
    });
  } catch (e) {
    r = null;
  }
  if (!r) {
    status.textContent = 'Could not reach the relay — check the connection and try again.';
    return;
  }
  if (r.status === 429) {
    // Rate-limited (ADR-3: codes are rate-limited relay-side). Distinct from
    // a rejected code on purpose: here waiting helps, retyping does not.
    status.textContent = 'Too many attempts — try again in a minute.';
    return;
  }
  if (!r.ok) {
    // The relay answers a deliberately uniform 401 for wrong, expired and
    // already-used codes alike — mirror that with one message.
    status.textContent = 'Code not accepted — mistyped, expired, or already used. Mint a fresh one on the Mac.';
    return;
  }
  let paired = null;
  try {
    paired = await r.json();
  } catch (e) {
    paired = null;
  }
  if (!paired || !paired.token) {
    status.textContent = 'The relay accepted the code but sent no device token — mint a fresh code on the Mac and try again.';
    return;
  }
  // Store the durable identity first (arc42 §8.1: the device token survives;
  // the subscription is only a delivery address). If anything below fails,
  // the §8.3 self-heal retries the subscription on next open.
  localStorage.setItem(DEVICE_TOKEN_KEY, paired.token);
  // …and configure the live view from the same identity, before the
  // subscription is attempted: a phone whose subscribe fails still watches the
  // relay, which is what keeps its ledger and badge honest until the §8.3
  // self-heal lands the delivery address.
  configureLiveView(paired.token);
  const ok = await subscribeForPush(paired.token, paired.vapid_public_key || info.vapid_public_key, status);
  if (!ok) return;
  await renderHealthPanel(phone, info, paired.token);
}

// Permission is already granted by the time this runs (see pairThisPhone).
// Every step here can throw on a real device — a worker that never activates,
// a push service that refuses the subscription, a relay that has gone away —
// and the code that got the user this far is already spent, so a throw must
// become a verdict rather than a UI frozen on "Pairing…".
async function subscribeForPush(deviceToken, vapidPublicKey, status) {
  try {
    const reg = await readyRegistration();
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(vapidPublicKey),
    });
    const posted = await postSubscription(deviceToken, sub);
    if (posted && posted.status === 401) {
      if (status) status.textContent = revokedText();
      return false;
    }
    if (!posted || !posted.ok) {
      if (status) status.textContent = 'Paired, but the relay did not record this phone’s subscription' + statusSuffix(posted) + '. Reopen this app to retry.';
      return false;
    }
    return true;
  } catch (e) {
    if (status) {
      status.textContent = 'Paired, but this phone could not subscribe for push (' + errorText(e) + '). That code is spent — reopen this app and it retries on its own, or mint a fresh code on the Mac.';
    }
    return false;
  }
}

// The ONLY registration call in the web tree (arc42 §5.2): lazy, reached from
// the pairing flow and its §8.3 self-heal only.
export function ensureServiceWorker() {
  return navigator.serviceWorker.register('./sw.js');
}

// How long to wait for the registered worker to reach `activated`: generous,
// because an installing worker on a cold phone is slow, but finite, because a
// worker that never activates would otherwise wedge the pairing UI on
// "Pairing…" with the code already spent.
const ACTIVATION_TIMEOUT_MS = 15000;

// register() resolves while the worker is still INSTALLING, and
// pushManager.subscribe() rejects for as long as the registration has no
// active worker — so every subscribe in this module goes through here rather
// than through ensureServiceWorker directly (arc42 §6.1 subscribes after the
// worker is up).
async function readyRegistration() {
  await ensureServiceWorker();
  let timer = null;
  try {
    return await Promise.race([
      navigator.serviceWorker.ready,
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error('the service worker did not start')), ACTIVATION_TIMEOUT_MS);
      }),
    ]);
  } finally {
    if (timer !== null) clearTimeout(timer);
  }
}

function postSubscription(deviceToken, sub) {
  return fetch('api/v1/push/subscriptions', {
    method: 'POST',
    headers: {
      Authorization: 'Bearer ' + deviceToken,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(sub && typeof sub.toJSON === 'function' ? sub.toJSON() : sub),
  }).catch(() => null);
}

function statusSuffix(r) {
  return r ? ' (relay answered ' + r.status + ')' : ' — the relay was unreachable';
}

function errorText(e) {
  return (e && (e.message || e.name)) || 'unknown error';
}

// A 401 on a device-token route is not an outage: the relay operator revoked
// this phone (arc42 §8.1 — revoking the token drops its subscription too), so
// no amount of retrying helps and the only way back is a fresh pairing.
function revokedText() {
  return 'This phone is no longer paired — its access was revoked on the relay. Pair again with a fresh code from the Mac.';
}

// §8.3 self-heal, in two independent halves that the original single early
// return collapsed into one:
//   · the browser lost the subscription (iOS invalidates silently) — subscribe
//     again, which also covers a relay whose VAPID keypair was lost and
//     reminted (§8.6), since the fresh key arrives via push/info;
//   · the browser still holds one but the RELAY's registry lost it (restored
//     backup, pruned entry, a registry deleted per §8.6) — re-POST the address
//     the browser already has. Without this half that state is permanent: the
//     panel keeps advising a reopen that returns early every time.
// The device token is the durable identity that makes either re-registration
// safe (§8.1); a 401 on the way says the operator revoked this phone, which is
// the one outcome retrying cannot fix.
const HEAL_NONE = 'none';
const HEAL_REPAIRED = 'repaired';
const HEAL_REVOKED = 'revoked';

async function selfHeal(info, deviceToken, relayRegistered) {
  if (!('serviceWorker' in navigator)) return HEAL_NONE;
  let reg = null;
  let existing = null;
  try {
    // A paired device owns its worker — registering here is the self-heal, not
    // an eager install (§5.2's lazy rule gates on the pairing, which the stored
    // token proves happened).
    reg = await readyRegistration();
    existing = await reg.pushManager.getSubscription();
  } catch (e) {
    return HEAL_NONE; // leave it for the next open; the panel states the gap
  }
  if (existing && relayRegistered) return HEAL_NONE;
  let sub = existing;
  if (!sub) {
    try {
      sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlBase64ToUint8Array(info.vapid_public_key),
      });
    } catch (e) {
      return HEAL_NONE;
    }
  }
  const posted = await postSubscription(deviceToken, sub);
  if (posted && posted.status === 401) return HEAL_REVOKED;
  if (!posted || !posted.ok) return HEAL_NONE;
  return HEAL_REPAIRED;
}

// ── The phone's own live view (arc42 §6.2, ADR-9) ────────────────────────
//
// Pairing does not only register a delivery address; it also makes this phone
// an ordinary client of the relay it just paired with. That is possible
// because a device token is a full client token (§8.1) — an ordinary
// TokenRecord every client route accepts — and it is NECESSARY because of
// §8.4: nothing is pushed on `* → working`, and iOS forbids a silent push, so
// a phone fed only by push learns that sessions need attention and never that
// one stopped. The badge could climb and never fall. The live view is the
// correcting signal (§8.5).

// sameRelay compares two relay addresses the way the dashboard's own source
// list does — through relayWsUrl, so `https://relay.example`, the same with a
// trailing slash, and the full stream URL are one address rather than three.
function sameRelay(a, b) {
  return !!a && !!b && relayWsUrl(a) === relayWsUrl(b);
}

// The live view is OURS when it watches this relay with this phone's own
// device token. Anything else — pointed elsewhere, switched off, carrying a
// token the user typed — belongs to the user, and this module neither
// publishes through it nor takes it down.
//
// `origin` is a parameter rather than a read of `location.origin`, because
// this predicate is half of liveViewNoteText, which is exported as a pure
// function and took an origin its verdict then ignored — the two disagreed
// for any origin but the page's own, and the disagreement was invisible in
// production, where they are always the same string.
function liveViewIsOurs(current, deviceToken, origin) {
  return !!(current && deviceToken
    && current.enableRelaySource
    && sameRelay(current.relayUrl, origin)
    && current.relayToken === deviceToken);
}

// Called on a successful pair, and only then. Returns what happened so the
// health panel can say it out loud.
function configureLiveView(deviceToken) {
  if (!liveView) return 'no-seam';
  const current = liveView.read();
  // Do not clobber a live view aimed at a different relay: the phone may
  // legitimately watch another one, and overwriting it would both lose that
  // configuration and hand this phone's device token to a host it was not
  // minted for. Left alone, and the health panel says so.
  if (current.relayUrl && !sameRelay(current.relayUrl, location.origin)) return 'elsewhere';
  liveView.write({
    enableRelaySource: true,
    // location.origin, not the page URL: relayWsUrl maps https:→wss: and
    // appends the stream path itself (connectionProtocol.js).
    relayUrl: location.origin,
    relayToken: deviceToken,
  });
  return 'configured';
}

// Unpairing undoes it symmetrically. Without this the phone keeps a live view
// holding a revoked token: the socket does not reconnect-loop — a 4401 close
// parks the source in `unauthorized` with no retry scheduled (the ev.code
// branch in irrlicht.js connectSource) — but it does leave a source that can
// never connect, and a Sources panel configured by something the user just
// switched off.
function releaseLiveView(deviceToken) {
  if (!liveView) return false;
  if (!liveViewIsOurs(liveView.read(), deviceToken, location.origin)) return false;
  liveView.write({ enableRelaySource: false, relayUrl: '', relayToken: '' });
  return true;
}

// The health panel's second line. Empty when the live view is ours, because
// then there is nothing to explain.
export function liveViewNoteText(current, deviceToken, origin) {
  if (!current) return '';
  if (liveViewIsOurs(current, deviceToken, origin)) return '';
  if (current.relayUrl && !sameRelay(current.relayUrl, origin)) {
    return 'Live view is pointed at ' + current.relayUrl + ', not this relay — left as you set it. '
      + 'Notifications still arrive, but this app cannot see when a session stops needing you, so the badge only counts up.';
  }
  return 'Live view is off for this relay — notifications still arrive, but the badge only counts up until you open the app.';
}

// ── Talking to the service worker (arc42 §8.5, R6) ───────────────────────

// getRegistration rather than navigator.serviceWorker.ready: `ready` never
// settles when there is no registration at all, and a promise that hangs is
// the one failure this whole slice is about. A worker still installing has a
// null `active` — that publish is skipped and the next one lands.
async function activeWorker() {
  if (!('serviceWorker' in navigator)) return null;
  if (!localStorage.getItem(DEVICE_TOKEN_KEY)) return null; // unpaired: no worker of ours
  try {
    const reg = await navigator.serviceWorker.getRegistration();
    return (reg && reg.active) || null;
  } catch (e) {
    return null;
  }
}

// Publish the dashboard's live session list into the worker's ledger (§8.5).
// Gated on the live view being ours, because the ledger's key space is the
// paired relay's bare session ids: a live view watching a DIFFERENT relay
// would fold foreign sessions in and delete the paired relay's rows as absent.
// Returns whether it published, so the caller (and a test) can tell.
export async function publishLedgerSnapshot(sessions) {
  const deviceToken = localStorage.getItem(DEVICE_TOKEN_KEY);
  if (!deviceToken || !liveView) return false;
  if (!liveViewIsOurs(liveView.read(), deviceToken, location.origin)) return false;
  const worker = await activeWorker();
  if (!worker) return false;
  try {
    worker.postMessage({ type: BEACON_MESSAGES.liveSessions, sessions: sessions || [] });
  } catch (e) {
    return false;
  }
  return true;
}

// Read one session's last-known state back out of the ledger (§8.5). Answers
// null for "no entry" and for "could not ask" alike: both mean the caller has
// no last-known state to show, and it says so either way.
export async function ledgerEntry(sessionId) {
  const worker = await activeWorker();
  if (!worker || !sessionId || typeof MessageChannel === 'undefined') return null;
  return new Promise((resolve) => {
    const channel = new MessageChannel();
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try { channel.port1.close(); } catch (e) { /* already gone with its page */ }
      resolve(value);
    };
    const timer = setTimeout(() => finish(null), LEDGER_REPLY_TIMEOUT_MS);
    channel.port1.onmessage = (event) => finish((event.data && event.data.entry) || null);
    try {
      worker.postMessage(
        { type: BEACON_MESSAGES.ledgerGet, session_id: sessionId },
        [channel.port2],
      );
    } catch (e) {
      finish(null);
    }
  });
}

// sessionFromHash reads the deep-link target out of a URL fragment. The worker
// uses the fragment for a COLD open, where a postMessage would race the page's
// own module init; a focused window gets the message instead. Pure; exported
// for tests.
export function sessionFromHash(hash) {
  const raw = String(hash || '').replace(/^#/, '');
  for (const part of raw.split('&')) {
    const eq = part.indexOf('=');
    if (eq === -1 || part.slice(0, eq) !== BEACON_MESSAGES.sessionHashKey) continue;
    const value = part.slice(eq + 1);
    try {
      return decodeURIComponent(value);
    } catch (e) {
      return value; // a malformed escape is still a better target than none
    }
  }
  return '';
}

function wireNotificationTargets() {
  if (!openSessionHandler) return;
  if (!localStorage.getItem(DEVICE_TOKEN_KEY)) return;
  if (!notificationTargetsWired && 'serviceWorker' in navigator
      && typeof navigator.serviceWorker.addEventListener === 'function') {
    navigator.serviceWorker.addEventListener('message', (event) => {
      const msg = event.data || {};
      if (msg.type === BEACON_MESSAGES.openSession && msg.session_id) {
        openSessionHandler(msg.session_id);
      }
    });
    notificationTargetsWired = true;
  }
  const target = sessionFromHash(location.hash);
  if (!target) return;
  // Consumed: a reload is not a second tap, and the fragment is a message
  // rather than state.
  try {
    history.replaceState(null, '', location.pathname + location.search);
  } catch (e) {
    // A browser that refuses the rewrite still gets the selection below.
  }
  openSessionHandler(target);
}

// ── Health panel + unpair (arc42 §8.3: doubt must be answerable) ─────────

// Reads the relay's own view of this phone. The three outcomes are kept apart
// because they need different advice: `revoked` (401) is terminal until the
// user re-pairs, `unreachable` is transient, and a body is a verdict.
async function fetchSubscriptionStatus(deviceToken) {
  let r = null;
  try {
    r = await fetch('api/v1/push/subscriptions', {
      headers: { Authorization: 'Bearer ' + deviceToken },
    });
  } catch (e) {
    return { unreachable: true };
  }
  if (r && r.status === 401) return { revoked: true };
  if (!r || !r.ok) return { unreachable: true };
  try {
    return { body: await r.json() };
  } catch (e) {
    return { unreachable: true };
  }
}

// The panel's one line of copy. `last_delivery` is an OBJECT on the wire —
// {at, ok, detail}, see handleSubscriptionStatus in
// core/cmd/irrlichtrelay/push_handlers.go — so it is read field by field;
// concatenating it renders "[object Object]" and makes a success and a
// failure indistinguishable, which is the opposite of what §8.3 asks of this
// surface.
export function healthLineText(s) {
  if (!s || s.registered !== true) {
    return 'Paired, but push is not registered — reopen this app to re-subscribe.';
  }
  return 'Push registered via ' + (s.endpoint_host || 'the push service') + ' — ' + deliveryText(s.last_delivery) + '.';
}

function deliveryText(d) {
  // Delivery health is RAM-only on the relay (§8.6): absent means "nothing
  // sent since the relay started", which is not a claim that anything failed.
  if (!d || typeof d !== 'object') return 'no delivery attempted since the relay started';
  const when = timeText(d.at);
  if (d.ok) return 'last delivery ' + when + ' succeeded';
  return 'last delivery ' + when + ' failed' + (d.detail ? ' (' + d.detail + ')' : '');
}

function timeText(atSeconds) {
  const n = Number(atSeconds);
  if (!Number.isFinite(n) || n <= 0) return 'at an unknown time';
  return 'at ' + new Date(n * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

async function renderHealthPanel(phone, info, deviceToken) {
  phone.innerHTML = '';
  const line = el('div', 'beacon-health');
  line.id = 'beacon-health-line';
  line.textContent = 'Paired — checking delivery status…';
  // The live view is half of what pairing configured (§6.2), so the panel that
  // answers doubt answers for it too — silence here would leave a badge that
  // only counts up looking like a badge that is broken.
  const liveLine = el('div', 'beacon-health');
  liveLine.id = 'beacon-live-view-line';
  const note = liveView ? liveViewNoteText(liveView.read(), deviceToken, location.origin) : '';
  liveLine.textContent = note;
  liveLine.hidden = !note;
  const btn = el('button', 'settings-action-btn');
  btn.type = 'button';
  btn.id = 'beacon-unpair';
  btn.textContent = 'Unpair';
  btn.addEventListener('click', () => unpair(phone, info, deviceToken));
  phone.appendChild(line);
  phone.appendChild(liveLine);
  phone.appendChild(btn);

  let status = await fetchSubscriptionStatus(deviceToken);
  if (status.revoked) return renderRevoked(phone, info, deviceToken);
  // The self-heal runs BEFORE the verdict is painted. Reading the status
  // first and repairing afterwards is what made the panel advise reopening an
  // app that had just repaired itself.
  const healed = await selfHeal(info, deviceToken, !!(status.body && status.body.registered === true));
  if (healed === HEAL_REVOKED) return renderRevoked(phone, info, deviceToken);
  if (healed === HEAL_REPAIRED) {
    status = await fetchSubscriptionStatus(deviceToken);
    if (status.revoked) return renderRevoked(phone, info, deviceToken);
  }
  if (status.unreachable) {
    line.textContent = 'Paired — delivery status unavailable right now.';
    return;
  }
  line.textContent = healthLineText(status.body);
}

// A revoked phone cannot heal: the relay dropped its subscription along with
// the token (§8.1), so the only route back is a fresh pairing code.
function renderRevoked(phone, info, deviceToken) {
  phone.innerHTML = '';
  const line = el('div', 'beacon-health');
  line.id = 'beacon-health-line';
  line.textContent = revokedText();
  const btn = el('button', 'settings-action-btn');
  btn.type = 'button';
  btn.id = 'beacon-repair';
  btn.textContent = 'Pair again';
  btn.addEventListener('click', () => {
    // The revoked token is dead on every route, live view included (§8.1) —
    // let go of both together or the Sources panel keeps a source that can
    // only ever answer 4401.
    releaseLiveView(deviceToken);
    localStorage.removeItem(DEVICE_TOKEN_KEY);
    renderPairEntry(phone, info);
  });
  phone.appendChild(line);
  phone.appendChild(btn);
}

async function unpair(phone, info, deviceToken) {
  try {
    await fetch('api/v1/push/subscriptions', {
      method: 'DELETE',
      headers: { Authorization: 'Bearer ' + deviceToken },
    });
  } catch (e) {
    // Forget locally regardless — the relay-side record dies with the token
    // (arc42 §8.1 revocation coupling).
  }
  try {
    if ('serviceWorker' in navigator) {
      const reg = await navigator.serviceWorker.getRegistration();
      const sub = reg && (await reg.pushManager.getSubscription());
      if (sub) await sub.unsubscribe();
    }
  } catch (e) {
    // Best effort — a dangling OS subscription without a relay record
    // delivers nothing.
  }
  // Symmetric with pairing: what it configured, this takes back down.
  releaseLiveView(deviceToken);
  localStorage.removeItem(DEVICE_TOKEN_KEY);
  renderPairEntry(phone, info);
}

// ── Small helpers ────────────────────────────────────────────────────────

function el(tag, cls) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  return n;
}

function deviceLabel() {
  return (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || 'phone';
}

function isTouchDevice() {
  return (navigator.maxTouchPoints || 0) > 0;
}

function isStandalone() {
  return !!(window.matchMedia && window.matchMedia('(display-mode: standalone)').matches);
}
