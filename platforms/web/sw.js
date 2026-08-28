// Irrlicht Elfdans service worker (docs/mobile-notifications-arc42.md §6.2,
// §8.2, §8.4, §8.5).
//
// Additivity contract (arc42 §5.2): this worker handles `push`,
// `notificationclick` and `message` ONLY. It must never grow a handler for the
// fetch event — that would interpose on asset loading and could cache the
// dashboard stale, breaking the guarantee that local daemon serving stays
// byte-identical. sw-contract.test.js pins this statically. The `message`
// handler is reachable only from a page that already holds this registration
// (the dashboard, via elfdans.js), and touches nothing but the ledger below, so
// it cannot come between the page and its assets.
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
//
// `data` is what a tap can act on (R6): only the session kind names one
// session, so only it carries an id. A summary names several and the daemon
// kinds name none — they open the app with no target rather than guessing one.
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
        data: { session_id: payload.session_id },
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
    case 'test':
      // The diagnostic push (arc42 §8.3): the relay sent this because
      // somebody pressed "Send a test notification", so seeing it IS the
      // result. Its tag mirrors the relay's collapse key — testPushTopic in
      // core/cmd/irrlichtrelay/push_observer.go — which is deliberately a
      // string no session id, daemon topic or summary can be, so proving
      // delivery never replaces the banner the user actually needed. It
      // carries no session, so the ledger fold below ignores it and the
      // badge is untouched.
      return {
        title: 'Elfdans test notification',
        body: 'Push delivery from your relay is working.',
        tag: 'elfdans-test',
        renotify: !!payload.renotify,
      };
    default:
      // A kind added by a newer relay: same rule as an unknown version.
      return genericNotification();
  }
};

// The three message types the dashboard and this worker exchange, plus the
// URL-fragment key a cold open carries. Spelled as literals because this file
// is a classic script with nothing to import from (see the header); elfdans.js
// exports the same four values and sw-contract.test.js pins the two copies
// together, so a rename on one side cannot quietly stop the other from being
// heard.
const MSG_LIVE_SESSIONS = 'elfdans-live-sessions';
const MSG_LEDGER_GET = 'elfdans-ledger-get';
const MSG_OPEN_SESSION = 'elfdans-open-session';
const SESSION_HASH_KEY = 'elfdans-session';

// Last-known-state ledger (arc42 §8.5): folded from push payloads while
// backgrounded and from the dashboard's own live view while open, never
// authoritative — the daemon is. The fold takes an injected backend so its
// logic is directly testable: production passes the IndexedDB backend below,
// tests pass an in-memory Map.
//
// The live fold is what lets the badge FALL. §8.4 never pushes on
// `* → working`, and iOS forbids a silent push (`userVisibleOnly` forces a
// visible notification per push), so a backgrounded phone only ever learns
// that sessions NEED attention — a count derived from push alone would climb
// and never come down. The correcting signal is the dashboard's own WS stream,
// which is why pairing now also configures the phone's live view (ADR-9) and
// why the dashboard posts MSG_LIVE_SESSIONS here.
//
// Keys are the relay's BARE session id — what a push payload carries. The
// dashboard re-keys relay-sourced sessions to a compound `<daemon>\0<id>` and
// unfolds them again before publishing (see publishLedgerSnapshot in
// irrlicht.js), so the two folds share one key space. Two daemons sharing a
// bare id therefore share a ledger row; that ambiguity is already inherent in
// the push payload, which names no daemon (Payload in
// core/domain/notify/notify.go), and the ledger is non-authoritative anyway.
//
// Reads distinguish "nothing in the ledger" from "could not read the ledger"
// (arc42 §8.3): `all()` answers `[]` for the first and `null` for the second,
// and a null keeps the badge exactly where it is rather than clearing it —
// clearing would state "nothing needs you", which is a claim we do not have.
self.ledgerStore = function (backend) {
  return {
    // The push fold: one payload, one row (arc42 §8.5).
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

    get: function (sessionId) {
      if (!sessionId) return Promise.resolve(null);
      return backend.get(sessionId);
    },

    all: function () {
      return backend.all();
    },

    // The live fold: the dashboard's visible set REPLACES the ledger, because
    // while it is connected it is the closer thing to truth — a session that
    // left `waiting`, and a session that ended entirely, both have to stop
    // counting. A row the live set does not name is dropped for the same
    // reason. elfdans.js publishes only while the live view watches the relay
    // that pairs this phone, so "the live set does not name it" means the
    // session is gone rather than merely out of view.
    foldLiveSessions: function (sessions) {
      const rows = (Array.isArray(sessions) ? sessions : []).filter(function (s) {
        return s && s.session_id;
      });
      const live = new Set(rows.map(function (s) { return s.session_id; }));
      return backend.all().then(function (existing) {
        const work = rows.map(function (s) {
          return backend.put(s.session_id, {
            state: s.state,
            label: s.label,
            project: s.project,
            at: s.at,
          });
        });
        // A null read is "could not look" — drop nothing on it, or an
        // unreadable ledger would empty itself on the next snapshot.
        for (const entry of (existing || [])) {
          if (!live.has(entry.key)) work.push(backend.remove(entry.key));
        }
        return Promise.all(work);
      });
    },
  };
};

