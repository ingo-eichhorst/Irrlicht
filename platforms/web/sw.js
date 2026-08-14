// Irrlicht Beacon service worker (docs/mobile-notifications-arc42.md §6.2,
// §8.2, §8.4, §8.5).
//
// Additivity contract (arc42 §5.2): this worker handles `push` and
// `notificationclick` ONLY. It must never grow a handler for the fetch event —
// that would interpose on asset loading and could cache the dashboard stale,
// breaking the guarantee that local daemon serving stays byte-identical.
// sw-contract.test.js pins this statically.
//
// Classic script, not a module — broadest WebKit compatibility, and it keeps
// the worker self-contained (nothing to importScripts, nothing extra to 404).
// The logic lives as pure functions on `self` so tests can evaluate this file
// against a stubbed `self` and drive each function directly.

'use strict';

// Fallback for payloads this worker version does not understand. Deliberately
// content-free: a wrong guess about a newer payload's fields would be a
// mis-render; "update me" is always true and always safe (arc42 §8.2).
function genericNotification() {
  return {
    title: 'Irrlicht',
    body: 'Update the app page',
    tag: 'irrlicht-update',
    renotify: false,
  };
}

// The daemon collapse key, spelled to match the relay's own: daemonTopic in
// core/domain/notify/engine.go is "daemon:" + id, and `daemon_id` is
// `omitempty` on the wire (Payload in core/domain/notify/notify.go), so an
// empty id arrives as an absent field. Concatenating it undefined would make
// the tag "daemon:undefined" while the relay's Topic stayed "daemon:" — the
// two stop matching and a later daemon notification no longer replaces the
// earlier one (arc42 §8.4, R3).
function daemonTag(payload) {
  return 'daemon:' + (payload.daemon_id || '');
}

// Same shape as the session kind's label fallback: an id if there is one, a
// neutral noun if there is not — never the string "undefined" in a title the
// user reads.
function daemonName(payload) {
  return payload.daemon_label || payload.daemon_id || 'this Mac';
}

// On-device composition (arc42 §8.2): the relay sends structured data (ids,
// labels, states), never prose — the text a user reads is composed here, on
// the phone. `tag` mirrors the relay's collapse key exactly (arc42 §8.4):
// session id / "summary" / "daemon:"+daemon_id — so a newer notification for
// the same subject replaces the older one instead of stacking (R3).
self.composeNotification = function (payload) {
  if (!payload || payload.v !== 1 || typeof payload.kind !== 'string') {
    return genericNotification();
  }
  switch (payload.kind) {
    case 'session': {
      const verb = payload.state === 'waiting' ? 'needs input' : 'is ready';
      return {
        title: (payload.label || payload.session_id || 'Session') + ' ' + verb,
        body: payload.project || '',
        tag: payload.session_id,
        renotify: !!payload.renotify,
      };
    }
    case 'summary':
      return {
        title: payload.count + ' agents need attention',
        body: Array.isArray(payload.sessions) ? payload.sessions.join(', ') : '',
        tag: 'summary',
        renotify: !!payload.renotify,
      };
    case 'daemon_down':
      return {
        title: 'Mac ' + daemonName(payload) + ' disconnected',
        body: '',
        tag: daemonTag(payload),
        renotify: !!payload.renotify,
      };
    case 'daemon_up':
      return {
        title: 'Mac ' + daemonName(payload) + ' reconnected',
        body: '',
        tag: daemonTag(payload),
        renotify: !!payload.renotify,
      };
    default:
      // A kind added by a newer relay: same rule as an unknown version.
      return genericNotification();
  }
};

