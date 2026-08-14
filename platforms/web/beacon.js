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
  renderSection(section, info);
  await selfHeal(info);
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

function renderSection(section, info) {
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
  if (deviceToken) renderHealthPanel(phone, info, deviceToken);
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
  const minted = await r.json();
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
  const paired = await r.json();
  // Store the durable identity first (arc42 §8.1: the device token survives;
  // the subscription is only a delivery address). If anything below fails,
  // the §8.3 self-heal retries the subscription on next open.
  localStorage.setItem(DEVICE_TOKEN_KEY, paired.token);
  const ok = await subscribeForPush(paired.token, paired.vapid_public_key || info.vapid_public_key, status);
  if (!ok) return;
  renderHealthPanel(phone, info, paired.token);
}

async function subscribeForPush(deviceToken, vapidPublicKey, status) {
  if (!('serviceWorker' in navigator)) {
    if (status) status.textContent = 'This browser cannot receive push notifications.';
    return false;
  }
  const reg = await ensureServiceWorker();
  // Still inside the pair button's user-gesture chain — required for the
  // permission prompt to show at all (arc42 §6.1).
  const perm = await Notification.requestPermission();
  if (perm !== 'granted') {
    if (status) status.textContent = 'Notification permission was not granted — paired, but nothing will arrive until it is.';
    return false;
  }
  const sub = await reg.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(vapidPublicKey),
  });
  await postSubscription(deviceToken, sub);
  return true;
}

// The ONLY registration call in the web tree (arc42 §5.2): lazy, reached from
// the pairing flow and its §8.3 self-heal only.
export function ensureServiceWorker() {
  return navigator.serviceWorker.register('./sw.js');
}

function postSubscription(deviceToken, sub) {
  return fetch('api/v1/push/subscriptions', {
    method: 'POST',
    headers: {
      Authorization: 'Bearer ' + deviceToken,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(sub && typeof sub.toJSON === 'function' ? sub.toJSON() : sub),
  });
}

// §8.3 self-heal: iOS may invalidate a subscription silently. Identity (the
// device token) survives; the delivery address is re-registered on open —
// which also covers a relay whose VAPID keypair was lost and reminted (§8.6),
// since the fresh key arrives via push/info.
async function selfHeal(info) {
  if (!('serviceWorker' in navigator)) return;
  const deviceToken = localStorage.getItem(DEVICE_TOKEN_KEY);
  if (!deviceToken) return;
  try {
    let reg = await navigator.serviceWorker.getRegistration();
    // A paired device owns its worker — re-registering here is the self-heal,
    // not an eager install (§5.2's lazy rule gates on the pairing, which the
    // stored token proves happened).
    if (!reg) reg = await ensureServiceWorker();
    const existing = await reg.pushManager.getSubscription();
    if (existing) return;
    const fresh = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(info.vapid_public_key),
    });
    await postSubscription(deviceToken, fresh);
  } catch (e) {
    // Leave it for the next open; the health panel surfaces the gap (§8.3).
  }
}

// ── Health panel + unpair (arc42 §8.3: doubt must be answerable) ─────────

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
  let r = null;
  try {
    r = await fetch('api/v1/push/subscriptions', {
      headers: { Authorization: 'Bearer ' + deviceToken },
    });
  } catch (e) {
    r = null;
  }
  if (!r || !r.ok) {
    line.textContent = 'Paired — delivery status unavailable right now.';
    return;
  }
  const s = await r.json();
  if (!s || s.registered !== true) {
    line.textContent = 'Paired, but push is not registered — reopen this app to re-subscribe.';
    return;
  }
  line.textContent = 'Push registered via ' + s.endpoint_host + ' — last delivery ' + (s.last_delivery || 'never') + '.';
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