// The badge counts sessions in `waiting` and nothing else. §8.4 pushes a
// banner for `working → ready` too, but the two states make different claims:
// `waiting` is "an agent is blocked on you", which you clear by answering,
// while `ready` is "work you could pick up", which a finished session keeps
// asserting until you start another turn. A badge counting `ready` would
// therefore sit non-zero for every session that ever finished — the same
// climbs-and-never-falls failure the live fold exists to remove, wearing a
// different hat.
//
// Returns null when the ledger could not be read, which applyBadge treats as
// "leave it alone" rather than "zero".
self.badgeCount = function (entries) {
  if (!Array.isArray(entries)) return null;
  let n = 0;
  for (const entry of entries) {
    if (entry && entry.value && entry.value.state === 'waiting') n++;
  }
  return n;
};

// Feature-detected per call: the Badging API is absent on plenty of targets
// (every desktop Firefox, and Safari outside an installed app), and its
// absence must cost nothing. A rejected promise is ignored for the same
// reason a ledger write is — no badge is a worse outcome than a broken
// notification chain, and the chain is the feature.
self.applyBadge = function (count) {
  if (count === null) return Promise.resolve();
  const nav = self.navigator;
  if (!nav) return Promise.resolve();
  const fn = count > 0 ? nav.setAppBadge : nav.clearAppBadge;
  if (typeof fn !== 'function') return Promise.resolve();
  try {
    return Promise.resolve(count > 0 ? fn.call(nav, count) : fn.call(nav)).catch(function () {});
  } catch (e) {
    return Promise.resolve();
  }
};

self.refreshBadge = function (store) {
  return store.all().then(function (entries) {
    return self.applyBadge(self.badgeCount(entries));
  });
};

