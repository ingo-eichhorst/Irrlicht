import { describe, expect, test } from 'vitest';
import {
  quotaWindowLabel, providerKeyFor, usageCreditsLine, providerIconHTML,
  quotaChipLayout, buildQuotaRowDOM, buildOverflowChipDOM, applyStripDensity,
  bucketChips, productLabel, quotaTooltip, providerRowChips,
} from './quotaChips.js';

describe('quotaWindowLabel', () => {
  test('labels a Codex single-weekly snapshot without inferring a five-hour window', () => {
    expect(quotaWindowLabel(10080)).toBe('7d');
  });

  test('continues to label a reported five-hour window', () => {
    expect(quotaWindowLabel(300)).toBe('5h');
  });
});

// Red-first coverage for issue #1995: providerKeyFor infers a billing
// identity from plan_type/adapter today, which misbrands any non-Anthropic
// provider whose tier happens to be called "pro" (several providers sell a
// tier by that name — e.g. GitHub Copilot Pro).
describe('providerKeyFor', () => {
  test('does not brand a non-Anthropic pro-tier snapshot as Anthropic', () => {
    const snap = { plan_type: 'pro', windows: [] };
    expect(providerKeyFor(snap, 'copilot')).not.toBe('anthropic');
  });
});

// Red-first coverage for issue #1995: usageCreditsLine hardcodes a "$"
// prefix today, which is wrong for any provider balance denominated in a
// non-USD currency (e.g. DeepSeek's CNY balances).
describe('usageCreditsLine', () => {
  test('renders a non-dollar balance in its own currency, not $', () => {
    const line = usageCreditsLine({ has_credits: true, balance: 12.34, currency: 'CNY' });
    expect(line).not.toContain('$');
  });

  // Issue #2009 — LOCKS, not red-first: these pass on the #1995 renderer
  // unchanged. They pin the wire shapes core/adapters/outbound/
  // deepseekaccountapi produces (balance omitted by omitempty on a zero,
  // balance_observed kept) and the four states #2009 §1.3 requires to stay
  // apart. QuotaChipModeTests.swift (lines 19-71) pins the same four states on macOS.
  test('keeps observed zero, unknown, unlimited and missing distinct', () => {
    const zero = usageCreditsLine({ has_credits: false, balance_observed: true, currency: 'CNY', total: 0, granted: 0, topped_up: 0 });
    const unknown = usageCreditsLine({ has_credits: true });
    const unlimited = usageCreditsLine({ has_credits: true, unlimited: true });
    const missing = usageCreditsLine(null);
    expect(zero).toBe('Credits balance: 0.00 CNY');
    expect(unknown).toBe('Credits: available');
    expect(unlimited).toBe('Credits: unlimited');
    expect(missing).toBeNull();
    expect(new Set([zero, unknown, unlimited, missing]).size).toBe(4);
  });

  test('renders a DeepSeek CNY balance in CNY', () => {
    const line = usageCreditsLine({ has_credits: true, balance: 110, balance_observed: true, currency: 'CNY', total: 110, granted: 10, topped_up: 100 });
    expect(line).toBe('Credits balance: 110.00 CNY');
  });
});

// Issue #2057: the daemon stamps Muse Code's account-API snapshots with
// provider "meta", so a Meta chip needs a bundled mark rather than the
// generic-circle fallback. Seen red before the meta entry existed
// (providerIconHTML('meta') returned '').
describe('providerIconHTML', () => {
  test('bundles a Meta mark for the meta provider key', () => {
    const svg = providerIconHTML('meta');
    expect(svg).toContain('<svg');
    expect(svg).toContain('fill="currentColor"');
  });

  test('returns empty for an unknown provider so the caller falls back', () => {
    expect(providerIconHTML('nonexistent-provider')).toBe('');
  });
});

