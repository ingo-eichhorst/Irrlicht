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
// self-heal only — plain dashboard usage never installs a worker).
// sw-contract.test.js pins both properties.

const DEVICE_TOKEN_KEY = 'beaconDeviceToken';

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

// Entry point, called from irrlicht.js at wiring time. `opts.relayToken` is an
// accessor for the Settings → Sources relay token (the authed client token
// that may mint pairing codes, arc42 §8.1); the phone side needs none.
export async function initBeacon(opts = {}) {
  relayTokenAccessor = opts.relayToken || (() => '');
  if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
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
  const btn = el('button', 'settings-action-btn');
  btn.type = 'button';
  btn.id = 'beacon-unpair';
  btn.textContent = 'Unpair';
  btn.addEventListener('click', () => unpair(phone, info, deviceToken));
  phone.appendChild(line);
  phone.appendChild(btn);

  let status = await fetchSubscriptionStatus(deviceToken);
  if (status.revoked) return renderRevoked(phone, info);
  // The self-heal runs BEFORE the verdict is painted. Reading the status
  // first and repairing afterwards is what made the panel advise reopening an
  // app that had just repaired itself.
  const healed = await selfHeal(info, deviceToken, !!(status.body && status.body.registered === true));
  if (healed === HEAL_REVOKED) return renderRevoked(phone, info);
  if (healed === HEAL_REPAIRED) {
    status = await fetchSubscriptionStatus(deviceToken);
    if (status.revoked) return renderRevoked(phone, info);
  }
  if (status.unreachable) {
    line.textContent = 'Paired — delivery status unavailable right now.';
    return;
  }
  line.textContent = healthLineText(status.body);
}

// A revoked phone cannot heal: the relay dropped its subscription along with
// the token (§8.1), so the only route back is a fresh pairing code.
function renderRevoked(phone, info) {
  phone.innerHTML = '';
  const line = el('div', 'beacon-health');
  line.id = 'beacon-health-line';
  line.textContent = revokedText();
  const btn = el('button', 'settings-action-btn');
  btn.type = 'button';
  btn.id = 'beacon-repair';
  btn.textContent = 'Pair again';
  btn.addEventListener('click', () => {
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
