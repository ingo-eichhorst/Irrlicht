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
// permissionAnswerFor decides the single answer entry (or null to skip)
// for one agent+permission pair given the draft toggle state.
function permissionAnswerFor(a, p, draft, onlyPending) {
  const k = a.name + '/' + p.key;
  if (!(k in draft)) return null;
  const grant = !!draft[k];
  if (p.state === 'pending') return { agent: a.name, permission: p.key, grant };
  // A permission whose consent effect failed is resubmitted even when the
  // toggle is unchanged: the daemon reads the identical answer as a retry
  // and re-runs the closure (#1362). Without this, an unchanged toggle is
  // filtered out here and Apply can never repair a failed install.
  if (p.effect_error) return { agent: a.name, permission: p.key, grant };
  if (onlyPending) return null;
  if (grant !== (p.state === 'granted')) return { agent: a.name, permission: p.key, grant };
  return null;
}

// permissionEffectNotice describes the warning to show for a permission
// whose consent effect failed, or null when there is nothing wrong.
// "granted" and "applied" are two different facts: a granted permission
// with an effect_error means the user said yes and the modification did
// NOT happen. Pure; exported for tests.
export function permissionEffectNotice(p) {
  const reason = p?.effect_error;
  if (!reason) return null;
  if (p.state === 'granted') {
    return {
      label: 'Granted, but not applied',
      reason,
      retryLabel: 'Retry',
      grant: true,
    };
  }
  if (p.state === 'denied') {
    return {
      label: 'Revoked, but not undone',
      reason,
      retryLabel: 'Retry undo',
      grant: false,
    };
  }
  // Pending permissions never run an effect; ignore a stray error.
  return null;
}

// anyEffectFailed reports whether any permission of the named agents has
// a failed consent effect. `names` null/undefined means every agent (the
// review wizard); the auto wizard passes its locked set so an unrelated
// agent's old failure can't hold it open. Pure; exported for tests.
export function anyEffectFailed(snap, names) {
  return (snap?.agents || [])
    .filter(a => !Array.isArray(names) || names.includes(a.name))
    .some(a => (a.permissions || []).some(p => !!p.effect_error));
}

// unappliedGrantSummary describes the daemon's aggregate of permissions
// the user granted whose effect is not in force, or null when there are
// none. The daemon computes the list (snapshot.unapplied_grants, #1385) —
// this only words it, so the dashboard and the macOS app can never
// disagree about the count.
//
// Kept as {count, text, items} rather than a bare number: the headline
// aggregates, but the click-through has to stay specific. An install that
// FAILED (#1362) and a refusal because the CLI is below its version floor
// (#1365) both land here and are told apart by their reason text, exactly
// as they are in the wizard. Pure; exported for tests.
export function unappliedGrantSummary(snap) {
  const items = Array.isArray(snap?.unapplied_grants) ? snap.unapplied_grants : [];
  if (!items.length) return null;
  const n = items.length;
  return {
    count: n,
    text: n === 1
      ? '1 permission is granted but not applied'
      : `${n} permissions are granted but not applied`,
    items,
  };
}

