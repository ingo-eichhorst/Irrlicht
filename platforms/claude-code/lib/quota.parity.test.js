// @vitest-environment jsdom
//
// Parity: lib/quota.js must bucket sessions exactly as the dashboard's shipped
// bucketChips does (platforms/web/quotaChips.js), so the panel, the dashboard
// and the menu bar (which the dashboard mirrors) show the same quota for the
// same /api/v1/sessions answer.
//
// The dashboard's module is imported as it ships. Its import graph
// (quotaChips.js → irrlicht.js) wires the dashboard's DOM at load, so this one
// file runs under jsdom and loads the dashboard's own test scaffold first —
// the same file platforms/web's suite uses, imported rather than copied so it
// cannot go stale beside it. ES modules evaluate in import order, so the
// scaffold is in place before quotaChips.js loads.
//
// irrlicht.js is imported before quotaChips.js on purpose: the two import
// each other, and the page loads irrlicht.js as its entry. Entered from
// quotaChips.js instead, irrlicht.js's first render runs while quotaChips.js
// is still uninitialised and logs "quota chip render failed: ReferenceError:
// Cannot access 'QUOTA_FORECAST_KEY' before initialization" (seen on a run
// entered that way). Production's order makes the load quiet and the same
// every run.
import '../../web/vitest.setup.js';
import '../../web/irrlicht.js';
import { bucketChips as webBucketChips, buildQuotaRowDOM } from '../../web/quotaChips.js';

import { isDeepStrictEqual } from 'node:util';
import { describe, expect, test } from 'vitest';
import { bucketChips, makeBucketChips, providerKeyFor, quotaView, selectBucket } from './quota.js';

const NOW_MS = 1_900_000_000_000;
const NOW_S = NOW_MS / 1000;
const FRESH = NOW_S + 3600; // resets in an hour
const PAST = NOW_S - 60; // reset a minute ago: stale

let seq = 0;
function session({ adapter = 'claude-code', binding, cost, ...rl }) {
  seq += 1;
  const s = {
    session_id: 'sess-parity-' + String(seq).padStart(4, '0'),
    state: 'ready',
    adapter,
    metrics: { rate_limit: rl },
  };
  if (cost !== undefined) s.metrics.estimated_cost_usd = cost;
  if (binding !== undefined) s.account_binding = { decided: true, ref: binding };
  return s;
}
const windows = (used, resetsAt) => [
  { used_percent: used, window_minutes: 300, resets_at: resetsAt },
  { used_percent: used / 2, window_minutes: 10080, resets_at: resetsAt + 86400 },
];