// The production ledger backend. Every operation resolves — a ledger failure
// must never fail the notification chain — but a failed READ resolves `null`
// rather than an empty result, so the caller can tell it apart from an empty
// ledger (arc42 §8.3).
self.idbLedgerBackend = function () {
  const DB = 'elfdans-ledger';
  const STORE = 'sessions';
  const VERSION = 1;

  // `run` returns a reader called once the transaction completes, so a cursor
  // walk and a single get share one shape. Absent outside a real worker scope
  // (jsdom has none), which resolves null exactly like a failed open.
  function withStore(mode, run) {
    return new Promise(function (resolve) {
      if (!self.indexedDB) return resolve(null);
      let open;
      try {
        open = self.indexedDB.open(DB, VERSION);
      } catch (e) {
        return resolve(null);
      }
      open.onupgradeneeded = function () {
        const db = open.result;
        if (!db.objectStoreNames.contains(STORE)) db.createObjectStore(STORE);
      };
      open.onerror = function () { resolve(null); };
      open.onblocked = function () { resolve(null); };
      open.onsuccess = function () {
        const db = open.result;
        let tx = null;
        let read = null;
        try {
          tx = db.transaction(STORE, mode);
          read = run(tx.objectStore(STORE));
        } catch (e) {
          db.close();
          return resolve(null);
        }
        const settle = function (value) { db.close(); resolve(value); };
        tx.oncomplete = function () { settle(read()); };
        tx.onabort = function () { settle(null); };
        tx.onerror = function () { settle(null); };
      };
    });
  }

  return {
    put: function (key, value) {
      // IDBObjectStore.put takes (value, key) for a store without a key path —
      // the reversed order writes the id as the record and is invisible until
      // something reads back.
      return withStore('readwrite', function (store) {
        store.put(value, key);
        return function () { return null; };
      });
    },
    get: function (key) {
      return withStore('readonly', function (store) {
        const req = store.get(key);
        return function () { return req.result === undefined ? null : req.result; };
      });
    },
    // Cursor rather than getAll(): the fold needs each row's KEY to drop it,
    // and the stored record carries no id of its own.
    all: function () {
      return withStore('readonly', function (store) {
        const out = [];
        const req = store.openCursor();
        req.onsuccess = function () {
          const cursor = req.result;
          if (!cursor) return;
          out.push({ key: cursor.key, value: cursor.value });
          cursor.continue();
        };
        return function () { return out; };
      });
    },
    remove: function (key) {
      return withStore('readwrite', function (store) {
        store.delete(key);
        return function () { return null; };
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
  const store = self.ledgerStore(self.idbLedgerBackend());
  return Promise.all([
    self.registration.showNotification(n.title, {
      body: n.body,
      tag: n.tag,
      renotify: n.renotify,
      icon: 'elfdans-icon.svg',
      data: n.data || null,
    }),
    store.update(payload).then(function () { return self.refreshBadge(store); }),
  ]);
};

// A tap opens the app on the session the notification is about (R6). The
// worker controls no clients (no fetch handler, arc42 §5.2), so it cannot
// navigate an open window — the target rides a message to a focused client and
// the URL fragment to a cold one. Both land on the same resolver in
// irrlicht.js; neither carries the compound id, because only the dashboard
// knows which daemon delivered the session.
self.focusOrOpenClient = function (sessionId) {
  return self.clients
    // With no fetch handler this worker controls no clients, so
    // includeUncontrolled is what finds an already-open app window.
    .matchAll({ type: 'window', includeUncontrolled: true })
    .then(function (wins) {
      if (wins.length > 0) {
        if (sessionId) {
          try {
            wins[0].postMessage({ type: MSG_OPEN_SESSION, session_id: sessionId });
          } catch (e) {
            // A client that refuses a message still gets focused; the app then
            // shows what it has rather than nothing.
          }
        }
        return wins[0].focus();
      }
      const url = sessionId ? './#' + SESSION_HASH_KEY + '=' + encodeURIComponent(sessionId) : './';
      return self.clients.openWindow(url);
    });
};

// Messages from the app (elfdans.js). Anything with a reply port is ANSWERED,
// including a message this worker does not understand: a caller awaiting a
// reply that never arrives is the silent failure arc42 §8.3 forbids, and an
// older worker meeting a newer app is exactly when it would happen.
self.handleMessage = function (event) {
  const msg = event.data || {};
  const port = (event.ports && event.ports[0]) || null;
  const reply = function (value) {
    if (!port) return;
    try {
      port.postMessage(value);
    } catch (e) {
      // Nothing left to do: the caller's port is gone with its page.
    }
  };
  const store = self.ledgerStore(self.idbLedgerBackend());
  let work;
  if (msg.type === MSG_LIVE_SESSIONS) {
    work = store.foldLiveSessions(msg.sessions)
      .then(function () { return self.refreshBadge(store); })
      .then(function () { return { ok: true }; });
  } else if (msg.type === MSG_LEDGER_GET) {
    work = store.get(msg.session_id).then(function (entry) { return { entry: entry || null }; });
  } else {
    work = Promise.resolve(null);
  }
  return work.catch(function (e) {
    return { error: (e && (e.message || e.name)) || 'ledger error' };
  }).then(reply);
};

self.addEventListener('push', function (event) {
  // The whole chain — banner + ledger fold + badge — rides one waitUntil
  // (arc42 §6.2).
  event.waitUntil(self.handlePush(event));
});

self.addEventListener('notificationclick', function (event) {
  event.notification.close();
  const data = event.notification.data || null;
  event.waitUntil(self.focusOrOpenClient(data && data.session_id));
});

self.addEventListener('message', function (event) {
  // Same-origin only. A service worker is reachable solely from its own
  // origin's clients — a cross-origin page cannot get a handle on this
  // registration — so this refuses nothing a browser can send today. It is
  // here because the handler WRITES: a live-sessions message rewrites the
  // ledger and repaints the badge, and "nothing can reach it" is a property
  // of the platform, not of this file. A blank origin is what
  // ExtendableMessageEvent carries for a port-delivered message from our own
  // page, so it passes; a foreign one never does.
  if (event.origin && event.origin !== self.location.origin) return;
  event.waitUntil(self.handleMessage(event));
});
