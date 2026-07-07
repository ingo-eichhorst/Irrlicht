import { closeSettings } from './irrlicht.js';

// --- Permission wizard (issue #570) ---
// The daemon is consent-first: every read/modification it performs is a
// declared per-agent permission, and the wizard collects the answers.
// State lives with the daemon (GET /api/v1/permissions); this overlay
// appears when a detected agent has pending permissions and dismisses
// live when the other surface (macOS app) answers first — the daemon
// broadcasts `permissions_updated` and we re-fetch.
let permissionsSnapshot = null;
// 'auto' = new-agent wizard (pending items only); 'review' = Settings
// re-entry (all items, toggles preloaded with current grants).
let permissionsWizardMode = null;
// Agent names LOCKED into the open auto wizard at presentation time:
// a mid-decision detection flip (the agent's process exited) can't
// tear it down, and a newly detected agent can't inject rows into it —
// it gets its own prompt once this one resolves.
let permissionsWizardAgents = null;

// pendingWizardAgents returns the agents that should trigger the auto
// wizard: detected, with at least one pending permission, in ask mode.
// Pure; exported for tests.
export function pendingWizardAgents(snap) {
  if (snap?.mode !== 'ask' || !Array.isArray(snap?.agents)) return [];
  return snap.agents.filter(a =>
    a.detected && (a.permissions || []).some(p => p.state === 'pending'));
}

// stillPendingForAgents reports whether any of the named agents still
// has a pending permission. Drives auto-wizard dismissal: only answers
// dismiss an open wizard (submitted here or on the macOS app — first
// answer wins); a detection flip alone must not. Pure; exported for
// tests.
export function stillPendingForAgents(snap, names) {
  if (!snap || !Array.isArray(snap.agents) || !Array.isArray(names)) return false;
  return snap.agents.some(a => names.includes(a.name) &&
    (a.permissions || []).some(p => p.state === 'pending'));
}

// buildPermissionAnswers computes the POST payload from the wizard's
// toggle states. draft maps "agent/key" → bool. In onlyPending mode
// (auto wizard) every displayed pending item is answered explicitly;
// in review mode unchanged already-answered items are skipped. Pure;
// exported for tests.
export function buildPermissionAnswers(snap, draft, onlyPending) {
  const out = [];
  if (!snap || !Array.isArray(snap.agents)) return out;
  for (const a of snap.agents) {
    for (const p of (a.permissions || [])) {
      const k = a.name + '/' + p.key;
      if (!(k in draft)) continue;
      const grant = !!draft[k];
      if (p.state === 'pending') {
        out.push({ agent: a.name, permission: p.key, grant });
        continue;
      }
      if (onlyPending) continue;
      if (grant !== (p.state === 'granted')) {
        out.push({ agent: a.name, permission: p.key, grant });
      }
    }
  }
  return out;
}

export function refreshPermissions() {
  return fetch('/api/v1/permissions')
    .then(r => r.ok ? r.json() : null)
    .catch(() => null)
    .then(snap => {
      if (!snap) return;
      permissionsSnapshot = snap;
      updatePermissionsWizard();
    });
}

// updatePermissionsWizard reconciles overlay visibility with the
// snapshot: opens the auto wizard when a detected agent has pending
// permissions, and dismisses it once its LOCKED agents have no pending
// items left — answered here or on the macOS app (first answer wins).
// A detection flip alone never dismisses an open wizard, and an open
// wizard is not re-rendered, so in-flight toggling isn't clobbered.
function updatePermissionsWizard() {
  const backdrop = document.getElementById('permissions-backdrop');
  if (!backdrop) return;
  if (backdrop.classList.contains('open')) {
    if (permissionsWizardMode !== 'auto') return;
    if (stillPendingForAgents(permissionsSnapshot, permissionsWizardAgents)) return;
    closePermissionsWizard();
    // Fall through: another agent may be waiting for its own prompt.
  }
  if (pendingWizardAgents(permissionsSnapshot).length > 0) openPermissionsWizard('auto');
}

function openPermissionsWizard(mode) {
  const backdrop = document.getElementById('permissions-backdrop');
  if (!backdrop || !permissionsSnapshot) return;
  permissionsWizardMode = mode;
  permissionsWizardAgents = mode === 'auto'
    ? pendingWizardAgents(permissionsSnapshot).map(a => a.name)
    : null;
  renderPermissionsWizard();
  backdrop.classList.add('open');
}

function closePermissionsWizard() {
  const backdrop = document.getElementById('permissions-backdrop');
  if (backdrop) backdrop.classList.remove('open');
  permissionsWizardMode = null;
  permissionsWizardAgents = null;
}

