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

// hasWindows is the "this snapshot carries quota windows" test that
// quotaChips.js spells out in chipModeFor and bucketChips
// (`Array.isArray(windows) && windows.length > 0`); imminentWindow's first
// guard is its negation.
function hasWindows(snap) {
  return Array.isArray(snap?.windows) && snap.windows.length > 0;
}

// chipModeFor mirrors quotaChips.js chipModeFor's `auto` branch.
function chipModeFor(snap) {
  if (hasWindows(snap)) return 'subscription';
  return snap?.credits ? 'usage' : 'subscription';
}

// usedPercentOf is a window's used_percent as quotaChips.js reads it,
// `w.used_percent || 0`.
function usedPercentOf(w) {
  return w.used_percent || 0;
}

// imminentWindow mirrors quotaChips.js imminentWindow: the window used the
// most (the first of equals), or null when none is used above 0.
function imminentWindow(snap) {
  if (!hasWindows(snap)) return null;
  let best = null;
  for (const w of snap.windows) best = moreUsedWindow(best, w);
  if (!best || usedPercentOf(best) <= 0) return null;
  return best;
}

// moreUsedWindow is one turn of imminentWindow's loop: `w` takes over from
// `best` only when it is used strictly more; a missing window is passed over.
function moreUsedWindow(best, w) {
  if (!w) return best;
  if (!best || usedPercentOf(w) > usedPercentOf(best)) return w;
  return best;
}

// sampledAtOf is a snapshot's sampled_at as quotaChips.js compares it,
// `snap.sampled_at || 0`.
function sampledAtOf(snap) {
  return snap.sampled_at || 0;
}

// mergeChipIntoBucket mirrors quotaChips.js mergeChipIntoBucket's rule, its
// three branches in the web's order: subscription beats usage, fresh beats
// stale, then the newest sampled_at wins. The web's (snap, s, mode, imm,
// stale) arguments arrive here as one `chip` (chipFor's).
function mergeChipIntoBucket(existing, chip) {
  if (subscriptionBeatsUsage(existing, chip)) {
    existing.mode = 'subscription';
    adoptChip(existing, chip);
    existing.isStale = chip.isStale;
  } else if (freshBeatsStale(existing, chip)) {
    adoptChip(existing, chip);
    existing.isStale = false;
  } else if (newerSampleWins(existing, chip)) {
    adoptChip(existing, chip);
  }
}

// subscriptionBeatsUsage is the web's first branch: a subscription sample
// replaces a usage bucket's, unless it is stale and the bucket's is not.
function subscriptionBeatsUsage(existing, chip) {
  if (existing.mode !== 'usage' || chip.mode !== 'subscription') return false;
  return !chip.isStale || existing.isStale;
}

// freshBeatsStale is the web's second branch: a fresh sample replaces a
// stale one, whatever either was sampled at.
function freshBeatsStale(existing, chip) {
  return existing.isStale && !chip.isStale;
}

// newerSampleWins is the web's third branch: between two equally stale
// samples, the later sampled_at.
function newerSampleWins(existing, chip) {
  return existing.isStale === chip.isStale && sampledAtOf(chip.snapshot) > sampledAtOf(existing.snapshot);
}

// adoptChip is what every branch of mergeChipIntoBucket takes from the chip.
function adoptChip(existing, chip) {
  existing.snapshot = chip.snapshot;
  existing.session = chip.session;
  existing.imminent = chip.imminent;
}

// makeBucketChips builds bucketChips over a key rule. Production uses
// bucketKeyFor; the parity test builds a mutant over a wrong rule and checks
// the parity comparison catches it.
export function makeBucketChips(keyFor) {
  return function bucketChips(sessions, nowMs) {
    const buckets = new Map();
    for (const s of sessions) {
      const chip = chipFor(s, nowMs, keyFor);
      if (chip) addChip(buckets, chip);
    }
    return Array.from(buckets.values()).sort((a, b) => a.key.localeCompare(b.key));
  };
}

