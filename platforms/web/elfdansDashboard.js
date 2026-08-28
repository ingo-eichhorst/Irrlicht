// Irrlicht Elfdans — the DASHBOARD half (arc42 §8.5, R6).
//
// elfdans.js is the phone-facing half: pairing, push settings, the service
// worker, and the ledger it talks to. This module is everything Elfdans adds
// to the dashboard itself — where a notification tap lands, and what the live
// view publishes back into the phone's ledger.
//
// It lives apart from irrlicht.js on purpose. The dashboard must be buildable
// without Elfdans (and Elfdans, one day, shippable as its own artifact), so
// the coupling is one import, one construction, and the five calls the
// returned object exposes — one inbound (a notification tap) and four moments
// the dashboard reports. irrlicht.js holds no Elfdans state and takes no
// Elfdans branch: dropping the feature is deleting those calls, not unpicking
// a feature from a 2400-line file.
//
// Everything this module needs from the dashboard arrives through `host` —
// see createElfdansDashboard. Nothing here reaches back into irrlicht.js.

import { isGroupCollapsed, toggleGroupCollapsed } from './collapsedGroups.js';
import { displaySessionId } from './sessionIdentity.js';
import { publishLedgerSnapshot, ledgerEntry } from './elfdans.js';

// How long a tap waits for the session list before "not here" is a verdict
// rather than a race: the app may have been opened COLD by the tap and be
// waiting on its first frame.
const ELFDANS_TARGET_GRACE_MS = 2500;
// The live fold is published on a debounce: a metrics tick is not news to
// the ledger, and a phone should not post one message per row update.
const ELFDANS_PUBLISH_DEBOUNCE_MS = 500;