// renderUnappliedGrantsBanner reconciles the passive dashboard banner with
// the snapshot. Three things it deliberately does NOT do, all inherited
// from #1362's reasoning (#1385):
//   - it never opens the wizard: that is the fail → wizard → retry → fail
//     loop #1362 avoided. The route on is a button the user clicks.
//   - it never nags: role="status"/aria-live="polite", announced once, no
//     modal, no repeat.
//   - it offers no dismiss. "Dismissible by fixing" is the whole contract:
//     a hide button would let a live fault be silenced while still broken,
//     which is the defect, not the cure. Fix it and it disappears by itself.
export function renderUnappliedGrantsBanner(snap) {
  const el = document.getElementById('permission-apply-banner');
  if (!el) return;
  const summary = unappliedGrantSummary(snap);
  el.textContent = '';
  if (!summary) {
    el.hidden = true;
    return;
  }
  const head = document.createElement('strong');
  head.textContent = summary.text;
  el.appendChild(head);

  const list = document.createElement('ul');
  list.className = 'perm-unapplied-list';
  for (const g of summary.items) {
    const li = document.createElement('li');
    const who = document.createElement('span');
    who.className = 'perm-unapplied-what';
    who.textContent = `${g.agent_display_name || g.agent} — ${g.title || g.key}: `;
    li.appendChild(who);
    // The reason verbatim: it is what distinguishes a failed install from
    // a version-floor refusal, and it carries the refusal's own advice.
    li.appendChild(document.createTextNode(g.reason || ''));
    list.appendChild(li);
  }
  el.appendChild(list);

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'settings-action-btn perm-unapplied-review';
  btn.textContent = 'Review permissions';
  btn.addEventListener('click', () => {
    refreshPermissions().then(() => {
      if (permissionsSnapshot) openPermissionsWizard('review');
    });
  });
  el.appendChild(btn);
  el.hidden = false;
}

// findPermission looks one permission up in a snapshot. Pure; exported
// for tests.
export function findPermission(snap, agentName, permKey) {
  const agent = (snap?.agents || []).find(a => a.name === agentName);
  return (agent?.permissions || []).find(p => p.key === permKey) || null;
}

// visiblePermissionsFor picks the rows one agent shows. Auto mode asks
// only about unanswered items — plus any whose effect failed, so a broken
// install is not invisible on the one surface the user is already looking
// at. It does NOT widen pendingWizardAgents, so a failure never pops the
// wizard open by itself. Pure; exported for tests.
export function visiblePermissionsFor(a, auto) {
  const perms = a?.permissions || [];
  if (!auto) return perms;
  return perms.filter(p => p.state === 'pending' || !!p.effect_error);
}