export const bucketChips = makeBucketChips(bucketKeyFor);

// chipFor is what one session brings to bucketChips: its rate-limit snapshot
// with that snapshot's bucket key (by `keyFor`), mode, staleness, imminent
// window and the session's cost; or null when it brings nothing (no
// snapshot, or a subscription snapshot with nothing to show).
function chipFor(s, nowMs, keyFor) {
  const snap = s?.metrics?.rate_limit;
  if (!snap) return null;
  const isStale = isStaleSnapshot(snap, nowMs);
  const { providerKey, accountRef, key } = keyFor(s, snap);
  const mode = chipModeFor(snap);
  if (isBlankSubscription(snap, mode)) return null;
  return {
    key,
    providerKey,
    accountRef,
    snapshot: snap,
    session: s,
    imminent: imminentWindow(snap),
    cost: sessionCostOf(s),
    mode,
    isStale,
  };
}

// isStaleSnapshot: any window whose reset is in the past (or zero) makes the
// snapshot stale — Swift's mergeIntoBuckets rule, as the web ports it.
function isStaleSnapshot(snap, nowMs) {
  return Array.isArray(snap.windows) && snap.windows.some(w => w && w.resets_at * 1000 <= nowMs);
}

// isBlankSubscription is the snapshot bucketChips skips (#1995): a
// subscription one with nothing to show, no windows, no credits and no
// retrieval failure.
function isBlankSubscription(snap, mode) {
  if (mode !== 'subscription' || hasWindows(snap)) return false;
  return !snap.credits && !snap.retrieval_failure;
}

// sessionCostOf is the cost a session adds to its bucket's total.
function sessionCostOf(s) {
  return s.metrics?.estimated_cost_usd || 0;
}

// addChip opens the chip's bucket, or merges the chip into the bucket its key
// already has; either way the session's cost counts toward that bucket.
function addChip(buckets, chip) {
  const existing = buckets.get(chip.key);
  if (existing) {
    mergeChipIntoBucket(existing, chip);
    existing.totalCostUSD += chip.cost;
    return;
  }
  buckets.set(chip.key, {
    key: chip.key,
    providerKey: chip.providerKey,
    accountRef: chip.accountRef,
    snapshot: chip.snapshot,
    session: chip.session,
    imminent: chip.imminent,
    totalCostUSD: chip.cost,
    mode: chip.mode,
    isStale: chip.isStale,
  });
}

// selectBucket picks the one bucket the panel shows: the bucket the current
// session's own snapshot lands in, else the most recently sampled bucket.
// `current` is the API session whose session_id equals this Claude Code
// session's id, or undefined when the daemon lists none.
export function selectBucket(buckets, current) {
  if (buckets.length === 0) return null;
  return ownBucket(buckets, current) || newestBucket(buckets);
}

// ownBucket is the bucket the current session's own snapshot lands in, found
// by the rule that built the buckets; undefined when it has no snapshot or
// that bucket is not among them.
function ownBucket(buckets, current) {
  const snap = current?.metrics?.rate_limit;
  if (!snap) return undefined;
  const { key } = bucketKeyFor(current, snap);
  return buckets.find(b => b.key === key);
}

// newestBucket is the bucket sampled last, the first of equals.
function newestBucket(buckets) {
  let newest = buckets[0];
  for (const b of buckets) {
    if (sampledAtOf(b.snapshot) > sampledAtOf(newest.snapshot)) newest = b;
  }
  return newest;
}

// --- presentation, ported from quotaChips.js where the dashboard has one ---

// The window lengths quotaWindowLabel names outright, Codex v1's off-by-one
// 299 and 10079 included.
const NAMED_WINDOW_LABELS = new Map([[299, '5h'], [300, '5h'], [10079, '7d'], [10080, '7d']]);