// createElfdansDashboard wires Elfdans to one dashboard. `host` is the whole
// of what it may know about that dashboard:
//   · `sessionIds()`          — every indexed session id (compound, #537).
//   · `groupOf(sessionId)`    — the group record a session sits in, or null.
//   · `groups()`              — the dashboard's current group tree.
//   · `render()`              — repaint the session list.
//   · `anySourceConnected()`  — is at least one live source connected.
//
// The returned object is the other direction: where a tap lands, plus the
// four moments the dashboard has to report. All of it is inert until a
// notification tap or a paired phone gives it something to do.
export function createElfdansDashboard(host) {
  let elfdansTarget = null;          // { bareId, deadline } — a tap not yet resolved
  let elfdansGraceTimer = null;
  let elfdansLiveDataArrived = false;
  let elfdansFocusedSessionId = '';

  // --- Notification deep link ---
  //
  // A push payload names the relay's BARE session id — Payload in
  // core/domain/notify/notify.go carries no daemon id for the session kind —
  // while a relay-sourced row is keyed by the compound `<daemon>\0<id>` that
  // normalizeSourcedFrame folds in (#537), and that compound id is what
  // reaches `data-session-id`. A link keyed on the bare id therefore matches
  // no row at all, silently, for exactly the sessions notifications are
  // about. Resolution runs through displaySessionId — compoundSessionId's
  // documented inverse (sessionIdentity.js) — rather than through a second
  // derivation that could drift from it.

  // Entry point for a notification tap, handed to elfdans.js at wiring time.
  function focusSessionFromNotification(bareId) {
    if (!bareId) return;
    elfdansTarget = { bareId: String(bareId), deadline: Date.now() + ELFDANS_TARGET_GRACE_MS };
    clearElfdansGraceTimer();
    // A retry rides every render, but a phone that receives no frame at all
    // renders nothing — so the verdict carries its own clock too.
    elfdansGraceTimer = setTimeout(() => { elfdansGraceTimer = null; attemptElfdansFocus(); }, ELFDANS_TARGET_GRACE_MS);
    attemptElfdansFocus();
  }

  function clearElfdansGraceTimer() {
    if (elfdansGraceTimer === null) return;
    clearTimeout(elfdansGraceTimer);
    elfdansGraceTimer = null;
  }

  function attemptElfdansFocus() {
    if (!elfdansTarget) return;
    const bareId = elfdansTarget.bareId;
    const ids = rowIdsFor(host.sessionIds(), bareId);
    if (ids.length === 0 && !elfdansLiveDataArrived && Date.now() < elfdansTarget.deadline) return;
    elfdansTarget = null;
    clearElfdansGraceTimer();
    if (ids.length > 0) { selectElfdansRow(ids, bareId); return; }
    // Never a tap that appears to have done nothing (R6): if the session is
    // not in the list, the app opens and says so.
    reportElfdansSessionMissing(bareId);
  }

  function selectElfdansRow(ids, bareId) {
    elfdansFocusedSessionId = ids[0];
    let row = elfdansRowElement(ids[0]);
    if (!row) {
      // The dashboard holds the session but is not painting it: its group is
      // collapsed. Expanding is the honest move — reporting "not in the list"
      // about a row a chevron would reveal is a lie.
      expandGroupsForSession(host.groups(), host.groupOf(ids[0]));
      host.render();
      row = elfdansRowElement(ids[0]);
    }
    if (!row) { reportElfdansSessionMissing(bareId); return; }
    paintElfdansFocus();
    if (typeof row.scrollIntoView === 'function') row.scrollIntoView({ block: 'center' });
    showElfdansNotice(ids.length > 1
      ? 'Two sessions share that id — showing the first. The notification named no daemon, so this app cannot tell them apart.'
      : '');
  }

  // reconcile rewrites a session row's className on every update, so the
  // selection is re-applied from state after each render rather than set once
  // on the element.
  function paintElfdansFocus() {
    for (const el of document.querySelectorAll('#session-list .session-row')) {
      el.classList.toggle('elfdans-focus', !!elfdansFocusedSessionId && el.dataset.sessionId === elfdansFocusedSessionId);
    }
  }

  async function reportElfdansSessionMissing(bareId) {
    elfdansFocusedSessionId = '';
    paintElfdansFocus();
    let entry = null;
    try {
      entry = await ledgerEntry(bareId);
    } catch (e) {
      console.debug('irrlicht: failed to read the elfdans ledger', e);
    }
    showElfdansNotice(missingSessionText(bareId, entry));
  }

  // --- The live fold (arc42 §8.5) ---
  let elfdansPublishTimer = null;
  // null, not '': "nothing has been published yet" and "the empty set has
  // been published" are different states, and an empty set is the single
  // most important snapshot this fold sends. A phone opened after every
  // session ended connects, renders zero rows, and that nothing is exactly
  // the news — it is what takes the last `waiting` row the push fold wrote
  // out of the ledger. Held as '' they compare equal, the first publish is
  // skipped, and the badge never returns to zero.
  let elfdansPublishedSignature = null;

  function scheduleLedgerPublish() {
    // A disconnected dashboard's list is not a smaller truth, it is no truth
    // at all — publishing it would delete the ledger §8.5 keeps for exactly
    // that moment ("as of 14:32").
    if (!host.anySourceConnected()) return;
    if (elfdansPublishTimer !== null) return;
    elfdansPublishTimer = setTimeout(() => {
      elfdansPublishTimer = null;
      const rows = elfdansLedgerSessions(host.groups());
      const signature = rows.map(r => r.session_id + ':' + r.state).join('|');
      if (signature === elfdansPublishedSignature) return;
      // Recorded only once it actually published: an unpaired dashboard
      // answers false, and a phone paired a minute later must not then be
      // skipped because the set has not changed since.
      publishLedgerSnapshot(rows).then((published) => {
        elfdansPublishedSignature = published ? signature : null;
      });
    }, ELFDANS_PUBLISH_DEBOUNCE_MS);
  }

  return {
    // Handed to initElfdans as `openSession` — where a notification tap lands.
    focusSessionFromNotification,
    // Called at the end of every render: reconcile has just rewritten every
    // row's className, so the notification selection is repainted from state;
    // a tap still waiting on its session gets another look at the fresh list;
    // and the phone's ledger is told what the dashboard can now see.
    onRender() {
      paintElfdansFocus();
      attemptElfdansFocus();
      scheduleLedgerPublish();
    },
    // A session frame landed, from any source — so "the session is not in the
    // list" has stopped being a race and can be reported (arc42 R6).
    noteLiveData() {
      elfdansLiveDataArrived = true;
    },
    // A live source's socket just opened. Publishing on the CONNECT edge as
    // well as from render() is load-bearing: nothing renders when a socket
    // opens, so a phone whose handshake completes after the last render would
    // otherwise publish nothing until the next session frame — and if there
    // are no sessions there is no next frame. That is precisely the case the
    // fold exists for: opened after everything ended, the empty set is the
    // news that takes the push fold's last `waiting` row and its badge back
    // down (§8.5).
    noteSourceConnected() {
      scheduleLedgerPublish();
    },
    // Forget what we published: the worker's ledger does NOT stand still while
    // the page is away — sw.js's push fold keeps writing it. On reconnect an
    // unchanged live set would otherwise be suppressed as "already published",
    // leaving the push fold's `waiting` row and its badge in place with
    // nothing left to correct them.
    noteSourceDisconnected() {
      elfdansPublishedSignature = null;
    },
  };
}

