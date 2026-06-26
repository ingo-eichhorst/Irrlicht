import { isGroupCollapsed, toggleGroupCollapsed } from './collapsedGroups.js';
import { isSummaryCollapsed, toggleSummaryCollapsed, anySummaryCollapsed, collapseAllSummaries, expandAllSummaries } from './collapsedSummaries.js';

    // --- State ---
    let dashboardGroups = [];
    // Per-provider trailing-window spend (providerKey → timeframe → USD) from
    // the /api/v1/sessions `provider_costs` field. Feeds the usage chips.
    let dashboardProviderCosts = {};
    // Adapter branding from /api/v1/agents — keyed by adapter `name`
    // (e.g. "claude-code"). Populated once on initial load; the daemon's
    // registry is essentially static (changes require a daemon restart).
    let agentRegistry = {};
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
      return (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) ? 'light' : 'dark';
    }
    function applyStoredTheme() {
      const t = storedTheme();
      if (t) document.documentElement.setAttribute('data-theme', t);
      else document.documentElement.removeAttribute('data-theme');
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
      if (mq.addEventListener) mq.addEventListener('change', onChange);
      else if (mq.addListener) mq.addListener(onChange);
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
    if (!DISPLAY_MODES.find(m => m.key === currentDisplayMode)) currentDisplayMode = 'context';
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
        if (!raw) return Object.assign({}, SETTINGS_DEFAULTS);
        const parsed = JSON.parse(raw);
        return Object.assign({}, SETTINGS_DEFAULTS, (parsed && typeof parsed === 'object') ? parsed : {});
      } catch (e) {
        return Object.assign({}, SETTINGS_DEFAULTS);
      }
    }
    let settings = loadSettings();
    function persistSettings() {
      try { localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings)); } catch (e) {}
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
      try { new Notification(title, { body: body || '', tag: 'irrlicht-' + toggleKey, silent: false }); } catch (e) {}
    }
    function rowLabel(s) {
      // Best-effort "project · branch" label that matches what the row shows.
      const proj = s && s.project_name ? s.project_name : '';
      const branch = s && s.git_branch ? s.git_branch : '';
      if (proj && branch) return proj + ' · ' + branch;
      return proj || branch || (s && s.session_id ? displaySessionId(s.session_id).slice(0, 8) : 'session');
    }
    function maybeNotifyOnUpdate(prev, next) {
      if (!next) return;
      const prevState    = prev ? prev.state : '';
      const prevPressure = (prev && prev.metrics) ? prev.metrics.pressure_level : '';
      const nextState    = next.state || '';
      const nextPressure = (next.metrics && next.metrics.pressure_level) || '';
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
    const COST_TIMEFRAMES = [
      { key: 'day',   suffix: '/d'  },
      { key: 'week',  suffix: '/w'  },
      { key: 'month', suffix: '/mo' },
      { key: 'year',  suffix: '/yr' },
    ];
    let currentTimeframe = localStorage.getItem('irrlicht_costTimeframe') || 'day';
    if (!COST_TIMEFRAMES.find(t => t.key === currentTimeframe)) currentTimeframe = 'day';

    // Usage chips share one timeframe, cycled by clicking any of them and
    // persisted independently of the project-cost timeframe above — mirrors
    // macOS's separate usageCostTimeframe (#386).
    let currentUsageTimeframe = localStorage.getItem('irrlicht_usageCostTimeframe') || 'day';
    if (!COST_TIMEFRAMES.find(t => t.key === currentUsageTimeframe)) currentUsageTimeframe = 'day';

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
      try { raw = atob(b64); } catch (e) { return null; }
      if (raw.length !== 15) return null;
      const out = new Array(60);
      for (let i = 0; i < 15; i++) {
        const byte = raw.charCodeAt(i);
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
      for (const granKey of Object.keys(history)) {
        const gran = parseInt(granKey, 10);
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
          const gran = parseInt(granKey, 10);
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

    function applyHistoryTick(granularitySec, buckets, bucketGenerations) {
      if (![1, 10, 60].includes(granularitySec)) return;
      const dict = historyByGranularity[granularitySec];
      let changed = false;
      for (const sid of Object.keys(buckets)) {
        // Skip if this tick has already been folded into our snapshot.
        if (bucketGenerations && bucketGenerations[sid] !== undefined) {
          const gen = bucketGenerations[sid];
          const last = (lastTickGen[sid] && lastTickGen[sid][granularitySec]) || 0;
          if (last && gen <= last) continue;
          if (!lastTickGen[sid]) lastTickGen[sid] = Object.create(null);
          lastTickGen[sid][granularitySec] = gen;
        }
        let arr = dict[sid];
        if (!arr) arr = new Array(60).fill('');
        arr.shift();
        arr.push(HISTORY_PRIORITY_TO_STATE[buckets[sid] & 0x3]);
        while (arr.length < 60) arr.unshift('');
        dict[sid] = arr;
        changed = true;
      }
      if (changed && granularitySec === currentGranularity) rebuildTimelineHistory();
    }

    function applyHistoryUpgrade(sessionID, priority) {
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

    // --- SVG Icons (compact 12px) ---
    const svgIcons = {
      working: '<svg viewBox="0 0 16 16" fill="none"><circle class="core" cx="8" cy="8" r="8" fill="#8B5CF6"/></svg>',
      waiting: '<svg viewBox="0 0 16 16" fill="none"><rect x="4" y="3" width="2.5" height="10" rx="1" fill="#FF9500"/><rect x="9.5" y="3" width="2.5" height="10" rx="1" fill="#FF9500"/></svg>',
      ready: '<svg viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.5" stroke="#34C759" stroke-width="1.5"/><path d="M5 8.2l2 2 4-4.4" stroke="#34C759" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
      cancelled: '<svg viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="6.5" stroke="#8E8E93" stroke-width="1.5"/><path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="#8E8E93" stroke-width="1.5" stroke-linecap="round"/></svg>',
    };

    function stateIcon(state) { return svgIcons[state] || svgIcons.ready; }

    // --- Helpers ---
    function shortModel(m) {
      if (!m || m === 'unknown') return '';
      return m.replace(/^claude-/, '').replace(/-(\d)/, '.$1');
    }

    function formatCost(usd) {
      if (!usd || usd <= 0) return '';
      return '$' + usd.toFixed(2);
    }

    function fmtDuration(secs) {
      const h = Math.floor(secs / 3600);
      const m = Math.floor((secs % 3600) / 60);
      const s = secs % 60;
      if (h > 0) return h + 'h' + m + 'm';
      if (m > 0) return m + 'm' + s + 's';
      return s + 's';
    }

    function formatElapsed(firstSeen, elapsedStored, isActive) {
      if (isActive && firstSeen) {
        return fmtDuration(Math.max(0, Math.floor(Date.now() / 1000 - firstSeen)));
      }
      if (elapsedStored && elapsedStored > 0) return fmtDuration(elapsedStored);
      return '';
    }

    // Minute-resolution duration for the task-ETA chip — fmtDuration's
    // second-level detail would make the chip flicker every tick for a
    // number that is inherently rough.
    function fmtEtaDuration(secs) {
      if (secs < 60) return '<1m';
      const mins = Math.round(secs / 60);
      const h = Math.floor(mins / 60);
      const m = mins % 60;
      if (h > 0) return h + 'h' + (m > 0 ? m + 'm' : '');
      return m + 'm';
    }

    // fmtEtaText renders the remaining-time text with exactly ONE sign —
    // "~" (approximate) or "<" (upper bound), never both, never a degenerate
    // "2m–2m" range. highSecs null → point estimate.
    //   point, ≥1m   → "~12m left"
    //   point, <1m   → "<1m left"
    //   range, low <1m → "<2m left"   (the range collapses to its upper bound)
    //   range, low==high → point rules
    //   range        → "~8m–12m left"
    function fmtEtaText(remaining, highSecs) {
      const low = fmtEtaDuration(remaining);
      if (highSecs !== null) {
        const high = fmtEtaDuration(highSecs);
        if (low !== high) {
          if (remaining < 60) return '<' + high + ' left';
          return '~' + low + '–' + high + ' left';
        }
      }
      if (remaining < 60) return '<1m left';
      return '~' + low + ' left';
    }

    // taskEtaPresentation decides the task-completion ETA chip for a session
    // (issue #558, agent-authored estimate). Returns null when the chip must
    // be hidden: session not `working`, no estimate, no reported progress, or
    // no projected eta. Otherwise { text, stale, title }: a range whose HIGH
    // bound is pinned at the last marker — 1.5× the projected remaining time
    // below half the rounds ("~8m–12m left"), the bare projected remaining
    // at/above half (#616) — and stale=true when the last marker is older
    // than 3min so the chip degrades instead of letting the ETA drift.
    //
    // The eta is anchored at the marker (daemon-side), so the LOW bound
    // counts down in real time between marker updates while the HIGH bound
    // stays pinned until the agent reports fresh progress: "~3m–5m left"
    // becomes "~2m–5m left" a minute later, never the other way around.
    // At/above half the rounds low == high right at a marker, so the range
    // collapses to a point ("~5m left") and widens as wall clock passes
    // without fresh progress — never a bare countdown (#616). Mirrored in
    // SessionListView.swift's taskEtaPresentation.
    function taskEtaPresentation(metrics, state, nowSec) {
      const est = metrics && metrics.task_estimate;
      const eta = metrics && metrics.task_completion_eta;
      if (state !== 'working' || !est) return null;
      const sourceLabel = est.source === 'tasks' ? 'from task list'
        : est.source === 'subagents' ? 'from subagents' : 'agent-reported';
      // No completed rounds yet: no measurable rate, but the agent HAS
      // committed to a plan — show a progress-only chip so the user gets
      // feedback within seconds of the first marker (issue #604/#602).
      if (!(est.completed_rounds > 0)) {
        if (!(est.total_rounds > 0)) return null;
        const age = est.updated_at > 0 ? Math.max(0, Math.floor(nowSec - est.updated_at)) : 0;
        let zeroTitle = 'Task ETA — ' + sourceLabel + ' 0/' + est.total_rounds + ' rounds';
        if (est.updated_at > 0) zeroTitle += ', updated ' + fmtDuration(age) + ' ago';
        return { text: 'estimating…', stale: est.updated_at > 0 && age > 180, title: zeroTitle };
      }
      // Progress without a projection (e.g. a subagent aggregate whose
      // children carry no etas yet, #626): show a rounds-only chip rather
      // than hiding one that was visible moments ago.
      if (!eta) {
        const age = est.updated_at > 0 ? Math.max(0, Math.floor(nowSec - est.updated_at)) : 0;
        let roundsTitle = 'Task ETA — ' + sourceLabel + ' ' + est.completed_rounds + '/' + est.total_rounds + ' rounds';
        if (est.updated_at > 0) roundsTitle += ', updated ' + fmtDuration(age) + ' ago';
        return {
          text: est.completed_rounds + '/' + est.total_rounds,
          stale: est.updated_at > 0 && age > 180,
          title: roundsTitle,
        };
      }
      const remaining = Math.max(0, Math.floor(eta - nowSec));
      const frac = est.total_rounds > 0 ? est.completed_rounds / est.total_rounds : 0;
      // 1.5× padding while the rate is barely measurable, bare projected
      // remaining once it's trusted; no marker timestamp at/above half →
      // nothing to pin to, keep the point estimate.
      const factor = frac < 0.5 ? 1.5 : 1;
      let highSecs = null;
      if (est.updated_at > 0) {
        highSecs = Math.max(remaining, Math.floor((eta - est.updated_at) * factor));
      } else if (frac < 0.5) {
        highSecs = Math.floor(remaining * 1.5);
      }
      const text = fmtEtaText(remaining, highSecs);
      const ageSec = est.updated_at > 0 ? Math.max(0, Math.floor(nowSec - est.updated_at)) : 0;
      const stale = est.updated_at > 0 && ageSec > 180;
      let title = 'Task ETA — ' + sourceLabel + ' ' + est.completed_rounds + '/' + est.total_rounds + ' rounds';
      if (est.updated_at > 0) title += ', updated ' + fmtDuration(ageSec) + ' ago';
      return { text: text, stale: stale, title: title };
    }

    function shortID(id) { return id ? displaySessionId(id).slice(0, 6) : ''; }

    function pressureClass(level) {
      if (level === 'critical') return 'critical';
      if (level === 'warning' || level === 'high') return 'high';
      if (level === 'caution' || level === 'medium') return 'medium';
      return '';
    }

    function pressureColor(level) {
      if (level === 'critical') return 'var(--pressure-critical)';
      if (level === 'warning' || level === 'high') return 'var(--pressure-high)';
      if (level === 'caution' || level === 'medium') return 'var(--pressure-medium)';
      return 'var(--pressure-low)';
    }

    function formatTokens(n) {
      if (n < 1000) return n + '';
      if (n < 1000000) return (n / 1000).toFixed(1) + 'K';
      return (n / 1000000).toFixed(1) + 'M';
    }

    function esc(s) {
      if (s == null) return '';
      return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    }

    function groupMaxPressure(g) {
      let worst = 0;
      for (const a of g.agents) {
        const ctx = a.metrics ? a.metrics.context_utilization_percentage || 0 : 0;
        if (ctx > worst) worst = ctx;
      }
      return worst;
    }

    function groupTotalCost(g) {
      let total = 0;
      for (const a of g.agents) {
        if (a.metrics && a.metrics.estimated_cost_usd) total += a.metrics.estimated_cost_usd;
        for (const c of (a.children || [])) {
          if (c.metrics && c.metrics.estimated_cost_usd) total += c.metrics.estimated_cost_usd;
        }
      }
      return total;
    }

    function activeSubagentCount(a) {
      if (!a.children) return 0;
      return a.children.filter(c => c.state === 'working' || c.state === 'waiting').length;
    }

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
          if (g.groups && g.groups.length) walk(g.groups, true);
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
      var entry = sessionIndex.get(s.session_id);
      if (entry) {
        var a = entry.agent;
        // Capture previous state + pressure before merging so we can fire
        // notifications on the transition (web parity for the menu-bar app's
        // ready/waiting/pressure alerts).
        var prevSnap = { state: a.state, metrics: a.metrics ? { pressure_level: a.metrics.pressure_level } : null };
        var role = a.role, wn = a.worker_name, wid = a.worker_id, ch = a.children;
        Object.assign(a, s);
        if (role && !a.role) a.role = role;
        if (wn && !a.worker_name) a.worker_name = wn;
        if (wid && !a.worker_id) a.worker_id = wid;
        a.children = ch;
        // Migrate the agent if its project_name now points at a different
        // group than where it currently lives. Without this, sessions whose
        // first WS push arrived before metadata enrichment landed (empty
        // project_name → "unknown" bucket) stay stranded there forever even
        // after later updates carry the correct name. Children inherit their
        // parent's group, so only migrate top-level entries. Rig sessions
        // (nested under a Gas Town sub-group) are exempt — their group is
        // structural, not project-derived, so a project_name mismatch must
        // not tear them out of their rig.
        if (!entry.parent && !entry.group._nested) {
          var desired = a.project_name || 'unknown';
          if (entry.group.name !== desired) {
            var oldGroup = entry.group;
            var ai = oldGroup.agents.findIndex(x => x.session_id === s.session_id);
            if (ai >= 0) oldGroup.agents.splice(ai, 1);
            var target = dashboardGroups.find(g => g.name === desired);
            if (!target) {
              target = {name: desired, agents: []};
              dashboardGroups.push(target);
            }
            target.agents.push(a);
            entry.group = target;
            // Children's sessionIndex entries still point at the old group;
            // refresh them so any future site that reads child.entry.group
            // (e.g. a parent-deletion cleanup that drills into children)
            // doesn't follow a reference to a group we just spliced out.
            indexChildren(target, a);
            if (oldGroup.agents.length === 0) {
              var gi = dashboardGroups.indexOf(oldGroup);
              if (gi >= 0) dashboardGroups.splice(gi, 1);
            }
          }
        }
        maybeNotifyOnUpdate(prevSnap, a);
        return;
      }
      // First sight of this session — treat any non-trivial state as a
      // transition for notification purposes.
      maybeNotifyOnUpdate(null, s);
      var groupName = s.project_name || 'unknown';
      var group = dashboardGroups.find(g => g.name === groupName);
      if (!group) {
        group = {name: groupName, agents: []};
        dashboardGroups.push(group);
      }
      if (s.parent_session_id) {
        var parentEntry = sessionIndex.get(s.parent_session_id);
        if (parentEntry) {
          if (!parentEntry.agent.children) parentEntry.agent.children = [];
          parentEntry.agent.children.push(s);
          sessionIndex.set(s.session_id, {group: parentEntry.group, agent: s, parent: parentEntry.agent});
          return;
        }
      }
      group.agents.push(s);
      sessionIndex.set(s.session_id, {group: group, agent: s, parent: null});
    }

    function applySessionDelete(sessionId) {
      var entry = sessionIndex.get(sessionId);
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
        var ci = entry.parent.children.findIndex(c => c.session_id === sessionId);
        if (ci >= 0) entry.parent.children.splice(ci, 1);
      } else {
        var ai = entry.group.agents.findIndex(a => a.session_id === sessionId);
        if (ai >= 0) entry.group.agents.splice(ai, 1);
      }
      if (entry.group.agents.length === 0) {
        var gi = dashboardGroups.indexOf(entry.group);
        if (gi >= 0) dashboardGroups.splice(gi, 1);
      }
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

    // --- DOM Reconciliation ---
    // Keyed reconcile: patches children of `parent` to match `items`.
    function reconcile(parent, items, keyFn, createFn, updateFn) {
      const existingByKey = new Map();
      for (const child of parent.children) {
        const k = child.dataset.key;
        if (k) existingByKey.set(k, child);
      }

      const desiredKeys = new Set();
      let prevNode = null;

      for (const item of items) {
        const key = keyFn(item);
        desiredKeys.add(key);
        let el = existingByKey.get(key);

        if (el) {
          updateFn(el, item);
          // Move to correct position if needed
          const expected = prevNode ? prevNode.nextSibling : parent.firstChild;
          if (el !== expected) {
            parent.insertBefore(el, expected);
          }
        } else {
          el = createFn(item);
          el.dataset.key = key;
          const ref = prevNode ? prevNode.nextSibling : parent.firstChild;
          parent.insertBefore(el, ref);
        }
        prevNode = el;
      }

      // Remove orphans
      for (const [key, el] of existingByKey) {
        if (!desiredKeys.has(key)) {
          parent.removeChild(el);
        }
      }
    }

    // Writes the row-num slot: if updateSessionRow flagged an icon override
    // (agent.icon set, agent.role empty), show the icon glyph instead of the
    // numeric agent number. Matches macOS at SessionListView.swift:469-479.
    function paintRowNum(el, num) {
      const numEl = el.querySelector('.row-num');
      if (!numEl) return;
      const icon = numEl.dataset.iconOverride || '';
      const desired = icon || String(num);
      if (numEl.textContent !== desired) numEl.textContent = desired;
    }

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

    function cycleUsageTimeframe() {
      const idx = COST_TIMEFRAMES.findIndex(t => t.key === currentUsageTimeframe);
      currentUsageTimeframe = COST_TIMEFRAMES[(idx + 1) % COST_TIMEFRAMES.length].key;
      localStorage.setItem('irrlicht_usageCostTimeframe', currentUsageTimeframe);
      render();
    }

    // Windowed spend for a usage chip, keyed by providerKey (chip.key, e.g.
    // "anthropic"/"openai") and the selected timeframe. 0 when the daemon has
    // no provider_costs entry for that provider/window.
    function usageSpendForChip(chip) {
      const byTf = dashboardProviderCosts[chip.key];
      const v = byTf && byTf[currentUsageTimeframe];
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
        '<span class="row-cost"></span>' +
        '<canvas class="row-history"></canvas>' +
        '<span class="row-eta" style="display:none"></span>' +
        '<span class="row-spacer"></span>' +
        '<span class="row-model"></span>' +
        '<span class="row-adapter-icon" style="display:none"></span>' +
        '<span class="row-elapsed"></span>' +
        '<span class="row-created"></span>' +
        '<span class="row-id"></span>';
      updateSessionRow(el, agent, isChild);
      return el;
    }

    function updateSessionRow(el, agent, isChild) {
      const state = agent.state || 'ready';
      const metrics = agent.metrics || {};
      const model = shortModel(metrics.model_name || agent.model || '');
      const ctxPct = metrics.context_utilization_percentage || 0;
      const pressure = metrics.pressure_level || '';
      const branch = agent.git_branch || '';
      const cost = formatCost(metrics.estimated_cost_usd);
      const isActive = state === 'working' || state === 'waiting';
      const elapsed = formatElapsed(agent.first_seen, metrics.elapsed_seconds, isActive);

      // State icon — only update if state changed
      if (el.dataset.state !== state) {
        el.dataset.state = state;
        el.querySelector('.row-state-icon').innerHTML = stateIcon(state);
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

      // Agent number — set from outside via data attribute
      // (set by render loop)

      // Branch
      const branchEl = el.querySelector('.row-branch');
      if (branchEl.textContent !== branch) branchEl.textContent = branch || '\u2014';

      // Task summary + waiting question render in their own collapsible row
      // beneath the parent (see createSummaryRow / render() emit).

      // Task progress dots render in a separate row beneath the parent
      // (see createTaskListRow / render() emit). Mirrors TaskListView in
      // SessionListView.swift:746–769.

      // Subagent count badge — small filled circle next to the working
      // icon when the parent has active sub-agents. Mirrors macOS at
      // SessionListView.swift:481-490. Toggling .has-sub on the row
      // shrinks the branch column so columns downstream still align with
      // rows that don't have a badge.
      // Origin glyph (#538) — a cloud marks a session delivered by a relay
      // (remote daemon); local-socket sessions show nothing, so a local-only
      // dashboard is visually unchanged. Tooltip = the daemon's hostname.
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

      // Background-agent badge (#744) — a moon glyph marks a Claude Code Agent
      // View background agent that keeps running detached in the daemon pool
      // after its window is closed. Always shown for kind:bg; the amber
      // .is-detached state emphasizes "no open window owns this".
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

      // Context bar
      const fill = el.querySelector('.row-ctx-fill');
      const ctxWidth = Math.min(100, ctxPct).toFixed(1) + '%';
      if (fill.style.width !== ctxWidth) fill.style.width = ctxWidth;
      fill.className = 'row-ctx-fill ' + pressureClass(pressure);

      // Token count — rendered as an overlay inside the context bar so
      // the row stays compact (mirrors macOS ContextBar's `label` slot at
      // SessionListView.swift:432-439). The standalone .row-ctx-pct lives
      // on only when Settings hides the cost column (showCostDisplay off),
      // in which case it shows the % utilization instead.
      const totalTok = metrics.total_tokens || 0;
      const labelEl = el.querySelector('.row-ctx-label');
      const labelText = totalTok > 0 ? formatTokens(totalTok) : '';
      if (labelEl.textContent !== labelText) labelEl.textContent = labelText;

      const pctEl = el.querySelector('.row-ctx-pct');
      const pctText = ctxPct > 0 ? ctxPct.toFixed(0) + '%' : '';
      if (pctEl.textContent !== pctText) pctEl.textContent = pctText;
      pctEl.style.color = ctxPct > 0 ? pressureColor(pressure) : '';

      // Cost
      const costEl = el.querySelector('.row-cost');
      if (costEl.textContent !== cost) costEl.textContent = cost;

      // Model
      const modelEl = el.querySelector('.row-model');
      if (modelEl.textContent !== model) modelEl.textContent = model;

      // Elapsed
      const elapsedEl = el.querySelector('.row-elapsed');
      elapsedEl.textContent = elapsed;
      elapsedEl.dataset.firstSeen = isActive ? (agent.first_seen || '') : '';
      elapsedEl.dataset.elapsedStored = metrics.elapsed_seconds || '';
      elapsedEl.dataset.active = isActive ? '1' : '0';

      // Task-completion ETA chip (issue #558) — agent-authored estimate,
      // shown only while working. The 1s tickElapsed loop re-derives the
      // countdown from the dataset between polls.
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

      // Created chip — only stamp the first_seen on the element; the 1s
      // tickElapsed loop owns the textContent. first_seen is immutable per
      // session so this is a constant after row creation.
      const createdEl = el.querySelector('.row-created');
      if (createdEl) {
        const fs = String(agent.first_seen || '');
        if (createdEl.dataset.firstSeen !== fs) {
          createdEl.dataset.firstSeen = fs;
          if (!fs) createdEl.textContent = '';
        }
      }

      // Short ID
      const idEl = el.querySelector('.row-id');
      const sid = shortID(agent.session_id);
      if (idEl.textContent !== sid) idEl.textContent = sid;

      // Role badge — icon + role name when both are present. When only an
      // icon is set (no role string), the icon replaces the agent number in
      // .row-num — matches macOS at SessionListView.swift:469-479. The
      // render loop puts the numeric agentNum into .row-num after this fn
      // returns, so we set a flag the loop reads back.
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

      // Active tool label — shown only when agent is working and inside a tool call.
      // When no tool is open but the session is held working by a live Bash
      // background process (run_in_background), surface that instead so the row
      // explains why it's still working past the turn's end. See issue #445.
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

      // Per-row history canvas is repainted by repaintHistory() on the
      // history-event WS path — no need to repaint here on every session
      // update (data didn't change).
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
          dashboardGroups.some(g => g.groups && g.groups.length);
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
            items.push({type: 'agent', key: 'a:' + a.session_id, agent: a, num: agentNum, isChild: false, depth: depth});
            // Pressure alert
            const isActive = a.state === 'working' || a.state === 'waiting';
            const pressure = a.metrics ? a.metrics.pressure_level : '';
            if (isActive && (pressure === 'high' || pressure === 'warning' || pressure === 'critical')) {
              items.push({type: 'alert', key: 'al:' + a.session_id, pressure: pressure});
            }
            // Task summary + waiting question — collapsible block beneath the
            // parent (issue #738). Renders when there's a summary (any state)
            // or a pending question (waiting).
            if (a.metrics && (a.metrics.task_summary || (a.state === 'waiting' && a.metrics.last_assistant_text))) {
              items.push({type: 'summary', key: 's:' + a.session_id, agent: a, isChild: false});
            }
            // Task progress dots — separate row beneath the parent so they
            // don't push the meta columns off the session row. Matches the
            // overlay's TaskListView placement (SessionListView.swift:629-632).
            const tasks = (a.metrics && a.metrics.tasks) || [];
            const tasksOpen = tasks.length > 0 && !tasks.every(t => t.status === 'completed');
            if (tasksOpen) {
              items.push({type: 'tasks', key: 'tk:' + a.session_id, agent: a, isChild: false});
            }
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
          if (g.groups && g.groups.length) walk(g.groups);
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
      const tasks = (agent.metrics && agent.metrics.tasks) || [];
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
      const summary = (agent.metrics && agent.metrics.task_summary) || '';
      const question = (agent.state === 'waiting' && agent.metrics && agent.metrics.last_assistant_text) || '';
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
          qEl.title = question;
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
      const sig = list.length === 0
        ? 'empty'
        : list.length <= 3
          ? 'icons:' + list.map(s => s.state || 'ready').join(',')
          : 'count:' + list.length;
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
      for (const el of document.querySelectorAll('.row-elapsed')) {
        if (el.dataset.active === '1') {
          const fs = parseFloat(el.dataset.firstSeen);
          if (fs) el.textContent = fmtDuration(Math.max(0, Math.floor(now - fs)));
        }
      }
      // Task-ETA chips burn down between polls. Only rows whose chip is
      // visible carry dataset.eta (updateSessionRow clears it otherwise),
      // and visibility/suppression decisions stay with updateSessionRow —
      // the tick only refreshes the countdown and staleness.
      for (const el of document.querySelectorAll('.row-eta')) {
        const eta = parseFloat(el.dataset.eta);
        if (!eta) continue;
        const info = taskEtaPresentation({
          task_completion_eta: eta,
          task_estimate: {
            total_rounds: parseInt(el.dataset.total, 10) || 0,
            completed_rounds: parseInt(el.dataset.completed, 10) || 0,
            updated_at: parseFloat(el.dataset.updatedAt) || 0,
          },
        }, 'working', now);
        if (info) {
          el.textContent = info.text;
          el.title = info.title;
          el.classList.toggle('stale', info.stale);
        }
      }
      // Debug-mode "created" chip — only iterate when the toggle is on so
      // we don't waste DOM reads when the chip is hidden.
      if (settings.debugMode) {
        for (const el of document.querySelectorAll('.row-created')) {
          const fs = parseFloat(el.dataset.firstSeen);
          if (fs) el.textContent = 'created ' + fmtDuration(Math.max(0, Math.floor(now - fs))) + ' ago';
        }
      }
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
      if (!rec || !rec.states.length) return;
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
      if (Array.isArray(entries)) {
        for (const e of entries) {
          if (e && e.name) agentRegistry[e.name] = e;
        }
      }
      if (resp) {
        dashboardGroups = Array.isArray(resp) ? resp : (resp.groups || []);
        // A group with no direct agents (e.g. a gastown orchestrator group
        // whose agents all live in rig sub-groups) omits `agents` entirely
        // (json omitempty). Normalize once at ingest so every consumer —
        // rebuildIndex, render, group headers — can iterate unconditionally.
        for (const g of dashboardGroups) {
          if (g && !g.agents) g.agents = [];
        }
        dashboardProviderCosts = (resp && !Array.isArray(resp) && resp.provider_costs) || {};
        rebuildIndex();
      }
      render();
      repaintHistory();
    });
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
              if (a && a.session_id) freshAgents.set(a.session_id, a);
              if (a && a.children) (function ix(arr){ for (const c of arr||[]){ if (c.session_id) freshAgents.set(c.session_id, c); if (c.children) ix(c.children);} })(a.children);
            }
            if (g.groups) walkFresh(g.groups, key);
          }
        })(fresh, '');
        let changed = false;
        (function walkGroups(groups, parentKey) {
          for (const g of groups) {
            const key = parentKey ? parentKey + '/' + g.name : (g.name || '');
            const f = byPath.get(key);
            if (f && f.costs && JSON.stringify(g.costs) !== JSON.stringify(f.costs)) {
              g.costs = f.costs;
              changed = true;
            }
            (function mergeRateLimit(arr) {
              for (const a of arr || []) {
                const fa = freshAgents.get(a.session_id);
                if (fa && fa.metrics) {
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
        g && (g.type === 'gastown' || (g.groups && g.groups.length)));
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

    // compoundSessionId folds a relay daemon id into a daemon-local session_id
    // to make a globally-unique, client-internal id. NUL-delimited so it can't
    // collide with a real id (daemon ids may be arbitrary labels) and never
    // renders; a falsy daemon id (local frames) yields the bare session id.
    // Pure; exported for tests.
    function compoundSessionId(daemonId, sessionId) {
      if (!daemonId) return sessionId || '';
      return daemonId + ' ' + (sessionId || '');
    }

    // displaySessionId is the inverse used for display slices — it recovers the
    // daemon-local id and passes bare (local) ids through unchanged.
    // Pure; exported for tests.
    function displaySessionId(id) {
      if (!id) return '';
      const i = id.indexOf(' ');
      return i === -1 ? id : id.slice(i + 1);
    }

    // sessionOrigin classifies a session by its id (#538): a relay session's id
    // is compound (its bare form differs), a local-socket session's id is bare.
    // Derives from displaySessionId so it never embeds the delimiter literal.
    // Pure; exported for tests.
    function sessionOrigin(a) {
      const id = a && a.session_id;
      return id && displaySessionId(id) !== id ? 'remote' : 'local';
    }

    // sourceIdOf recovers the relay daemon id (the compound prefix) from a
    // session id, or '' for a bare/local id. Pure; exported for tests.
    function sourceIdOf(id) {
      if (!id) return '';
      const bare = displaySessionId(id);
      return bare === id ? '' : id.slice(0, id.length - bare.length - 1);
    }

    // localBareIds collects the bare session ids delivered by a local source,
    // so a relay duplicate of the same session collapses to the local row
    // (local wins, #538). Pure; exported for tests.
    function localBareIds(groups) {
      const out = new Set();
      (function walk(gs) {
        for (const g of (gs || [])) {
          for (const a of (g.agents || [])) {
            if (sessionOrigin(a) === 'local' && a.session_id) out.add(a.session_id);
          }
          if (g.groups) walk(g.groups);
        }
      })(groups);
      return out;
    }

    // isShadowedRemote is true when a relay session is also present locally —
    // the same daemon reached over both paths shows once, as the local row.
    // Pure; exported for tests.
    function isShadowedRemote(a, localIds) {
      return sessionOrigin(a) === 'remote' && localIds.has(displaySessionId(a.session_id));
    }

    // daemonSessionIds returns the session ids delivered by one relay daemon.
    // Used to drop its rows when it disconnects (#540): the relay no longer
    // deletes them per-session, so the web dashboard removes them itself to keep
    // its existing behaviour (macOS fades instead). Pure; exported for tests.
    function daemonSessionIds(groups, daemonId) {
      const out = [];
      if (!daemonId) return out;
      (function walk(gs) {
        for (const g of (gs || [])) {
          for (const a of (g.agents || [])) {
            if (sourceIdOf(a.session_id) === daemonId) out.push(a.session_id);
          }
          if (g.groups) walk(g.groups);
        }
      })(groups);
      return out;
    }

    // relayFrameKind classifies an incoming frame so the handler can branch.
    // Pure; exported for tests.
    function relayFrameKind(msg) {
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
    function seqGap(last, seq) {
      return seq > 0 && last > 0 && seq > last + 1;
    }

    // aggregateConnState reduces per-source states into the single header dot:
    // connected wins (we're watching at least one source), then connecting,
    // then reconnecting, else disconnected. Pure; exported for tests.
    function aggregateConnState(states) {
      if (!states || states.length === 0) return 'disconnected';
      if (states.some(s => s === 'connected')) return 'connected';
      if (states.some(s => s === 'connecting')) return 'connecting';
      if (states.some(s => s === 'reconnecting')) return 'reconnecting';
      return 'disconnected';
    }

    // relayWsUrl normalizes a user-entered relay address into a ws(s):// stream
    // URL. Accepts http(s)://, ws(s)://, or a bare host[:port], with or without
    // the stream path. Pure; exported for tests. Returns '' for empty input.
    function relayWsUrl(raw) {
      let u = (raw || '').trim();
      if (!u) return '';
      u = u.replace(/^http:/i, 'ws:').replace(/^https:/i, 'wss:');
      if (!/^wss?:\/\//i.test(u)) u = 'ws://' + u;
      u = u.replace(/\/+$/, '');
      if (!/\/api\/v1\/sessions\/stream$/.test(u)) u += '/api/v1/sessions/stream';
      return u;
    }

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
        const d = src.daemons && src.daemons.get(daemonId);
        if (d && d.label) return d.label;
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
      for (const [id, src] of [...sources]) {
        const want = desiredById.get(id);
        // Drop a source that's no longer desired, OR a relay source whose token
        // changed / is parked in 'unauthorized'. The source id is keyed by URL
        // only, so without this a corrected token would never reconnect (the id
        // still matches) and an auth-failed relay would stay stuck. Tearing it
        // down here lets the loop below recreate it with the new credential.
        const stale = want && src.kind === 'relay' && (src.token !== want.token || src.state === 'unauthorized');
        if (!want || stale) {
          src.closing = true;
          try { if (src.ws) src.ws.close(); } catch (e) {}
          sources.delete(id);
        }
      }
      for (const d of desired) {
        if (!sources.has(d.id)) {
          const src = Object.assign({ state: 'connecting', ws: null, reconnectDelay: 1000, closing: false, daemons: new Map(), seqCursors: new Map() }, d);
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
      try { ws = new WebSocket(src.wsUrl); } catch (e) { scheduleReconnect(src); return; }
      src.ws = ws;
      ws.onopen = function() {
        src.state = 'connected';
        src.reconnectDelay = 1000;
        // A browser can't set request headers on a WebSocket, so an auth-enabled
        // relay's bearer token rides in this first frame (relay sources only).
        const hello = { type: 'hello', protocol_version: 1, role: 'client' };
        if (src.kind === 'relay' && settings.relayToken) hello.token = settings.relayToken;
        try { ws.send(JSON.stringify(hello)); } catch (e) {}
        updateWsStatus();
      };
      ws.onmessage = function(evt) {
        let msg;
        try { msg = JSON.parse(evt.data); } catch (e) { return; }
        handleSourceFrame(src, msg);
      };
      ws.onerror = function() {};
      ws.onclose = function(ev) {
        if (src.closing) return;
        src.daemons.clear();
        src.seqCursors.clear();
        // 4401 = auth failed/revoked: retrying with the same token just loops, so
        // stop reconnecting (until settings change) instead of a tight loop.
        if (ev && ev.code === 4401) { src.state = 'unauthorized'; updateWsStatus(); return; }
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
            if (d && d.daemon_id) src.daemons.set(d.daemon_id, { label: d.daemon_label || d.daemon_id, status: d.status || 'connected' });
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
      var s = msg.session;
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

    // --- Provider quota chips ---
    // Port of the macOS overlay's quotaChipView (SessionListView.swift:378-900)
    // and ProviderModePreference (SessionState.swift:190-262). Bucketing,
    // mode resolution, bar coloring, and tooltip text mirror the Swift
    // sources so opening the popover and the dashboard side-by-side shows
    // identical state for the same `/api/v1/sessions` response.

    // localStorage keys are unprefixed to match macOS @AppStorage names —
    // not because the storages are shared (UserDefaults and localStorage
    // are not), but so the same documentation, screenshots, and mental
    // model apply to both surfaces.
    const QUOTA_FORECAST_KEY = 'showQuotaForecast';

    function showQuotaForecast() {
      // Default ON, mirroring macOS @AppStorage("showQuotaForecast") default.
      const v = localStorage.getItem(QUOTA_FORECAST_KEY);
      if (v === '0' || v === 'false') return false;
      return true;
    }
    function setShowQuotaForecast(on) {
      // Safari Private mode and storage-disabled contexts throw on
      // setItem — swallow per the project pattern (see persistCollapsedGroups
      // at line ~1099 and the settings save loop).
      try { localStorage.setItem(QUOTA_FORECAST_KEY, on ? '1' : '0'); } catch (e) {}
    }

    // Provider-icon registry. SVGs are byte-for-byte copies of
    // ProviderIconRegistry.anthropicSVG / openaiSVG in Swift so both
    // surfaces ship the same mark.
    const PROVIDER_ICON_SVG = {
      anthropic: '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24"><path d="M14.83 4.5h-3.49l5.83 15h3.5l-5.84-15zM6.49 4.5l-5.83 15h3.57l1.19-3.13h6.09l1.2 3.13h3.57l-5.83-15H6.49zm-.05 8.98l1.99-5.2 1.99 5.2H6.44z" fill="currentColor"/></svg>',
      openai: '<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24"><path d="M22.28 9.821a5.985 5.985 0 0 0-.515-4.91 6.046 6.046 0 0 0-6.51-2.9A6.065 6.065 0 0 0 4.981 4.18a5.985 5.985 0 0 0-3.998 2.9 6.046 6.046 0 0 0 .743 7.097 5.98 5.98 0 0 0 .51 4.911 6.051 6.051 0 0 0 6.515 2.9A5.985 5.985 0 0 0 13.26 24a6.056 6.056 0 0 0 5.772-4.206 5.99 5.99 0 0 0 3.997-2.9 6.056 6.056 0 0 0-.747-7.073zM13.26 22.43a4.476 4.476 0 0 1-2.876-1.04l.142-.08 4.774-2.758a.795.795 0 0 0 .392-.681v-6.737l2.018 1.168a.071.071 0 0 1 .038.052v5.583a4.504 4.504 0 0 1-4.488 4.493zM3.6 18.304a4.47 4.47 0 0 1-.535-3.014l.142.085 4.778 2.758a.795.795 0 0 0 .787 0l5.832-3.367v2.332a.08.08 0 0 1-.033.062L9.74 19.95a4.5 4.5 0 0 1-6.14-1.646zM2.34 7.896a4.485 4.485 0 0 1 2.366-1.973V11.6a.766.766 0 0 0 .388.676l5.815 3.355-2.02 1.168a.077.077 0 0 1-.062 0l-4.83-2.79A4.504 4.504 0 0 1 2.34 7.872zm16.597 3.855l-5.833-3.387L15.119 7.2a.077.077 0 0 1 .062 0l4.83 2.79a4.5 4.5 0 0 1-.676 8.05v-5.678a.79.79 0 0 0-.398-.66zm2.01-3.023l-.142-.085-4.774-2.782a.776.776 0 0 0-.787 0L9.409 9.23V6.897a.066.066 0 0 1 .028-.061l4.83-2.787a4.5 4.5 0 0 1 6.68 4.66zm-12.64 4.135l-2.02-1.164a.08.08 0 0 1-.038-.057V6.075a4.504 4.504 0 0 1 7.375-3.453l-.142.08L8.704 5.46a.795.795 0 0 0-.392.681v6.737zm1.097-2.365l2.602-1.5 2.607 1.5v2.999l-2.597 1.5-2.607-1.5z" fill="currentColor"/></svg>',
    };
    function providerIconHTML(key) { return PROVIDER_ICON_SVG[key] || ''; }

    function providerKeyFor(snap, adapter) {
      if (!snap) return null;
      switch (snap.plan_type) {
        case 'max':
        case 'pro':  return 'anthropic';
        case 'plus': return 'openai';
      }
      switch (adapter) {
        case 'claude-code': return 'anthropic';
        case 'codex':       return 'openai';
        default:            return null;
      }
    }

    function planTypeLabel(planType) {
      if (!planType) return null;
      switch (planType) {
        case 'max':        return 'Claude Max';
        case 'pro':        return 'Claude Pro';
        case 'plus':       return 'ChatGPT Plus';
        case 'team':       return 'Team';
        case 'enterprise': return 'Enterprise';
        default:           return planType.charAt(0).toUpperCase() + planType.slice(1);
      }
    }

    function providerModeStorageKey(providerKey) {
      // Mirrors ProviderModePreference.storageKey(providerKey:) in Swift.
      return 'providerMode_' + providerKey;
    }
    function providerModePreference(providerKey) {
      const raw = localStorage.getItem(providerModeStorageKey(providerKey)) || '';
      if (raw === 'subscription' || raw === 'usage' || raw === 'auto') return raw;
      return 'auto';
    }
    function setProviderModePreference(providerKey, mode) {
      try {
        if (mode === 'auto') localStorage.removeItem(providerModeStorageKey(providerKey));
        else localStorage.setItem(providerModeStorageKey(providerKey), mode);
      } catch (e) {}
    }

    function chipModeFor(snap, providerKey) {
      const pref = providerModePreference(providerKey);
      if (pref === 'subscription') return 'subscription';
      if (pref === 'usage')        return 'usage';
      // Auto: a credits balance without a plan tier means the API-key /
      // usage path; everything else (plan_type set or no credits) renders
      // bars. Same rule as Swift's resolveChipMode.
      if (snap && snap.credits && !snap.plan_type) return 'usage';
      return 'subscription';
    }

    function imminentWindow(snap) {
      if (!snap || !Array.isArray(snap.windows) || snap.windows.length === 0) return null;
      let best = null;
      for (const w of snap.windows) {
        if (!w) continue;
        if (!best || (w.used_percent || 0) > (best.used_percent || 0)) best = w;
      }
      if (!best || (best.used_percent || 0) <= 0) return null;
      return best;
    }

    // Returns where the user *should be* in the window if pacing evenly,
    // anchored to now (not sampled_at) — matches macOS quotaPacePercent.
    function paceFor(w, nowMs) {
      if (!w || !w.window_minutes || w.window_minutes <= 0) return null;
      if (!w.resets_at || w.resets_at <= 0) return null;
      const windowMs = w.window_minutes * 60 * 1000;
      const startMs = w.resets_at * 1000 - windowMs;
      const pct = ((nowMs - startMs) / windowMs) * 100;
      return Math.min(100, Math.max(0, pct));
    }

    // Pace-aware bar color. Thresholds match QuotaBarThreshold in
    // SessionListView.swift so the two surfaces flip color in lockstep.
    const QUOTA_BAR_THRESHOLDS = {
      absoluteOrange:  85,
      paceDeltaOrange: 15,
      paceDeltaYellow: 5,
      fallbackOrange:  70,
      fallbackYellow:  50,
    };
    function barColorClass(used, pace) {
      if (used >= QUOTA_BAR_THRESHOLDS.absoluteOrange) return 'quota-bar-fill--orange';
      if (pace == null) {
        if (used >= QUOTA_BAR_THRESHOLDS.fallbackOrange) return 'quota-bar-fill--orange';
        if (used >= QUOTA_BAR_THRESHOLDS.fallbackYellow) return 'quota-bar-fill--yellow';
        return 'quota-bar-fill--green';
      }
      const delta = used - pace;
      if (delta >= QUOTA_BAR_THRESHOLDS.paceDeltaOrange) return 'quota-bar-fill--orange';
      if (delta >= QUOTA_BAR_THRESHOLDS.paceDeltaYellow) return 'quota-bar-fill--yellow';
      return 'quota-bar-fill--green';
    }

    function quotaWindowLabel(minutes) {
      // Tolerate Codex v1's 299 / 10079 off-by-one quirk.
      if (minutes === 299 || minutes === 300) return '5h';
      if (minutes === 10079 || minutes === 10080) return '7d';
      if (minutes >= 1440) return Math.floor(minutes / 1440) + 'd';
      if (minutes >= 60)   return Math.floor(minutes / 60) + 'h';
      return minutes + 'm';
    }

    function formatUsageCost(cost) {
      if (!cost || cost <= 0) return '$0';
      if (cost < 0.01) return '<$0.01';
      if (cost >= 100) return '$' + Math.round(cost);
      return '$' + cost.toFixed(2);
    }
    function formatTimeUntil(unixSec) {
      let s = unixSec - Date.now() / 1000;
      if (s < 0) s = 0;
      const h = Math.floor(s / 3600);
      const m = Math.floor((s % 3600) / 60);
      if (h > 0) return h + 'h ' + m + 'm';
      return m + 'm';
    }
    function formatClockTime(unixSec) {
      return new Date(unixSec * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
    }

    function collectFlatSessions() {
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

    // Fold session snapshots into one bucket per provider. Rules mirror
    // macOS mergeIntoBuckets: subscription beats usage; fresh beats stale;
    // among same-staleness snapshots, the highest sampled_at wins.
    function bucketChips(sessions, nowMs) {
      const buckets = new Map();
      for (const s of sessions) {
        const snap = s && s.metrics && s.metrics.rate_limit;
        if (!snap) continue;
        // Match Swift's mergeIntoBuckets stale rule exactly: any window
        // whose resets_at is in the past (or zero — Date(0) is 1970, well
        // before now) makes the snapshot stale. An earlier truthy guard
        // on resets_at let resets_at=0 windows slip through as "fresh"
        // here while macOS marked them stale.
        const stale = Array.isArray(snap.windows) && snap.windows.some(w => w && w.resets_at * 1000 <= nowMs);
        const key = providerKeyFor(snap, s.adapter) || ('unknown:' + (s.adapter || ''));
        const mode = chipModeFor(snap, key);
        // Subscription chips with no window data would render as an empty body
        // (icon only, no bars) while hiding the app title. Skip them entirely.
        if (mode === 'subscription' && !Array.isArray(snap.windows)) continue;
        const imm = imminentWindow(snap);
        const cost = (s.metrics && s.metrics.estimated_cost_usd) || 0;
        const existing = buckets.get(key);
        if (!existing) {
          buckets.set(key, {
            key: key,
            snapshot: snap,
            session: s,
            imminent: imm,
            totalCostUSD: cost,
            mode: mode,
            isStale: stale,
          });
          continue;
        }
        // Subscription wins over usage when both are seen (rare — one OAuth
        // account on both paths). Bars are the richer signal, but only when
        // the subscription snap is at least as fresh as the usage snap.
        if (existing.mode === 'usage' && mode === 'subscription' && (!stale || existing.isStale)) {
          existing.mode = 'subscription';
          existing.snapshot = snap;
          existing.session = s;
          existing.imminent = imm;
          existing.isStale = stale;
        } else if (existing.isStale && !stale) {
          // Always prefer fresh over stale, regardless of sampled_at.
          existing.snapshot = snap;
          existing.session = s;
          existing.imminent = imm;
          existing.isStale = false;
        } else if (existing.isStale === stale && (snap.sampled_at || 0) > (existing.snapshot.sampled_at || 0)) {
          existing.snapshot = snap;
          existing.session = s;
          existing.imminent = imm;
        }
        existing.totalCostUSD += cost;
      }
      return Array.from(buckets.values()).sort((a, b) =>
        a.key < b.key ? -1 : a.key > b.key ? 1 : 0
      );
    }

    function quotaTooltip(chip, nowMs) {
      const lines = [];
      const plan = planTypeLabel(chip.snapshot.plan_type);
      if (plan) lines.push(plan);
      if (chip.isStale) lines.push('⚠️ snapshot pre-dates current window — waiting for next statusline tick');
      if (chip.mode === 'subscription') {
        for (const w of (chip.snapshot.windows || [])) {
          const used = Math.round(w.used_percent || 0);
          const label = quotaWindowLabel(w.window_minutes || 0);
          const pace = paceFor(w, nowMs);
          let line = label + ': ' + used + '% used';
          if (pace != null) {
            const delta = used - Math.round(pace);
            const verdict = delta > 0 ? (delta + 'pt over pace')
                          : delta < 0 ? ((-delta) + 'pt under pace')
                          : 'on pace';
            line += ' · ' + verdict;
          }
          if (w.resets_at) line += ' · resets in ' + formatTimeUntil(w.resets_at);
          lines.push(line);
        }
        const eta = chip.session && chip.session.metrics && chip.session.metrics.rate_limit_forecast_eta;
        if (eta) lines.push('Projected cap: ' + formatClockTime(eta));
        else if ((chip.snapshot.windows || []).some(w => (w.used_percent || 0) > 0))
          lines.push("Forecast: won't hit cap this window");
      } else {
        lines.push(formatUsageCost(chip.totalCostUSD) + ' · cumulative spend across active sessions');
        const c = chip.snapshot.credits;
        if (c) {
          if (c.unlimited === true) lines.push('Credits: unlimited');
          else if (typeof c.balance === 'number') lines.push('Credits balance: $' + c.balance.toFixed(2));
          else if (c.has_credits) lines.push('Credits: available');
        }
      }
      if (chip.snapshot.reached_type) lines.push('⚠️ rate limit reached: ' + chip.snapshot.reached_type);
      return lines.join('\n');
    }

    const MAX_VISIBLE_QUOTA_CHIPS = 2;

    function adapterIconHTML(adapterKey) {
      // Adapter SVGs are bytes from the daemon's /api/v1/agents response —
      // not source code we control. Sandbox them through a base64 data-URL
      // <img>, the same pattern the row adapter icon uses (line ~2063), so
      // no inline event handler or <script> can escape into the page.
      // Hardcoded provider icons above are trusted and stay as raw inline
      // SVG (currentColor template-fill is load-bearing for them).
      const branding = adapterKey ? agentRegistry[adapterKey] : null;
      if (!branding) return '';
      const theme = resolvedTheme();
      const svg = (theme === 'dark' ? branding.icon_svg_dark : branding.icon_svg_light)
               || branding.icon_svg_light
               || branding.icon_svg_dark
               || '';
      if (!svg) return '';
      try { return '<img alt="" src="data:image/svg+xml;base64,' + btoa(svg) + '">'; }
      catch (e) { return ''; }
    }

    function buildQuotaRowDOM(w, compact, nowMs) {
      const row = document.createElement('span');
      row.className = 'quota-row';

      const label = document.createElement('span');
      label.className = 'quota-row-label';
      label.textContent = quotaWindowLabel(w.window_minutes || 0);
      row.appendChild(label);

      const used = w.used_percent || 0;
      const pace = paceFor(w, nowMs);
      const bar = document.createElement('span');
      bar.className = 'quota-bar';

      const fill = document.createElement('span');
      fill.className = 'quota-bar-fill ' + barColorClass(used, pace);
      fill.style.width = Math.max(0, Math.min(100, used)).toFixed(2) + '%';
      bar.appendChild(fill);

      if (pace != null) {
        const marker = document.createElement('span');
        marker.className = 'quota-bar-pace';
        marker.style.left = 'calc(' + Math.max(0, Math.min(100, pace)).toFixed(2) + '% - 0.5px)';
        bar.appendChild(marker);
      }
      row.appendChild(bar);

      const pct = document.createElement('span');
      pct.className = 'quota-row-percent';
      pct.textContent = Math.round(used) + '%';
      row.appendChild(pct);

      if (!compact && w.resets_at) {
        const reset = document.createElement('span');
        reset.className = 'quota-row-reset';
        reset.textContent = 'resets ' + formatClockTime(w.resets_at);
        row.appendChild(reset);
      }
      return row;
    }

    function buildQuotaChipDOM(chip, compact, nowMs) {
      const root = document.createElement('div');
      root.className = 'quota-chip' + (chip.isStale ? ' quota-stale' : '');
      root.title = quotaTooltip(chip, nowMs);

      const icon = document.createElement('span');
      icon.className = 'quota-chip-icon';
      const svg = providerIconHTML(chip.key)
                || adapterIconHTML(chip.session && chip.session.adapter);
      if (svg) icon.innerHTML = svg;
      root.appendChild(icon);

      const body = document.createElement('span');
      body.className = 'quota-chip-body';
      if (chip.mode === 'subscription') {
        for (const w of (chip.snapshot.windows || [])) {
          body.appendChild(buildQuotaRowDOM(w, compact, nowMs));
        }
      } else {
        // Windowed per-provider spend for the selected timeframe, click-to-
        // cycle — mirrors the project-group cost text and the macOS usage
        // chip (#386). $0/<frame> is honest for a windowed zero, so there's
        // no separate em-dash zero-state.
        const tf = COST_TIMEFRAMES.find(t => t.key === currentUsageTimeframe) || COST_TIMEFRAMES[0];
        const head = document.createElement('span');
        head.className = 'quota-usage-headline';
        head.textContent = formatUsageCost(usageSpendForChip(chip)) + tf.suffix;
        const sub = document.createElement('span');
        sub.className = 'quota-usage-sublabel';
        sub.textContent = 'spend';
        body.appendChild(head);
        body.appendChild(sub);
        body.style.cursor = 'pointer';
        body.title = 'Click to cycle time frame (day → week → month → year)';
        body.addEventListener('click', (e) => { e.stopPropagation(); cycleUsageTimeframe(); });
      }
      root.appendChild(body);
      return root;
    }

    function buildOverflowChipDOM(hidden) {
      const pill = document.createElement('span');
      pill.className = 'quota-overflow';
      pill.textContent = '+' + hidden.length + ' more';
      const usageTf = COST_TIMEFRAMES.find(t => t.key === currentUsageTimeframe) || COST_TIMEFRAMES[0];
      pill.title = hidden.map(h => {
        const label = planTypeLabel(h.snapshot.plan_type)
                   || (h.key.charAt(0).toUpperCase() + h.key.slice(1));
        if (h.mode === 'subscription') {
          const imm = h.imminent;
          return imm ? (label + ': ' + Math.round(imm.used_percent || 0) + '%') : label;
        }
        return label + ': ' + formatUsageCost(usageSpendForChip(h)) + usageTf.suffix;
      }).join('\n');
      return pill;
    }

    function renderHeaderTitle() {
      const host = document.getElementById('quota-chips');
      const header = document.querySelector('header');
      if (!host || !header) return;
      // Error-boundary: a malformed snapshot or a localStorage failure
      // inside any helper would otherwise unwind out of render() and skip
      // the rest of the surrounding render pass. Fail soft: clear the chip
      // strip, log to console, and leave the version line visible.
      try {
        host.innerHTML = '';
        host.classList.remove('quota-chips--single');
        if (!showQuotaForecast()) {
          header.classList.remove('has-quota-chips');
          return;
        }
        const nowMs = Date.now();
        const chips = bucketChips(collectFlatSessions(), nowMs);
        if (chips.length === 0) {
          header.classList.remove('has-quota-chips');
          return;
        }
        const visible = chips.slice(0, MAX_VISIBLE_QUOTA_CHIPS);
        const hidden = chips.slice(MAX_VISIBLE_QUOTA_CHIPS);
        const compact = chips.length > 1;
        if (chips.length === 1) host.classList.add('quota-chips--single');
        for (const c of visible) host.appendChild(buildQuotaChipDOM(c, compact, nowMs));
        if (hidden.length > 0) host.appendChild(buildOverflowChipDOM(hidden));
        header.classList.add('has-quota-chips');
      } catch (e) {
        try { console.error('quota chip render failed:', e); } catch (_) {}
        host.innerHTML = '';
        header.classList.remove('has-quota-chips');
      }
    }

    // Per-provider mode selectors in the settings modal. Populated whenever
    // settings opens with the providers we've actually observed in the
    // current session list — same set the chip strip would render. If
    // we've never seen a rate_limit snapshot, the section explains why.
    function refreshProviderSettings() {
      const host = document.getElementById('settings-providers');
      if (!host) return;
      host.innerHTML = '';
      const chips = bucketChips(collectFlatSessions(), Date.now());
      if (chips.length === 0) {
        const empty = document.createElement('div');
        empty.className = 'provider-empty';
        empty.textContent = 'No provider quota data observed yet.';
        host.appendChild(empty);
        return;
      }
      for (const c of chips) {
        const row = document.createElement('div');
        row.className = 'provider-row';

        const name = document.createElement('span');
        name.className = 'provider-name';
        const iconSvg = providerIconHTML(c.key) || adapterIconHTML(c.session && c.session.adapter);
        if (iconSvg) {
          const iconSpan = document.createElement('span');
          iconSpan.innerHTML = iconSvg;
          name.appendChild(iconSpan);
        }
        const labelText = document.createElement('span');
        labelText.textContent = planTypeLabel(c.snapshot.plan_type)
                             || (c.key.charAt(0).toUpperCase() + c.key.slice(1));
        name.appendChild(labelText);
        row.appendChild(name);

        const sel = document.createElement('select');
        for (const opt of [['auto','Auto'], ['subscription','Subscription'], ['usage','Usage']]) {
          const o = document.createElement('option');
          o.value = opt[0];
          o.textContent = opt[1];
          sel.appendChild(o);
        }
        sel.value = providerModePreference(c.key);
        sel.addEventListener('change', function() {
          setProviderModePreference(c.key, this.value);
          renderHeaderTitle();
        });
        row.appendChild(sel);
        host.appendChild(row);
      }
    }

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
    function closeSettings() {
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
      try { advancedDetails.open = localStorage.getItem('irrlicht_advancedSettingsExpanded') === 'true'; } catch (e) {}
      advancedDetails.addEventListener('toggle', () => {
        try { localStorage.setItem('irrlicht_advancedSettingsExpanded', advancedDetails.open ? 'true' : 'false'); } catch (e) {}
      });
    }

    // --- Daemon version (Irrlicht v$VERSION in the header) ---
    fetch('/api/v1/version').then(r => r.ok ? r.json() : null).catch(() => null).then(v => {
      const el = document.getElementById('app-version');
      if (el && v && v.version) el.textContent = 'v' + v.version;
    });

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
    function pendingWizardAgents(snap) {
      if (!snap || snap.mode !== 'ask' || !Array.isArray(snap.agents)) return [];
      return snap.agents.filter(a =>
        a.detected && (a.permissions || []).some(p => p.state === 'pending'));
    }

    // stillPendingForAgents reports whether any of the named agents still
    // has a pending permission. Drives auto-wizard dismissal: only answers
    // dismiss an open wizard (submitted here or on the macOS app — first
    // answer wins); a detection flip alone must not. Pure; exported for
    // tests.
    function stillPendingForAgents(snap, names) {
      if (!snap || !Array.isArray(snap.agents) || !Array.isArray(names)) return false;
      return snap.agents.some(a => names.includes(a.name) &&
        (a.permissions || []).some(p => p.state === 'pending'));
    }

    // buildPermissionAnswers computes the POST payload from the wizard's
    // toggle states. draft maps "agent/key" → bool. In onlyPending mode
    // (auto wizard) every displayed pending item is answered explicitly;
    // in review mode unchanged already-answered items are skipped. Pure;
    // exported for tests.
    function buildPermissionAnswers(snap, draft, onlyPending) {
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

    function refreshPermissions() {
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

    {
      const applyBtn = document.getElementById('permissions-apply');
      if (applyBtn) applyBtn.addEventListener('click', submitPermissionsWizard);
      const backdrop = document.getElementById('permissions-backdrop');
      if (backdrop) backdrop.addEventListener('click', (e) => {
        if (e.target.id === 'permissions-backdrop') closePermissionsWizard();
      });
      document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape' && backdrop && backdrop.classList.contains('open')) {
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
    }

    refreshPermissions();

export {
  resolvedTheme, rowLabel, maybeNotifyOnUpdate,
  formatCost, formatUsageCost, pressureClass, historyPriorityForState,
  taskEtaPresentation,
  lastNotifiedPressure,
  relayFrameKind, aggregateConnState, relayWsUrl, seqGap,
  compoundSessionId, displaySessionId,
  sessionOrigin, sourceIdOf, localBareIds, isShadowedRemote,
  daemonSessionIds, structureSignature,
  pendingWizardAgents, buildPermissionAnswers, stillPendingForAgents,
};