// renderPermissionsWizard rebuilds the overlay body. Auto mode shows
// the LOCKED agents' unanswered items only (an upgrade that adds one
// new permission re-asks just that one); review mode shows everything
// with current grants preloaded.
function renderPermissionsWizard() {
  const body = document.getElementById('permissions-body');
  const title = document.getElementById('permissions-title');
  const intro = document.getElementById('permissions-intro');
  if (!body || !permissionsSnapshot) return;
  const auto = permissionsWizardMode === 'auto';
  const agents = auto
    ? (permissionsSnapshot.agents || []).filter(a =>
        (permissionsWizardAgents || []).includes(a.name))
    : (permissionsSnapshot.agents || []);
  if (title) title.textContent = auto ? 'Agent detected — choose permissions' : 'Agent permissions';
  if (intro) {
    intro.textContent = auto
      ? 'irrlicht monitors coding agents only with your consent. Choose what it may do for each detected agent.'
      : 'Everything irrlicht may read or modify, per agent. Toggling off undoes the modification and stops all reading.';
  }
  body.innerHTML = '';
  for (const a of agents) {
    const perms = auto ? a.permissions.filter(p => p.state === 'pending') : a.permissions;
    if (!perms.length) continue;
    const section = document.createElement('div');
    section.className = 'perm-agent';
    const h = document.createElement('h3');
    h.textContent = a.display_name || a.name;
    if (a.detected) {
      const badge = document.createElement('span');
      badge.className = 'perm-detected';
      badge.textContent = 'running';
      h.appendChild(badge);
    }
    section.appendChild(h);
    for (const p of perms) {
      section.appendChild(renderPermissionRow(a, p));
    }
    body.appendChild(section);
  }
}

function renderPermissionRow(a, p) {
  const row = document.createElement('div');
  row.className = 'perm-row';
  const label = document.createElement('label');
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.dataset.permAgent = a.name;
  cb.dataset.permKey = p.key;
  // Pending items default on (granting is the value proposition; the
  // explicit Apply click is the consent). Answered items show their
  // current state.
  cb.checked = p.state === 'pending' ? true : p.state === 'granted';
  const text = document.createElement('span');
  text.className = 'settings-label-text';
  const titleEl = document.createElement('span');
  titleEl.className = 'settings-title';
  titleEl.textContent = p.title || p.key;
  const hint = document.createElement('span');
  hint.className = 'settings-hint';
  hint.textContent = p.feature_unlocked || '';
  text.appendChild(titleEl);
  text.appendChild(hint);
  label.appendChild(cb);
  label.appendChild(text);
  row.appendChild(label);
  // The (i) affordance: an expander with what it touches + full detail.
  const details = document.createElement('details');
  details.className = 'perm-details';
  const summary = document.createElement('summary');
  summary.textContent = 'ⓘ ' + (p.touches || 'details');
  const detail = document.createElement('div');
  detail.className = 'perm-detail-text';
  detail.textContent = p.detail || '';
  details.appendChild(summary);
  details.appendChild(detail);
  row.appendChild(details);
  return row;
}

function submitPermissionsWizard() {
  const body = document.getElementById('permissions-body');
  if (!body) return;
  const draft = {};
  for (const cb of body.querySelectorAll('input[data-perm-agent]')) {
    draft[cb.dataset.permAgent + '/' + cb.dataset.permKey] = cb.checked;
  }
  const answers = buildPermissionAnswers(permissionsSnapshot, draft, permissionsWizardMode === 'auto');
  if (!answers.length) {
    closePermissionsWizard();
    return;
  }
  // Keep the wizard up until the daemon confirms: a failed POST must
  // not silently drop consent decisions while monitoring stays paused
  // — the user can just hit Apply again.
  const applyBtn = document.getElementById('permissions-apply');
  if (applyBtn) applyBtn.disabled = true;
  const review = permissionsWizardMode === 'review';
  fetch('/api/v1/permissions/answer', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ answers }),
  }).then(r => r.ok ? r.json() : null).catch(() => null).then(snap => {
    if (applyBtn) applyBtn.disabled = false;
    if (!snap) return; // failed — wizard stays for retry
    permissionsSnapshot = snap;
    if (review) closePermissionsWizard();
    updatePermissionsWizard();
  });
}


// initPermissionsWizard wires the wizard's buttons/backdrop and kicks off the
// first permissions fetch. Called once from irrlicht.js's top-level init, in
// the same relative position this code used to run inline.
export function initPermissionsWizard() {
  const applyBtn = document.getElementById('permissions-apply');
  if (applyBtn) applyBtn.addEventListener('click', submitPermissionsWizard);
  const backdrop = document.getElementById('permissions-backdrop');
  if (backdrop) backdrop.addEventListener('click', (e) => {
    if (e.target.id === 'permissions-backdrop') closePermissionsWizard();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && backdrop?.classList.contains('open')) {
      closePermissionsWizard();
    }
  });
  const review = document.getElementById('settings-review-permissions');
  if (review) review.addEventListener('click', () => {
    closeSettings();
    // Re-fetch so the review view reflects the live state, then open.
    refreshPermissions().then(() => {
      if (permissionsSnapshot) openPermissionsWizard('review');
    });
  });

  refreshPermissions();
}