// Issue #2063: the header capped quota chips at two, so a third provider
// (Meta, since #2058) pushed OpenAI into a "+1 more" pill. Same table as
// QuotaChipLayoutTests.swift on macOS. Counts 0-2 are locks (unchanged since
// before #2063); 3..7 were seen red against the cap-2 extraction.
describe('quotaChipLayout', () => {
  test.each([
    [0, 0, 0, 'regular'],
    [1, 1, 0, 'regular'],
    [2, 2, 0, 'compact'],
    [3, 3, 0, 'tight'],
    [4, 4, 0, 'dense'],
    [5, 5, 0, 'dense'],
    // Past five, the pill takes a chip's slot (see quotaChipLayout).
    [6, 4, 2, 'dense'],
    [7, 4, 3, 'dense'],
  ])('%i providers -> %i inline, %i in the overflow pill, %s', (count, visible, overflow, density) => {
    expect(quotaChipLayout(count)).toEqual({ visible, overflow, density });
  });
});

describe('buildQuotaRowDOM per density', () => {
  // resets_at is unix seconds on the wire; an hour out keeps the pace marker in range.
  const nowMs = Date.UTC(2026, 8, 26, 17, 0, 0);
  const w = { window_minutes: 300, used_percent: 42, resets_at: nowMs / 1000 + 3600 };
  const parts = (row) => Array.from(row.children).map((c) => c.className.split(' ')[0]);

  test('regular keeps the window label and the inline reset time', () => {
    expect(parts(buildQuotaRowDOM(w, 'regular', nowMs)))
      .toEqual(['quota-row-label', 'quota-bar', 'quota-row-percent', 'quota-row-reset']);
  });

  test.each(['compact', 'tight'])('%s drops only the reset time', (density) => {
    expect(parts(buildQuotaRowDOM(w, density, nowMs)))
      .toEqual(['quota-row-label', 'quota-bar', 'quota-row-percent']);
  });

  test('dense also drops the window label but keeps the percent', () => {
    const row = buildQuotaRowDOM(w, 'dense', nowMs);
    expect(parts(row)).toEqual(['quota-bar', 'quota-row-percent']);
    expect(row.querySelector('.quota-row-percent').textContent).toBe('42%');
  });
});

describe('buildOverflowChipDOM', () => {
  test('the pill carries its "+N more" label', () => {
    const hidden = [
      { key: 'openai|unconfirmed', providerKey: 'openai', accountRef: '', mode: 'subscription', snapshot: {}, imminent: { used_percent: 90 } },
      { key: 'zai|zzzz-1234', providerKey: 'zai', accountRef: 'zzzz-1234', mode: 'subscription', snapshot: {}, imminent: null },
    ];
    const pill = buildOverflowChipDOM(hidden);
    expect(pill.textContent).toBe('+2 more');
    expect(pill.title).toBe('Openai · Account unconfirmed: 90%\nZai · Account …1234');
  });
});

// The per-tier CSS only applies through the strip's class (review finding on
// PR #2065: nothing exercised it). Each count's layout goes through the real
// quotaChipLayout, so the table above and the CSS selectors are connected.
describe('applyStripDensity', () => {
  const stripClasses = (el) => Array.from(el.classList).filter((c) => c.startsWith('quota-chips--'));

  test.each([
    [1, ['quota-chips--regular']],
    [2, ['quota-chips--compact']],
    [3, ['quota-chips--tight', 'quota-chips--flex']],
    [5, ['quota-chips--dense', 'quota-chips--flex']],
    [6, ['quota-chips--dense', 'quota-chips--flex']],
  ])('%i providers put %j on the strip', (count, want) => {
    const el = document.createElement('div');
    applyStripDensity(el, quotaChipLayout(count).density);
    expect(stripClasses(el)).toEqual(want);
  });

  test('a new density replaces the previous one, and null clears it', () => {
    const el = document.createElement('div');
    el.classList.add('quota-chips');
    applyStripDensity(el, 'dense');
    applyStripDensity(el, 'tight');
    expect(stripClasses(el)).toEqual(['quota-chips--tight', 'quota-chips--flex']);
    applyStripDensity(el, null);
    expect(Array.from(el.classList)).toEqual(['quota-chips']);
  });
});

