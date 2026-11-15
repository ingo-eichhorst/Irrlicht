// Quota buckets: a port of the web dashboard's shipped rule
// (platforms/web/quotaChips.js: providerKeyFor, chipModeFor, imminentWindow,
// mergeChipIntoBucket, bucketChips), which itself mirrors the macOS app's
// RateLimitInfo.accountBucketKey / mergeIntoBuckets (SessionState.swift). The
// menu bar, the dashboard and this panel must put the same sessions in the
// same bucket, so this file is held to the web's by quota.parity.test.js,
// which feeds both the same sessions and requires identical buckets.
//
// The web module cannot be imported here: it imports irrlicht.js, which wires
// the DOM at import time, and a hooks module has no DOM. Hence a port.
//
// The one thing deliberately not ported: the per-provider mode preference the
// dashboard keeps in localStorage. The panel has no settings, so it always
// takes the dashboard's `auto` branch, which is also what the dashboard does
// until someone changes that setting.
//
// The rule, as shipped since #2136:
//   key = <provider>|<confirmed_account_ref>   when attribution is confirmed
//         <provider>|unconfirmed               confirmed provider, no account
//         unknown:<adapter>|unconfirmed        no confirmed provider
// and whole snapshots merge into a bucket: subscription beats usage, fresh
// beats stale, then the newest sampled_at wins. `account_binding.ref` is NOT
// the key: neither shipped client reads it, and a panel that did would
// disagree with both (the parity test carries that mutant).

// Mirrors session.AttributionQualityConfirmed
// (core/domain/session/rate_limit.go).
const ATTRIBUTION_QUALITY_CONFIRMED = 'confirmed';

// providerKeyFor mirrors quotaChips.js providerKeyFor: only the daemon's
// confirmed identity names a provider.
export function providerKeyFor(snap) {
  if (!snap) return null;
  if (snap.attribution_quality === ATTRIBUTION_QUALITY_CONFIRMED && snap.provider) {
    return snap.provider;
  }
  return null;
}

// bucketKeyFor is the key half of bucketChips, split out so the panel can
// find the current session's own bucket with exactly the rule that built it.
export function bucketKeyFor(session, snap) {
  const confirmedProvider = providerKeyFor(snap);
  const providerKey = confirmedProvider || ('unknown:' + (session.adapter || ''));
  const accountRef = (confirmedProvider && snap.confirmed_account_ref) || '';
  return { providerKey, accountRef, key: providerKey + '|' + (accountRef || 'unconfirmed') };
}

// chipModeFor mirrors quotaChips.js chipModeFor's `auto` branch.
function chipModeFor(snap) {
  if (Array.isArray(snap?.windows) && snap.windows.length > 0) return 'subscription';
  if (snap?.credits) return 'usage';
  return 'subscription';
}

// imminentWindow mirrors quotaChips.js imminentWindow.
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