// --- Pure helpers (no dashboard state; exported for tests) ---

// Every row a bare id can mean. Plural on purpose: two daemons may deliver
// the same bare session_id (`proc-<pid>` collides readily) — the ambiguity
// the compound key exists to keep apart, and the one a notification cannot
// resolve, since it names no daemon.
export function rowIdsFor(sessionIds, bareId) {
  const out = [];
  for (const id of sessionIds) {
    if (displaySessionId(id) === bareId) out.push(id);
  }
  return out;
}

// The collapse key is path-qualified (see emitGroup), so the chain is rebuilt
// the same way rather than guessed from the group's own name.
export function groupKeyChain(groups, group) {
  let chain = [];
  (function walk(gs, parentKey, ancestors) {
    for (const g of (gs || [])) {
      if (chain.length) return;
      const key = parentKey ? parentKey + '/' + g.name : g.name;
      const next = ancestors.concat([key]);
      if (g === group) { chain = next; return; }
      if (g.groups?.length) walk(g.groups, key, next);
    }
  })(groups, '', []);
  return chain;
}

function expandGroupsForSession(groups, group) {
  if (!group) return;
  for (const key of groupKeyChain(groups, group)) {
    if (isGroupCollapsed(key)) toggleGroupCollapsed(key);
  }
}

function elfdansRowElement(sessionId) {
  // Attribute-selector lookup would have to escape a compound id, which
  // carries a NUL delimiter; the existing repaintHistory walk is the idiom.
  for (const el of document.querySelectorAll('#session-list .session-row')) {
    if (el.dataset.sessionId === sessionId) return el;
  }
  return null;
}

// Created on demand and removed when it has nothing to say: this notice is
// Elfdans's only mark on the dashboard chrome, and a dashboard nobody taps a
// notification into never grows it (arc42 §5.2).
function showElfdansNotice(text) {
  const existing = document.getElementById('elfdans-notice');
  if (!text) {
    existing?.remove();
    return;
  }
  const el = existing || insertNotice();
  el.textContent = text;
}

function insertNotice() {
  const el = document.createElement('div');
  el.id = 'elfdans-notice';
  el.setAttribute('role', 'status');
  el.title = 'Dismiss';
  el.addEventListener('click', () => el.remove());
  const list = document.getElementById('session-list');
  if (list?.parentNode) list.parentNode.insertBefore(el, list);
  else document.body.appendChild(el);
  return el;
}

function elfdansAsOfText(atSeconds) {
  const n = Number(atSeconds);
  if (!Number.isFinite(n) || n <= 0) return 'at an unknown time';
  return 'as of ' + new Date(n * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

// What the app says when a tap resolves to no row. The ledger is the only
// thing that still knows anything about that session (arc42 §8.5), so its
// last-known state IS the answer — stated "as of <time>", never as now.
export function missingSessionText(bareId, entry) {
  const who = [entry?.label, entry?.project].filter(Boolean).join(' · ')
    || ('Session ' + String(bareId || '').slice(0, 8));
  if (entry?.state) {
    return who + ' — last known ' + entry.state + ' ' + elfdansAsOfText(entry.at)
      + ', and not in the list this device is watching.';
  }
  return who + ' is not in the list this device is watching, and this phone kept no last-known state for it.';
}

// What the ledger gets from the live view. Top-level sessions only: §8.4
// never notifies about a subagent (the parent covers it), so counting one
// in the badge would claim attention nobody was asked for. Keyed by the
// BARE id — the key space the push fold already writes.
export function elfdansLedgerSessions(groups) {
  const at = Math.floor(Date.now() / 1000);
  const out = [];
  forEachTopLevelAgent(groups, (a) => {
    if (a.session_id) out.push(ledgerRow(a, at));
  });
  return out;
}

function forEachTopLevelAgent(groups, visit) {
  for (const g of (groups || [])) {
    for (const a of (g.agents || [])) visit(a);
    if (g.groups?.length) forEachTopLevelAgent(g.groups, visit);
  }
}

function ledgerRow(agent, at) {
  return {
    session_id: displaySessionId(agent.session_id),
    state: agent.state || '',
    // Mirrors what the relay composes into a push payload (push_observer.go:
    // Label = adapter, Project = project name), so the two folds write the
    // same fields rather than two dialects.
    label: agent.adapter || '',
    project: agent.project_name || '',
    at,
  };
}