// Issue #2095: one chip per confirmed account. Issue #2136 revised #2095's D3
// for the unconfirmed case: every session of one provider whose snapshot has
// no confirmed account shares ONE "Account unconfirmed" chip (key
// `<provider>|unconfirmed`) instead of getting a chip each. A confirmed
// account never merges with that bucket or with another account.
describe('bucketChips per account (#2095)', () => {
  const nowMs = Date.UTC(2026, 0, 1);
  const future = nowMs / 1000 + 3600;
  const mk = ({ id, adapter, extra, sampledAt, pct }) => ({
    session_id: id, adapter,
    metrics: { rate_limit: {
      attribution_quality: 'confirmed', provider: 'anthropic',
      sampled_at: sampledAt, windows: [{ used_percent: pct, window_minutes: 300, resets_at: future }],
      ...extra,
    } },
  });
  const pctOf = (chip) => chip.snapshot.windows[0].used_percent;

  test('C1: two confirmed accounts of one provider stay two chips across interleaved ticks', () => {
    for (const [aT, bT] of [[2, 1], [3, 4], [6, 5], [7, 8]]) {
      const chips = bucketChips([
        mk({ id: 's1', adapter: 'claude-code', extra: { confirmed_account_ref: 'acct-A' }, sampledAt: aT, pct: 10 }),
        mk({ id: 's2', adapter: 'claude-code', extra: { confirmed_account_ref: 'acct-B' }, sampledAt: bT, pct: 20 }),
      ], nowMs);
      expect(chips).toHaveLength(2);
      expect(pctOf(chips.find((c) => c.accountRef === 'acct-A'))).toBe(10);
      expect(pctOf(chips.find((c) => c.accountRef === 'acct-B'))).toBe(20);
      expect(chips.every((c) => c.providerKey === 'anthropic')).toBe(true);
    }
  });

  // Flipped by #2136 (was "never merges across sessions"): an empty and a
  // missing ref are both unconfirmed, so the two sessions share the
  // provider's one unconfirmed chip and the newer reading wins.
  test('C2: confirmed provider with empty or missing ref folds into one unconfirmed chip', () => {
    for (const [aT, bT] of [[2, 1], [3, 4], [6, 5]]) {
      const chips = bucketChips([
        mk({ id: 's1', adapter: 'claude-code', extra: { confirmed_account_ref: '' }, sampledAt: aT, pct: 10 }),
        mk({ id: 's2', adapter: 'claude-code', extra: {}, sampledAt: bT, pct: 20 }),
      ], nowMs);
      expect(chips).toHaveLength(1);
      expect(chips[0].key).toBe('anthropic|unconfirmed');
      expect(chips[0].accountRef).toBe('');
      expect(pctOf(chips[0])).toBe(aT > bT ? 10 : 20);
    }
  });

  // Flipped by #2136 (was "stay two chips"): an unknown provider's sessions
  // are unconfirmed too, so one adapter's share one chip, as in v0.6.7.
  test('C2: two unknown-provider sessions of one adapter fold into one chip', () => {
    const unk = (id, t, pct) => ({
      session_id: id, adapter: 'codex',
      metrics: { rate_limit: { sampled_at: t, windows: [{ used_percent: pct, window_minutes: 300, resets_at: future }] } },
    });
    for (const [aT, bT] of [[2, 1], [3, 4]]) {
      const chips = bucketChips([unk('u1', aT, 10), unk('u2', bT, 20)], nowMs);
      expect(chips).toHaveLength(1);
      expect(chips[0].key).toBe('unknown:codex|unconfirmed');
      expect(chips[0].providerKey).toBe('unknown:codex');
      expect(pctOf(chips[0])).toBe(aT > bT ? 10 : 20);
    }
  });

  test('same account on two sessions still folds to one chip (newest wins)', () => {
    const chips = bucketChips([
      mk({ id: 's1', adapter: 'claude-code', extra: { confirmed_account_ref: 'acct-A' }, sampledAt: 1, pct: 10 }),
      mk({ id: 's2', adapter: 'claude-code', extra: { confirmed_account_ref: 'acct-A' }, sampledAt: 2, pct: 30 }),
    ], nowMs);
    expect(chips).toHaveLength(1);
    expect(pctOf(chips[0])).toBe(30);
  });

  // C3 LOCKS (see run notes): the stored preference stays keyed by the bare provider.
  test('C3 lock: providerMode_anthropic still governs an account bucket', () => {
    localStorage.setItem('providerMode_anthropic', 'usage');
    try {
      const chips = bucketChips([
        mk({ id: 's1', adapter: 'claude-code', extra: { confirmed_account_ref: 'acct-A', credits: { has_credits: true } }, sampledAt: 1, pct: 10 }),
      ], nowMs);
      expect(chips).toHaveLength(1);
      expect(chips[0].mode).toBe('usage');
    } finally { localStorage.removeItem('providerMode_anthropic'); }
  });

  test('C3 lock: settings rows stay one per provider with two account buckets', () => {
    const chips = bucketChips([
      mk({ id: 's1', adapter: 'claude-code', extra: { confirmed_account_ref: 'acct-A' }, sampledAt: 1, pct: 10 }),
      mk({ id: 's2', adapter: 'claude-code', extra: { confirmed_account_ref: 'acct-B' }, sampledAt: 2, pct: 20 }),
    ], nowMs);
    expect(providerRowChips(chips)).toHaveLength(1);
  });

  test('C4: tooltip and overflow name the account; productLabel maps known products', () => {
    expect(productLabel('claude_team')).toBe('Claude Team');
    expect(productLabel('some_new_tier')).toBe('Some New Tier');
    const [a] = bucketChips([mk({ id: 's1', adapter: 'claude-code', extra: { confirmed_account_ref: 'acct-ABCD', product: 'claude_team' }, sampledAt: 1, pct: 10 })], nowMs);
    expect(quotaTooltip(a, nowMs).split('\n').slice(0, 2)).toEqual(['Claude Team', 'Account …ABCD']);
    const [u] = bucketChips([mk({ id: 's2', adapter: 'claude-code', extra: {}, sampledAt: 1, pct: 10 })], nowMs);
    expect(quotaTooltip(u, nowMs)).toContain('Account unconfirmed');
    expect(buildOverflowChipDOM([a, u]).title).toBe('Claude Team · Account …ABCD: 10%\nAnthropic · Account unconfirmed: 10%');
  });
});