// Last-known-state ledger (arc42 §8.5): folded from push payloads while
// backgrounded, never authoritative — the daemon is. The fold takes an
// injected backend so its logic is directly testable: production passes the
// IndexedDB backend below, tests pass an in-memory Map. Honest split: jsdom
// has no IndexedDB and the repo takes no new deps, so the IndexedDB half has
// structural coverage only (the calls exist and are wired); the fold decisions
// (which payloads write, what is written) are what the tests pin.
//
// DEFERRED, and named here rather than left to be discovered: this ledger is
// WRITE-ONLY. Three things §8.5 and R6 describe do not exist yet, and each is
// deferred for a reason, not an oversight.
//   · Nothing reads it back. A read alone would buy nothing without the next
//     two, so it lands with them.
//   · No fold from WS snapshots while the app is open. §8.4 never pushes on
//     `* → working`, so a backgrounded phone only ever LEARNS that sessions
//     need attention and never that one stopped — which is why the §6.2
//     diagram's `set badge` is absent too: derived from this ledger alone a
//     badge would climb and never fall. The correcting fold belongs to the
//     dashboard's own WS path, not to this worker.
//   · No deep link on notificationclick (R6, "opens the app on that session's
//     last-known state"): the payload carries the relay's bare session id,
//     while the dashboard re-keys relay-sourced sessions to a compound
//     `<source>:<id>` before writing `data-session-id` on a row (see the
//     compoundSessionId rewrites in irrlicht.js). A link keyed on the bare id
//     would select nothing, silently, for exactly the sessions push
//     notifications are about — so the mapping is part of the work, and the
//     work is a slice rather than a line.
// Until they land, a tap focuses or opens the app at its start URL, below.
self.ledgerStore = function (backend) {
  return {
    update: function (payload) {
      if (!payload || payload.v !== 1 || payload.kind !== 'session' || !payload.session_id) {
        return Promise.resolve();
      }
      return backend.put(payload.session_id, {
        state: payload.state,
        label: payload.label,
        project: payload.project,
        at: payload.at,
      });
    },
  };
};

self.idbLedgerBackend = function () {
  return {
    put: function (key, value) {
      return new Promise(function (resolve) {
        // Absent outside a real worker scope (see the ledgerStore comment);
        // and a ledger write must never fail the notification chain, so every
        // error path resolves.
        if (!self.indexedDB) return resolve();
        const open = self.indexedDB.open('beacon-ledger', 1);
        open.onupgradeneeded = function () {
          open.result.createObjectStore('sessions');
        };
        open.onsuccess = function () {
          const tx = open.result.transaction('sessions', 'readwrite');
          tx.objectStore('sessions').put(value, key);
          tx.oncomplete = function () { open.result.close(); resolve(); };
          tx.onabort = function () { open.result.close(); resolve(); };
          tx.onerror = function () { open.result.close(); resolve(); };
        };
        open.onerror = function () { resolve(); };
      });
    },
  };
};

self.handlePush = function (event) {
  let payload = null;
  try {
    payload = event.data ? event.data.json() : null;
  } catch (e) {
    // Malformed JSON must never throw out of the handler — it falls through
    // to the generic notification exactly like an unknown version.
    payload = null;
  }
  const n = self.composeNotification(payload);
  return Promise.all([
    self.registration.showNotification(n.title, {
      body: n.body,
      tag: n.tag,
      renotify: n.renotify,
      icon: 'beacon-icon.svg',
    }),
    self.ledgerStore(self.idbLedgerBackend()).update(payload),
  ]);
};

self.focusOrOpenClient = function () {
  // With no fetch handler (arc42 §5.2) this worker controls no clients, so
  // includeUncontrolled is what finds an already-open app window.
  return self.clients
    .matchAll({ type: 'window', includeUncontrolled: true })
    .then(function (wins) {
      if (wins.length > 0) return wins[0].focus();
      return self.clients.openWindow('./');
    });
};

self.addEventListener('push', function (event) {
  // The whole chain — banner + ledger fold — rides one waitUntil (arc42 §6.2).
  event.waitUntil(self.handlePush(event));
});

self.addEventListener('notificationclick', function (event) {
  event.notification.close();
  event.waitUntil(self.focusOrOpenClient());
});