// quotaWindowLabel mirrors quotaChips.js quotaWindowLabel.
export function quotaWindowLabel(minutes) {
  if (NAMED_WINDOW_LABELS.has(minutes)) return NAMED_WINDOW_LABELS.get(minutes);
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
// SessionListView.swift), answering a tone instead of a CSS class: orange
// from 85 % used whatever the pace; with no pace, by the percent used
// (70 / 50); else by how far usage runs ahead of pace (15 / 5).
export function barTone(used, pace) {
  if (used >= 85) return 'orange';
  if (pace == null) return thresholdTone(used, 70, 50);
  return thresholdTone(used - pace, 15, 5);
}

// thresholdTone is orange from `orangeAt` up, yellow from `yellowAt` up, else
// green.
function thresholdTone(value, orangeAt, yellowAt) {
  if (value >= orangeAt) return 'orange';
  if (value >= yellowAt) return 'yellow';
  return 'green';
}

// formatTimeUntil is adapted from quotaChips.js formatTimeUntil: the clock is
// passed in, and a days branch is added ("3d 4h"), since the panel shows a
// 7d window's reset on one short line where the dashboard's tooltip prints
// "76h 12m". Relative on purpose: the hooks runtime's time zone is not the
// person's to rely on, and "2h 14m" reads the same everywhere.
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

// capitalize upper-cases a word's first character, as quotaChips.js does
// inline for a plan type, a product word and a provider key.
function capitalize(word) {
  return word.charAt(0).toUpperCase() + word.slice(1);
}

// The plan types quotaChips.js planTypeLabel's switch names. A Map, not an
// object, so the lookup neither coerces a key nor finds anything on a
// prototype: Map.get's SameValueZero differs from the switch's `===` only on
// NaN, which planTypeLabel's `!planType` turns away first.
const PLAN_TYPE_LABELS = new Map([
  ['max', 'Claude Max'],
  ['pro', 'Claude Pro'],
  ['plus', 'ChatGPT Plus'],
  ['team', 'Team'],
  ['enterprise', 'Enterprise'],
]);

// planTypeLabel mirrors quotaChips.js planTypeLabel: a named plan type's
// label, any other capitalised.
function planTypeLabel(planType) {
  if (!planType) return null;
  return PLAN_TYPE_LABELS.get(planType) ?? capitalize(planType);
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
  return product.split('_').filter(Boolean).map(capitalize).join(' ');
}

// bucketTitle mirrors the dashboard's chip heading: tier (plan type, then
// product, then the capitalised provider key) and "…<last 4 of the account>"
// or "unconfirmed" (#2095 D2).
export function bucketTitle(bucket) {
  const tier = planTypeLabel(bucket.snapshot.plan_type) ||
    productLabel(bucket.snapshot.product) ||
    capitalize(bucket.providerKey);
  const account = bucket.accountRef ? '…' + bucket.accountRef.slice(-4) : 'unconfirmed';
  return tier + ' · ' + account;
}

// quotaView is what the panel draws for one bucket: a title, one line per
// window (label, percent used, tone, time to reset), and whether the sample
// is stale.
export function quotaView(bucket, nowMs) {
  if (!bucket) return null;
  const windows = (bucket.snapshot.windows || []).filter(Boolean).map(w => windowView(w, nowMs));
  return {
    title: bucketTitle(bucket),
    windows,
    isStale: bucket.isStale,
    retrievalFailure: bucket.snapshot.retrieval_failure || '',
  };
}

// windowView is one window's line in quotaView.
function windowView(w, nowMs) {
  // The tone is decided on the raw figure, as the dashboard decides it
  // (buildQuotaRowDOM: barColorClass(w.used_percent || 0, pace)); only the
  // printed percent is rounded. Rounding first would turn 84.6 % at pace 80
  // orange here while the dashboard shows it green.
  const used = usedPercentOf(w);
  return {
    label: quotaWindowLabel(w.window_minutes || 0),
    usedPercent: Math.round(used),
    tone: barTone(used, paceFor(w, nowMs)),
    resetsIn: w.resets_at > 0 ? formatTimeUntil(w.resets_at, nowMs) : '',
  };
}