// mergeChipIntoBucket mirrors quotaChips.js mergeChipIntoBucket.
function mergeChipIntoBucket(existing, snap, s, mode, imm, stale) {
  if (existing.mode === 'usage' && mode === 'subscription' && (!stale || existing.isStale)) {
    existing.mode = 'subscription';
    existing.snapshot = snap;
    existing.session = s;
    existing.imminent = imm;
    existing.isStale = stale;
  } else if (existing.isStale && !stale) {
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

// makeBucketChips builds bucketChips over a key rule. Production uses
// bucketKeyFor; the parity test builds a mutant over a wrong rule and checks
// the parity comparison catches it.
export function makeBucketChips(keyFor) {
  return function bucketChips(sessions, nowMs) {
    const buckets = new Map();
    for (const s of sessions) {
      const snap = s?.metrics?.rate_limit;
      if (!snap) continue;
      // Any window whose reset is in the past (or zero) makes the snapshot
      // stale — Swift's mergeIntoBuckets rule, as the web ports it.
      const stale = Array.isArray(snap.windows) && snap.windows.some(w => w && w.resets_at * 1000 <= nowMs);
      const { providerKey, accountRef, key } = keyFor(s, snap);
      const mode = chipModeFor(snap);
      const hasWindows = Array.isArray(snap.windows) && snap.windows.length > 0;
      // A subscription snapshot with nothing to show is skipped (#1995).
      if (mode === 'subscription' && !hasWindows && !snap.credits && !snap.retrieval_failure) continue;
      const imm = imminentWindow(snap);
      const cost = s.metrics?.estimated_cost_usd || 0;
      const existing = buckets.get(key);
      if (!existing) {
        buckets.set(key, {
          key: key,
          providerKey: providerKey,
          accountRef: accountRef,
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
    return Array.from(buckets.values()).sort((a, b) => a.key.localeCompare(b.key));
  };
}

export const bucketChips = makeBucketChips(bucketKeyFor);

// selectBucket picks the one bucket the panel shows: the bucket the current
// session's own snapshot lands in, else the most recently sampled bucket.
// `current` is the API session whose session_id equals this Claude Code
// session's id, or undefined when the daemon lists none.
export function selectBucket(buckets, current) {
  if (buckets.length === 0) return null;
  const snap = current?.metrics?.rate_limit;
  if (snap) {
    const { key } = bucketKeyFor(current, snap);
    const own = buckets.find(b => b.key === key);
    if (own) return own;
  }
  let newest = buckets[0];
  for (const b of buckets) {
    if ((b.snapshot.sampled_at || 0) > (newest.snapshot.sampled_at || 0)) newest = b;
  }
  return newest;
}

// --- presentation, ported from quotaChips.js where the dashboard has one ---

// quotaWindowLabel mirrors quotaChips.js quotaWindowLabel.
export function quotaWindowLabel(minutes) {
  if (minutes === 299 || minutes === 300) return '5h';
  if (minutes === 10079 || minutes === 10080) return '7d';
  if (minutes >= 1440) return Math.floor(minutes / 1440) + 'd';
  if (minutes >= 60) return Math.floor(minutes / 60) + 'h';
  return minutes + 'm';
}

// paceFor mirrors quotaChips.js paceFor: where usage would sit if paced
// evenly through the window, anchored to now.
function paceFor(w, nowMs) {
  if (!w?.window_minutes || w.window_minutes <= 0) return null;
  if (!w.resets_at || w.resets_at <= 0) return null;
  const windowMs = w.window_minutes * 60 * 1000;
  const startMs = w.resets_at * 1000 - windowMs;
  const pct = ((nowMs - startMs) / windowMs) * 100;
  return Math.min(100, Math.max(0, pct));
}

// barTone mirrors quotaChips.js barColorClass (QuotaBarThreshold in
// SessionListView.swift), answering a tone instead of a CSS class.
export function barTone(used, pace) {
  if (used >= 85) return 'orange';
  if (pace == null) {
    if (used >= 70) return 'orange';
    if (used >= 50) return 'yellow';
    return 'green';
  }
  const delta = used - pace;
  if (delta >= 15) return 'orange';
  if (delta >= 5) return 'yellow';
  return 'green';
}

// formatTimeUntil mirrors quotaChips.js formatTimeUntil, with the clock
// passed in. Relative on purpose: the hooks runtime's time zone is not the
// person's to rely on, and "resets in 2h 14m" reads the same everywhere.
export function formatTimeUntil(unixSec, nowMs) {
  let s = unixSec - nowMs / 1000;
  if (s < 0) s = 0;
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return d + 'd ' + h + 'h';
  if (h > 0) return h + 'h ' + m + 'm';
  return m + 'm';
}

function planTypeLabel(planType) {
  if (!planType) return null;
  switch (planType) {
    case 'max': return 'Claude Max';
    case 'pro': return 'Claude Pro';
    case 'plus': return 'ChatGPT Plus';
    case 'team': return 'Team';
    case 'enterprise': return 'Enterprise';
    default: return planType.charAt(0).toUpperCase() + planType.slice(1);
  }
}

const PRODUCT_LABELS = {
  claude_team: 'Claude Team',
  claude_max: 'Claude Max',
  claude_pro: 'Claude Pro',
  claude_enterprise: 'Claude Enterprise',
};

function productLabel(product) {
  if (!product) return null;
  if (PRODUCT_LABELS[product]) return PRODUCT_LABELS[product];
  return product.split('_').filter(Boolean)
    .map(w => w.charAt(0).toUpperCase() + w.slice(1)).join(' ');
}

// bucketTitle mirrors the dashboard's chip heading: tier (plan type, then
// product, then the capitalised provider key) and "…<last 4 of the account>"
// or "unconfirmed" (#2095 D2).
export function bucketTitle(bucket) {
  const tier = planTypeLabel(bucket.snapshot.plan_type) ||
    productLabel(bucket.snapshot.product) ||
    (bucket.providerKey.charAt(0).toUpperCase() + bucket.providerKey.slice(1));
  const account = bucket.accountRef ? '…' + bucket.accountRef.slice(-4) : 'unconfirmed';
  return tier + ' · ' + account;
}

// quotaView is what the panel draws for one bucket: a title, one line per
// window (label, percent used, tone, time to reset), and whether the sample
// is stale.
export function quotaView(bucket, nowMs) {
  if (!bucket) return null;
  const windows = (bucket.snapshot.windows || []).filter(Boolean).map(w => {
    const used = Math.round(w.used_percent || 0);
    return {
      label: quotaWindowLabel(w.window_minutes || 0),
      usedPercent: used,
      tone: barTone(used, paceFor(w, nowMs)),
      resetsIn: w.resets_at > 0 ? formatTimeUntil(w.resets_at, nowMs) : '',
    };
  });
  return {
    title: bucketTitle(bucket),
    windows,
    isStale: bucket.isStale,
    retrievalFailure: bucket.snapshot.retrieval_failure || '',
  };
}