export function buildPermissionAnswers(snap, draft, onlyPending) {
  const out = [];
  if (!snap || !Array.isArray(snap.agents)) return out;
  for (const a of snap.agents) {
    for (const p of (a.permissions || [])) {
      const answer = permissionAnswerFor(a, p, draft, onlyPending);
      if (answer) out.push(answer);
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
      // The passive banner is reconciled on every refresh, including the
      // one at page load — that is what makes a startup re-apply failure
      // reach a user who never opens Settings (#1385).
      renderUnappliedGrantsBanner(snap);
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
    if (autoWizardStillHasSomethingToSay()) return;
    closePermissionsWizard();
    // Fall through: another agent may be waiting for its own prompt.
  }
  if (pendingWizardAgents(permissionsSnapshot).length > 0) openPermissionsWizard('auto');
}

// autoWizardStillHasSomethingToSay reports whether the OPEN auto wizard
// must stay up, and renders a not-yet-visible failure into it if so.
//
// A failed consent effect holds it open just as a pending permission does
// — otherwise the daemon's own permissions_updated broadcast closes it the
// instant a grant is recorded, taking the new failure warning with it
// (#1362). Mirrors macOS's AgentPermissions.hasUnresolvedPermissions.
function autoWizardStillHasSomethingToSay() {
  const failed = anyEffectFailed(permissionsSnapshot, permissionsWizardAgents);
  if (!failed && !stillPendingForAgents(permissionsSnapshot, permissionsWizardAgents)) {
    return false;
  }
  // An open wizard is deliberately NOT re-rendered, so in-flight toggling
  // isn't clobbered — with one exception: a failure that isn't on screen
  // yet has to be shown. It can arrive from the macOS app answering first,
  // or from the daemon's startup re-apply, not just from our own Apply.
  if (failed && !document.querySelector('#permissions-body .perm-effect-error')) {
    renderPermissionsWizard();
  }
  return true;
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

// setWizardHeaderText fills the title/intro copy for the given mode.
function setWizardHeaderText(title, intro, auto) {
  if (title) title.textContent = auto ? 'Agent detected — choose permissions' : 'Agent permissions';
  if (intro) {
    intro.textContent = auto
      ? 'irrlicht monitors coding agents only with your consent. Choose what it may do for each detected agent.'
      : 'Everything irrlicht may read or modify, per agent. Toggling off undoes the modification and stops all reading.';
  }
}

// buildAgentPermSection renders one agent's permission group (heading +
// detected badge + rows), or null when it has nothing to show. Builds
// detached DOM and reads no module state; exported so tests can assert
// against the REAL render path the wizard uses.
export function buildAgentPermSection(a, perms) {
  if (!perms.length) return null;
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
  return section;
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
  setWizardHeaderText(title, intro, auto);
  body.innerHTML = '';
  for (const a of agents) {
    const section = buildAgentPermSection(a, visiblePermissionsFor(a, auto));
    if (section) body.appendChild(section);
  }
}

// buildEffectNotice renders the "granted but not applied" warning strip
// plus its Retry button, or null when the permission is healthy.
function buildEffectNotice(a, p) {
  const notice = permissionEffectNotice(p);
  if (!notice) return null;
  const box = document.createElement('div');
  box.className = 'perm-effect-error';
  box.setAttribute('role', 'alert');
  const text = document.createElement('span');
  text.className = 'perm-effect-error-text';
  const strong = document.createElement('strong');
  strong.textContent = notice.label + ': ';
  text.appendChild(strong);
  text.appendChild(document.createTextNode(notice.reason));
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'settings-action-btn perm-retry';
  btn.textContent = notice.retryLabel;
  btn.addEventListener('click', () => retryPermissionEffect(a.name, p.key, notice.grant, box, btn));
  box.appendChild(text);
  box.appendChild(btn);
  return box;
}

// retryPermissionEffect re-submits the SAME answer for one permission.
// The daemon re-runs the closure because it has a recorded failure for it
// (#1362) — the user never has to revoke and re-grant. Only this row is
// re-rendered, so other rows' in-flight toggles survive.
function retryPermissionEffect(agentName, permKey, grant, box, btn) {
  btn.disabled = true;
  const original = btn.textContent;
  btn.textContent = 'Retrying…';
  postPermissionAnswers([{ agent: agentName, permission: permKey, grant }]).then(snap => {
    btn.disabled = false;
    btn.textContent = original;
    if (!snap) return; // request failed — leave the notice up for another try
    permissionsSnapshot = snap;
    const fresh = findPermission(snap, agentName, permKey);
    const replacement = fresh ? buildEffectNotice({ name: agentName }, fresh) : null;
    if (replacement) box.replaceWith(replacement);
    else box.remove();
    // Patching this one node keeps other rows' in-flight toggles, but the
    // wizard's own open/closed state still has to be reconciled: a retry
    // that fixed the last outstanding problem should let it close.
    updatePermissionsWizard();
  });
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
  // "granted" alone would be a lie when the modification never landed.
  const notice = buildEffectNotice(a, p);
  if (notice) row.appendChild(notice);
  return row;
}

// postPermissionAnswers submits an answer batch and resolves with the new
// snapshot, or null on any failure. Shared by Apply and per-row Retry.
function postPermissionAnswers(answers) {
  return fetch('/api/v1/permissions/answer', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ answers }),
  }).then(r => r.ok ? r.json() : null).catch(() => null);
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
  postPermissionAnswers(answers).then(snap => {
    if (applyBtn) applyBtn.disabled = false;
    if (!snap) return; // failed — wizard stays for retry
    permissionsSnapshot = snap;
    // An effect that failed is reported, not hidden: keep the wizard up
    // and re-render so the user sees WHY, instead of it closing on a
    // "success" that installed nothing (#1362). Scoped to the agents THIS
    // batch answered — an unrelated agent's pre-existing failure must not
    // make Apply look like it does nothing.
    if (anyEffectFailed(snap, answers.map(x => x.agent))) {
      renderPermissionsWizard();
      return;
    }
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