// Issue #2136: v0.6.8 gave every unconfirmed session its own chip
// (`<provider>|session:<id>`), so N Claude Code sessions on one subscription
// showed N Anthropic chips and N Muse sessions N identical Meta chips. The
// approved fix keys an unconfirmed snapshot by `<provider>|unconfirmed`. The
// first and last cases are red on main; the two-chip cases are locks (they
// pass on main too) that pin what the fix must NOT merge.
describe('bucketChips unconfirmed bucket per provider (#2136)', () => {
  const nowMs = Date.UTC(2026, 10, 10, 23, 0, 0);
  const now = nowMs / 1000;
  // One subscription: every session reports the same 5h and 7d resets.
  const reset5h = now + 2 * 3600;
  const reset7d = now + 4 * 86400;
  // A Claude Code statusline reading: provider confirmed, account not
  // (confirmed_account_ref omitted, as omitempty drops "" on the wire).
  const claude = ({ id, sampledAt, pct5h, ref, cost = 0 }) => ({
    session_id: id, adapter: 'claude-code',
    metrics: {
      estimated_cost_usd: cost,
      rate_limit: {
        provider: 'anthropic', attribution_quality: 'confirmed',
        attribution_evidence: 'claude_code_statusline', observation_source: 'hook',
        sampled_at: sampledAt,
        windows: [
          { used_percent: pct5h, window_minutes: 300, resets_at: reset5h },
          { used_percent: 40, window_minutes: 10080, resets_at: reset7d },
        ],
        ...(ref ? { confirmed_account_ref: ref } : {}),
      },
    },
  });
  const pct5h = (chip) => chip.snapshot.windows[0].used_percent;

  test('two unconfirmed Anthropic sessions share ONE chip carrying the newer reading', () => {
    const older = claude({ id: 's-older', sampledAt: now - 1800, pct5h: 6, cost: 1.5 });
    const newer = claude({ id: 's-newer', sampledAt: now - 120, pct5h: 22, cost: 2.5 });
    for (const order of [[older, newer], [newer, older]]) {
      const chips = bucketChips(order, nowMs);
      expect(chips).toHaveLength(1);
      expect(chips[0].key).toBe('anthropic|unconfirmed');
      expect(chips[0].providerKey).toBe('anthropic');
      expect(chips[0].accountRef).toBe('');
      expect(pct5h(chips[0])).toBe(22);
      expect(chips[0].session.session_id).toBe('s-newer');
      expect(chips[0].totalCostUSD).toBe(4);
      expect(quotaTooltip(chips[0], nowMs)).toContain('Account unconfirmed');
    }
  });

  test('lock: a confirmed and an unconfirmed session of one provider stay two chips', () => {
    const confirmed = claude({ id: 's-acct', sampledAt: now - 1800, pct5h: 10, ref: 'acct-A' });
    const unconfirmed = claude({ id: 's-unc', sampledAt: now - 120, pct5h: 30 });
    for (const order of [[confirmed, unconfirmed], [unconfirmed, confirmed]]) {
      // Asserted through accountRef rather than the bucket key, so the lock
      // reads the same before and after the key change.
      const chips = bucketChips(order, nowMs);
      expect(chips).toHaveLength(2);
      expect(chips.every((c) => c.providerKey === 'anthropic')).toBe(true);
      expect(pct5h(chips.find((c) => c.accountRef === 'acct-A'))).toBe(10);
      expect(pct5h(chips.find((c) => c.accountRef === ''))).toBe(30);
    }
  });

  test('lock: two different confirmed accounts stay two chips', () => {
    const a = claude({ id: 's-a', sampledAt: now - 1800, pct5h: 10, ref: 'acct-A' });
    const b = claude({ id: 's-b', sampledAt: now - 120, pct5h: 80, ref: 'acct-B' });
    const chips = bucketChips([b, a], nowMs);
    expect(chips.map((c) => c.key)).toEqual(['anthropic|acct-A', 'anthropic|acct-B']);
    expect(chips.map(pct5h)).toEqual([10, 80]);
  });

  test('lock: unconfirmed sessions of two providers stay two chips', () => {
    const anthropic = claude({ id: 's-claude', sampledAt: now - 120, pct5h: 30 });
    const openai = {
      session_id: 's-codex', adapter: 'codex',
      metrics: { rate_limit: {
        provider: 'openai', attribution_quality: 'confirmed', sampled_at: now - 60,
        windows: [{ used_percent: 50, window_minutes: 10080, resets_at: reset7d }],
      } },
    };
    const chips = bucketChips([openai, anthropic], nowMs);
    expect(chips.map((c) => c.providerKey)).toEqual(['anthropic', 'openai']);
    expect(chips.every((c) => c.accountRef === '')).toBe(true);
  });

  test('two Muse sessions carrying the same Meta snapshot give ONE chip', () => {
    // The shape museaccountapi.buildSnapshot writes: provider "meta",
    // confirmed attribution, and NO account ref (accountRef() is always ""
    // and omitempty drops it). The account-API sweep stamps this same
    // snapshot onto every live top-level Muse session.
    const meta = {
      provider: 'meta', attribution_quality: 'confirmed',
      attribution_evidence: 'muse_account_api', observation_source: 'account_api',
      product: 'Muse Code Everyday Usage',
      sampled_at: now - 30, last_success_at: now - 30, last_attempt_at: now - 30,
      windows: [
        { used_percent: 12, window_minutes: 300, resets_at: reset5h },
        { used_percent: 35, window_minutes: 10080, resets_at: reset7d },
      ],
    };
    const muse = (id) => ({ session_id: id, adapter: 'muse', metrics: { rate_limit: meta } });
    const chips = bucketChips([muse('m1'), muse('m2'), muse('m3')], nowMs);
    expect(chips).toHaveLength(1);
    expect(chips[0].key).toBe('meta|unconfirmed');
    expect(chips[0].providerKey).toBe('meta');
    expect(chips[0].mode).toBe('subscription');
  });
});
