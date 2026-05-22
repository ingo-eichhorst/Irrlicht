    // --- State ---
    let dashboardGroups = [];
    let orchestrator = null;   // OrchestratorSummary from initial load
    let orchFull = null;       // Full orchestrator.State (global_agents, codebases) from WS / secondary fetch
    // Adapter branding from /api/v1/agents — keyed by adapter `name`
    // (e.g. "claude-code"). Populated once on initial load; the daemon's
    // registry is essentially static (changes require a daemon restart).
    let agentRegistry = {};
    let sessionIndex = new Map();
    // Collapsed group names persist across reloads so a deliberate collapse
    // survives a refresh. Restored from localStorage on load.
    let collapsedGroups = new Set();
    try {
      const raw = localStorage.getItem('irrlicht_collapsedGroups');
      if (raw) {
        const parsed = JSON.parse(raw);
        if (Array.isArray(parsed)) collapsedGroups = new Set(parsed.filter(s => typeof s === 'string'));
      }
    } catch (e) {}
    function persistCollapsedGroups() {
      try { localStorage.setItem('irrlicht_collapsedGroups', JSON.stringify([...collapsedGroups])); } catch (e) {}
    }

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
    };
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
      return proj || branch || (s && s.session_id ? s.session_id.slice(0, 8) : 'session');
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
          if (gen <= last) continue;
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

    function shortID(id) { return id ? id.slice(0, 6) : ''; }

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
      for (const g of dashboardGroups) {
        for (const a of g.agents) {
          sessionIndex.set(a.session_id, {group: g, agent: a, parent: null});
          indexChildren(g, a);
        }
      }
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
        // parent's group, so only migrate top-level entries.
        if (!entry.parent) {
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

    // --- Orchestrator rendering (innerHTML is fine — updates rarely) ---
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

    function dotBar(total, done) {
      const maxDots = 7;
      // Coerce inputs to integers and bound the final dot counts by maxDots
      // (a literal) so repeat() is provably bounded regardless of WS payload.
      // Behaviour for legitimate inputs is unchanged at any scale: the
      // done/total ratio still drives filled-vs-empty.
      const t = Number(total) | 0;
      const d = Number(done) | 0;
      const totalDots = Math.max(0, Math.min(t, maxDots));
      if (totalDots <= 0) return '';
      const filled = t <= maxDots ? d : Math.round(d / t * maxDots);
      const filledDots = Math.max(0, Math.min(filled, totalDots, maxDots));
      const emptyDots = Math.max(0, Math.min(totalDots - filledDots, maxDots));
      return '\u25CF'.repeat(filledDots) + '\u25CB'.repeat(emptyDots);
    }

    function renderOrchestrator() {
      const container = document.getElementById('gt-container');
      const hasOrch = orchestrator && orchestrator.running;
      const hasFull = orchFull && orchFull.running;
      if (!hasOrch && !hasFull) { container.style.display = 'none'; return; }
      container.style.display = 'block';

      let html = '<div class="gt-section">';
      html += '<div class="gt-header">';
      html += '<div class="gt-title"><span class="gt-emoji">\u26FD</span> Gas Town</div>';
      html += '<div class="gt-status-label"><div class="gt-status-dot" style="background:var(--ready)"></div>running</div>';
      html += '</div>';

      // Global agents — icon/description come from the API, no hardcoding here.
      if (hasFull && orchFull.global_agents && orchFull.global_agents.length) {
        html += '<div class="gt-agents-row">';
        for (const ga of orchFull.global_agents) {
          const sid = ga.session_id ? shortID(ga.session_id) : 'idle';
          html += '<div class="gt-agent-chip" title="' + esc(ga.description || ga.role) + '">';
          html += '<span>' + (ga.icon ? esc(ga.icon) + ' ' : '') + esc(ga.role) + '</span>';
          html += '<span class="gt-chip-dot" style="background:' + stateColor(ga.state) + '"></span>';
          html += '<span class="gt-chip-id">' + esc(sid) + '</span>';
          html += '</div>';
        }
        html += '</div>';
      }

      // Rig/codebase blocks — flatten worktrees for readability.
      if (hasFull && orchFull.codebases && orchFull.codebases.length) {
        for (const cb of orchFull.codebases) {
          const workers = (cb.worktrees || []).flatMap(wt => wt.workers || []);
          if (!workers.length) continue;
          html += '<div class="gt-rig-block">';
          html += '<div class="gt-rig-name">rig: ' + esc(cb.name) + '</div>';
          html += '<div class="gt-workers">';
          for (const w of workers) {
            html += '<div class="gt-agent-chip" title="' + esc(w.description || w.role) + '">';
            html += '<span>' + (w.icon ? esc(w.icon) + ' ' : '') + esc(w.role);
            if (w.name) html += ' <span style="color:var(--text)">' + esc(w.name) + '</span>';
            html += '</span>';
            html += '<span class="gt-chip-dot" style="background:' + stateColor(w.state) + '"></span>';
            if (w.id) html += '<span class="gt-chip-id">' + esc(w.id.slice(0, 8)) + '</span>';
            html += '</div>';
          }
          html += '</div></div>';
        }
      }

      // Convoys — use work_units from whichever source is available.
      const workUnits = ((orchFull || orchestrator) || {}).work_units || [];
      const convoys = workUnits.filter(w => w.type === 'convoy');
      if (convoys.length > 0) {
        html += '<div class="gt-body">';
        html += '<div class="gt-convoy-header">\u{1F69A} Convoys</div>';
        for (const c of convoys) {
          const isDone = c.done >= c.total;
          html += '<div class="gt-convoy-row">';
          html += '<span class="gt-convoy-name' + (isDone ? ' done' : '') + '">' + esc(c.name) + '</span>';
          html += '<span class="gt-dotbar" style="color:' + (isDone ? 'var(--ready)' : 'var(--working)') + '">' + dotBar(c.total, c.done) + '</span>';
          html += '<span class="gt-convoy-fraction">' + (Number(c.done) | 0) + ' / ' + (Number(c.total) | 0) + '</span>';
          if (isDone) html += '<span class="gt-convoy-check">\u2713</span>';
          html += '</div>';
        }
        html += '</div>';
      }

      html += '</div>';
      container.innerHTML = html;
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
    function createGroupHeader(group) {
      const el = document.createElement('div');
      el.className = 'group-hdr';
      // Anatomy mirrors overlay GroupView at SessionListView.swift:871:
      // chevron + name + per-day cost on the left, session count on the right.
      el.innerHTML = '<span class="group-chevron">\u25BE</span>' +
        '<span class="group-name"></span>' +
        '<span class="group-cost" title="Click to cycle time frame"></span>' +
        '<span class="group-count"></span>';
      el.addEventListener('click', () => {
        if (collapsedGroups.has(group.name)) {
          collapsedGroups.delete(group.name);
        } else {
          collapsedGroups.add(group.name);
        }
        persistCollapsedGroups();
        render();
      });
      const costEl = el.querySelector('.group-cost');
      costEl.addEventListener('click', (e) => {
        e.stopPropagation();
        cycleCostTimeframe();
      });
      updateGroupHeader(el, group);
      return el;
    }

    function cycleCostTimeframe() {
      const idx = COST_TIMEFRAMES.findIndex(t => t.key === currentTimeframe);
      currentTimeframe = COST_TIMEFRAMES[(idx + 1) % COST_TIMEFRAMES.length].key;
      localStorage.setItem('irrlicht_costTimeframe', currentTimeframe);
      render();
    }

    function updateGroupHeader(el, group) {
      // Gas Town groups get a ⛽ glyph prefix on the title — matches macOS
      // group-title rendering at SessionListView.swift:945.
      const isGastown = group.type === 'gastown';
      const desiredName = (isGastown ? '⛽ ' : '') + (group.name || '');
      const nameEl = el.querySelector('.group-name');
      if (nameEl.textContent !== desiredName) nameEl.textContent = desiredName;
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
      const totalAgents = group.agents.length + group.agents.reduce((n, a) => n + (a.children ? a.children.length : 0), 0);
      const countText = totalAgents + (totalAgents === 1 ? ' session' : ' sessions');
      const countEl = el.querySelector('.group-count');
      if (countEl.textContent !== countText) countEl.textContent = countText;

      const isCollapsed = collapsedGroups.has(group.name);
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
        '<span class="row-role-badge" style="display:none"></span>' +
        '<span class="row-branch"></span>' +
        '<span class="row-tool" style="display:none"></span>' +
        '<span class="row-ctx-bar"><span class="row-ctx-fill"></span><span class="row-ctx-label"></span></span>' +
        '<span class="row-ctx-pct"></span>' +
        '<span class="row-cost"></span>' +
        '<canvas class="row-history"></canvas>' +
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

      // Waiting question — show last assistant text when waiting
      // (waiting question now renders in its own row beneath the parent;
      // see createQuestionRow / render() emit.)

      // Task progress dots render in a separate row beneath the parent
      // (see createTaskListRow / render() emit). Mirrors TaskListView in
      // SessionListView.swift:746–769.

      // Subagent count badge — small filled circle next to the working
      // icon when the parent has active sub-agents. Mirrors macOS at
      // SessionListView.swift:481-490. Toggling .has-sub on the row
      // shrinks the branch column so columns downstream still align with
      // rows that don't have a badge.
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
      const toolEl = el.querySelector('.row-tool');
      const tools = metrics.last_open_tool_names || [];
      if (metrics.has_open_tool_call && tools[0] && state === 'working') {
        const raw = tools[0];
        const isUser = raw === 'AskUserQuestion' || raw === 'ExitPlanMode';
        toolEl.style.display = '';
        toolEl.textContent = toolLabel[raw] || raw.slice(0, 12);
        toolEl.className = 'row-tool' + (isUser ? ' tool-user' : '');
      } else {
        toolEl.style.display = 'none';
      }

      // Per-row history canvas is repainted by repaintHistory() on the
      // history-event WS path — no need to repaint here on every session
      // update (data didn't change).
    }

    // --- Render ---
    function render() {
      renderOrchestrator();

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
        const showHeaders = dashboardGroups.length > 1;
        let agentNum = 0;

        for (const g of dashboardGroups) {
          if (showHeaders) {
            items.push({type: 'group', key: 'g:' + g.name, group: g});
          }
          if (!collapsedGroups.has(g.name)) {
            for (const a of g.agents) {
              agentNum++;
              items.push({type: 'agent', key: 'a:' + a.session_id, agent: a, num: agentNum, isChild: false});
              // Pressure alert
              const isActive = a.state === 'working' || a.state === 'waiting';
              const pressure = a.metrics ? a.metrics.pressure_level : '';
              if (isActive && (pressure === 'high' || pressure === 'warning' || pressure === 'critical')) {
                items.push({type: 'alert', key: 'al:' + a.session_id, pressure: pressure});
              }
              // Waiting question — separate row beneath the parent (matches
              // SessionListView.swift:588-604). Renders only while the
              // session is in 'waiting' AND has a last_assistant_text.
              if (a.state === 'waiting' && a.metrics && a.metrics.last_assistant_text) {
                items.push({type: 'question', key: 'q:' + a.session_id, agent: a, isChild: false});
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
          }
        }

        // Reconcile
        reconcile(list, items,
          item => item.key,
          item => {
            if (item.type === 'group') return createGroupHeader(item.group);
            if (item.type === 'alert') return createAlertRow(item.pressure);
            if (item.type === 'tasks') return createTaskListRow(item.agent, item.isChild);
            if (item.type === 'question') return createQuestionRow(item.agent, item.isChild);
            const el = createSessionRow(item.agent, item.isChild);
            paintRowNum(el, item.num);
            return el;
          },
          (el, item) => {
            if (item.type === 'group') { updateGroupHeader(el, item.group); return; }
            if (item.type === 'alert') { updateAlertRow(el, item.pressure); return; }
            if (item.type === 'tasks') { updateTaskListRow(el, item.agent); return; }
            if (item.type === 'question') { updateQuestionRow(el, item.agent); return; }
            updateSessionRow(el, item.agent, item.isChild);
            paintRowNum(el, item.num);
            el.className = 'session-row' + (item.isChild ? ' child' : '');
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
      for (const g of dashboardGroups) {
        for (const a of g.agents) topLevel.push(a);
        collectAll(g.agents);
      }
      updateSummary(all, topLevel);
      renderHeaderTitle();
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

    // Waiting question row — shows the assistant's last message when the
    // session is waiting on the user. Mirrors macOS at
    // SessionListView.swift:588-604 (waiting-colored text in a padded,
    // dim, rounded box rendered beneath the parent row).
    function createQuestionRow(agent, isChild) {
      return makeRow('row-question-row', '', agent, updateQuestionRow, isChild);
    }
    function updateQuestionRow(el, agent) {
      const text = (agent.metrics && agent.metrics.last_assistant_text) || '';
      if (!text || agent.state !== 'waiting') { el.style.display = 'none'; return; }
      el.style.display = '';
      if (el.dataset.full !== text) {
        el.dataset.full = text;
        el.textContent = text;
        el.title = text;
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
        rebuildIndex();
      }
      render();
      repaintHistory();
    });
    // Periodic re-hydration so trailing-window costs (carried on each group
    // under `costs`) don't go stale between WebSocket deltas, which only
    // carry individual session updates. We only copy the `costs` field —
    // replacing the whole array would shuffle WS-added sessions back into
    // the daemon's UUID order, which is the position-jump the user flagged.
    //
    // We additionally merge per-agent `metrics.rate_limit` and
    // `metrics.rate_limit_forecast_eta` for any agent already in our state.
    // The quota chip strip would otherwise stay pinned to the last value
    // a WS message left in place during a WS disconnect.
    setInterval(() => {
      fetch('/api/v1/sessions').then(r => r.json()).catch(() => null).then(resp => {
        if (!resp) return;
        const fresh = Array.isArray(resp) ? resp : (resp.groups || []);
        const byName = new Map();
        for (const g of fresh) if (g && g.name) byName.set(g.name, g);
        const freshAgents = new Map();
        (function indexAgents(arr) {
          for (const a of arr || []) {
            if (a && a.session_id) freshAgents.set(a.session_id, a);
            if (a && a.children) indexAgents(a.children);
          }
        })(fresh.flatMap(g => g.agents || []));
        let changed = false;
        for (const g of dashboardGroups) {
          const f = byName.get(g.name);
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
        }
        if (changed) render();
      });
    }, 30000);

    // --- WebSocket ---
    let ws = null;
    let reconnectDelay = 1000;

    // Status labels mirror the overlay's statusIndicator at SessionListView.swift:387:
    // connected → "watching", reconnecting → "reconnecting", else state name.
    function setWsStatus(status) {
      const dot = document.getElementById('ws-dot');
      const label = document.getElementById('ws-label');
      dot.className = 'ws-dot ' + status;
      let text;
      if (status === 'connected') text = 'watching';
      else if (status === 'reconnecting') text = 'reconnecting';
      else if (status === 'connecting') text = 'connecting';
      else text = 'disconnected';
      label.textContent = text;
      // Surface a full-width banner over the session list when the daemon
      // connection is impaired — the header dot alone is easy to miss.
      const banner = document.getElementById('connection-banner');
      if (banner) {
        if (status === 'reconnecting') {
          banner.className = 'reconnecting';
          banner.textContent = 'Reconnecting to daemon…';
        } else if (status === 'disconnected') {
          banner.className = 'disconnected';
          banner.textContent = 'Disconnected — the irrlicht daemon is unreachable. Check that it is running.';
        } else {
          banner.className = '';
          banner.textContent = '';
        }
      }
    }

    function connect() {
      // ws is non-null on every subsequent reconnect attempt; use that to
      // distinguish the initial "connecting" from a "reconnecting" cycle.
      setWsStatus(ws ? 'reconnecting' : 'connecting');
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      ws = new WebSocket(proto + '://' + location.host + '/api/v1/sessions/stream');

      ws.onopen = function() {
        setWsStatus('connected');
        reconnectDelay = 1000;
      };

      ws.onmessage = function(evt) {
        var msg;
        try { msg = JSON.parse(evt.data); } catch(e) { return; }
        if (!msg) return;
        if (msg.type === 'orchestrator_state' && msg.orchestrator) {
          const orch = msg.orchestrator;
          orchestrator = orch.running ? orch : null;
          orchFull = orch.running ? orch : null;
          render();
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
          applySessionUpdate(s);
        }
        render();
      };

      ws.onerror = function() {};

      ws.onclose = function() {
        setWsStatus('disconnected');
        setTimeout(connect, reconnectDelay);
        reconnectDelay = Math.min(reconnectDelay * 2, 30000);
      };
    }

    connect();

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
      if (!cost || cost <= 0) return '$0.00';
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
        // account on both paths). Bars are the richer signal.
        if (existing.mode === 'usage' && mode === 'subscription') {
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
        const hasSpend = chip.totalCostUSD > 0;
        const head = document.createElement('span');
        head.className = 'quota-usage-headline';
        head.textContent = hasSpend ? formatUsageCost(chip.totalCostUSD) : '—';
        const sub = document.createElement('span');
        sub.className = 'quota-usage-sublabel';
        sub.textContent = hasSpend ? 'spend' : 'no spend yet';
        body.appendChild(head);
        body.appendChild(sub);
      }
      root.appendChild(body);
      return root;
    }

    function buildOverflowChipDOM(hidden) {
      const pill = document.createElement('span');
      pill.className = 'quota-overflow';
      pill.textContent = '+' + hidden.length + ' more';
      pill.title = hidden.map(h => {
        const label = planTypeLabel(h.snapshot.plan_type)
                   || (h.key.charAt(0).toUpperCase() + h.key.slice(1));
        if (h.mode === 'subscription') {
          const imm = h.imminent;
          return imm ? (label + ': ' + Math.round(imm.used_percent || 0) + '%') : label;
        }
        return label + ': ' + (h.totalCostUSD > 0 ? formatUsageCost(h.totalCostUSD) : '—');
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
        if (key in settings) el.checked = !!settings[key];
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
        settings[key] = this.checked;
        persistSettings();
        applySettings();
        refreshPermNote();
      });
    }

    // --- Daemon version (Irrlicht v$VERSION in the header) ---
    fetch('/api/v1/version').then(r => r.ok ? r.json() : null).catch(() => null).then(v => {
      const el = document.getElementById('app-version');
      if (el && v && v.version) el.textContent = 'v' + v.version;
    });

export {
  resolvedTheme, rowLabel, maybeNotifyOnUpdate,
  formatCost, pressureClass, historyPriorityForState,
};