// Every fixture is synthetic. The account_binding refs deliberately DISAGREE
// with confirmed_account_ref: a rule keyed on the binding must land these
// sessions in different buckets than the shipped rule does, or the mutant
// below could pass for the real thing.
const FIXTURES = {
  'two confirmed accounts on one provider': [
    session({ provider: 'anthropic', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-aaaa', windows: windows(10, FRESH), sampled_at: NOW_S - 100, binding: 'bind-shared', cost: 1 }),
    session({ provider: 'anthropic', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-bbbb', windows: windows(60, FRESH), sampled_at: NOW_S - 50, binding: 'bind-shared', cost: 2 }),
    session({ provider: 'anthropic', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-aaaa', windows: windows(12, FRESH), sampled_at: NOW_S - 10, binding: 'bind-shared', cost: 4 }),
  ],
  'several unconfirmed sessions on one provider': [
    session({ provider: 'anthropic', attribution_quality: 'inferred', windows: windows(20, FRESH), sampled_at: NOW_S - 300, binding: 'bind-one' }),
    session({ provider: 'anthropic', attribution_quality: 'inferred', windows: windows(25, FRESH), sampled_at: NOW_S - 200, binding: 'bind-two' }),
    session({ provider: 'anthropic', windows: windows(30, FRESH), sampled_at: NOW_S - 100, binding: 'bind-three' }),
  ],
  'an unconfirmed provider': [
    session({ adapter: 'pi', windows: windows(5, FRESH), sampled_at: NOW_S - 30, binding: 'bind-pi-1' }),
    session({ adapter: 'pi', windows: windows(7, FRESH), sampled_at: NOW_S - 20, binding: 'bind-pi-2' }),
    session({ adapter: '', windows: windows(9, FRESH), sampled_at: NOW_S - 10 }),
  ],
  'stale versus fresh': [
    session({ provider: 'openai', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-cccc', windows: windows(90, PAST), sampled_at: NOW_S - 5, binding: 'bind-c1' }),
    session({ provider: 'openai', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-cccc', windows: windows(30, FRESH), sampled_at: NOW_S - 500, binding: 'bind-c2' }),
    session({ provider: 'openai', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-dddd', windows: [{ used_percent: 1, window_minutes: 300, resets_at: 0 }], sampled_at: NOW_S - 1, binding: 'bind-d' }),
  ],
  'subscription beats usage': [
    session({ provider: 'deepseek', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-eeee', windows: [], credits: { has_credits: true, balance: 3, currency: 'CNY' }, sampled_at: NOW_S - 1, binding: 'bind-e' }),
    session({ provider: 'deepseek', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-eeee', windows: windows(40, FRESH), sampled_at: NOW_S - 400, binding: 'bind-e' }),
  ],
  'snapshots with nothing to show, and a retrieval failure': [
    session({ provider: 'anthropic', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-ffff', windows: [], sampled_at: NOW_S - 1 }),
    session({ provider: 'anthropic', attribution_quality: 'confirmed', confirmed_account_ref: 'acct-test-gggg', windows: [], retrieval_failure: 'http 429', sampled_at: NOW_S - 1 }),
    session({ provider: 'anthropic', attribution_quality: 'confirmed', windows: windows(3, FRESH), sampled_at: NOW_S - 2 }),
  ],
};
FIXTURES['everything at once'] = Object.values(FIXTURES).flat();

// parityDiff names every fixture where `impl` buckets differently from the
// dashboard: deep, strict equality over the whole bucket list (keys, chosen
// snapshot and session, mode, staleness, summed cost, order).
function parityDiff(impl) {
  return Object.entries(FIXTURES)
    .filter(([, sessions]) => !isDeepStrictEqual(impl(sessions, NOW_MS), webBucketChips(sessions, NOW_MS)))
    .map(([name]) => name);
}

describe('lib/quota.js buckets exactly as the dashboard does', () => {
  test('the dashboard module loaded and buckets the fixtures (positive control)', () => {
    // An import that silently resolved to something else, or a fixture the
    // dashboard drops entirely, would make "equal" vacuous: both empty.
    expect(typeof webBucketChips).toBe('function');
    for (const sessions of Object.values(FIXTURES)) {
      expect(webBucketChips(sessions, NOW_MS).length).toBeGreaterThan(0);
    }
    // The dashboard reads a per-provider mode preference from localStorage;
    // the panel always takes `auto`. Parity holds only while nothing is set.
    expect(localStorage.length).toBe(0);
  });

  test('every fixture: identical buckets', () => {
    expect(parityDiff(bucketChips)).toEqual([]);
  });

  test('the rule the fixtures exercise is the #2136 one', () => {
    const keys = sessions => bucketChips(sessions, NOW_MS).map(b => b.key);
    expect(keys(FIXTURES['two confirmed accounts on one provider']))
      .toEqual(['anthropic|acct-test-aaaa', 'anthropic|acct-test-bbbb']);
    expect(keys(FIXTURES['several unconfirmed sessions on one provider']))
      .toEqual(['unknown:claude-code|unconfirmed']);
    expect(keys(FIXTURES['an unconfirmed provider']))
      .toEqual(['unknown:|unconfirmed', 'unknown:pi|unconfirmed']);
  });

  // The committed mutation, in the in-language idiom of
  // platforms/web/irrlicht.history.autonomy.backfill.test.js: the rule the
  // issue first proposed, keying a bucket on account_binding.ref. The parity
  // check above must catch it, or its green says nothing about the key.
  test('a port keyed on account_binding.ref fails the parity check', () => {
    const keyOnBindingRef = (s, snap) => {
      const providerKey = providerKeyFor(snap) || ('unknown:' + (s.adapter || ''));
      const accountRef = s.account_binding?.ref || '';
      return { providerKey, accountRef, key: providerKey + '|' + (accountRef || 'unconfirmed') };
    };
    const diff = parityDiff(makeBucketChips(keyOnBindingRef));
    expect(diff).toContain('two confirmed accounts on one provider');
    expect(diff).toContain('several unconfirmed sessions on one provider');
    expect(diff).toContain('everything at once');
  });
});

describe('selectBucket', () => {
  const sessions = FIXTURES['two confirmed accounts on one provider'];
  const buckets = bucketChips(sessions, NOW_MS);

  test("shows the current session's own account bucket, even when another is newer", () => {
    expect(selectBucket(buckets, sessions[0]).key).toBe('anthropic|acct-test-aaaa');
    expect(selectBucket(buckets, sessions[1]).key).toBe('anthropic|acct-test-bbbb');
  });

  test('falls back to the most recently sampled bucket without a current session', () => {
    // acct-test-aaaa's newest snapshot (NOW-10) is newer than bbbb's (NOW-50).
    expect(selectBucket(buckets, undefined).key).toBe('anthropic|acct-test-aaaa');
  });

  test('falls back too when the current session has no sample of its own', () => {
    expect(selectBucket(buckets, { session_id: 'x', adapter: 'pi', metrics: {} }).key).toBe('anthropic|acct-test-aaaa');
  });

  test('nothing to show', () => {
    expect(selectBucket([], undefined)).toBe(null);
  });
});

// Red-first for the review finding that the panel coloured a bar from the
// ROUNDED percent while the dashboard colours it from the raw one
// (buildQuotaRowDOM: `barColorClass(w.used_percent || 0, pace)`): 84.6 % used
// an hour before a 5h reset (pace 80) is green on the dashboard and was orange
// here. The dashboard's own row builder is the reference, read off the class
// it puts on the bar's fill.
describe("a window's bar tone is the dashboard's", () => {
  const webTone = w => {
    const fill = buildQuotaRowDOM(w, 'normal', NOW_MS).querySelector('.quota-bar-fill');
    const m = /quota-bar-fill--(\w+)/.exec(fill?.className || '');
    if (!m) throw new Error('no tone class on the dashboard bar: ' + (fill?.className ?? 'no fill'));
    return m[1];
  };
  const panelTone = w => quotaView({ snapshot: { windows: [w] }, providerKey: 'anthropic', accountRef: '', isStale: false }, NOW_MS).windows[0].tone;
  const cases = {
    '84.6 % an hour before a 5h reset (pace 80)': { used_percent: 84.6, window_minutes: 300, resets_at: NOW_S + 3600 },
    '69.5 % with no pace (no reset time)': { used_percent: 69.5, window_minutes: 300, resets_at: 0 },
    '49.5 % with no pace (no window length)': { used_percent: 49.5, window_minutes: 0, resets_at: NOW_S + 3600 },
    '90 % (absolute orange)': { used_percent: 90, window_minutes: 300, resets_at: NOW_S + 3600 },
    '40 % just after a 7d reset (pace near 0)': { used_percent: 40, window_minutes: 10080, resets_at: NOW_S + 10080 * 60 - 60 },
    '10 % an hour before a 5h reset': { used_percent: 10, window_minutes: 300, resets_at: NOW_S + 3600 },
  };
  for (const [name, w] of Object.entries(cases)) {
    test(name, () => {
      expect(panelTone(w)).toBe(webTone(w));
    });
  }
  test('the displayed figure is still the rounded one', () => {
    expect(quotaView({ snapshot: { windows: [cases['84.6 % an hour before a 5h reset (pace 80)']] }, providerKey: 'anthropic', accountRef: '', isStale: false }, NOW_MS)
      .windows[0].usedPercent).toBe(85);
  });
});
