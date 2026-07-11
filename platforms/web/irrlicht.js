import { isGroupCollapsed, toggleGroupCollapsed } from './collapsedGroups.js';
import { isSummaryCollapsed, toggleSummaryCollapsed, anySummaryCollapsed, collapseAllSummaries, expandAllSummaries } from './collapsedSummaries.js';
import { initHistoryTab } from './historyTab.js';
import { initPermissionsWizard, refreshPermissions } from './permissionsWizard.js';
import {
  showQuotaForecast, setShowQuotaForecast, renderHeaderTitle, refreshProviderSettings,
} from './quotaChips.js';
import {
  compoundSessionId, displaySessionId, sessionOrigin, sourceIdOf, localBareIds, isShadowedRemote, daemonSessionIds,
} from './sessionIdentity.js';
import { relayFrameKind, seqGap, aggregateConnState, relayWsUrl } from './connectionProtocol.js';
import {
  stateIcon, shortModel, formatCost, costCellDisplay, fmtDuration, formatElapsed,
  taskEtaPresentation, shortID, pressureClass, pressureColor, formatTokens, esc, activeSubagentCount,
  cacheBloatBadgeText,
} from './formatters.js';
import { reconcile, paintRowNum } from './domReconcile.js';

    // --- State ---
    let dashboardGroups = [];
    // Per-provider trailing-window spend (providerKey → timeframe → USD) from
    // the /api/v1/sessions `provider_costs` field. Feeds the usage chips.
    let dashboardProviderCosts = {};
    // Adapter branding from /api/v1/agents — keyed by adapter `name`
    // (e.g. "claude-code"). Populated once on initial load; the daemon's
    // registry is essentially static (changes require a daemon restart).
    // Exported (live binding) so quotaChips.js can resolve provider/adapter
    // icon branding without owning session state itself. Object.create(null)
    // rather than {}: it's keyed by e.name straight out of the /api/v1/agents
    // response body (see the fetch below), so a "__proto__"-named agent entry
    // can't repoint this object's prototype — same idiom as lastTickGen/
    // historyByGranularity below, which key off session-derived strings too.
    export const agentRegistry = Object.create(null);
    let sessionIndex = new Map();

    // --- Theme ---
    // The explicit override (localStorage["irrlicht_theme"] = "light" | "dark")
    // sets data-theme on <html>, which the CSS @media (prefers-color-scheme)
    // rules respect. Absent override → follow OS preference.
    const themeKey = 'irrlicht_theme';
    function storedTheme() {
      const t = localStorage.getItem(themeKey);
      return (t === 'light' || t === 'dark') ? t : null;
    }
    function resolvedTheme() {
      const t = storedTheme();
      if (t) return t;
      return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    }
    function applyStoredTheme() {
      const t = storedTheme();
      if (t) document.documentElement.dataset.theme = t;
      else delete document.documentElement.dataset.theme;
      updateThemeToggleGlyph();
    }
    function updateThemeToggleGlyph() {
      const btn = document.getElementById('theme-toggle');
      if (!btn) return;
      // ☀ when current theme is dark (clicking switches to light)
      // ☾ when current theme is light (clicking switches to dark)
      btn.textContent = resolvedTheme() === 'dark' ? '☀' : '☾';
    }
    function toggleTheme() {
      const next = resolvedTheme() === 'dark' ? 'light' : 'dark';
      localStorage.setItem(themeKey, next);
      applyStoredTheme();
      // Re-render so adapter icons pick up the new light/dark variant.
      for (const el of document.querySelectorAll('.session-row')) {
        el.dataset.adapterPopulated = '0';
      }
      render();
    }
    applyStoredTheme();
    if (window.matchMedia) {
      const mq = window.matchMedia('(prefers-color-scheme: light)');
      const onChange = () => {
        if (!storedTheme()) { updateThemeToggleGlyph(); render(); }
      };
      mq.addEventListener('change', onChange);
    }

    // --- Display mode (Context / 1 Min / 10 Min / 60 Min) ---
    // Single cycling toggle in the header replaces the standalone view-mode
    // bar and the timeline-granularity buttons. Mirrors overlay's
    // DisplayMode enum at SessionListView.swift:148.
    const DISPLAY_MODES = [
      { key: 'context', label: 'Context', isHistory: false, granularity: 1  },
      { key: '1min',    label: '1 Min',   isHistory: true,  granularity: 1  },
      { key: '10min',   label: '10 Min',  isHistory: true,  granularity: 10 },
      { key: '60min',   label: '60 Min',  isHistory: true,  granularity: 60 },
    ];
    let currentDisplayMode = localStorage.getItem('irrlicht_displayMode') || 'context';
    if (!DISPLAY_MODES.some(m => m.key === currentDisplayMode)) currentDisplayMode = 'context';
    function currentMode() {
      return DISPLAY_MODES.find(m => m.key === currentDisplayMode) || DISPLAY_MODES[0];
    }
    function applyDisplayMode() {
      const m = currentMode();
      const btn = document.getElementById('view-mode-cycle');
      if (btn) {
        btn.textContent = m.label;
        btn.classList.toggle('history', m.isHistory);
      }
      document.body.classList.toggle('view-history', m.isHistory);
      if (m.isHistory) applyGranularity(m.granularity);
      // Repaint so the timeline panel and per-row history canvases match the
      // new mode (canvases need offsetWidth>0 before painting; the panel
      // hides outside history modes).
      repaintHistory();
    }
    function cycleDisplayMode() {
      const idx = DISPLAY_MODES.findIndex(m => m.key === currentDisplayMode);
      currentDisplayMode = DISPLAY_MODES[(idx + 1) % DISPLAY_MODES.length].key;
      localStorage.setItem('irrlicht_displayMode', currentDisplayMode);
      applyDisplayMode();
    }

    // --- Settings ---
    // Mirrors the macOS app's SettingsView (platforms/macos/Irrlicht/Views/
    // SettingsView.swift). Defaults match @AppStorage defaults in Swift:
    // showCostDisplay defaults on; debug + notify toggles default off.
    // `launchAtLogin` and per-event sound pickers don't apply to the web view.
    const SETTINGS_KEY = 'irrlicht_settings';
    const SETTINGS_DEFAULTS = {
      debugMode: false,
      showCostDisplay: true,
      notifyOnReady: false,
      notifyOnWaiting: false,
      notifyOnContextPressure: false,
      // Sources: the local source (the origin that served this page) is on by
      // default; a relay source is opt-in by URL. Mirrors the macOS
      // useLocalDaemon / useRelayServer / relayServerURL @AppStorage keys.
      enableLocalSource: true,
      enableRelaySource: false,
      relayUrl: '',
      relayToken: '',
    };
    // Settings keys that change the live source connections (vs. display-only
    // toggles), so the change handler knows to reconnect.
    const SOURCE_SETTING_KEYS = new Set(['enableLocalSource', 'enableRelaySource', 'relayUrl', 'relayToken']);
    function loadSettings() {
      try {
        const raw = localStorage.getItem(SETTINGS_KEY);
        if (!raw) return { ...SETTINGS_DEFAULTS };
        const parsed = JSON.parse(raw);
        return { ...SETTINGS_DEFAULTS, ...((parsed && typeof parsed === 'object') ? parsed : {}) };
      } catch (e) {
        console.debug('irrlicht: failed to load settings, using defaults', e);
        return { ...SETTINGS_DEFAULTS };
      }
    }
    let settings = loadSettings();
    function persistSettings() {
      try { localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings)); } catch (e) {
        console.debug('irrlicht: failed to persist settings', e);
      }
    }
    function applySettings() {
      document.body.classList.toggle('no-cost', !settings.showCostDisplay);
      document.body.classList.toggle('debug-mode', !!settings.debugMode);
    }
    applySettings();

    // --- Notifications ---
    // tryNotify gates browser notifications behind: matching settings toggle,
    // permission grant, and tab being hidden (so we don't ping while the user
    // is actively looking at the dashboard). Mirrors what the macOS app would
    // surface via UNUserNotificationCenter.
    const lastNotifiedPressure = new Map();
    function tryNotify(toggleKey, title, body) {
      if (!settings[toggleKey]) return;
      if (typeof Notification === 'undefined') return;
      if (Notification.permission !== 'granted') return;
      if (document.visibilityState === 'visible') return;
      try { new Notification(title, { body: body || '', tag: 'irrlicht-' + toggleKey, silent: false }); } catch (e) {
        console.debug('irrlicht: failed to show notification', e);
      }
    }
    function rowLabel(s) {
      // Best-effort "project · branch" label that matches what the row shows.
      const proj = s?.project_name || '';
      const branch = s?.git_branch || '';
      if (proj && branch) return proj + ' · ' + branch;
      return proj || branch || (s?.session_id ? displaySessionId(s.session_id).slice(0, 8) : 'session');
    }
    function maybeNotifyOnUpdate(prev, next) {
      if (!next) return;
      const prevState    = prev ? prev.state : '';
      const prevPressure = prev?.metrics?.pressure_level || '';
      const nextState    = next.state || '';
      const nextPressure = next.metrics?.pressure_level || '';
      const sid          = next.session_id;
      const label        = rowLabel(next);

      if (nextState === 'ready' && prevState !== 'ready') {
        tryNotify('notifyOnReady', 'Session ready', label);
      } else if (nextState === 'waiting' && prevState !== 'waiting') {
        tryNotify('notifyOnWaiting', 'Awaiting input', label);
      }

      const isHigh = nextPressure === 'warning' || nextPressure === 'critical';
      const wasHigh = prevPressure === 'warning' || prevPressure === 'critical';
      const lastFor = sid ? lastNotifiedPressure.get(sid) : null;
      if (isHigh && (!wasHigh || lastFor !== nextPressure)) {
        const title = nextPressure === 'critical' ? 'Context pressure: critical' : 'Context pressure: high';
        tryNotify('notifyOnContextPressure', title, label + ' — switch to a fresh session soon');
        if (sid) lastNotifiedPressure.set(sid, nextPressure);
      } else if (!isHigh && sid && lastNotifiedPressure.has(sid)) {
        lastNotifiedPressure.delete(sid);
      }
    }

    // --- Cost timeframes ---
    // COST_TIMEFRAMES/currentUsageTimeframe are exported (live binding) so
    // quotaChips.js can render the usage-chip headline in the selected
    // timeframe without owning the timeframe cycling itself.
    export const COST_TIMEFRAMES = [
      { key: 'day',   suffix: '/d'  },
      { key: 'week',  suffix: '/w'  },
      { key: 'month', suffix: '/mo' },
      { key: 'year',  suffix: '/yr' },
    ];
    let currentTimeframe = localStorage.getItem('irrlicht_costTimeframe') || 'day';
    if (!COST_TIMEFRAMES.some(t => t.key === currentTimeframe)) currentTimeframe = 'day';

    // Usage chips share one timeframe, cycled by clicking any of them and
    // persisted independently of the project-cost timeframe above — mirrors
    // macOS's separate usageCostTimeframe (#386).
    export let currentUsageTimeframe = localStorage.getItem('irrlicht_usageCostTimeframe') || 'day';
    if (!COST_TIMEFRAMES.some(t => t.key === currentUsageTimeframe)) currentUsageTimeframe = 'day';

    // costDisplayMode selects what the per-session row's cost slot shows —
    // 'cost' ($) or 'co2' (estimated CO2e), issue #829. Click-to-cycle rather
    // than a fourth always-on column, matching the group header's existing
    // cycleCostTimeframe() interaction pattern below.
    let costDisplayMode = localStorage.getItem('irrlicht_costDisplayMode') === 'co2' ? 'co2' : 'cost';

    function cycleCostDisplayMode() {
      costDisplayMode = costDisplayMode === 'cost' ? 'co2' : 'cost';
      localStorage.setItem('irrlicht_costDisplayMode', costDisplayMode);
      render();
    }

    // --- Timeline / history state (WebSocket-streamed, see history_snapshot/tick/upgrade) ---
    let timelineHistory = new Map(); // session_id -> {label, states: string[]} for the active granularity
    // Derived from the display-mode cycle (`currentMode().granularity`); see
    // applyDisplayMode → applyGranularity below.
    let currentGranularity = 1;
    let currentBucketCount = 60;
    // historyByGranularity[g][sessionID] is a 60-element oldest→newest array of state names
    // (or "" for no-data buckets). Mirrored from the daemon's bit-packed wire format.
    // Inner dicts are null-prototype so server-supplied keys can never reach Object.prototype.
    const historyByGranularity = {1: Object.create(null), 10: Object.create(null), 60: Object.create(null)};
    // lastTickGen[sessionID][granularity] = highest applied tick generation. Lets us drop
    // a tick that's already reflected in our snapshot (closing the connect-time race).
    const lastTickGen = Object.create(null);

    // isDangerousKey guards every historyByGranularity[gran][sessionID] /
    // lastTickGen[sessionID] access below. Those dicts are already
    // Object.create(null) (no prototype to hijack via a "__proto__" key),
    // but CodeQL's prototype-pollution query doesn't credit that — it
    // flags the later `arr[...] = value` write on the value pulled out by
    // a server-controlled key, several lines downstream of where the dict
    // itself was hardened. Rejecting the dangerous keys at the point of
    // lookup makes the safety explicit right where CodeQL's dataflow
    // expects to see it, on top of the null-prototype backstop.
    function isDangerousKey(key) {
      return key === '__proto__' || key === 'constructor' || key === 'prototype';
    }

    const HISTORY_PRIORITY_TO_STATE = ['ready', 'working', 'waiting', ''];
    function historyPriorityForState(s) {
      switch (s) {
        case 'waiting': return 2;
        case 'working': return 1;
        case 'ready':   return 0;
        default:        return -1;
      }
    }
    function decodeHistoryBuckets(b64) {
      // Daemon ships 60 buckets × 2 bits MSB-first = 15 bytes = 20 base64 chars.
      let raw;
      try { raw = atob(b64); } catch (e) { console.debug('irrlicht: failed to decode history buckets', e); return null; }
      if (raw.length !== 15) return null;
      const out = new Array(60);
      for (let i = 0; i < 15; i++) {
        const byte = raw.codePointAt(i);
        out[i * 4 + 0] = HISTORY_PRIORITY_TO_STATE[(byte >> 6) & 0x3];
        out[i * 4 + 1] = HISTORY_PRIORITY_TO_STATE[(byte >> 4) & 0x3];
        out[i * 4 + 2] = HISTORY_PRIORITY_TO_STATE[(byte >> 2) & 0x3];
        out[i * 4 + 3] = HISTORY_PRIORITY_TO_STATE[byte & 0x3];
      }
      return out;
    }

    // Rebuild `timelineHistory` from the chosen granularity dict. The renderer
    // expects `states` to omit leading no-data slots so the bar right-anchors.
    function rebuildTimelineHistory() {
      const dict = historyByGranularity[currentGranularity] || Object.create(null);
      const newMap = new Map();
      for (const sid of Object.keys(dict)) {
        const buckets = dict[sid];
        let i = 0;
        while (i < buckets.length && buckets[i] === '') i++;
        const states = buckets.slice(i);
        const entry = sessionIndex.get(sid);
        const label = entry
          ? (entry.agent.project_name || '').slice(0, 10) + ' ' + shortID(sid)
          : shortID(sid);
        newMap.set(sid, {label, states});
      }
      timelineHistory = newMap;
    }

    function applyHistorySnapshot(sessionID, history, generations) {
      if (isDangerousKey(sessionID)) return;
      for (const granKey of Object.keys(history)) {
        const gran = Number.parseInt(granKey, 10);
        if (![1, 10, 60].includes(gran)) continue;
        const buckets = decodeHistoryBuckets(history[granKey]);
        if (!buckets) continue;
        historyByGranularity[gran][sessionID] = buckets;
      }
      // Seed the dedup high-water-mark from the snapshot so any tick already
      // folded into this snapshot gets skipped on arrival.
      if (generations) {
        const perGran = lastTickGen[sessionID] || Object.create(null);
        for (const granKey of Object.keys(generations)) {
          const gran = Number.parseInt(granKey, 10);
          if (gran === 1 || gran === 10 || gran === 60) perGran[gran] = generations[granKey];
        }
        lastTickGen[sessionID] = perGran;
      } else if (!lastTickGen[sessionID]) {
        // No generation numbers in the snapshot — establish an empty entry so
        // applyHistoryTick treats last=0 correctly (see `last &&` guard there).
        lastTickGen[sessionID] = Object.create(null);
      }
      rebuildTimelineHistory();
    }

    // applyOneTickBucket folds one session's bucket update into dict,
    // returning whether it actually changed anything. Extracted from
    // applyHistoryTick so that function's own loop/dedup branching doesn't
    // compound with this per-session bucket-shifting logic (CodeScene
    // flagged applyHistoryTick as a declining hotspot once the
    // isDangerousKey guard added a branch on top of an already-complex
    // method).
    function applyOneTickBucket(dict, sid, granularitySec, buckets, bucketGenerations) {
      if (isDangerousKey(sid)) return false;
      // Skip if this tick has already been folded into our snapshot.
      if (bucketGenerations?.[sid] !== undefined) {
        const gen = bucketGenerations[sid];
        const last = lastTickGen[sid]?.[granularitySec] || 0;
        if (last && gen <= last) return false;
        if (!lastTickGen[sid]) lastTickGen[sid] = Object.create(null);
        lastTickGen[sid][granularitySec] = gen;
      }
      let arr = dict[sid];
      if (!arr) arr = new Array(60).fill('');
      arr.shift();
      arr.push(HISTORY_PRIORITY_TO_STATE[buckets[sid] & 0x3]);
      while (arr.length < 60) arr.unshift('');
      dict[sid] = arr;
      return true;
    }

    function applyHistoryTick(granularitySec, buckets, bucketGenerations) {
      if (![1, 10, 60].includes(granularitySec)) return;
      const dict = historyByGranularity[granularitySec];
      let changed = false;
      for (const sid of Object.keys(buckets)) {
        if (applyOneTickBucket(dict, sid, granularitySec, buckets, bucketGenerations)) changed = true;
      }
      if (changed && granularitySec === currentGranularity) rebuildTimelineHistory();
    }

    function applyHistoryUpgrade(sessionID, priority) {
      if (isDangerousKey(sessionID)) return;
      const newState = HISTORY_PRIORITY_TO_STATE[priority & 0x3];
      const newPrio = historyPriorityForState(newState);
      let changedActive = false;
      for (const gran of [1, 10, 60]) {
        const arr = historyByGranularity[gran][sessionID];
        if (!arr || arr.length === 0) continue;
        const lastPrio = historyPriorityForState(arr[arr.length - 1]);
        if (newPrio > lastPrio) {
          arr[arr.length - 1] = newState;
          if (gran === currentGranularity) changedActive = true;
        }
      }
      if (changedActive) rebuildTimelineHistory();
    }

    // The per-state SVG icon registry and all pure formatting/ETA helpers
    // (stateIcon, shortModel, formatCost, fmtDuration, formatElapsed,
    // fmtEtaDuration, fmtEtaText, taskEtaPresentation, shortID, pressureClass,
    // pressureColor, formatTokens, esc, activeSubagentCount) live in
    // formatters.js — imported above. (groupMaxPressure/groupTotalCost were
    // dropped here: dead code with zero call sites anywhere in the repo.)

    // --- Session index ---
    function rebuildIndex() {
      sessionIndex.clear();
      // Walk top-level groups and their nested sub-groups (Gas Town rigs).
      // Nested groups are tagged `_nested` so applySessionUpdate won't migrate
      // a rig session out into a flat project group on its next WS update.
      (function walk(groups, nested) {
        for (const g of groups) {
          if (nested) g._nested = true;
          for (const a of (g.agents || [])) {
            sessionIndex.set(a.session_id, {group: g, agent: a, parent: null});
            indexChildren(g, a);
          }
          if (g.groups?.length) walk(g.groups, true);
        }
      })(dashboardGroups, false);
    }
    function indexChildren(group, parent) {
      if (!parent.children) return;
      for (const c of parent.children) {
        sessionIndex.set(c.session_id, {group: group, agent: c, parent: parent});
        indexChildren(group, c);
      }
    }

    function applySessionUpdate(s) {
      const entry = sessionIndex.get(s.session_id);
      if (entry) {
        updateExistingSession(entry, s);
        return;
      }
      insertNewSession(s);
    }

    function updateExistingSession(entry, s) {
      const a = entry.agent;
      // Capture previous state + pressure before merging so we can fire
      // notifications on the transition (web parity for the menu-bar app's
      // ready/waiting/pressure alerts).
      const prevSnap = { state: a.state, metrics: a.metrics ? { pressure_level: a.metrics.pressure_level } : null };
      const role = a.role, wn = a.worker_name, wid = a.worker_id, ch = a.children;
      Object.assign(a, s);
      if (role && !a.role) a.role = role;
      if (wn && !a.worker_name) a.worker_name = wn;
      if (wid && !a.worker_id) a.worker_id = wid;
      a.children = ch;
      migrateSessionGroupIfNeeded(entry, a);
      maybeNotifyOnUpdate(prevSnap, a);
    }

    // Migrate the agent if its project_name now points at a different
    // group than where it currently lives. Without this, sessions whose
    // first WS push arrived before metadata enrichment landed (empty
    // project_name → "unknown" bucket) stay stranded there forever even
    // after later updates carry the correct name. Children inherit their
    // parent's group, so only migrate top-level entries. Rig sessions
    // (nested under a Gas Town sub-group) are exempt — their group is
    // structural, not project-derived, so a project_name mismatch must
    // not tear them out of their rig.
    // findOrCreateGroup returns the dashboard group with the given name,
    // creating and registering an empty one if none exists yet. Extracted
    // from migrateSessionGroupIfNeeded (issue #901 cognitive-complexity
    // cleanup) and shared with insertNewSession, which needed the same
    // lookup-or-create logic.
    function findOrCreateGroup(name) {
      let group = dashboardGroups.find(g => g.name === name);
      if (!group) {
        group = {name: name, agents: []};
        dashboardGroups.push(group);
      }
      return group;
    }

    // removeAgentFromGroup splices an agent out of a group's flat agent
    // list and drops the group entirely once it's left empty. Extracted
    // from migrateSessionGroupIfNeeded (issue #901 cognitive-complexity
    // cleanup).
    function removeAgentFromGroup(group, sessionId) {
      const ai = group.agents.findIndex(x => x.session_id === sessionId);
      if (ai >= 0) group.agents.splice(ai, 1);
      if (group.agents.length === 0) {
        const gi = dashboardGroups.indexOf(group);
        if (gi >= 0) dashboardGroups.splice(gi, 1);
      }
    }

    function migrateSessionGroupIfNeeded(entry, a) {
      if (entry.parent || entry.group._nested) return;
      const desired = a.project_name || 'unknown';
      if (entry.group.name === desired) return;
      const oldGroup = entry.group;
      removeAgentFromGroup(oldGroup, a.session_id);
      const target = findOrCreateGroup(desired);
      target.agents.push(a);
      entry.group = target;
      // Children's sessionIndex entries still point at the old group;
      // refresh them so any future site that reads child.entry.group
      // (e.g. a parent-deletion cleanup that drills into children)
      // doesn't follow a reference to a group we just spliced out.
      indexChildren(target, a);
    }

    function insertNewSession(s) {
      // First sight of this session — treat any non-trivial state as a
      // transition for notification purposes.
      maybeNotifyOnUpdate(null, s);
      const groupName = s.project_name || 'unknown';
      const group = findOrCreateGroup(groupName);
      if (attachToParentIfPresent(s)) return;
      group.agents.push(s);
      sessionIndex.set(s.session_id, {group: group, agent: s, parent: null});
    }

    // attachToParentIfPresent links a newly-seen subagent session under its
    // already-indexed parent (as a child), if the parent is known. Returns
    // whether it did so, so the caller can skip the flat group.agents insert.
    function attachToParentIfPresent(s) {
      if (!s.parent_session_id) return false;
      const parentEntry = sessionIndex.get(s.parent_session_id);
      if (!parentEntry) return false;
      if (!parentEntry.agent.children) parentEntry.agent.children = [];
      parentEntry.agent.children.push(s);
      sessionIndex.set(s.session_id, {group: parentEntry.group, agent: s, parent: parentEntry.agent});
      return true;
    }

    function applySessionDelete(sessionId) {
      const entry = sessionIndex.get(sessionId);
      delete lastTickGen[sessionId];
      delete historyByGranularity[1][sessionId];
      delete historyByGranularity[10][sessionId];
      delete historyByGranularity[60][sessionId];
      // Plug the slow leak — without this, the notify-dedupe map keeps
      // entries for deleted sessions indefinitely.
      lastNotifiedPressure.delete(sessionId);
      if (!entry) return;
      sessionIndex.delete(sessionId);
      if (entry.parent) {
        const ci = entry.parent.children.findIndex(c => c.session_id === sessionId);
        if (ci >= 0) entry.parent.children.splice(ci, 1);
      } else {
        const ai = entry.group.agents.findIndex(a => a.session_id === sessionId);
        if (ai >= 0) entry.group.agents.splice(ai, 1);
      }
      if (entry.group.agents.length === 0) {
        const gi = dashboardGroups.indexOf(entry.group);
        if (gi >= 0) dashboardGroups.splice(gi, 1);
      }
    }

    // collectFlatSessions flattens every session (including subagents) across
    // all groups — the input bucketChips() (quotaChips.js) folds into
    // per-provider quota chips. Kept here (not in quotaChips.js) since it's
    // the one place that reads dashboardGroups directly.
    export function collectFlatSessions() {
      const out = [];
      function visit(arr) {
        if (!Array.isArray(arr)) return;
        for (const a of arr) {
          out.push(a);
          if (a.children) visit(a.children);
        }
      }
      for (const g of dashboardGroups) visit(g.agents || []);
      return out;
    }

    // toolLabel maps raw tool names to short display strings for session rows.
    const toolLabel = {
      Bash: 'Bash', Read: 'Read', Write: 'Write', Edit: 'Edit',
      Glob: 'Glob', Grep: 'Grep', Agent: 'Agent',
      AskUserQuestion: 'Ask User', ExitPlanMode: 'Plan Mode',
      WebSearch: 'Search', WebFetch: 'Fetch', TodoWrite: 'Todo',
    };

    function stateColor(s) {
      if (s === 'working') return 'var(--working)';
      if (s === 'waiting') return 'var(--waiting)';
      if (s === 'ready') return 'var(--ready)';
      return 'var(--muted)';
    }

    // Generic keyed DOM-list reconciliation (reconcile, paintRowNum) lives in
    // domReconcile.js — imported above.

    // --- Create/Update functions for session rows ---
    function createGroupHeader(group, groupKey, depth) {
      const el = document.createElement('div');
      el.className = 'group-hdr';
      // Anatomy mirrors overlay GroupView at SessionListView.swift:871:
      // chevron + name + status + per-day cost on the left, count on the right.
      el.innerHTML = '<span class="group-chevron">\u25BE</span>' +
        '<span class="group-name"></span>' +
        '<span class="group-status" style="display:none"></span>' +
        '<span class="group-cost" title="Click to cycle time frame"></span>' +
        '<span class="group-count"></span>';
      // Collapse keys off the live path-qualified key (set in updateGroupHeader)
      // so reused header elements toggle the right rig, not a stale closure.
      el.addEventListener('click', () => {
        const k = el._groupKey || group.name;
        toggleGroupCollapsed(k);
        render();
      });
      const costEl = el.querySelector('.group-cost');
      costEl.addEventListener('click', (e) => {
        e.stopPropagation();
        cycleCostTimeframe();
      });
      updateGroupHeader(el, group, groupKey, depth);
      return el;
    }

    function cycleCostTimeframe() {
      const idx = COST_TIMEFRAMES.findIndex(t => t.key === currentTimeframe);
      currentTimeframe = COST_TIMEFRAMES[(idx + 1) % COST_TIMEFRAMES.length].key;
      localStorage.setItem('irrlicht_costTimeframe', currentTimeframe);
      render();
    }

    // Exported: quotaChips.js's usage-mode chip body calls this to advance
    // the shared usage timeframe on click.
    export function cycleUsageTimeframe() {
      const idx = COST_TIMEFRAMES.findIndex(t => t.key === currentUsageTimeframe);
      currentUsageTimeframe = COST_TIMEFRAMES[(idx + 1) % COST_TIMEFRAMES.length].key;
      localStorage.setItem('irrlicht_usageCostTimeframe', currentUsageTimeframe);
      render();
    }

    // Windowed spend for a usage chip, keyed by providerKey (chip.key, e.g.
    // "anthropic"/"openai") and the selected timeframe. 0 when the daemon has
    // no provider_costs entry for that provider/window. Exported for
    // quotaChips.js.
    export function usageSpendForChip(chip) {
      const byTf = dashboardProviderCosts[chip.key];
      const v = byTf?.[currentUsageTimeframe];
      return typeof v === 'number' ? v : 0;
    }

    function updateGroupHeader(el, group, groupKey, depth) {
      const key = groupKey || group.name;
      el._groupKey = key;
      el.classList.toggle('nested', !!depth);
      // Gas Town groups get a ⛽ glyph prefix on the title — matches macOS
      // group-title rendering at SessionListView.swift:945.
      const isGastown = group.type === 'gastown';
      const desiredName = (isGastown ? '⛽ ' : '') + (group.name || '');
      const nameEl = el.querySelector('.group-name');
      if (nameEl.textContent !== desiredName) nameEl.textContent = desiredName;
      // Rig/codebase status (Codebase.Status) — surfaced as a small badge on
      // nested rig headers; degrade gracefully when absent.
      const statusEl = el.querySelector('.group-status');
      if (statusEl) {
        if (group.status) {
          statusEl.style.display = '';
          if (statusEl.textContent !== group.status) statusEl.textContent = group.status;
          statusEl.style.color = stateColor(group.status);
          statusEl.title = group.status;
        } else {
          statusEl.style.display = 'none';
        }
      }
      const tf = COST_TIMEFRAMES.find(t => t.key === currentTimeframe) || COST_TIMEFRAMES[0];
      const windowed = group.costs ? group.costs[currentTimeframe] : undefined;
      // Hide the cost until the daemon has windowed data for this project —
      // mixing a cumulative lifetime number with a per-window suffix would
      // misrepresent the value. macOS does the same.
      const costText = (typeof windowed === 'number' && windowed > 0)
        ? formatCost(windowed) + tf.suffix
        : '';
      const costEl = el.querySelector('.group-cost');
      if (costEl.textContent !== costText) costEl.textContent = costText;
      const agents = group.agents || [];
      const totalAgents = agents.length + agents.reduce((n, a) => n + (a.children ? a.children.length : 0), 0);
      const countText = totalAgents + (totalAgents === 1 ? ' session' : ' sessions');
      const countEl = el.querySelector('.group-count');
      if (countEl.textContent !== countText) countEl.textContent = countText;

      const isCollapsed = isGroupCollapsed(key);
      const chevron = el.querySelector('.group-chevron');
      chevron.classList.toggle('collapsed', isCollapsed);

      // Update the click handler's group reference
      el._group = group;
    }

    function createSessionRow(agent, isChild) {
      const el = document.createElement('div');
      el.className = 'session-row' + (isChild ? ' child' : '');
      el.dataset.sessionId = agent.session_id;
      // Slot order mirrors the overlay row anatomy
      // (issue #354; assets/overlay-reference.png):
      // 1 state · 2 project/branch · 3 context-bar · 4 tokens · 5 cost ·
      // 6 model · 7 adapter icon. Extras (role badge, in-progress task dots,
      // waiting question, active-tool label, elapsed/id, history canvas) are
      // tucked between the primary slots with `display:none` until populated.
      el.innerHTML =
        '<span class="row-state-icon"></span>' +
        '<span class="row-num"></span>' +
        '<span class="row-sub-badge" style="display:none"></span>' +
        '<span class="row-bg-badge" style="display:none"></span>' +
        '<span class="row-role-badge" style="display:none"></span>' +
        '<span class="row-origin" style="display:none"></span>' +
        '<span class="row-branch"></span>' +
        '<span class="row-tool" style="display:none"></span>' +
        '<span class="row-ctx-bar"><span class="row-ctx-fill"></span><span class="row-ctx-label"></span></span>' +
        '<span class="row-ctx-pct"></span>' +
        '<span class="row-yield-revert" style="display:none" title="Session work was reverted">↩</span>' +
        '<span class="row-cost"></span>' +
        '<canvas class="row-history"></canvas>' +
        '<span class="row-eta" style="display:none"></span>' +
        '<span class="row-spacer"></span>' +
        '<span class="row-model"></span>' +
        '<span class="row-adapter-icon" style="display:none"></span>' +
        '<span class="row-elapsed"></span>' +
        '<span class="row-created"></span>' +
        '<span class="row-id"></span>';
      el.querySelector('.row-cost').addEventListener('click', (e) => {
        e.stopPropagation();
        cycleCostDisplayMode();
      });
      updateSessionRow(el, agent, isChild);
      return el;
    }

    function updateSessionRow(el, agent, isChild) {
      const state = agent.state || 'ready';
      const metrics = agent.metrics || {};
      const isActive = state === 'working' || state === 'waiting';

      // Each column updates independently; the renderRow* helpers below own
      // one concern each and touch only their own .row-* element(s) + dataset
      // caches, so this call order is cosmetic. Split out of what used to be
      // one ~270-line function (issue #872).
      //
      // Several per-row concerns are NOT rendered here — they emit as their
      // own collapsible rows beneath the parent (see render()):
      //   · Task summary + waiting question  → createSummaryRow
      //   · Task progress dots               → createTaskListRow
      //     (mirrors TaskListView, SessionListView.swift:746–769)
      //   · Cache-creation regression badge (#813) → createCacheBloatRow
      //     (the version attribution can be a full sentence — too wide for
      //      the fixed-width icon row; mirrors macOS's cacheBloatBlock)
      // The agent number is painted into .row-num by the render loop after
      // this returns (renderRowRoleBadge sets an iconOverride flag it reads).
      renderRowStateIcon(el, state);
      renderRowAdapterIcon(el, agent);
      renderRowBranch(el, agent);
      renderRowOrigin(el, agent);
      renderRowSubBadge(el, agent);
      renderRowBgBadge(el, agent);
      renderRowContextBar(el, metrics);
      renderRowCost(el, metrics, agent);
      renderRowModel(el, metrics, agent);
      renderRowElapsed(el, agent, metrics, isActive);
      renderRowEta(el, metrics, state);
      renderRowCreated(el, agent);
      renderRowId(el, agent);
      renderRowRoleBadge(el, agent);
      renderRowTool(el, metrics, state);

      // Per-row history canvas is repainted by repaintHistory() on the
      // history-event WS path — no need to repaint here on every session
      // update (data didn't change).
    }

    // State icon — only update if state changed.
    function renderRowStateIcon(el, state) {
      if (el.dataset.state !== state) {
        el.dataset.state = state;
        el.querySelector('.row-state-icon').innerHTML = stateIcon(state);
      }
    }

    // Adapter icon (issue #260) — looked up in the registry the daemon
    // publishes at /api/v1/agents. Hidden when the registry has no entry
    // for this adapter (e.g. older daemons, or before the registry fetch
    // completes); other row columns (project, branch, ID) already
    // disambiguate, so a missing icon doesn't impair the row's meaning.
    // The dataset cache only advances on a successful populate so a
    // late-arriving registry repopulates rows on the next update without
    // needing a full re-render. The cache also keys on the current theme
    // so toggling light/dark re-renders the icon with the matching variant.
    //
    // SVGs are rendered inside an <img> with a base64 data: URL rather
    // than via innerHTML. The browser's image-loading sandbox blocks
    // <script> and on*= handlers in SVGs loaded this way, so even a
    // tampered daemon binary cannot inject script into the dashboard.
    // (btoa requires Latin-1; current SVGs are pure ASCII. If a future
    // adapter ships an SVG with non-ASCII glyphs, swap the encoder to
    // btoa(unescape(encodeURIComponent(svg))).)
    function renderRowAdapterIcon(el, agent) {
      const adapterKey = agent.adapter || '';
      const cachedAdapter = el.dataset.adapter || '';
      const cachedTheme = el.dataset.adapterTheme || '';
      const theme = resolvedTheme();
      const branding = agentRegistry[adapterKey];
      const themeChanged = cachedTheme !== theme;
      if (branding && (cachedAdapter !== adapterKey || el.dataset.adapterPopulated !== '1' || themeChanged)) {
        const adapterEl = el.querySelector('.row-adapter-icon');
        // icon_svg_light is meant for LIGHT theme (dark strokes on white);
        // icon_svg_dark is meant for DARK theme (light strokes on dark).
        // The Codex icon strokes near-black/near-white, so the wrong pick
        // makes it disappear into the background (issue #354 round-4).
        const svg = (theme === 'light' ? (branding.icon_svg_light || branding.icon_svg_dark)
                                       : (branding.icon_svg_dark || branding.icon_svg_light)) || '';
        if (svg) {
          adapterEl.innerHTML = '<img alt="" src="data:image/svg+xml;base64,' + btoa(svg) + '">';
          adapterEl.title = branding.display_name || adapterKey;
          adapterEl.style.display = '';
        } else {
          adapterEl.innerHTML = '';
          adapterEl.style.display = 'none';
        }
        el.dataset.adapter = adapterKey;
        el.dataset.adapterTheme = theme;
        el.dataset.adapterPopulated = '1';
      } else if (!branding && cachedAdapter !== adapterKey) {
        const adapterEl = el.querySelector('.row-adapter-icon');
        adapterEl.innerHTML = '';
        adapterEl.style.display = 'none';
        el.dataset.adapter = adapterKey;
        el.dataset.adapterPopulated = '0';
      }
    }

    // Branch
    function renderRowBranch(el, agent) {
      const branch = agent.git_branch || '';
      const branchEl = el.querySelector('.row-branch');
      if (branchEl.textContent !== branch) branchEl.textContent = branch || '—';
    }

    // Origin glyph (#538) — a cloud marks a session delivered by a relay
    // (remote daemon); local-socket sessions show nothing, so a local-only
    // dashboard is visually unchanged. Tooltip = the daemon's hostname.
    function renderRowOrigin(el, agent) {
      const originEl = el.querySelector('.row-origin');
      if (sessionOrigin(agent) === 'remote') {
        if (!originEl.dataset.on) {
          originEl.innerHTML = '<svg viewBox="0 0 24 24" width="11" height="11" fill="currentColor" aria-hidden="true"><path d="M19 18H6a4 4 0 0 1-.55-7.96 5 5 0 0 1 9.65-1.65A4 4 0 0 1 19 18z"/></svg>';
          originEl.dataset.on = '1';
        }
        originEl.style.display = '';
        originEl.title = daemonLabelFor(sourceIdOf(agent.session_id));
      } else if (originEl.style.display !== 'none') {
        originEl.style.display = 'none';
      }
    }

    // Subagent count badge — small filled circle next to the working
    // icon when the parent has active sub-agents. Mirrors macOS at
    // SessionListView.swift:481-490. Toggling .has-sub on the row
    // shrinks the branch column so columns downstream still align with
    // rows that don't have a badge.
    function renderRowSubBadge(el, agent) {
      const subBadge = el.querySelector('.row-sub-badge');
      const activeSubs = activeSubagentCount(agent);
      if (activeSubs > 0) {
        subBadge.style.display = '';
        if (subBadge.textContent !== String(activeSubs)) subBadge.textContent = String(activeSubs);
        el.classList.add('has-sub');
      } else {
        subBadge.style.display = 'none';
        el.classList.remove('has-sub');
      }
    }

    // Background-agent badge (#744) — a moon glyph marks a Claude Code Agent
    // View background agent that keeps running detached in the daemon pool
    // after its window is closed. Always shown for kind:bg; the amber
    // .is-detached state emphasizes "no open window owns this".
    function renderRowBgBadge(el, agent) {
      const bgBadge = el.querySelector('.row-bg-badge');
      const bg = agent.background;
      if (bg) {
        if (!bgBadge.dataset.on) {
          bgBadge.innerHTML = '<svg viewBox="0 0 24 24" width="11" height="11" fill="currentColor" aria-hidden="true"><path d="M12.74 2.02a.6.6 0 0 0-.66.86 7 7 0 1 1-9.2 9.2.6.6 0 0 0-.86.66A8.5 8.5 0 1 0 12.74 2.02z"/></svg>';
          bgBadge.dataset.on = '1';
        }
        bgBadge.style.display = '';
        const detached = !!bg.detached;
        bgBadge.classList.toggle('is-detached', detached);
        const name = (bg.name || '').trim();
        const label = name ? ' (' + name + ')' : '';
        bgBadge.title = detached
          ? 'Detached background agent' + label + ' — no open window; runs in the Claude Code daemon pool'
          : 'Background agent' + label;
        el.classList.add('has-bg');
      } else {
        bgBadge.style.display = 'none';
        bgBadge.classList.remove('is-detached');
        el.classList.remove('has-bg');
      }
    }

    // Context bar + token/pct overlays. The token count renders as an overlay
    // inside the context bar so the row stays compact (mirrors macOS
    // ContextBar's `label` slot at SessionListView.swift:432-439). The
    // standalone .row-ctx-pct lives on only when Settings hides the cost
    // column (showCostDisplay off), in which case it shows the % utilization
    // instead.
    function renderRowContextBar(el, metrics) {
      const ctxPct = metrics.context_utilization_percentage || 0;
      const pressure = metrics.pressure_level || '';
      const fill = el.querySelector('.row-ctx-fill');
      const ctxWidth = Math.min(100, ctxPct).toFixed(1) + '%';
      if (fill.style.width !== ctxWidth) fill.style.width = ctxWidth;
      fill.className = 'row-ctx-fill ' + pressureClass(pressure);

      const totalTok = metrics.total_tokens || 0;
      const labelEl = el.querySelector('.row-ctx-label');
      const labelText = totalTok > 0 ? formatTokens(totalTok) : '';
      if (labelEl.textContent !== labelText) labelEl.textContent = labelText;

      const pctEl = el.querySelector('.row-ctx-pct');
      const pctText = ctxPct > 0 ? ctxPct.toFixed(0) + '%' : '';
      if (pctEl.textContent !== pctText) pctEl.textContent = pctText;
      pctEl.style.color = ctxPct > 0 ? pressureColor(pressure) : '';
    }

    // Cost / CO2 (click to cycle, issue #829) + yield revert marker (#373):
    // ↩ next to cost when the session's work was later git-reverted.
    function renderRowCost(el, metrics, agent) {
      const costEl = el.querySelector('.row-cost');
      const { text: cost, title: costTitle } = costCellDisplay(metrics, costDisplayMode);
      if (costEl.textContent !== cost) costEl.textContent = cost;
      if (costEl.title !== costTitle) costEl.title = costTitle;

      const yieldEl = el.querySelector('.row-yield-revert');
      if (yieldEl) yieldEl.style.display = (agent.yield_state === 'reverted') ? '' : 'none';
    }

    // Model
    function renderRowModel(el, metrics, agent) {
      const model = shortModel(metrics.model_name || agent.model || '');
      const modelEl = el.querySelector('.row-model');
      if (modelEl.textContent !== model) modelEl.textContent = model;
    }

    // Elapsed
    function renderRowElapsed(el, agent, metrics, isActive) {
      const elapsed = formatElapsed(agent.first_seen, metrics.elapsed_seconds, isActive);
      const elapsedEl = el.querySelector('.row-elapsed');
      elapsedEl.textContent = elapsed;
      elapsedEl.dataset.firstSeen = isActive ? (agent.first_seen || '') : '';
      elapsedEl.dataset.elapsedStored = metrics.elapsed_seconds || '';
      elapsedEl.dataset.active = isActive ? '1' : '0';
    }

    // Task-completion ETA chip (issue #558) — agent-authored estimate,
    // shown only while working. The 1s tickElapsed loop re-derives the
    // countdown from the dataset between polls.
    function renderRowEta(el, metrics, state) {
      const etaEl = el.querySelector('.row-eta');
      const etaInfo = taskEtaPresentation(metrics, state, Date.now() / 1000);
      if (etaInfo) {
        etaEl.style.display = '';
        etaEl.textContent = etaInfo.text;
        etaEl.title = etaInfo.title;
        etaEl.classList.toggle('stale', etaInfo.stale);
        etaEl.dataset.eta = metrics.task_completion_eta || '';
        etaEl.dataset.total = metrics.task_estimate.total_rounds;
        etaEl.dataset.completed = metrics.task_estimate.completed_rounds;
        etaEl.dataset.updatedAt = metrics.task_estimate.updated_at || '';
      } else {
        etaEl.style.display = 'none';
        etaEl.dataset.eta = '';
      }
    }

    // Created chip — only stamp the first_seen on the element; the 1s
    // tickElapsed loop owns the textContent. first_seen is immutable per
    // session so this is a constant after row creation.
    function renderRowCreated(el, agent) {
      const createdEl = el.querySelector('.row-created');
      if (createdEl) {
        const fs = String(agent.first_seen || '');
        if (createdEl.dataset.firstSeen !== fs) {
          createdEl.dataset.firstSeen = fs;
          if (!fs) createdEl.textContent = '';
        }
      }
    }

    // Short ID
    function renderRowId(el, agent) {
      const idEl = el.querySelector('.row-id');
      const sid = shortID(agent.session_id);
      if (idEl.textContent !== sid) idEl.textContent = sid;
    }

    // Role badge — icon + role name when both are present. When only an
    // icon is set (no role string), the icon replaces the agent number in
    // .row-num — matches macOS at SessionListView.swift:469-479. The
    // render loop puts the numeric agentNum into .row-num after this fn
    // returns, so we set a flag the loop reads back.
    function renderRowRoleBadge(el, agent) {
      const roleBadge = el.querySelector('.row-role-badge');
      const role = agent.role || '';
      const numEl = el.querySelector('.row-num');
      const wantIcon = (!role && agent.icon) ? agent.icon : '';
      if (role) {
        roleBadge.style.display = '';
        roleBadge.textContent = (agent.icon ? agent.icon + ' ' : '') + role;
        roleBadge.title = agent.description || role;
      } else {
        roleBadge.style.display = 'none';
        if (wantIcon) numEl.title = agent.description || '';
      }
      if ((numEl.dataset.iconOverride || '') !== wantIcon) {
        numEl.dataset.iconOverride = wantIcon;
      }
    }

    // Active tool label — shown only when agent is working and inside a tool call.
    // When no tool is open but the session is held working by a live Bash
    // background process (run_in_background), surface that instead so the row
    // explains why it's still working past the turn's end. See issue #445.
    function renderRowTool(el, metrics, state) {
      const toolEl = el.querySelector('.row-tool');
      const tools = metrics.last_open_tool_names || [];
      const bgCount = metrics.background_process_count || 0;
      if (metrics.has_open_tool_call && tools[0] && state === 'working') {
        const raw = tools[0];
        const isUser = raw === 'AskUserQuestion' || raw === 'ExitPlanMode';
        toolEl.style.display = '';
        toolEl.textContent = toolLabel[raw] || raw.slice(0, 12);
        toolEl.className = 'row-tool' + (isUser ? ' tool-user' : '');
        toolEl.title = '';
      } else if (bgCount > 0 && state === 'working') {
        toolEl.style.display = '';
        toolEl.textContent = '⚙ ' + bgCount + ' bg';
        toolEl.className = 'row-tool';
        toolEl.title = bgCount + ' background process' + (bgCount === 1 ? '' : 'es') + ' running';
      } else {
        toolEl.style.display = 'none';
        toolEl.title = '';
      }
    }

    // pressureAlertItem builds the pressure-alert sub-row for an active
    // agent in an alert-worthy pressure state, or null otherwise. Extracted
    // from emitAgentRowItems (issue #901 cognitive-complexity cleanup).
    function pressureAlertItem(a) {
      const isActive = a.state === 'working' || a.state === 'waiting';
      const pressure = a.metrics ? a.metrics.pressure_level : '';
      if (!isActive || !isAlertPressure(pressure)) return null;
      return {type: 'alert', key: 'al:' + a.session_id, pressure: pressure};
    }

    // cacheBloatItem builds the cache-creation regression badge sub-row
    // (#813) — separate row beneath the parent; the version-attribution
    // string can be a full sentence, too wide for this fixed-width icon row
    // (mirrors macOS's cacheBloatBlock) — or null when there's no bloat to
    // report. Extracted from emitAgentRowItems (issue #901
    // cognitive-complexity cleanup).
    function cacheBloatItem(a) {
      if (!a.metrics?.cache_bloat) return null;
      return {type: 'cachebloat', key: 'cb:' + a.session_id, agent: a};
    }

    // taskSummaryItem builds the task-summary + waiting-question
    // collapsible sub-row (issue #738) — shown when there's a summary (any
    // state) or a pending question (waiting) — or null otherwise. Extracted
    // from emitAgentRowItems (issue #901 cognitive-complexity cleanup).
    function taskSummaryItem(a) {
      if (!hasTaskSummaryOrQuestion(a)) return null;
      return {type: 'summary', key: 's:' + a.session_id, agent: a, isChild: false};
    }

    // taskProgressItem builds the task-progress-dots sub-row — separate
    // from the parent row so the dots don't push the meta columns off the
    // session row, matching the overlay's TaskListView placement
    // (SessionListView.swift:629-632) — or null when there are no open
    // tasks. Extracted from emitAgentRowItems (issue #901
    // cognitive-complexity cleanup).
    function taskProgressItem(a) {
      const tasks = a.metrics?.tasks || [];
      const tasksOpen = tasks.length > 0 && !tasks.every(t => t.status === 'completed');
      if (!tasksOpen) return null;
      return {type: 'tasks', key: 'tk:' + a.session_id, agent: a, isChild: false};
    }

    // emitAgentRowItems pushes an agent's own row plus its collapsible
    // sub-rows (pressure alert, cache-bloat badge, task summary, task
    // progress) onto the flat render-item list. Extracted from emitGroup's
    // per-agent loop body (issue #901 cognitive-complexity cleanup).
    function emitAgentRowItems(items, a, agentNum, depth) {
      items.push({type: 'agent', key: 'a:' + a.session_id, agent: a, num: agentNum, isChild: false, depth: depth});
      const alert = pressureAlertItem(a);
      if (alert) items.push(alert);
      const cacheBloat = cacheBloatItem(a);
      if (cacheBloat) items.push(cacheBloat);
      const summary = taskSummaryItem(a);
      if (summary) items.push(summary);
      const taskProgress = taskProgressItem(a);
      if (taskProgress) items.push(taskProgress);
    }

    function isAlertPressure(pressure) {
      return pressure === 'high' || pressure === 'warning' || pressure === 'critical';
    }

    function hasTaskSummaryOrQuestion(a) {
      return !!(a.metrics && (a.metrics.task_summary || (a.state === 'waiting' && a.metrics.last_assistant_text)));
    }

    // --- Render ---
    function render() {
      const list = document.getElementById('session-list');
      const empty = document.getElementById('empty-state');

      // Ordering: trust the daemon. /api/v1/sessions ships groups + agents
      // in a stable order; WS deltas only append. Sorting by state on every
      // render makes rows jump as sessions transition (working→ready), which
      // the macOS overlay doesn't do (SessionListView.swift:994 iterates
      // group.agents straight). Match that.

      if (dashboardGroups.length === 0) {
        empty.style.display = 'flex';
        list.innerHTML = '';
      } else {
        empty.style.display = 'none';

        // Build flat list of render items: [{type, key, data}]
        const items = [];
        // Show headers when there's more than one top-level group, or when any
        // group nests sub-groups (Gas Town rigs) that need their own headers.
        const showHeaders = dashboardGroups.length > 1 ||
          dashboardGroups.some(g => g.groups?.length);
        let agentNum = 0;

        // Local-wins (#538): a relay session whose bare id is also delivered by
        // a local source collapses to the local row — skip the relay duplicate.
        const localIds = localBareIds(dashboardGroups);

        // Emit a group header followed by its agents, then recurse into nested
        // sub-groups (Gas Town rigs) as further collapsible headers. Collapse
        // state is keyed by a path-qualified key (parent/name) so same-named
        // rigs under different orchestrators don't share a collapse toggle.
        function emitGroup(g, parentKey, depth) {
          const key = parentKey ? parentKey + '/' + g.name : g.name;
          if (showHeaders) {
            items.push({type: 'group', key: 'g:' + key, group: g, groupKey: key, depth: depth});
          }
          // Collapse only applies while its header is rendered (#564): with a
          // single flat group no header exists, so a stale persisted collapse
          // would otherwise blank the dashboard with no way to recover.
          if (showHeaders && isGroupCollapsed(key)) return;
          for (const a of (g.agents || [])) {
            if (isShadowedRemote(a, localIds)) continue;
            agentNum++;
            emitAgentRowItems(items, a, agentNum, depth);
          }
          for (const sub of (g.groups || [])) emitGroup(sub, key, depth + 1);
        }
        for (const g of dashboardGroups) emitGroup(g, '', 0);

        // Reconcile
        reconcile(list, items,
          item => item.key,
          item => {
            if (item.type === 'group') return createGroupHeader(item.group, item.groupKey, item.depth);
            if (item.type === 'alert') return createAlertRow(item.pressure);
            if (item.type === 'cachebloat') return createCacheBloatRow(item.agent);
            if (item.type === 'tasks') return createTaskListRow(item.agent, item.isChild);
            if (item.type === 'summary') return createSummaryRow(item.agent, item.isChild);
            const el = createSessionRow(item.agent, item.isChild);
            el.classList.toggle('nested', !!item.depth);
            paintRowNum(el, item.num);
            return el;
          },
          (el, item) => {
            if (item.type === 'group') { updateGroupHeader(el, item.group, item.groupKey, item.depth); return; }
            if (item.type === 'alert') { updateAlertRow(el, item.pressure); return; }
            if (item.type === 'cachebloat') { updateCacheBloatRow(el, item.agent); return; }
            if (item.type === 'tasks') { updateTaskListRow(el, item.agent); return; }
            if (item.type === 'summary') { updateSummaryRow(el, item.agent); return; }
            updateSessionRow(el, item.agent, item.isChild);
            paintRowNum(el, item.num);
            el.className = 'session-row' + (item.isChild ? ' child' : '') + (item.depth ? ' nested' : '');
          }
        );
      }

      // Summary: header state icons. Iterate top-level sessions in render
      // order (groups sorted, agents sorted) so the icon order matches the
      // visible row order.
      const all = [];
      const topLevel = [];
      function collectAll(agents) {
        for (const a of agents) {
          all.push(a);
          if (a.children) collectAll(a.children);
        }
      }
      // Walk top-level groups and nested rig sub-groups so the header summary
      // and counts include Gas Town rig agents.
      (function walk(groups) {
        for (const g of groups) {
          for (const a of (g.agents || [])) topLevel.push(a);
          collectAll(g.agents || []);
          if (g.groups?.length) walk(g.groups);
        }
      })(dashboardGroups);
      updateSummary(all, topLevel);
      renderHeaderTitle();
      refreshSummaryCollapseAllBtn();
    }

    // Collect every session id across all groups/sub-groups — backs the
    // header's collapse-all (issue #738).
    function allSessionIds() {
      const ids = [];
      (function walk(groups) {
        for (const g of (groups || [])) {
          for (const a of (g.agents || [])) ids.push(a.session_id);
          walk(g.groups);
        }
      })(dashboardGroups);
      return ids;
    }

    // Keep the header collapse-all button's glyph/label in sync with whether
    // any summary is currently collapsed.
    function refreshSummaryCollapseAllBtn() {
      const btn = document.getElementById('summary-collapse-all');
      if (!btn) return;
      const collapsedNow = anySummaryCollapsed();
      btn.textContent = collapsedNow ? '⊞' : '⊟';
      btn.title = collapsedNow ? 'Expand all task summaries' : 'Collapse all task summaries';
      btn.setAttribute('aria-label', btn.title);
    }

    function createAlertRow(pressure) {
      const el = document.createElement('div');
      el.className = 'pressure-alert-row';
      updateAlertRow(el, pressure);
      return el;
    }

    function updateAlertRow(el, pressure) {
      const cls = pressure === 'critical' ? 'alert-critical' : 'alert-high';
      el.innerHTML = '<span class="' + cls + '">\u26A0 Switch to a fresh session soon</span>';
    }

    // Cache-creation regression badge (#813) \u2014 a separate row beneath the
    // parent, like the pressure alert, since the version-attribution string
    // can be a full sentence rather than fitting a fixed-width icon slot.
    function createCacheBloatRow(agent) {
      const el = document.createElement('div');
      el.className = 'row-cache-bloat-row';
      el.dataset.sessionId = agent.session_id;
      updateCacheBloatRow(el, agent);
      return el;
    }

    function updateCacheBloatRow(el, agent) {
      const metrics = agent.metrics || {};
      const badgeText = cacheBloatBadgeText(metrics.cache_bloat_tooltip, metrics.cache_bloat_percent);
      if (el.dataset.badge !== badgeText) {
        el.dataset.badge = badgeText;
        el.innerHTML = '<span class="row-cache-bloat">' + esc(badgeText) + '</span>';
      }
      // The longer hover explanation is composed daemon-side (issue #827) and
      // rendered verbatim so both UIs never re-derive (and silently diverge
      // on) the wording.
      el.querySelector('.row-cache-bloat').title = metrics.cache_bloat_explanation || '';
    }

    // Shared factory for trailing-row variants rendered beneath the parent
    // session row (tasks, question).
    function makeRow(className, innerHTML, agent, update, isChild) {
      const el = document.createElement('div');
      el.className = className + (isChild ? ' child' : '');
      if (innerHTML) el.innerHTML = innerHTML;
      update(el, agent);
      return el;
    }

    // Task progress strip \u2014 rendered under a parent session row when the
    // session has in-progress TaskCreate/TaskUpdate tasks. Mirrors
    // TaskListView at SessionListView.swift:746-769.
    function createTaskListRow(agent, isChild) {
      return makeRow('row-tasks-row',
        '<div class="row-tasks-dots"></div><span class="row-tasks-counter"></span>',
        agent, updateTaskListRow, isChild);
    }

    // Statuses are a closed enum on the daemon side (claudecode parser).
    // We still whitelist them on the client so a future malformed status
    // can't inject CSS-class noise via the .task-dot template.
    const TASK_STATUSES = new Set(['pending', 'in_progress', 'completed']);
    function safeTaskStatus(s) {
      return TASK_STATUSES.has(s) ? s : 'pending';
    }
    function updateTaskListRow(el, agent) {
      const tasks = agent.metrics?.tasks || [];
      if (tasks.length === 0) { el.style.display = 'none'; return; }
      el.style.display = '';
      const done = tasks.filter(t => t.status === 'completed').length;
      // Signature avoids reflowing the dot strip when nothing has changed.
      const states = tasks.map(t => safeTaskStatus(t.status)[0]).join('');
      const sig = done + '/' + tasks.length + ':' + states;
      if (el.dataset.sig === sig) return;
      el.dataset.sig = sig;
      const dotsEl = el.querySelector('.row-tasks-dots');
      const taskLabel = (t) => (t.status === 'in_progress' && t.active_form) ? t.active_form : t.subject;
      const dotHtml = tasks.map(t =>
        '<span class="task-dot task-' + safeTaskStatus(t.status) + '" title="' + esc(taskLabel(t)) + '"></span>'
      ).join('');
      dotsEl.innerHTML = dotHtml;
      el.querySelector('.row-tasks-counter').textContent = done + ' / ' + tasks.length;
    }

    // Task summary + waiting question — a single collapsible block beneath the
    // parent row (issue #738). The summary ("what is this session about") shows
    // in any state; the question shows only while waiting. A chevron collapses
    // this one entry; the header offers a collapse-all. Mirrors the macOS
    // SessionRowView.summaryBlock.
    function createSummaryRow(agent, isChild) {
      const el = makeRow('row-summary-row',
        '<div class="summary-head"><span class="summary-chevron"></span>' +
        '<span class="summary-title"></span></div>' +
        '<div class="summary-question"></div>',
        agent, updateSummaryRow, isChild);
      el.querySelector('.summary-head').addEventListener('click', () => {
        if (el._sessionId) { toggleSummaryCollapsed(el._sessionId); render(); }
      });
      return el;
    }
    function updateSummaryRow(el, agent) {
      const summary = agent.metrics?.task_summary || '';
      // Prefer the terse one-line headline (issue #759); fall back to the full
      // last-assistant text for older daemons. The full text is kept for hover.
      const questionFull = (agent.state === 'waiting' && agent.metrics?.last_assistant_text) || '';
      const question = (agent.state === 'waiting' && agent.metrics && (agent.metrics.question_headline || agent.metrics.last_assistant_text)) || '';
      if (!summary && !question) { el.style.display = 'none'; return; }
      el.style.display = '';
      el._sessionId = agent.session_id;
      const collapsed = isSummaryCollapsed(agent.session_id);
      el.classList.toggle('collapsed', collapsed);
      el.querySelector('.summary-chevron').textContent = collapsed ? '▸' : '▾';
      const titleEl = el.querySelector('.summary-title');
      const title = summary || 'Waiting for input';
      if (titleEl.dataset.full !== title) {
        titleEl.textContent = title;
        titleEl.title = summary || '';
        titleEl.dataset.full = title;
      }
      titleEl.classList.toggle('no-summary', !summary);
      const qEl = el.querySelector('.summary-question');
      if (!collapsed && question) {
        qEl.style.display = '';
        if (qEl.dataset.full !== question) {
          qEl.textContent = question;
          qEl.title = questionFull || question;
          qEl.dataset.full = question;
        }
      } else {
        qEl.style.display = 'none';
      }
    }

    // Aggregated state icons in the header — mirrors overlay's sessionIconsView
    // at SessionListView.swift:346. Show ≤3 per-session glyphs colored by
    // state when there are 1–3 sessions, else collapse to "N sessions" text.
    // "Sessions" here means top-level sessions only (children excluded).
    function updateSummary(_allFlat, topLevel) {
      const host = document.getElementById('app-state-icons');
      if (!host) return;
      const list = topLevel || [];
      let sig = 'count:' + list.length;
      if (list.length === 0) sig = 'empty';
      else if (list.length <= 3) sig = 'icons:' + list.map(s => s.state || 'ready').join(',');
      if (host.dataset.sig === sig) return;
      host.dataset.sig = sig;
      if (list.length === 0) {
        host.innerHTML = '<span class="app-state-count" style="color:var(--muted)">○</span>';
        return;
      }
      if (list.length <= 3) {
        let html = '';
        for (const s of list) {
          html += '<span class="app-state-icon" title="' + esc(s.state || 'ready') + '">' + stateIcon(s.state || 'ready') + '</span>';
        }
        host.innerHTML = html;
        return;
      }
      host.innerHTML = '<span class="app-state-count">' + list.length + ' sessions</span>';
    }

    // --- Elapsed time tick (1s) ---
    function tickElapsed() {
      const now = Date.now() / 1000;
      for (const el of document.querySelectorAll('.row-elapsed')) tickRowElapsed(el, now);
      // Task-ETA chips burn down between polls. Only rows whose chip is
      // visible carry dataset.eta (updateSessionRow clears it otherwise),
      // and visibility/suppression decisions stay with updateSessionRow —
      // the tick only refreshes the countdown and staleness.
      for (const el of document.querySelectorAll('.row-eta')) tickRowEta(el, now);
      // Debug-mode "created" chip — only iterate when the toggle is on so
      // we don't waste DOM reads when the chip is hidden.
      if (settings.debugMode) {
        for (const el of document.querySelectorAll('.row-created')) tickRowCreated(el, now);
      }
    }

    function tickRowElapsed(el, now) {
      if (el.dataset.active !== '1') return;
      const fs = Number.parseFloat(el.dataset.firstSeen);
      if (fs) el.textContent = fmtDuration(Math.max(0, Math.floor(now - fs)));
    }

    function tickRowEta(el, now) {
      const eta = Number.parseFloat(el.dataset.eta);
      if (!eta) return;
      const info = taskEtaPresentation({
        task_completion_eta: eta,
        task_estimate: {
          total_rounds: Number.parseInt(el.dataset.total, 10) || 0,
          completed_rounds: Number.parseInt(el.dataset.completed, 10) || 0,
          updated_at: Number.parseFloat(el.dataset.updatedAt) || 0,
        },
      }, 'working', now);
      if (!info) return;
      el.textContent = info.text;
      el.title = info.title;
      el.classList.toggle('stale', info.stale);
    }

    function tickRowCreated(el, now) {
      const fs = Number.parseFloat(el.dataset.firstSeen);
      if (fs) el.textContent = 'created ' + fmtDuration(Math.max(0, Math.floor(now - fs))) + ' ago';
    }
    setInterval(tickElapsed, 1000);

    // Re-paint the quota chip strip every 30s so the pace marker, "resets
    // in …" labels, and forecast ETA tick forward even when no session
    // update arrives. Same cadence as the trailing-window cost re-hydrate
    // below — well under the 5-minute window-rollover horizon.
    setInterval(renderHeaderTitle, 30000);

    // --- Per-row history bar ---
    // The overlay shows history only as per-row mini-bars (no global heatmap).
    // We mirror that here; the daemon's bucket priorities drive the per-row
    // paint via paintRowHistory(). State→color is local so we don't depend on
    // CSS vars at canvas-paint time.
    const HISTORY_BAR_COLORS = {
      working: '#8B5CF6',
      waiting: '#FF9500',
      ready:   '#34C759',
    };

    function paintRowHistory(canvas, sessionID) {
      if (!canvas) return;
      const rec = timelineHistory.get(sessionID);
      const dpr = window.devicePixelRatio || 1;
      const w = canvas.offsetWidth || 120;
      const h = canvas.offsetHeight || 16;
      const pxW = w * dpr, pxH = h * dpr;
      if (canvas.width !== pxW || canvas.height !== pxH) {
        canvas.width = pxW;
        canvas.height = pxH;
      }
      const ctx = canvas.getContext('2d');
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, h);
      if (!rec?.states.length) return;
      // Right-anchor: newest bucket lands at the right edge, oldest fills
      // backwards leftward. Empty leading buckets (no data yet) are
      // represented by an unpainted left section. Mirrors macOS
      // HistoryBarView (SessionListView.swift:555).
      const states = rec.states;
      const colW = w / currentBucketCount;
      const startCol = currentBucketCount - states.length;
      for (let i = 0; i < states.length; i++) {
        const color = HISTORY_BAR_COLORS[states[i]];
        if (!color) continue;
        ctx.fillStyle = color;
        ctx.fillRect((startCol + i) * colW, 0, Math.max(colW, 0.5), h);
      }
    }

    function repaintHistory() {
      for (const el of document.querySelectorAll('.session-row')) {
        const canvas = el.querySelector('.row-history');
        const sid = el.dataset.sessionId;
        if (canvas && sid) paintRowHistory(canvas, sid);
      }
    }

    // --- Initial load ---
    // Wait for both agents (icon branding) and sessions (the actual data)
    // before the first render, so rows land with icons already populated.
    // Older daemons predating issue #260 won't have /api/v1/agents — the
    // null fallback leaves agentRegistry empty and rows render without
    // icons (project, branch, ID still disambiguate).
    Promise.all([
      fetch('/api/v1/agents').then(r => r.ok ? r.json() : null).catch(() => null),
      fetch('/api/v1/sessions').then(r => r.ok ? r.json() : null).catch(() => null),
    ]).then(([entries, resp]) => {
      ingestAgentRegistry(entries);
      ingestInitialSessions(resp);
      render();
      repaintHistory();
    });

    function ingestAgentRegistry(entries) {
      if (!Array.isArray(entries)) return;
      for (const e of entries) {
        if (e?.name) agentRegistry[e.name] = e;
      }
    }

    // normalizeGroupAgents ensures every group has an `agents` array. A
    // group with no direct agents (e.g. a gastown orchestrator group whose
    // agents all live in rig sub-groups) omits `agents` entirely (json
    // omitempty) — normalize once at ingest so every consumer —
    // rebuildIndex, render, group headers — can iterate unconditionally.
    // Extracted from ingestInitialSessions (issue #901
    // cognitive-complexity cleanup).
    function normalizeGroupAgents(groups) {
      for (const g of groups) {
        if (g && !g.agents) g.agents = [];
      }
    }

    function ingestInitialSessions(resp) {
      if (!resp) return;
      dashboardGroups = Array.isArray(resp) ? resp : (resp.groups || []);
      normalizeGroupAgents(dashboardGroups);
      dashboardProviderCosts = (resp && !Array.isArray(resp) && resp.provider_costs) || {};
      rebuildIndex();
    }
    // structureSignature captures the daemon-computed shape of the group tree:
    // group nesting (Gas Town rig sub-groups) + each session's role/icon/worker
    // id. WS session deltas carry NONE of this (just the flat session), and the
    // daemon re-nests a session / fills in its role asynchronously as the
    // orchestrator poller catches up — with no create/delete event. So the only
    // reliable way to reflect nesting + role-emoji changes is to poll the REST
    // structure and adopt it when this signature changes. Pure metric ticks
    // (cost/context/tokens) don't alter the signature, so they keep flowing
    // through the in-place merge below with no reorder.
    function structureSignature(groups) {
      const parts = [];
      (function walk(gs, depth) {
        for (const g of (gs || [])) {
          parts.push(depth + '#' + (g.name || '') + '#' + (g.type || '') + '#' + (g.status || ''));
          (function agents(arr) {
            for (const a of (arr || [])) {
              parts.push('a:' + a.session_id + ':' + (a.role || '') + ':' + (a.icon || '') + ':' + (a.worker_id || ''));
              if (a.children) agents(a.children);
            }
          })(g.agents);
          if (g.groups) walk(g.groups, depth + 1);
        }
      })(groups);
      return parts.join('|');
    }
    // Periodic re-hydration. Two jobs, one fetch:
    //   1. STRUCTURE — if the daemon's group tree / role metadata changed
    //      (rig nesting settled, a new agent gained its role emoji), adopt the
    //      fresh tree wholesale. Collapse state survives (collapsedGroups is
    //      keyed by name/path). This is the only path that reflects nesting +
    //      role changes, since WS deltas can't.
    //   2. METRICS — when structure is unchanged, merge trailing-window `costs`
    //      and per-agent `metrics.rate_limit*` in place (no reorder), so the
    //      cost figures and quota chips don't go stale between WS deltas.
    function rehydratePoll() {
      return fetch('/api/v1/sessions').then(r => r.json()).catch(() => null).then(resp => {
        if (!resp) return;
        const fresh = Array.isArray(resp) ? resp : (resp.groups || []);
        // Structure / role metadata changed → adopt the daemon's tree.
        if (structureSignature(fresh) !== structureSignature(dashboardGroups)) {
          dashboardGroups = fresh;
          if (!Array.isArray(resp) && resp.provider_costs) dashboardProviderCosts = resp.provider_costs;
          rebuildIndex();
          render();
          repaintHistory();
          return;
        }
        // Index fresh groups (by path, so a rig can't shadow a same-named
        // top-level project) + agents recursively for the in-place merge.
        const byPath = new Map();
        const freshAgents = new Map();
        (function walkFresh(gs, parentKey) {
          for (const g of (gs || [])) {
            const key = parentKey ? parentKey + '/' + g.name : (g.name || '');
            byPath.set(key, g);
            for (const a of (g.agents || [])) {
              if (a?.session_id) freshAgents.set(a.session_id, a);
              if (a?.children) (function ix(arr){ for (const c of arr||[]){ if (c.session_id) { freshAgents.set(c.session_id, c); } if (c.children) { ix(c.children); } } })(a.children);
            }
            if (g.groups) walkFresh(g.groups, key);
          }
        })(fresh, '');
        let changed = false;
        (function walkGroups(groups, parentKey) {
          for (const g of groups) {
            const key = parentKey ? parentKey + '/' + g.name : (g.name || '');
            const f = byPath.get(key);
            if (f?.costs && JSON.stringify(g.costs) !== JSON.stringify(f.costs)) {
              g.costs = f.costs;
              changed = true;
            }
            (function mergeRateLimit(arr) {
              for (const a of arr || []) {
                const fa = freshAgents.get(a.session_id);
                if (fa?.metrics) {
                  if (!a.metrics) a.metrics = {};
                  const newRL = fa.metrics.rate_limit || null;
                  const newETA = fa.metrics.rate_limit_forecast_eta || null;
                  if (JSON.stringify(a.metrics.rate_limit || null) !== JSON.stringify(newRL)) {
                    a.metrics.rate_limit = newRL;
                    changed = true;
                  }
                  if ((a.metrics.rate_limit_forecast_eta || null) !== newETA) {
                    a.metrics.rate_limit_forecast_eta = newETA;
                    changed = true;
                  }
                }
                if (a.children) mergeRateLimit(a.children);
              }
            })(g.agents);
            if (g.groups) walkGroups(g.groups, key);
          }
        })(dashboardGroups, '');
        // Refresh per-provider windowed spend the same way as group costs —
        // it rides this response, not the WebSocket deltas.
        const freshProviderCosts = (!Array.isArray(resp) && resp.provider_costs) || {};
        if (JSON.stringify(freshProviderCosts) !== JSON.stringify(dashboardProviderCosts)) {
          dashboardProviderCosts = freshProviderCosts;
          changed = true;
        }
        if (changed) render();
      });
    }
    // Adaptive cadence: an orchestrator's nesting/role metadata changes
    // asynchronously (no WS event), so poll fast (2.5s) while one is present to
    // reflect it promptly. With no orchestrator the structure is WS-driven and
    // only `costs` go stale, so fall back to 30s. Recursive setTimeout (not
    // setInterval) so each tick re-reads the cadence and never overlaps a fetch.
    function scheduleRehydratePoll() {
      const hasOrchestrator = dashboardGroups.some(g =>
        g && (g.type === 'gastown' || g.groups?.length));
      setTimeout(() => {
        rehydratePoll().finally(scheduleRehydratePoll);
      }, hasOrchestrator ? 2500 : 30000);
    }
    scheduleRehydratePoll();

    // --- Sources & connections (multi-source) ---
    // The dashboard connects to one or more sources at once: the local source
    // (the origin that served this page — a daemon or a relay) and/or a relay
    // server entered in Settings → Sources. A daemon speaks raw PushMessage
    // frames; a relay wraps them in a `push` envelope behind a `hello`
    // handshake. One handler covers both: we always send a client `hello` (a
    // daemon ignores unexpected frames; a relay requires it) and dispatch by
    // frame type — `push` unwraps `.msg`, relay control frames
    // (`snapshot`/`daemon_status`) feed the connection tooltip, and anything
    // else is a raw daemon frame processed exactly as the single socket was.
    //
    // Relay sessions are keyed by the compound `(daemon_id, session_id)` (#537):
    // the relay Push envelope carries the authoritative `source` (daemon id),
    // which `normalizeSourcedFrame` folds into the inner frame's id at the push
    // boundary so two *different* daemons sharing a session_id (e.g. proc-<pid>)
    // never merge into one row. Local-socket frames keep their bare id (daemon =
    // self), so the same daemon reached over both the local socket and a relay
    // still collapses to one row.
    //
    // The pure id-folding/origin helpers (compoundSessionId, displaySessionId,
    // sessionOrigin, sourceIdOf, localBareIds, isShadowedRemote,
    // daemonSessionIds) live in sessionIdentity.js; the pure wire-protocol
    // helpers (relayFrameKind, seqGap, aggregateConnState, relayWsUrl) live in
    // connectionProtocol.js — both imported above.

    function localWsUrl() {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      return proto + '://' + location.host + '/api/v1/sessions/stream';
    }

    // Active sources, keyed by a stable id. Each: { id, label, kind, wsUrl,
    // state, ws, reconnectDelay, closing, daemons: Map<id,{label,status}> }.
    const sources = new Map();

    // daemonLabelFor resolves a relay daemon id to its hostname label for the
    // origin-glyph tooltip (#538), scanning the live per-source daemon maps
    // (populated from snapshot/daemon_status). Falls back to the id itself.
    function daemonLabelFor(daemonId) {
      if (!daemonId) return '';
      for (const src of sources.values()) {
        const d = src.daemons?.get(daemonId);
        if (d?.label) return d.label;
      }
      return daemonId;
    }

    function desiredSources() {
      const out = [];
      if (settings.enableLocalSource) {
        out.push({ id: 'local', label: 'Local', kind: 'local', wsUrl: localWsUrl() });
      }
      if (settings.enableRelaySource) {
        const wsUrl = relayWsUrl(settings.relayUrl);
        if (wsUrl) out.push({ id: 'relay:' + wsUrl, label: settings.relayUrl || 'Relay', kind: 'relay', wsUrl, token: settings.relayToken || '' });
      }
      return out;
    }

    // rebuildSources reconciles the live connections with the configured
    // sources, closing dropped ones and opening new ones. Called on load and
    // whenever a Sources setting changes, so toggling reconnects without a
    // page reload.
    function rebuildSources() {
      const desired = desiredSources();
      const desiredById = new Map(desired.map(s => [s.id, s]));
      for (const [id, src] of sources) {
        const want = desiredById.get(id);
        // Drop a source that's no longer desired, OR a relay source whose token
        // changed / is parked in 'unauthorized'. The source id is keyed by URL
        // only, so without this a corrected token would never reconnect (the id
        // still matches) and an auth-failed relay would stay stuck. Tearing it
        // down here lets the loop below recreate it with the new credential.
        const stale = want && src.kind === 'relay' && (src.token !== want.token || src.state === 'unauthorized');
        if (!want || stale) {
          src.closing = true;
          try { if (src.ws) src.ws.close(); } catch (e) { console.debug('irrlicht: failed to close stale source socket', e); }
          sources.delete(id);
        }
      }
      for (const d of desired) {
        if (!sources.has(d.id)) {
          const src = { state: 'connecting', ws: null, reconnectDelay: 1000, closing: false, daemons: new Map(), seqCursors: new Map(), ...d };
          sources.set(d.id, src);
          connectSource(src);
        }
      }
      updateWsStatus();
    }

    function connectSource(src) {
      src.state = src.ws ? 'reconnecting' : 'connecting';
      updateWsStatus();
      let ws;
      try { ws = new WebSocket(src.wsUrl); } catch (e) {
        console.debug('irrlicht: failed to open source socket', e);
        scheduleReconnect(src);
        return;
      }
      src.ws = ws;
      ws.onopen = function() {
        src.state = 'connected';
        src.reconnectDelay = 1000;
        // A browser can't set request headers on a WebSocket, so an auth-enabled
        // relay's bearer token rides in this first frame (relay sources only).
        const hello = { type: 'hello', protocol_version: 1, role: 'client' };
        if (src.kind === 'relay' && settings.relayToken) hello.token = settings.relayToken;
        try { ws.send(JSON.stringify(hello)); } catch (e) { console.debug('irrlicht: failed to send hello frame', e); }
        updateWsStatus();
      };
      ws.onmessage = function(evt) {
        let msg;
        try { msg = JSON.parse(evt.data); } catch (e) { console.debug('irrlicht: failed to parse source frame', e); return; }
        handleSourceFrame(src, msg);
      };
      ws.onerror = function() {};
      ws.onclose = function(ev) {
        if (src.closing) return;
        src.daemons.clear();
        src.seqCursors.clear();
        // 4401 = auth failed/revoked: retrying with the same token just loops, so
        // stop reconnecting (until settings change) instead of a tight loop.
        if (ev?.code === 4401) { src.state = 'unauthorized'; updateWsStatus(); return; }
        src.state = 'disconnected';
        updateWsStatus();
        scheduleReconnect(src);
      };
    }

    function scheduleReconnect(src) {
      if (src.closing) return;
      const delay = src.reconnectDelay;
      src.reconnectDelay = Math.min(src.reconnectDelay * 2, 30000);
      setTimeout(() => { if (!src.closing && sources.get(src.id) === src) connectSource(src); }, delay);
    }

    // purgeDaemonSessions drops every row from one relay daemon — the web
    // dashboard's response to a daemon disconnect (#540). The relay stopped
    // fanning out per-session deletes on disconnect, so we reproduce the prior
    // behaviour here (macOS fades the rows instead).
    function purgeDaemonSessions(daemonId) {
      const ids = daemonSessionIds(dashboardGroups, daemonId);
      for (const sid of ids) applySessionDelete(sid);
      if (ids.length) render();
    }

    // trackPushSeq advances one daemon's seq cursor and, on a gap, triggers an
    // immediate rehydrate instead of waiting for the 30s cadence (#600). The
    // cursor is keyed per daemon: a relay socket interleaves multiple daemons'
    // independent counters, so a per-socket cursor would gap on every switch.
    // Gap-triggered rehydrates are leading-edge throttled: the first gap heals
    // immediately, refires from a sustained drop burst ride the same fetch
    // (macOS gets the same coalescing from its debounced scheduleRehydration).
    let lastSeqGapRehydrate = 0;
    function trackPushSeq(src, key, seq) {
      if (!seq) return; // unstamped: connect snapshots, relay replays, older daemons
      const last = src.seqCursors.get(key) || 0;
      src.seqCursors.set(key, seq);
      if (!seqGap(last, seq)) return;
      const now = Date.now();
      if (now - lastSeqGapRehydrate < 1000) return;
      lastSeqGapRehydrate = now;
      rehydratePoll();
    }

    function handleSourceFrame(src, msg) {
      if (!msg) return;
      switch (relayFrameKind(msg)) {
        case 'hello_ack':
          return;
        case 'snapshot':
          src.daemons.clear();
          for (const d of (msg.daemons || [])) {
            if (d?.daemon_id) src.daemons.set(d.daemon_id, { label: d.daemon_label || d.daemon_id, status: d.status || 'connected' });
          }
          updateWsStatus();
          return;
        case 'daemon_status':
          if (msg.daemon_id) {
            if (msg.status === 'disconnected') {
              src.daemons.delete(msg.daemon_id);
              src.seqCursors.delete(msg.daemon_id);
              purgeDaemonSessions(msg.daemon_id);   // #540: relay no longer deletes them for us
            } else {
              src.daemons.set(msg.daemon_id, { label: msg.daemon_label || msg.daemon_id, status: msg.status || 'connected' });
            }
          }
          updateWsStatus();
          return;
        case 'push':
          if (msg.msg) {
            trackPushSeq(src, msg.source || '', msg.msg.seq);
            dispatchRawFrame(normalizeSourcedFrame(msg.source, msg.msg));
          }
          return;
        default:
          trackPushSeq(src, '', msg.seq);
          dispatchRawFrame(msg);
      }
    }

    // normalizeSourcedFrame folds a relay Push envelope's `source` (daemon id)
    // into the inner frame's session identity, so two daemons sharing a
    // session_id stay distinct downstream (#537). Mutates the inner frame in
    // place — it's freshly JSON.parsed per message and not reused. A frame with
    // no source (or an un-sourced relay) passes through unchanged. Orchestrator
    // state is global, not per-daemon, so it's left alone.
    function normalizeSourcedFrame(source, inner) {
      if (!source || !inner) return inner;
      if (inner.session) {
        if (inner.session.session_id) inner.session.session_id = compoundSessionId(source, inner.session.session_id);
        if (inner.session.parent_session_id) inner.session.parent_session_id = compoundSessionId(source, inner.session.parent_session_id);
      } else if (inner.type === 'history_snapshot' || inner.type === 'history_upgrade') {
        if (inner.session_id) inner.session_id = compoundSessionId(source, inner.session_id);
      } else if (inner.type === 'history_tick') {
        inner.buckets = remapSourcedKeys(source, inner.buckets);
        inner.bucket_generations = remapSourcedKeys(source, inner.bucket_generations);
      }
      return inner;
    }

    // remapSourcedKeys returns a fresh object with each session_id key folded
    // with the daemon source. history_tick keys its bucket maps by session_id.
    function remapSourcedKeys(source, obj) {
      if (!obj || typeof obj !== 'object') return obj;
      const out = {};
      for (const k of Object.keys(obj)) out[compoundSessionId(source, k)] = obj[k];
      return out;
    }

    // dispatchRawFrame processes a raw daemon PushMessage — the local frame
    // format, or a relay push's unwrapped `.msg`. Both source kinds funnel
    // here, so the render/history/notification paths are unchanged.
    function dispatchRawFrame(msg) {
      if (!msg) return;
      if (msg.type === 'permissions_updated') {
        // Consent state changed (agent detected, or the other surface
        // answered the wizard). Dataless by design — re-fetch and let
        // updatePermissionsWizard open/dismiss the overlay (#570).
        refreshPermissions();
        return;
      }
      if (msg.type === 'history_snapshot' && msg.session_id && msg.history) {
        applyHistorySnapshot(msg.session_id, msg.history, msg.generations);
        repaintHistory();
        return;
      }
      if (msg.type === 'history_tick' && typeof msg.granularity_sec === 'number' && msg.buckets) {
        applyHistoryTick(msg.granularity_sec, msg.buckets, msg.bucket_generations);
        if (msg.granularity_sec === currentGranularity) repaintHistory();
        return;
      }
      if (msg.type === 'history_upgrade' && msg.session_id && typeof msg.priority === 'number') {
        applyHistoryUpgrade(msg.session_id, msg.priority);
        repaintHistory();
        return;
      }
      if (!msg.session) return;
      const s = msg.session;
      if (msg.type === 'session_deleted') {
        applySessionDelete(s.session_id);
      } else {
        // First sight of a session arrives flat with no role/icon and no rig
        // nesting (the WS delta lacks them). It renders immediately for
        // responsiveness; the structure poll above re-reads its nesting + role
        // emoji once the daemon's orchestrator poller has associated it.
        applySessionUpdate(s);
      }
      render();
    }

    // --- Connection status (header dot + banner + tooltip) ---
    function setDotLabel(status) {
      const dot = document.getElementById('ws-dot');
      const label = document.getElementById('ws-label');
      if (dot) dot.className = 'ws-dot ' + status;
      let text;
      if (status === 'connected') text = 'watching';
      else if (status === 'reconnecting') text = 'reconnecting';
      else if (status === 'connecting') text = 'connecting';
      else text = 'disconnected';
      if (label) label.textContent = text;
      const banner = document.getElementById('connection-banner');
      if (banner) {
        if (status === 'reconnecting') {
          banner.className = 'reconnecting';
          banner.textContent = 'Reconnecting…';
        } else if (status === 'disconnected') {
          banner.className = 'disconnected';
          banner.textContent = 'Disconnected — no configured source is reachable. Check that the daemon (or relay) is running.';
        } else {
          banner.className = '';
          banner.textContent = '';
        }
      }
    }

    // sourceTooltipLines builds the connection tooltip: one line per source. A
    // connected relay lists its daemons by label; everything else shows the
    // source's own label and state. Pure-ish (reads the sources map).
    function sourceTooltipLines() {
      const lines = [];
      for (const src of sources.values()) {
        if (src.kind === 'relay' && src.state === 'connected' && src.daemons.size > 0) {
          for (const d of src.daemons.values()) {
            lines.push(d.label + ' — ' + (d.status === 'connected' ? 'connected' : d.status));
          }
        } else {
          lines.push(src.label + ' — ' + src.state);
        }
      }
      return lines;
    }

    function updateWsStatus() {
      const states = [...sources.values()].map(s => s.state);
      setDotLabel(aggregateConnState(states));
      const wrap = document.querySelector('.ws-status');
      if (wrap) wrap.title = sourceTooltipLines().join('\n');
    }

    rebuildSources();

    // Granularity is derived from the display-mode cycle — no need to
    // persist it separately; restoring irrlicht_displayMode re-derives it.
    function applyGranularity(g) {
      currentGranularity = g;
      rebuildTimelineHistory();
      repaintHistory();
    }

    // Provider quota chips (header strip + Settings provider rows) live in
    // quotaChips.js — showQuotaForecast/setShowQuotaForecast/
    // renderHeaderTitle/refreshProviderSettings are imported above.

    // --- Header controls: theme toggle + display-mode cycle + settings ---
    document.getElementById('theme-toggle').addEventListener('click', toggleTheme);
    document.getElementById('view-mode-cycle').addEventListener('click', cycleDisplayMode);
    document.getElementById('summary-collapse-all').addEventListener('click', () => {
      if (anySummaryCollapsed()) expandAllSummaries();
      else collapseAllSummaries(allSessionIds());
      render();
    });
    // Initial paint of the header button state (also sets body.view-history
    // and aligns currentGranularity if restored from localStorage).
    applyDisplayMode();

    // --- Settings modal ---
    // The settings form has two attribute namespaces:
    //   * [data-setting=X]        — X is a key inside the bundled
    //                               irrlicht_settings localStorage object.
    //   * [data-quota-setting=X]  — X is its own top-level localStorage
    //                               key, matching a macOS @AppStorage
    //                               name verbatim (e.g. "showQuotaForecast").
    // Any settings export/import/reset helper should iterate BOTH.
    function syncSettingsForm() {
      for (const el of document.querySelectorAll('[data-setting]')) {
        const key = el.dataset.setting;
        if (!(key in settings)) continue;
        if (el.type === 'checkbox') el.checked = !!settings[key];
        else el.value = settings[key] != null ? String(settings[key]) : '';
      }
      for (const el of document.querySelectorAll('[data-quota-setting="showQuotaForecast"]')) {
        el.checked = showQuotaForecast();
      }
      refreshProviderSettings();
      refreshPermNote();
    }
    function refreshPermNote() {
      const note = document.getElementById('settings-perm-note');
      if (!note) return;
      const anyNotify = settings.notifyOnReady || settings.notifyOnWaiting || settings.notifyOnContextPressure;
      note.innerHTML = '';
      if (typeof Notification === 'undefined') {
        if (anyNotify) note.textContent = 'Browser notifications unsupported.';
        return;
      }
      if (Notification.permission === 'denied' && anyNotify) {
        // Two-line hint mirrors the macOS blocked-auth banner at
        // SettingsView.swift:88-106. We can't link to System Settings from
        // a webpage, so we describe the in-browser path instead.
        const line1 = document.createElement('div');
        line1.textContent = 'Notifications blocked for this site.';
        const line2 = document.createElement('div');
        line2.style.opacity = '0.8';
        line2.textContent = 'Click the site-info icon in the address bar, then enable Notifications.';
        note.appendChild(line1);
        note.appendChild(line2);
        return;
      }
      if (anyNotify && Notification.permission !== 'granted') {
        note.textContent = 'Browser will prompt for permission on next event.';
      }
    }
    function openSettings() {
      syncSettingsForm();
      document.getElementById('settings-backdrop').classList.add('open');
    }
    // Exported so permissionsWizard.js's "review permissions" entry point can
    // close the Settings modal before opening the permissions overlay.
    export function closeSettings() {
      document.getElementById('settings-backdrop').classList.remove('open');
    }
    document.getElementById('settings-toggle').addEventListener('click', openSettings);
    document.getElementById('settings-close').addEventListener('click', closeSettings);
    document.getElementById('settings-backdrop').addEventListener('click', (e) => {
      if (e.target.id === 'settings-backdrop') closeSettings();
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && document.getElementById('settings-backdrop').classList.contains('open')) {
        closeSettings();
      }
    });
    // Quota-forecast master toggle — flips visibility of the chip strip
    // header-wide. Persisted to `showQuotaForecast` (matching macOS).
    const quotaToggle = document.querySelector('[data-quota-setting="showQuotaForecast"]');
    if (quotaToggle) {
      quotaToggle.addEventListener('change', function() {
        setShowQuotaForecast(this.checked);
        renderHeaderTitle();
      });
    }

    // Wire each checkbox so toggling persists immediately and applies its
    // body-class effect (or, for notification toggles, kicks off the
    // permission prompt the first time).
    for (const el of document.querySelectorAll('[data-setting]')) {
      el.addEventListener('change', async function() {
        const key = this.dataset.setting;
        const turningOn = this.checked;
        if (turningOn && key.startsWith('notifyOn') && typeof Notification !== 'undefined' && Notification.permission === 'default') {
          try {
            const result = await Notification.requestPermission();
            if (result !== 'granted') {
              this.checked = false;
              refreshPermNote();
              return;
            }
          } catch (err) {
            console.debug('irrlicht: notification permission request failed', err);
            this.checked = false;
            refreshPermNote();
            return;
          }
        }
        settings[key] = (this.type === 'checkbox') ? this.checked : this.value;
        persistSettings();
        applySettings();
        refreshPermNote();
        // Source toggles/URL reconnect live, no page reload.
        if (SOURCE_SETTING_KEYS.has(key)) rebuildSources();
      });
    }

    // Advanced Settings disclosure (#694): collapsed by default, but its
    // open/closed choice persists across reloads (native <details> forgets).
    // Mirrors the macOS @AppStorage("advancedSettingsExpanded") behavior.
    const advancedDetails = document.getElementById('settings-advanced');
    if (advancedDetails) {
      try { advancedDetails.open = localStorage.getItem('irrlicht_advancedSettingsExpanded') === 'true'; } catch (e) {
        console.debug('irrlicht: failed to load advanced settings expanded state', e);
      }
      advancedDetails.addEventListener('toggle', () => {
        try { localStorage.setItem('irrlicht_advancedSettingsExpanded', advancedDetails.open ? 'true' : 'false'); } catch (e) {
          console.debug('irrlicht: failed to persist advanced settings expanded state', e);
        }
      });
    }

    // --- Daemon version (Irrlicht v$VERSION in the header) ---
    fetch('/api/v1/version').then(r => r.ok ? r.json() : null).catch(() => null).then(v => {
      const el = document.getElementById('app-version');
      if (el && v?.version) el.textContent = 'v' + v.version;
    });

    // Permission wizard (issue #570) — its state, wizard rendering, and POST
    // flow all live in permissionsWizard.js; this call wires it up (and fires
    // the first /api/v1/permissions fetch) in the same relative spot the
    // inline code used to run.
    initPermissionsWizard();

    // History tab (issue #369) — its controls, chart painting, and state all
    // live in historyTab.js; this call wires it up in the same relative spot
    // the inline code used to run.
    initHistoryTab();

export {
  resolvedTheme, rowLabel, maybeNotifyOnUpdate,
  formatCost, costCellDisplay, pressureClass, historyPriorityForState,
  taskEtaPresentation,
  lastNotifiedPressure,
  relayFrameKind, aggregateConnState, relayWsUrl, seqGap,
  compoundSessionId, displaySessionId,
  sessionOrigin, sourceIdOf, localBareIds, isShadowedRemote,
  daemonSessionIds, structureSignature,
};
export { formatUsageCost } from './quotaChips.js';
export { pendingWizardAgents, buildPermissionAnswers, stillPendingForAgents } from './permissionsWizard.js';
export {
  historyQuery, histTokens, histCount, histCO2, CHART_LABELS, DRILL_NEXT, historyRunningSum,
} from './historyTab.js';
