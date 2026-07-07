import {
  agentRegistry, resolvedTheme, cycleUsageTimeframe, usageSpendForChip,
  COST_TIMEFRAMES, currentUsageTimeframe, collectFlatSessions,
} from './irrlicht.js';

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

export function showQuotaForecast() {
  // Default ON, mirroring macOS @AppStorage("showQuotaForecast") default.
  const v = localStorage.getItem(QUOTA_FORECAST_KEY);
  if (v === '0' || v === 'false') return false;
  return true;
}
export function setShowQuotaForecast(on) {
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
  if (snap?.credits && !snap.plan_type) return 'usage';
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
  if (!w?.window_minutes || w.window_minutes <= 0) return null;
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

export function formatUsageCost(cost) {
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

// Fold session snapshots into one bucket per provider. Rules mirror
// macOS mergeIntoBuckets: subscription beats usage; fresh beats stale;
// among same-staleness snapshots, the highest sampled_at wins.
// mergeChipIntoBucket folds one more session's snapshot into an existing
// provider bucket, applying the subscription-beats-usage / fresh-beats-
// stale / newest-sampled_at-wins precedence (mirrors Swift's mergeIntoBuckets).
function mergeChipIntoBucket(existing, snap, s, mode, imm, stale) {
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
}

function bucketChips(sessions, nowMs) {
  const buckets = new Map();
  for (const s of sessions) {
    const snap = s?.metrics?.rate_limit;
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
    const cost = s.metrics?.estimated_cost_usd || 0;
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
    mergeChipIntoBucket(existing, snap, s, mode, imm, stale);
    existing.totalCostUSD += cost;
  }
  return Array.from(buckets.values()).sort((a, b) =>
    a.key < b.key ? -1 : a.key > b.key ? 1 : 0
  );
}

// subscriptionWindowLine renders one quota window's tooltip line: percent
// used, pace verdict (if computable), and reset countdown.
function subscriptionWindowLine(w, nowMs) {
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
  return line;
}

// subscriptionForecastLine returns the cap-forecast tooltip line, or null
// when there's neither a projected ETA nor any observed usage to reason
// about.
function subscriptionForecastLine(chip) {
  const eta = chip.session?.metrics?.rate_limit_forecast_eta;
  if (eta) return 'Projected cap: ' + formatClockTime(eta);
  if ((chip.snapshot.windows || []).some(w => (w.used_percent || 0) > 0)) {
    return "Forecast: won't hit cap this window";
  }
  return null;
}

// usageCreditsLine returns the credits sub-line for usage-mode chips, or
// null when there's nothing worth reporting.
function usageCreditsLine(c) {
  if (!c) return null;
  if (c.unlimited === true) return 'Credits: unlimited';
  if (typeof c.balance === 'number') return 'Credits balance: $' + c.balance.toFixed(2);
  if (c.has_credits) return 'Credits: available';
  return null;
}

function quotaTooltip(chip, nowMs) {
  const lines = [];
  const plan = planTypeLabel(chip.snapshot.plan_type);
  if (plan) lines.push(plan);
  if (chip.isStale) lines.push('⚠️ snapshot pre-dates current window — waiting for next statusline tick');
  if (chip.mode === 'subscription') {
    for (const w of (chip.snapshot.windows || [])) {
      lines.push(subscriptionWindowLine(w, nowMs));
    }
    const forecast = subscriptionForecastLine(chip);
    if (forecast) lines.push(forecast);
  } else {
    lines.push(formatUsageCost(chip.totalCostUSD) + ' · cumulative spend across active sessions');
    const creditsLine = usageCreditsLine(chip.snapshot.credits);
    if (creditsLine) lines.push(creditsLine);
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
            || adapterIconHTML(chip.session?.adapter);
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

export function renderHeaderTitle() {
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
export function refreshProviderSettings() {
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
    const iconSvg = providerIconHTML(c.key) || adapterIconHTML(c.session?.adapter);
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
